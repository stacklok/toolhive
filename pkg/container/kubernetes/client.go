// Package kubernetes provides a client for the Kubernetes runtime
// including creating, starting, stopping, and retrieving container information.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	apimwatch "k8s.io/apimachinery/pkg/watch"
	appsv1apply "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/tools/watch"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	transtypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// Constants for container status
const (
	// UnknownStatus represents an unknown container status
	UnknownStatus = "unknown"
	// mcpContainerName is the name of the MCP container. This is a known constant.
	mcpContainerName = "mcp"
	// defaultNamespace is the default Kubernetes namespace
	defaultNamespace = "default"
)

// RuntimeName is the name identifier for the Kubernetes runtime
const RuntimeName = "kubernetes"

// Client implements the Deployer interface for container operations
type Client struct {
	runtimeType      runtime.Type
	client           kubernetes.Interface
	config           *rest.Config
	platformDetector PlatformDetector
	// waitForStatefulSetReadyFunc is used for testing to mock the waitForStatefulSetReady function
	waitForStatefulSetReadyFunc func(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error
	// namespaceFunc is used for testing to override namespace detection
	namespaceFunc func() string
}

// getKubernetesConfig returns a Kubernetes REST config, trying in-cluster first,
// then falling back to out-of-cluster configuration using kubeconfig
func getKubernetesConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// If in-cluster config fails, try out-of-cluster config
	// This will use the default kubeconfig loading rules:
	// 1. KUBECONFIG environment variable
	// 2. ~/.kube/config file
	// 3. In-cluster config (already tried above)
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	config, err = kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes config (tried both in-cluster and out-of-cluster): %w", err)
	}

	return config, nil
}

// NewClient creates a new container client
func NewClient(_ context.Context) (*Client, error) {
	// Get kubernetes config (in-cluster or out-of-cluster)
	config, err := getKubernetesConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes config: %v", err)
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	return NewClientWithConfig(clientset, config), nil
}

// IsAvailable checks if kubernetes is available
func IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := NewClient(ctx)
	return err == nil
}

// NewClientWithConfig creates a new container client with a provided config
// This is primarily used for testing with fake clients
func NewClientWithConfig(clientset kubernetes.Interface, config *rest.Config) *Client {
	return &Client{
		runtimeType:      runtime.TypeKubernetes,
		client:           clientset,
		config:           config,
		platformDetector: NewDefaultPlatformDetector(),
	}
}

// NewClientWithConfigAndPlatformDetector creates a new container client with a provided config and platform detector
// This is primarily used for testing with fake clients and mock platform detectors
func NewClientWithConfigAndPlatformDetector(
	clientset kubernetes.Interface,
	config *rest.Config,
	platformDetector PlatformDetector,
) *Client {
	return &Client{
		runtimeType:      runtime.TypeKubernetes,
		client:           clientset,
		config:           config,
		platformDetector: platformDetector,
	}
}

// AttachToWorkload implements runtime.Runtime.
func (c *Client) AttachToWorkload(ctx context.Context, workloadName string) (io.WriteCloser, io.ReadCloser, error) {
	// AttachToWorkload attaches to a workload in Kubernetes
	// This is a more complex operation in Kubernetes compared to Docker/Podman
	// as it requires setting up an exec session to the pod

	// First, we need to find the pod associated with the workloadID (which is actually the statefulset name)
	namespace := c.getCurrentNamespace()
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", workloadName),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find pod for workload %s: %w", workloadName, err)
	}

	if len(pods.Items) == 0 {
		return nil, nil, fmt.Errorf("%w: no pods found for workload %s", runtime.ErrWorkloadNotFound, workloadName)
	}

	// Use the first pod found
	podName := pods.Items[0].Name

	attachOpts := &corev1.PodAttachOptions{
		Container: mcpContainerName,
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}

	// Set up the attach request
	req := c.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(c.getCurrentNamespace()).
		SubResource("attach").
		VersionedParams(attachOpts, scheme.ParameterCodec)

	config := c.config
	// Create a SPDY executor
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create SPDY executor: %v", err)
	}

	logger.Infof("Attaching to pod %s workload %s...", podName, workloadName)

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	//nolint:gosec // we don't check for an error here because it's not critical
	// and it also returns with an error of statuscode `0`'. perhaps someone
	// who knows the function a bit more can fix this.
	go func() {
		// wrap with retry so we can retry if the connection fails
		// Create exponential backoff with max 5 retries
		expBackoff := backoff.NewExponentialBackOff()

		_, err := backoff.Retry(ctx, func() (any, error) {
			return nil, exec.StreamWithContext(ctx, remotecommand.StreamOptions{
				Stdin:  stdinReader,
				Stdout: stdoutWriter,
				Stderr: stdoutWriter,
				Tty:    false,
			})
		},
			backoff.WithBackOff(expBackoff),
			backoff.WithMaxTries(5),
			backoff.WithNotify(func(err error, duration time.Duration) {
				logger.Errorf("Error attaching to workload %s: %v. Retrying in %s...", workloadName, err, duration)
			}),
		)
		if err != nil {
			if statusErr, ok := err.(*errors.StatusError); ok {
				logger.Errorf("Kubernetes API error: Status=%s, Message=%s, Reason=%s, Code=%d",
					statusErr.ErrStatus.Status,
					statusErr.ErrStatus.Message,
					statusErr.ErrStatus.Reason,
					statusErr.ErrStatus.Code)

				if statusErr.ErrStatus.Code == 0 && statusErr.ErrStatus.Message == "" {
					logger.Info("Empty status error - this typically means the connection was closed unexpectedly")
					logger.Info("This often happens when the container terminates or doesn't read from stdin")
				}
			} else {
				logger.Errorf("Non-status error: %v", err)
			}
		}
	}()

	return stdinWriter, stdoutReader, nil
}

// GetWorkloadLogs implements runtime.Runtime.
func (c *Client) GetWorkloadLogs(ctx context.Context, workloadName string, follow bool) (string, error) {
	// In Kubernetes, workloadID is the statefulset name
	namespace := c.getCurrentNamespace()

	// Get the pods associated with this statefulset
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "toolhive=true",
		FieldSelector: fmt.Sprintf("metadata.name=%s", workloadName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods for statefulset %s: %w", workloadName, err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("%w: no pods found for statefulset %s", runtime.ErrWorkloadNotFound, workloadName)
	}

	// Use the first pod
	podName := pods.Items[0].Name

	// Get logs from the pod
	logOptions := &corev1.PodLogOptions{
		Container:  mcpContainerName,
		Follow:     follow,
		Previous:   false,
		Timestamps: true,
	}

	req := c.client.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get logs for pod %s: %w", podName, err)
	}
	defer podLogs.Close()

	// Read logs
	logBytes, err := io.ReadAll(podLogs)
	if err != nil {
		return "", fmt.Errorf("failed to read logs for pod %s: %w", podName, err)
	}

	return string(logBytes), nil
}

// DeployWorkload implements runtime.Runtime.
func (c *Client) DeployWorkload(ctx context.Context,
	image string,
	containerName string,
	command []string,
	envVars map[string]string,
	containerLabels map[string]string,
	_ *permissions.Profile, // TODO: Implement permission profile support for Kubernetes
	transportType string,
	options *runtime.DeployWorkloadOptions,
	_ bool,
) (int, error) {
	namespace := c.getCurrentNamespace()
	containerLabels["app"] = containerName
	containerLabels["toolhive"] = "true"

	attachStdio := options == nil || options.AttachStdio

	// Convert environment variables to Kubernetes format
	var envVarList []*corev1apply.EnvVarApplyConfiguration
	for k, v := range envVars {
		envVarList = append(envVarList, corev1apply.EnvVar().WithName(k).WithValue(v))
	}

	// Create a pod template spec
	podTemplateSpec := ensureObjectMetaApplyConfigurationExists(corev1apply.PodTemplateSpec())

	// Apply the patch if provided
	if options != nil && options.K8sPodTemplatePatch != "" {
		var err error
		podTemplateSpec, err = applyPodTemplatePatch(podTemplateSpec, options.K8sPodTemplatePatch)
		if err != nil {
			return 0, fmt.Errorf("failed to apply pod template patch: %w", err)
		}
	}

	// Ensure the pod template has required configuration (labels, etc.)
	// Get a config to talk to the apiserver
	cfg := c.config

	// Detect platform type
	platformDetector := c.platformDetector
	if platformDetector == nil {
		platformDetector = NewDefaultPlatformDetector()
	}
	platform, err := platformDetector.DetectPlatform(cfg)
	if err != nil {
		return 0, fmt.Errorf("can't determine api server type: %w", err)
	}

	podTemplateSpec = ensurePodTemplateConfig(podTemplateSpec, containerLabels, platform)

	// Configure the MCP container
	err = configureMCPContainer(
		podTemplateSpec,
		image,
		command,
		attachStdio,
		envVarList,
		transportType,
		options,
		platform,
	)
	if err != nil {
		return 0, err
	}

	// Create an apply configuration for the statefulset
	statefulSetApply := appsv1apply.StatefulSet(containerName, namespace).
		WithLabels(containerLabels).
		WithSpec(appsv1apply.StatefulSetSpec().
			WithReplicas(1).
			WithSelector(metav1apply.LabelSelector().
				WithMatchLabels(map[string]string{
					"app": containerName,
				})).
			WithServiceName(containerName).
			WithTemplate(podTemplateSpec))

	// Apply the statefulset using server-side apply
	fieldManager := "toolhive-container-manager"
	createdStatefulSet, err := c.client.AppsV1().StatefulSets(namespace).
		Apply(ctx, statefulSetApply, metav1.ApplyOptions{
			FieldManager: fieldManager,
			Force:        true,
		})
	if err != nil {
		return 0, fmt.Errorf("failed to apply statefulset: %v", err)
	}

	logger.Infof("Applied statefulset %s", createdStatefulSet.Name)

	if transportTypeRequiresHeadlessService(transportType) && options != nil {
		// Create a headless service for SSE transport
		err := c.createHeadlessService(ctx, containerName, namespace, containerLabels, options)
		if err != nil {
			return 0, fmt.Errorf("failed to create headless service: %v", err)
		}
	}

	// Wait for the statefulset to be ready
	waitFunc := waitForStatefulSetReady
	if c.waitForStatefulSetReadyFunc != nil {
		waitFunc = c.waitForStatefulSetReadyFunc
	}
	err = waitFunc(ctx, c.client, namespace, createdStatefulSet.Name)
	if err != nil {
		return 0, fmt.Errorf("statefulset applied but failed to become ready: %w", err)
	}

	return 0, nil
}

// GetWorkloadInfo implements runtime.Runtime.
func (c *Client) GetWorkloadInfo(ctx context.Context, workloadName string) (runtime.ContainerInfo, error) {
	// In Kubernetes, workloadID is the statefulset name
	namespace := c.getCurrentNamespace()

	// Get the statefulset
	statefulset, err := c.client.AppsV1().StatefulSets(namespace).Get(ctx, workloadName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return runtime.ContainerInfo{}, fmt.Errorf("%w: statefulset %s not found", runtime.ErrWorkloadNotFound, workloadName)
		}
		return runtime.ContainerInfo{}, fmt.Errorf("failed to get statefulset %s: %w", workloadName, err)
	}

	// Get the pods associated with this workload.
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "toolhive=true",
		FieldSelector: fmt.Sprintf("metadata.name=%s", workloadName),
	})
	if err != nil {
		return runtime.ContainerInfo{}, fmt.Errorf("failed to list pods for statefulset %s: %w", workloadName, err)
	}

	// Extract port mappings from pods
	ports := make([]runtime.PortMapping, 0)
	if len(pods.Items) > 0 {
		ports = extractPortMappingsFromPod(&pods.Items[0])
	}

	// Get ports from associated service (for SSE transport)
	service, err := c.client.CoreV1().Services(namespace).Get(ctx, workloadName, metav1.GetOptions{})
	if err == nil {
		// Service exists, add its ports
		ports = extractPortMappingsFromService(service, ports)
	}

	// Determine status and state
	var status string
	var state runtime.WorkloadStatus
	if statefulset.Status.ReadyReplicas > 0 {
		status = "Running"
		state = runtime.WorkloadStatusRunning
	} else if statefulset.Status.Replicas > 0 {
		status = "Pending"
		state = runtime.WorkloadStatusStarting
	} else {
		// NOTE: Not clear if this is correct since the stop operation is a no-op.
		status = "Stopped"
		state = runtime.WorkloadStatusStopped
	}

	// Get the image from the pod template
	image := ""
	if len(statefulset.Spec.Template.Spec.Containers) > 0 {
		image = statefulset.Spec.Template.Spec.Containers[0].Image
	}

	return runtime.ContainerInfo{
		Name:    statefulset.Name,
		Image:   image,
		Status:  status,
		State:   state,
		Created: statefulset.CreationTimestamp.Time,
		Labels:  statefulset.Labels,
		Ports:   ports,
	}, nil
}

// IsWorkloadRunning implements runtime.Runtime.
func (c *Client) IsWorkloadRunning(ctx context.Context, workloadName string) (bool, error) {
	// In Kubernetes, workloadID is the statefulset name
	namespace := c.getCurrentNamespace()

	// Get the statefulset
	statefulset, err := c.client.AppsV1().StatefulSets(namespace).Get(ctx, workloadName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, fmt.Errorf("%w: statefulset %s not found", runtime.ErrWorkloadNotFound, workloadName)
		}
		return false, fmt.Errorf("failed to get statefulset %s: %w", workloadName, err)
	}

	// Check if the statefulset has at least one ready replica
	return statefulset.Status.ReadyReplicas > 0, nil
}

// ListWorkloads implements runtime.Runtime.
func (c *Client) ListWorkloads(ctx context.Context) ([]runtime.ContainerInfo, error) {
	// Create label selector for toolhive containers
	// Only show main MCP server pods (not proxy pods) by requiring toolhive-tool-type label
	labelSelector := "toolhive=true,toolhive-tool-type"

	// Determine namespace to search in
	var namespace string
	if strings.TrimSpace(os.Getenv("TOOLHIVE_KUBERNETES_ALL_NAMESPACES")) != "" {
		// Search in all namespaces
		namespace = ""
	} else {
		// Search in current namespace only
		namespace = c.getCurrentNamespace()
	}

	// List pods with the toolhive label
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}

	// Convert to our ContainerInfo format
	result := make([]runtime.ContainerInfo, 0, len(pods.Items))
	for _, pod := range pods.Items {
		// Extract port mappings from pod
		ports := extractPortMappingsFromPod(&pod)

		// Get ports from associated service (for SSE transport)
		service, err := c.client.CoreV1().Services(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err == nil {
			// Service exists, add its ports
			ports = extractPortMappingsFromService(service, ports)
		}

		// Get container status
		status := UnknownStatus
		state := runtime.WorkloadStatusUnknown
		if len(pod.Status.ContainerStatuses) > 0 {
			containerStatus := pod.Status.ContainerStatuses[0]
			if containerStatus.State.Running != nil {
				state = runtime.WorkloadStatusRunning
				status = "Running"
			} else if containerStatus.State.Waiting != nil {
				state = runtime.WorkloadStatusStarting
				status = containerStatus.State.Waiting.Reason
			} else if containerStatus.State.Terminated != nil {
				state = runtime.WorkloadStatusRemoving
				status = containerStatus.State.Terminated.Reason
			}
		}

		result = append(result, runtime.ContainerInfo{
			Name:    pod.Name,
			Image:   pod.Spec.Containers[0].Image,
			Status:  status,
			State:   state,
			Created: pod.CreationTimestamp.Time,
			Labels:  pod.Labels,
			Ports:   ports,
		})
	}

	return result, nil
}

// RemoveWorkload implements runtime.Runtime.
func (c *Client) RemoveWorkload(ctx context.Context, workloadName string) error {
	// In Kubernetes, we remove a workload by deleting the statefulset
	namespace := c.getCurrentNamespace()

	// Delete the statefulset
	deleteOptions := metav1.DeleteOptions{}
	err := c.client.AppsV1().StatefulSets(namespace).Delete(ctx, workloadName, deleteOptions)
	if err != nil {
		if errors.IsNotFound(err) {
			// If the statefulset doesn't exist, that's fine
			logger.Infof("Statefulset %s not found, nothing to remove", workloadName)
			return nil
		}
		return fmt.Errorf("failed to delete statefulset %s: %w", workloadName, err)
	}

	logger.Infof("Deleted statefulset %s", workloadName)
	return nil
}

// StopWorkload implements runtime.Runtime.
func (*Client) StopWorkload(_ context.Context, _ string) error {
	return nil
}

// IsRunning checks the health of the container runtime.
// This is used to verify that the runtime is operational and can manage workloads.
func (c *Client) IsRunning(ctx context.Context) error {
	// Use /readyz endpoint to check if the Kubernetes API server is ready.
	var status int
	result := c.client.Discovery().RESTClient().Get().AbsPath("/readyz").Do(ctx)
	if result.StatusCode(&status); status != 200 {
		return fmt.Errorf("kubernetes API server is not ready, status code: %d", status)
	}

	return nil
}

// waitForStatefulSetReady waits for a statefulset to be ready using the watch API
func waitForStatefulSetReady(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	// Create a field selector to watch only this specific statefulset
	fieldSelector := fmt.Sprintf("metadata.name=%s", name)

	// Set up the watch
	watcher, err := clientset.AppsV1().StatefulSets(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
		Watch:         true,
	})
	if err != nil {
		return fmt.Errorf("error watching statefulset: %w", err)
	}

	// Define the condition function that checks if the statefulset is ready
	isStatefulSetReady := func(event apimwatch.Event) (bool, error) {
		// Check if the event is a statefulset
		statefulSet, ok := event.Object.(*appsv1.StatefulSet)
		if !ok {
			return false, fmt.Errorf("unexpected object type: %T", event.Object)
		}

		// Check if the statefulset is ready
		if statefulSet.Status.ReadyReplicas == *statefulSet.Spec.Replicas {
			return true, nil
		}

		logger.Infof("Waiting for statefulset %s to be ready (%d/%d replicas ready)...",
			name, statefulSet.Status.ReadyReplicas, *statefulSet.Spec.Replicas)
		return false, nil
	}

	// Create a context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Wait for the statefulset to be ready
	_, err = watch.UntilWithoutRetry(timeoutCtx, watcher, isStatefulSetReady)
	if err != nil {
		return fmt.Errorf("error waiting for statefulset to be ready: %w", err)
	}

	return nil
}

// parsePortString parses a port string in the format "port/protocol" and returns the port number
func parsePortString(portStr string) (int, error) {
	// Split the port string to get just the port number
	port := strings.Split(portStr, "/")[0]
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return 0, fmt.Errorf("failed to parse port %s: %v", port, err)
	}
	return portNum, nil
}

// configureContainerPorts adds port configurations to a container for SSE transport
func configureContainerPorts(
	containerConfig *corev1apply.ContainerApplyConfiguration,
	options *runtime.DeployWorkloadOptions,
) (*corev1apply.ContainerApplyConfiguration, error) {
	if options == nil {
		return containerConfig, nil
	}

	// Use a map to track which ports have been added
	portMap := make(map[int32]bool)
	var containerPorts []*corev1apply.ContainerPortApplyConfiguration

	// Process exposed ports
	for portStr := range options.ExposedPorts {
		portNum, err := parsePortString(portStr)
		if err != nil {
			return nil, err
		}

		// Check for integer overflow
		if portNum < 0 || portNum > 65535 {
			return nil, fmt.Errorf("port number %d is out of valid range (0-65535)", portNum)
		}

		// Add port if not already in the map
		portInt32 := int32(portNum)
		if !portMap[portInt32] {
			containerPorts = append(containerPorts, corev1apply.ContainerPort().
				WithContainerPort(portInt32).
				WithProtocol(corev1.ProtocolTCP))
			portMap[portInt32] = true
		}
	}

	// Process port bindings
	for portStr := range options.PortBindings {
		portNum, err := parsePortString(portStr)
		if err != nil {
			return nil, err
		}

		// Check for integer overflow
		if portNum < 0 || portNum > 65535 {
			return nil, fmt.Errorf("port number %d is out of valid range (0-65535)", portNum)
		}

		// Add port if not already in the map
		portInt32 := int32(portNum)
		if !portMap[portInt32] {
			containerPorts = append(containerPorts, corev1apply.ContainerPort().
				WithContainerPort(portInt32).
				WithProtocol(corev1.ProtocolTCP))
			portMap[portInt32] = true
		}
	}

	// Add ports to container config
	if len(containerPorts) > 0 {
		containerConfig = containerConfig.WithPorts(containerPorts...)
	}

	return containerConfig, nil
}

// validatePortNumber checks if a port number is within the valid range
func validatePortNumber(portNum int) error {
	if portNum < 0 || portNum > 65535 {
		return fmt.Errorf("port number %d is out of valid range (0-65535)", portNum)
	}
	return nil
}

// createServicePortConfig creates a service port configuration for a given port number
func createServicePortConfig(portNum int) *corev1apply.ServicePortApplyConfiguration {
	//nolint:gosec // G115: Safe int->int32 conversion, range is checked in validatePortNumber
	portInt32 := int32(portNum)
	return corev1apply.ServicePort().
		WithName(fmt.Sprintf("port-%d", portInt32)).
		WithPort(portInt32).
		WithTargetPort(intstr.FromInt32(portInt32)).
		WithProtocol(corev1.ProtocolTCP)
}

// processExposedPorts processes exposed ports and adds them to the port map
func processExposedPorts(
	options *runtime.DeployWorkloadOptions,
	portMap map[int32]*corev1apply.ServicePortApplyConfiguration,
) error {
	for portStr := range options.ExposedPorts {
		portNum, err := parsePortString(portStr)
		if err != nil {
			return err
		}

		if err := validatePortNumber(portNum); err != nil {
			return err
		}

		//nolint:gosec // G115: Safe int->int32 conversion, range is checked in validatePortNumber
		portInt32 := int32(portNum)
		// Add port if not already in the map
		if _, exists := portMap[portInt32]; !exists {
			portMap[portInt32] = createServicePortConfig(portNum)
		}
	}
	return nil
}

// createServicePorts creates service port configurations from container options
func createServicePorts(options *runtime.DeployWorkloadOptions) ([]*corev1apply.ServicePortApplyConfiguration, error) {
	if options == nil {
		return nil, nil
	}

	// Use a map to track which ports have been added
	portMap := make(map[int32]*corev1apply.ServicePortApplyConfiguration)

	// Process exposed ports
	if err := processExposedPorts(options, portMap); err != nil {
		return nil, err
	}

	// Process port bindings
	for portStr, bindings := range options.PortBindings {
		portNum, err := parsePortString(portStr)
		if err != nil {
			return nil, err
		}

		if err := validatePortNumber(portNum); err != nil {
			return nil, err
		}

		//nolint:gosec // G115: Safe int->int32 conversion, range is checked in validatePortNumber
		portInt32 := int32(portNum)
		servicePort := portMap[portInt32]
		if servicePort == nil {
			// Create new service port if not in map
			servicePort = createServicePortConfig(portNum)
		}

		// If there are bindings with a host port, use the first one as node port
		if len(bindings) > 0 && bindings[0].HostPort != "" {
			hostPort, err := strconv.Atoi(bindings[0].HostPort)
			if err == nil && hostPort >= 30000 && hostPort <= 32767 {
				// NodePort must be in range 30000-32767
				// Safe to convert to int32 since we've verified the range (30000-32767)
				// which is well within int32 range (-2,147,483,648 to 2,147,483,647)
				//nolint:gosec // G109: Safe int->int32 conversion, range is checked above
				nodePort := int32(hostPort)
				servicePort = servicePort.WithNodePort(nodePort)
			}
		}

		//nolint:gosec // G115: Safe int->int32 conversion, range is checked above
		portMap[int32(portNum)] = servicePort
	}

	// Convert map to slice
	var servicePorts []*corev1apply.ServicePortApplyConfiguration
	for _, port := range portMap {
		servicePorts = append(servicePorts, port)
	}

	return servicePorts, nil
}

// createHeadlessService creates a headless Kubernetes service for the StatefulSet
func (c *Client) createHeadlessService(
	ctx context.Context,
	containerName string,
	namespace string,
	labels map[string]string,
	options *runtime.DeployWorkloadOptions,
) error {
	// Create service ports from the container ports
	servicePorts, err := createServicePorts(options)
	if err != nil {
		return err
	}

	// If no ports were configured, don't create a service
	if len(servicePorts) == 0 {
		logger.Info("No ports configured for SSE transport, skipping service creation")
		return nil
	}

	// Create service type based on whether we have node ports
	serviceType := corev1.ServiceTypeClusterIP
	for _, sp := range servicePorts {
		if sp.NodePort != nil {
			serviceType = corev1.ServiceTypeNodePort
			break
		}
	}

	// we want to generate a service name that is unique for the headless service
	// to avoid conflicts with the proxy service
	svcName := fmt.Sprintf("mcp-%s-headless", containerName)

	// Create the service apply configuration
	serviceApply := corev1apply.Service(svcName, namespace).
		WithLabels(labels).
		WithSpec(corev1apply.ServiceSpec().
			WithSelector(map[string]string{
				"app": containerName,
			}).
			WithPorts(servicePorts...).
			WithType(serviceType).
			WithClusterIP("None")) // "None" makes it a headless service

	// Apply the service using server-side apply
	fieldManager := "toolhive-container-manager"
	_, err = c.client.CoreV1().Services(namespace).
		Apply(ctx, serviceApply, metav1.ApplyOptions{
			FieldManager: fieldManager,
			Force:        true,
		})

	if err != nil {
		return fmt.Errorf("failed to apply service: %v", err)
	}

	logger.Infof("Created headless service %s for SSE transport", containerName)

	options.SSEHeadlessServiceName = svcName
	return nil
}

// extractPortMappingsFromPod extracts port mappings from a pod's containers
func extractPortMappingsFromPod(pod *corev1.Pod) []runtime.PortMapping {
	ports := make([]runtime.PortMapping, 0)

	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			ports = append(ports, runtime.PortMapping{
				ContainerPort: int(port.ContainerPort),
				HostPort:      int(port.HostPort),
				Protocol:      string(port.Protocol),
			})
		}
	}

	return ports
}

// transportTypeRequiresHeadlessService returns true if the transport type requires a headless service
func transportTypeRequiresHeadlessService(transportType string) bool {
	return transportType == string(transtypes.TransportTypeSSE) || transportType == string(transtypes.TransportTypeStreamableHTTP)
}

// extractPortMappingsFromService extracts port mappings from a Kubernetes service
func extractPortMappingsFromService(service *corev1.Service, existingPorts []runtime.PortMapping) []runtime.PortMapping {
	// Create a map of existing ports for easy lookup and updating
	portMap := make(map[int]runtime.PortMapping)
	for _, p := range existingPorts {
		portMap[p.ContainerPort] = p
	}

	// Update or add ports from the service
	for _, port := range service.Spec.Ports {
		containerPort := int(port.Port)
		hostPort := 0
		if port.NodePort > 0 {
			hostPort = int(port.NodePort)
		}

		// Update existing port or add new one
		portMap[containerPort] = runtime.PortMapping{
			ContainerPort: containerPort,
			HostPort:      hostPort,
			Protocol:      string(port.Protocol),
		}
	}

	// Convert map back to slice
	result := make([]runtime.PortMapping, 0, len(portMap))
	for _, p := range portMap {
		result = append(result, p)
	}

	return result
}

// applyPodTemplatePatch applies a JSON patch to a pod template spec
func applyPodTemplatePatch(
	baseTemplate *corev1apply.PodTemplateSpecApplyConfiguration,
	patchJSON string,
) (*corev1apply.PodTemplateSpecApplyConfiguration, error) {
	// Check if the base template is nil
	if baseTemplate == nil {
		return nil, fmt.Errorf("base template is nil")
	}

	// Parse the patch JSON
	patchedSpec, err := createPodTemplateFromPatch(patchJSON)
	if err != nil {
		return nil, err
	}

	// Check if the patched spec is nil
	if patchedSpec == nil {
		return baseTemplate, nil
	}

	// Copy fields from the patched spec to our template
	if patchedSpec.ObjectMetaApplyConfiguration != nil && len(patchedSpec.Labels) > 0 {
		baseTemplate = baseTemplate.WithLabels(patchedSpec.Labels)
	}

	if patchedSpec.Spec != nil {
		// Ensure baseTemplate.Spec is not nil
		if baseTemplate.Spec == nil {
			baseTemplate = baseTemplate.WithSpec(corev1apply.PodSpec())
		}
		// Copy the spec
		baseTemplate = baseTemplate.WithSpec(patchedSpec.Spec)
	}

	return baseTemplate, nil
}

// createPodTemplateFromPatch creates a pod template spec from a JSON string
func createPodTemplateFromPatch(patchJSON string) (*corev1apply.PodTemplateSpecApplyConfiguration, error) {
	// Ensure the patch is valid JSON
	var patchMap map[string]interface{}
	if err := json.Unmarshal([]byte(patchJSON), &patchMap); err != nil {
		return nil, fmt.Errorf("invalid JSON patch: %w", err)
	}

	var podTemplateSpec corev1apply.PodTemplateSpecApplyConfiguration
	if err := json.Unmarshal([]byte(patchJSON), &podTemplateSpec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal patch into pod template spec: %w", err)
	}

	// Ensure the pod template spec is not nil
	return ensureObjectMetaApplyConfigurationExists(&podTemplateSpec), nil
}

// ensurePodTemplateConfig ensures the pod template has required configuration
//
//nolint:gocyclo // Complex but necessary for platform-aware security context configuration
func ensurePodTemplateConfig(
	podTemplateSpec *corev1apply.PodTemplateSpecApplyConfiguration,
	containerLabels map[string]string,
	platform Platform,
) *corev1apply.PodTemplateSpecApplyConfiguration {
	podTemplateSpec = ensureObjectMetaApplyConfigurationExists(podTemplateSpec)
	// Ensure the pod template has labels
	if podTemplateSpec.Labels == nil {
		podTemplateSpec = podTemplateSpec.WithLabels(containerLabels)
	} else {
		// Merge with required labels
		for k, v := range containerLabels {
			podTemplateSpec.Labels[k] = v
		}
	}

	// Ensure the pod template has a spec
	if podTemplateSpec.Spec == nil {
		podTemplateSpec = podTemplateSpec.WithSpec(corev1apply.PodSpec())
	}

	// Ensure the pod template has a restart policy
	if podTemplateSpec.Spec.RestartPolicy == nil {
		podTemplateSpec.Spec = podTemplateSpec.Spec.WithRestartPolicy(corev1.RestartPolicyAlways)
	}

	// Add pod-level security context using SecurityContextBuilder
	if podTemplateSpec.Spec.SecurityContext == nil {
		securityBuilder := NewSecurityContextBuilder(platform)
		podTemplateSpec.Spec = podTemplateSpec.Spec.WithSecurityContext(
			securityBuilder.BuildPodSecurityContextApplyConfiguration(),
		)
	} else {
		// If the pod-level security context already exists, merge with platform-aware defaults
		securityBuilder := NewSecurityContextBuilder(platform)
		platformContext := securityBuilder.BuildPodSecurityContextApplyConfiguration()

		// Merge existing context with platform-aware settings
		if podTemplateSpec.Spec.SecurityContext.RunAsNonRoot == nil && platformContext.RunAsNonRoot != nil {
			podTemplateSpec.Spec.SecurityContext = podTemplateSpec.Spec.SecurityContext.WithRunAsNonRoot(*platformContext.RunAsNonRoot)
		}

		if podTemplateSpec.Spec.SecurityContext.RunAsUser == nil && platformContext.RunAsUser != nil {
			podTemplateSpec.Spec.SecurityContext = podTemplateSpec.Spec.SecurityContext.WithRunAsUser(*platformContext.RunAsUser)
		}

		if podTemplateSpec.Spec.SecurityContext.RunAsGroup == nil && platformContext.RunAsGroup != nil {
			podTemplateSpec.Spec.SecurityContext = podTemplateSpec.Spec.SecurityContext.WithRunAsGroup(*platformContext.RunAsGroup)
		}

		if podTemplateSpec.Spec.SecurityContext.FSGroup == nil && platformContext.FSGroup != nil {
			podTemplateSpec.Spec.SecurityContext = podTemplateSpec.Spec.SecurityContext.WithFSGroup(*platformContext.FSGroup)
		}

		if podTemplateSpec.Spec.SecurityContext.SeccompProfile == nil && platformContext.SeccompProfile != nil {
			podTemplateSpec.Spec.SecurityContext = podTemplateSpec.Spec.SecurityContext.WithSeccompProfile(platformContext.SeccompProfile)
		}

		// For OpenShift, override certain fields even if they exist
		if platform == PlatformOpenShift {
			if podTemplateSpec.Spec.SecurityContext.RunAsUser != nil {
				podTemplateSpec.Spec.SecurityContext.RunAsUser = nil
			}
			if podTemplateSpec.Spec.SecurityContext.RunAsGroup != nil {
				podTemplateSpec.Spec.SecurityContext.RunAsGroup = nil
			}
			if podTemplateSpec.Spec.SecurityContext.FSGroup != nil {
				podTemplateSpec.Spec.SecurityContext.FSGroup = nil
			}
		}
	}

	return podTemplateSpec
}

// getMCPContainer finds the "mcp" container in the pod template if it exists.
// Returns nil if the container doesn't exist.
func getMCPContainer(
	podTemplateSpec *corev1apply.PodTemplateSpecApplyConfiguration,
) *corev1apply.ContainerApplyConfiguration {
	// Ensure the pod template has a spec
	if podTemplateSpec.Spec == nil {
		podTemplateSpec = podTemplateSpec.WithSpec(corev1apply.PodSpec())
	}

	// Check if the container already exists
	if podTemplateSpec.Spec.Containers != nil {
		for i := range podTemplateSpec.Spec.Containers {
			// Get a pointer to the container in the slice
			container := &podTemplateSpec.Spec.Containers[i]
			if container.Name != nil && *container.Name == "mcp" {
				return container
			}
		}
	}

	// Container doesn't exist
	return nil
}

func ensureObjectMetaApplyConfigurationExists(
	podTemplateSpec *corev1apply.PodTemplateSpecApplyConfiguration,
) *corev1apply.PodTemplateSpecApplyConfiguration {
	if podTemplateSpec.ObjectMetaApplyConfiguration == nil {
		podTemplateSpec.ObjectMetaApplyConfiguration = &metav1apply.ObjectMetaApplyConfiguration{}
	}

	return podTemplateSpec
}

// configureContainer configures a container with the given settings
//
//nolint:gocyclo // Complex but necessary for platform-aware security context configuration
func configureContainer(
	container *corev1apply.ContainerApplyConfiguration,
	image string,
	command []string,
	attachStdio bool,
	envVars []*corev1apply.EnvVarApplyConfiguration,
	platform Platform,
) {
	logger.Debugf("Configuring container %s with image %s", *container.Name, image)
	logger.Debugf("Command: ")
	for _, arg := range command {
		logger.Debugf("Arg: %s", arg)
	}
	logger.Debugf("AttachStdio: %v", attachStdio)
	for _, envVar := range envVars {
		logger.Debugf("EnvVar: %s=%s", *envVar.Name, *envVar.Value)
	}

	container.WithImage(image).
		WithArgs(command...).
		WithStdin(attachStdio).
		WithTTY(false).
		WithEnv(envVars...)

	// Add container security context using SecurityContextBuilder
	securityBuilder := NewSecurityContextBuilder(platform)
	if container.SecurityContext == nil {
		container.WithSecurityContext(securityBuilder.BuildContainerSecurityContextApplyConfiguration())
	} else {
		// If the container security context already exists, merge with platform-aware defaults
		platformContext := securityBuilder.BuildContainerSecurityContextApplyConfiguration()

		// Merge existing context with platform-aware settings
		if container.SecurityContext.Privileged == nil && platformContext.Privileged != nil {
			container.SecurityContext = container.SecurityContext.WithPrivileged(*platformContext.Privileged)
		}

		if container.SecurityContext.RunAsNonRoot == nil && platformContext.RunAsNonRoot != nil {
			container.SecurityContext = container.SecurityContext.WithRunAsNonRoot(*platformContext.RunAsNonRoot)
		}

		if container.SecurityContext.RunAsUser == nil && platformContext.RunAsUser != nil {
			container.SecurityContext = container.SecurityContext.WithRunAsUser(*platformContext.RunAsUser)
		}

		if container.SecurityContext.RunAsGroup == nil && platformContext.RunAsGroup != nil {
			container.SecurityContext = container.SecurityContext.WithRunAsGroup(*platformContext.RunAsGroup)
		}

		if container.SecurityContext.AllowPrivilegeEscalation == nil && platformContext.AllowPrivilegeEscalation != nil {
			container.SecurityContext = container.SecurityContext.WithAllowPrivilegeEscalation(*platformContext.AllowPrivilegeEscalation)
		}

		if container.SecurityContext.ReadOnlyRootFilesystem == nil && platformContext.ReadOnlyRootFilesystem != nil {
			container.SecurityContext = container.SecurityContext.WithReadOnlyRootFilesystem(*platformContext.ReadOnlyRootFilesystem)
		}

		if container.SecurityContext.SeccompProfile == nil && platformContext.SeccompProfile != nil {
			container.SecurityContext = container.SecurityContext.WithSeccompProfile(platformContext.SeccompProfile)
		}

		if container.SecurityContext.Capabilities == nil && platformContext.Capabilities != nil {
			container.SecurityContext = container.SecurityContext.WithCapabilities(platformContext.Capabilities)
		}

		// For OpenShift, override certain fields even if they exist
		if platform == PlatformOpenShift {
			logger.Infof("Setting OpenShift security context requirements to container %s", *container.Name)

			if container.SecurityContext.RunAsUser != nil {
				container.SecurityContext.RunAsUser = nil
			}
			if container.SecurityContext.RunAsGroup != nil {
				container.SecurityContext.RunAsGroup = nil
			}
		}
	}
}

// configureMCPContainer configures the MCP container in the pod template
func configureMCPContainer(
	podTemplateSpec *corev1apply.PodTemplateSpecApplyConfiguration,
	image string,
	command []string,
	attachStdio bool,
	envVarList []*corev1apply.EnvVarApplyConfiguration,
	transportType string,
	options *runtime.DeployWorkloadOptions,
	platform Platform,
) error {
	// Get the "mcp" container if it exists
	mcpContainer := getMCPContainer(podTemplateSpec)

	// If the container doesn't exist, create a new one
	if mcpContainer == nil {
		mcpContainer = corev1apply.Container().WithName("mcp")

		// Configure the container
		configureContainer(mcpContainer, image, command, attachStdio, envVarList, platform)

		// Configure ports if needed
		if options != nil && transportType == string(transtypes.TransportTypeSSE) {
			var err error
			mcpContainer, err = configureContainerPorts(mcpContainer, options)
			if err != nil {
				return err
			}
		}

		// Add the fully configured container to the pod template
		podTemplateSpec.Spec.WithContainers(mcpContainer)
	} else {
		// Configure the existing container
		configureContainer(mcpContainer, image, command, attachStdio, envVarList, platform)

		// Configure ports if needed
		if options != nil && transportType == string(transtypes.TransportTypeSSE) {
			var err error
			_, err = configureContainerPorts(mcpContainer, options)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// getCurrentNamespace returns the namespace the pod is running in.
// It tries multiple methods in order:
// 1. Reading from the service account token file (when running inside a pod)
// 2. Getting the namespace from environment variables
// 3. Getting the namespace from the current kubectl context
// 4. Falling back to "default" if all methods fail
func (c *Client) getCurrentNamespace() string {
	// If a custom namespace function is set (for testing), use it
	if c.namespaceFunc != nil {
		return c.namespaceFunc()
	}

	return GetCurrentNamespace()
}

// Package kubernetes provides a client for the Kubernetes runtime
// including creating, starting, stopping, and retrieving container information.
package kubernetes

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimwatch "k8s.io/apimachinery/pkg/watch"
	appsv1apply "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/tools/watch"

	"github.com/stacklok/vibetool/pkg/container/runtime"
	// Avoid import cycle
	"github.com/stacklok/vibetool/pkg/permissions"
)

// Constants for container status
const (
	// UnknownStatus represents an unknown container status
	UnknownStatus = "unknown"
)

// Client implements the Runtime interface for container operations
type Client struct {
	runtimeType runtime.Type
	client      *kubernetes.Clientset
}

// NewClient creates a new container client
func NewClient(_ context.Context) (*Client, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %v", err)
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	return &Client{
		runtimeType: runtime.TypeKubernetes,
		client:      clientset,
	}, nil
}

// getNamespaceFromServiceAccount attempts to read the namespace from the service account token file
func getNamespaceFromServiceAccount() (string, error) {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("failed to read namespace file: %w", err)
	}
	return string(data), nil
}

// getNamespaceFromEnv attempts to get the namespace from environment variables
func getNamespaceFromEnv() (string, error) {
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		return "", fmt.Errorf("POD_NAMESPACE environment variable not set")
	}
	return ns, nil
}

// getCurrentNamespace returns the namespace the pod is running in.
// It tries multiple methods in order:
// 1. Reading from the service account token file
// 2. Getting the namespace from environment variables
// 3. Falling back to "default" if both methods fail
func getCurrentNamespace() string {
	// Method 1: Try to read from the service account namespace file
	ns, err := getNamespaceFromServiceAccount()
	if err == nil {
		return ns
	}

	// Method 2: Try to get the namespace from environment variables
	ns, err = getNamespaceFromEnv()
	if err == nil {
		return ns
	}

	// Method 3: Fall back to default
	return "default"
}

// AttachContainer implements runtime.Runtime.
func (c *Client) AttachContainer(ctx context.Context, containerID string) (io.WriteCloser, io.ReadCloser, error) {
	// AttachContainer attaches to a container in Kubernetes
	// This is a more complex operation in Kubernetes compared to Docker/Podman
	// as it requires setting up an exec session to the pod

	// First, we need to find the pod associated with the containerID (which is actually the statefulset name)
	namespace := getCurrentNamespace()
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", containerID),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find pod for container %s: %w", containerID, err)
	}

	if len(pods.Items) == 0 {
		return nil, nil, fmt.Errorf("no pods found for container %s", containerID)
	}

	// Use the first pod found
	podName := pods.Items[0].Name

	attachOpts := &corev1.PodAttachOptions{
		Container: containerID,
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}

	// Set up the attach request
	req := c.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(getCurrentNamespace()).
		SubResource("attach").
		VersionedParams(attachOpts, scheme.ParameterCodec)

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(fmt.Errorf("failed to create k8s config: %v", err))
	}
	// Create a SPDY executor
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create SPDY executor: %v", err)
	}

	fmt.Printf("Attaching to pod %s container %s...\n", podName, containerID)

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	//nolint:gosec // we don't check for an error here because it's not critical
	// and it also returns with an error of statuscode `0`'. perhaps someone
	// who knows the function a bit more can fix this.
	go func() {
		// wrap with retry so we can retry if the connection fails
		// Create exponential backoff with max 5 retries
		expBackoff := backoff.NewExponentialBackOff()
		backoffWithRetries := backoff.WithMaxRetries(expBackoff, 5)

		err := backoff.RetryNotify(func() error {
			return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
				Stdin:  stdinReader,
				Stdout: stdoutWriter,
				Stderr: stdoutWriter,
				Tty:    false,
			})
		}, backoffWithRetries, func(err error, duration time.Duration) {
			fmt.Printf("Error attaching to container %s: %v. Retrying in %s...\n", containerID, err, duration)
		})
		if err != nil {
			if statusErr, ok := err.(*errors.StatusError); ok {
				fmt.Printf("Kubernetes API error: Status=%s, Message=%s, Reason=%s, Code=%d\n",
					statusErr.ErrStatus.Status,
					statusErr.ErrStatus.Message,
					statusErr.ErrStatus.Reason,
					statusErr.ErrStatus.Code)

				if statusErr.ErrStatus.Code == 0 && statusErr.ErrStatus.Message == "" {
					fmt.Println("Empty status error - this typically means the connection was closed unexpectedly")
					fmt.Println("This often happens when the container terminates or doesn't read from stdin")
				}
			} else {
				fmt.Printf("Non-status error: %v\n", err)
			}
		}
	}()

	return stdinWriter, stdoutReader, nil
}

// ContainerLogs implements runtime.Runtime.
func (c *Client) ContainerLogs(ctx context.Context, containerID string) (string, error) {
	// In Kubernetes, containerID is the statefulset name
	namespace := getCurrentNamespace()

	// Get the pods associated with this statefulset
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", containerID),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods for statefulset %s: %w", containerID, err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for statefulset %s", containerID)
	}

	// Use the first pod
	podName := pods.Items[0].Name

	// Get logs from the pod
	logOptions := &corev1.PodLogOptions{
		Container:  containerID, // Use the container name within the pod
		Follow:     false,
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

// CreateContainer implements runtime.Runtime.
func (c *Client) CreateContainer(ctx context.Context,
	image string,
	containerName string,
	command []string,
	_ map[string]string,
	containerLabels map[string]string,
	_ *permissions.Profile,
	_ string,
	options *runtime.CreateContainerOptions) (string, error) {
	namespace := getCurrentNamespace()
	containerLabels["app"] = containerName
	containerLabels["vibetool"] = "true"

	attachStdio := options == nil || options.AttachStdio

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
			WithTemplate(corev1apply.PodTemplateSpec().
				WithLabels(containerLabels).
				WithSpec(corev1apply.PodSpec().
					WithContainers(corev1apply.Container().
						WithName(containerName).
						WithImage(image).
						WithArgs(command...).
						WithStdin(attachStdio).
						WithTTY(false)).
					WithRestartPolicy(corev1.RestartPolicyAlways))))

	// Apply the statefulset using server-side apply
	fieldManager := "vibetool-container-manager"
	createdStatefulSet, err := c.client.AppsV1().StatefulSets(namespace).
		Apply(ctx, statefulSetApply, metav1.ApplyOptions{
			FieldManager: fieldManager,
			Force:        true,
		})
	if err != nil {
		return "", fmt.Errorf("failed to apply statefulset: %v", err)
	}

	fmt.Printf("Applied statefulset %s\n", createdStatefulSet.Name)

	// Wait for the statefulset to be ready
	err = waitForStatefulSetReady(ctx, c.client, namespace, createdStatefulSet.Name)
	if err != nil {
		return createdStatefulSet.Name, fmt.Errorf("statefulset applied but failed to become ready: %w", err)
	}

	return createdStatefulSet.Name, nil
}

// GetContainerInfo implements runtime.Runtime.
func (c *Client) GetContainerInfo(ctx context.Context, containerID string) (runtime.ContainerInfo, error) {
	// In Kubernetes, containerID is the statefulset name
	namespace := getCurrentNamespace()

	// Get the statefulset
	statefulset, err := c.client.AppsV1().StatefulSets(namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return runtime.ContainerInfo{}, fmt.Errorf("statefulset %s not found", containerID)
		}
		return runtime.ContainerInfo{}, fmt.Errorf("failed to get statefulset %s: %w", containerID, err)
	}

	// Get the pods associated with this statefulset
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", containerID),
	})
	if err != nil {
		return runtime.ContainerInfo{}, fmt.Errorf("failed to list pods for statefulset %s: %w", containerID, err)
	}

	// Extract port mappings if available
	ports := make([]runtime.PortMapping, 0)
	if len(pods.Items) > 0 {
		for _, container := range pods.Items[0].Spec.Containers {
			for _, port := range container.Ports {
				ports = append(ports, runtime.PortMapping{
					ContainerPort: int(port.ContainerPort),
					HostPort:      int(port.HostPort),
					Protocol:      string(port.Protocol),
				})
			}
		}
	}

	// Determine status and state
	var status, state string
	if statefulset.Status.ReadyReplicas > 0 {
		status = "Running"
		state = "running"
	} else if statefulset.Status.Replicas > 0 {
		status = "Pending"
		state = "pending"
	} else {
		status = "Stopped"
		state = "stopped"
	}

	// Get the image from the pod template
	image := ""
	if len(statefulset.Spec.Template.Spec.Containers) > 0 {
		image = statefulset.Spec.Template.Spec.Containers[0].Image
	}

	return runtime.ContainerInfo{
		ID:      string(statefulset.UID),
		Name:    statefulset.Name,
		Image:   image,
		Status:  status,
		State:   state,
		Created: statefulset.CreationTimestamp.Time,
		Labels:  statefulset.Labels,
		Ports:   ports,
	}, nil
}

// ImageExists implements runtime.Runtime.
func (*Client) ImageExists(_ context.Context, imageName string) (bool, error) {
	// In Kubernetes, we can't directly check if an image exists in the cluster
	// without trying to use it. For simplicity, we'll assume the image exists
	// if it's a valid image name.
	//
	// In a more complete implementation, we could:
	// 1. Create a temporary pod with the image to see if it can be pulled
	// 2. Use the Kubernetes API to check node status for the image
	// 3. Use an external registry API to check if the image exists

	// For now, just return true if the image name is not empty
	if imageName == "" {
		return false, fmt.Errorf("image name cannot be empty")
	}

	// We could add more validation here if needed
	return true, nil
}

// IsContainerRunning implements runtime.Runtime.
func (c *Client) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	// In Kubernetes, containerID is the statefulset name
	namespace := getCurrentNamespace()

	// Get the statefulset
	statefulset, err := c.client.AppsV1().StatefulSets(namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, fmt.Errorf("statefulset %s not found", containerID)
		}
		return false, fmt.Errorf("failed to get statefulset %s: %w", containerID, err)
	}

	// Check if the statefulset has at least one ready replica
	return statefulset.Status.ReadyReplicas > 0, nil
}

// ListContainers implements runtime.Runtime.
func (c *Client) ListContainers(ctx context.Context) ([]runtime.ContainerInfo, error) {
	// Create label selector for vibetool containers
	labelSelector := "vibetool=true"

	// List pods with the vibetool label
	pods, err := c.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}

	// Convert to our ContainerInfo format
	result := make([]runtime.ContainerInfo, 0, len(pods.Items))
	for _, pod := range pods.Items {
		// Extract port mappings if available
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

		// Get container status
		status := UnknownStatus
		state := UnknownStatus
		if len(pod.Status.ContainerStatuses) > 0 {
			containerStatus := pod.Status.ContainerStatuses[0]
			if containerStatus.State.Running != nil {
				state = "running"
				status = "Running"
			} else if containerStatus.State.Waiting != nil {
				state = "waiting"
				status = containerStatus.State.Waiting.Reason
			} else if containerStatus.State.Terminated != nil {
				state = "terminated"
				status = containerStatus.State.Terminated.Reason
			}
		}

		result = append(result, runtime.ContainerInfo{
			ID:      string(pod.UID),
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

// PullImage implements runtime.Runtime.
func (*Client) PullImage(_ context.Context, imageName string) error {
	// In Kubernetes, we don't need to explicitly pull images as they are pulled
	// automatically when creating pods. The kubelet on each node will pull the
	// image when needed.

	// Log that we're skipping the pull operation
	fmt.Printf("Skipping explicit image pull for %s in Kubernetes - "+
		"images are pulled automatically when pods are created\n", imageName)

	return nil
}

// RemoveContainer implements runtime.Runtime.
func (c *Client) RemoveContainer(ctx context.Context, containerID string) error {
	// In Kubernetes, we remove a container by deleting the statefulset
	namespace := getCurrentNamespace()

	// Delete the statefulset
	deleteOptions := metav1.DeleteOptions{}
	err := c.client.AppsV1().StatefulSets(namespace).Delete(ctx, containerID, deleteOptions)
	if err != nil {
		if errors.IsNotFound(err) {
			// If the statefulset doesn't exist, that's fine
			fmt.Printf("Statefulset %s not found, nothing to remove\n", containerID)
			return nil
		}
		return fmt.Errorf("failed to delete statefulset %s: %w", containerID, err)
	}

	fmt.Printf("Deleted statefulset %s\n", containerID)
	return nil
}

// StartContainer implements runtime.Runtime.
func (*Client) StartContainer(_ context.Context, containerID string) error {
	// In Kubernetes, we don't need to explicitly start containers as they are started
	// automatically when created. However, we could scale up a statefulset if it's scaled to 0.
	fmt.Printf("Container %s is managed by Kubernetes and started automatically\n", containerID)
	return nil
}

// StopContainer implements runtime.Runtime.
func (*Client) StopContainer(_ context.Context, _ string) error {
	return nil
}

// waitForStatefulSetReady waits for a statefulset to be ready using the watch API
func waitForStatefulSetReady(ctx context.Context, clientset *kubernetes.Clientset, namespace, name string) error {
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

		fmt.Printf("Waiting for statefulset %s to be ready (%d/%d replicas ready)...\n",
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

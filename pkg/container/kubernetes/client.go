// Package kubernetes provides a client for the Kubernetes runtime
// including creating, starting, stopping, and retrieving container information.
package kubernetes

import (
	"context"
	"fmt"
	"io"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"

	"github.com/stacklok/vibetool/pkg/container/runtime"
	// Avoid import cycle
	"github.com/stacklok/vibetool/pkg/permissions"
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

// AttachContainer implements runtime.Runtime.
func (c *Client) AttachContainer(ctx context.Context, containerID string) (io.WriteCloser, io.ReadCloser, error) {
	// AttachContainer attaches to a container in Kubernetes
	// This is a more complex operation in Kubernetes compared to Docker/Podman
	// as it requires setting up an exec session to the pod

	// First, we need to find the pod associated with the containerID (which is actually the deployment name)
	pods, err := c.client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
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
		Namespace("default").
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
func (*Client) ContainerLogs(_ context.Context, _ string) (string, error) {
	return "", nil
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
	_ *runtime.CreateContainerOptions) (string, error) {

	fmt.Printf("Checking if container exists...\n")
	// Check if a deployment with this name already exists
	_, err := c.client.AppsV1().Deployments("default").Get(ctx, containerName, metav1.GetOptions{})
	if err == nil {
		return "", fmt.Errorf("deployment %s already exists", containerName)
	}
	fmt.Printf("Container doesn't exist, creating container %s from image %s...\n", containerName, image)

	containerLabels["app"] = containerName
	containerLabels["vibetool"] = "true"

	// Create a deployment with the given image and args
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   containerName,
			Labels: containerLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": containerName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: containerLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  containerName,
							Image: image,
							Args:  command,
							Stdin: true,
							TTY:   false,
						},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
		},
	}

	// Create the deployment in the default namespace
	createdDeployment, err := c.client.AppsV1().Deployments("default").Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create deployment: %v %s", err, "this is a test")
	}

	fmt.Printf("Created deployment %s\n", createdDeployment.Name)
	// remove this later on when we have a better way to wait for the deployment to be ready
	time.Sleep(10 * time.Second)
	return createdDeployment.Name, nil
}

// GetContainerIP implements runtime.Runtime.
func (*Client) GetContainerIP(_ context.Context, _ string) (string, error) {
	panic("unimplemented")
}

// GetContainerInfo implements runtime.Runtime.
func (*Client) GetContainerInfo(_ context.Context, _ string) (runtime.ContainerInfo, error) {
	panic("unimplemented")
}

// ImageExists implements runtime.Runtime.
func (*Client) ImageExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// IsContainerRunning implements runtime.Runtime.
func (*Client) IsContainerRunning(_ context.Context, _ string) (bool, error) {
	return true, nil
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
		status := "unknown"
		state := "unknown"
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
func (*Client) PullImage(_ context.Context, _ string) error {
	return nil
}

// RemoveContainer implements runtime.Runtime.
func (*Client) RemoveContainer(_ context.Context, _ string) error {
	return nil
}

// StartContainer implements runtime.Runtime.
func (*Client) StartContainer(_ context.Context, _ string) error {
	return nil
}

// StopContainer implements runtime.Runtime.
func (*Client) StopContainer(_ context.Context, _ string) error {
	return nil
}

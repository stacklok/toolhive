package kubernetes

import (
	"context"
	"fmt"
	"io"

	"github.com/stacklok/vibetool/pkg/container/runtime"
	"github.com/stacklok/vibetool/pkg/permissions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client implements the Runtime interface for container operations
type Client struct {
	runtimeType runtime.Type
	client      *kubernetes.Clientset
}

// NewClient creates a new container client
func NewClient(ctx context.Context) (*Client, error) {
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
	panic("unimplemented")
}

// ContainerLogs implements runtime.Runtime.
func (c *Client) ContainerLogs(ctx context.Context, containerID string) (string, error) {
	panic("unimplemented")
}

// CreateContainer implements runtime.Runtime.
func (c *Client) CreateContainer(ctx context.Context, image string, name string, command []string, envVars map[string]string, labels map[string]string, permissionProfile *permissions.Profile, transportType string, options *runtime.CreateContainerOptions) (string, error) {
	panic("unimplemented")
}

// GetContainerIP implements runtime.Runtime.
func (c *Client) GetContainerIP(ctx context.Context, containerID string) (string, error) {
	panic("unimplemented")
}

// GetContainerInfo implements runtime.Runtime.
func (c *Client) GetContainerInfo(ctx context.Context, containerID string) (runtime.ContainerInfo, error) {
	panic("unimplemented")
}

// ImageExists implements runtime.Runtime.
func (c *Client) ImageExists(ctx context.Context, image string) (bool, error) {
	panic("unimplemented")
}

// IsContainerRunning implements runtime.Runtime.
func (c *Client) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	panic("unimplemented")
}

// ListContainers implements runtime.Runtime.
func (c *Client) ListContainers(ctx context.Context) ([]runtime.ContainerInfo, error) {
	pods, err := c.client.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))

	for _, pod := range pods.Items {
		fmt.Printf("Pod: %s\n", pod.Name)
	}
	fmt.Printf("Test, worked!")
	return nil, nil
}

// PullImage implements runtime.Runtime.
func (c *Client) PullImage(ctx context.Context, image string) error {
	panic("unimplemented")
}

// RemoveContainer implements runtime.Runtime.
func (c *Client) RemoveContainer(ctx context.Context, containerID string) error {
	panic("unimplemented")
}

// StartContainer implements runtime.Runtime.
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	panic("unimplemented")
}

// StopContainer implements runtime.Runtime.
func (c *Client) StopContainer(ctx context.Context, containerID string) error {
	panic("unimplemented")
}

// Package kubernetes provides a client for the Kubernetes runtime
// including creating, starting, stopping, and retrieving container information.
package kubernetes

import (
	"context"
	"fmt"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stacklok/vibetool/pkg/container/runtime"
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
func (*Client) AttachContainer(_ context.Context, _ string) (io.WriteCloser, io.ReadCloser, error) {
	panic("unimplemented")
}

// ContainerLogs implements runtime.Runtime.
func (*Client) ContainerLogs(_ context.Context, _ string) (string, error) {
	panic("unimplemented")
}

// CreateContainer implements runtime.Runtime.
func (*Client) CreateContainer(_ context.Context,
	_ string,
	_ string,
	_ []string,
	_ map[string]string,
	_ map[string]string,
	_ *permissions.Profile,
	_ string,
	_ *runtime.CreateContainerOptions) (string, error) {
	panic("unimplemented")
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
	panic("unimplemented")
}

// IsContainerRunning implements runtime.Runtime.
func (*Client) IsContainerRunning(_ context.Context, _ string) (bool, error) {
	panic("unimplemented")
}

// ListContainers implements runtime.Runtime.
func (c *Client) ListContainers(_ context.Context) ([]runtime.ContainerInfo, error) {
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
func (*Client) PullImage(_ context.Context, _ string) error {
	panic("unimplemented")
}

// RemoveContainer implements runtime.Runtime.
func (*Client) RemoveContainer(_ context.Context, _ string) error {
	panic("unimplemented")
}

// StartContainer implements runtime.Runtime.
func (*Client) StartContainer(_ context.Context, _ string) error {
	panic("unimplemented")
}

// StopContainer implements runtime.Runtime.
func (*Client) StopContainer(_ context.Context, _ string) error {
	panic("unimplemented")
}

package kubernetes

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Client provides a wrapper around the controller-runtime client with
// convenience methods for common Kubernetes operations.
type Client struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewClient creates a new Client instance with the provided controller-runtime client and scheme.
// The scheme is required for operations that need to set owner references.
func NewClient(c client.Client, scheme *runtime.Scheme) *Client {
	return &Client{
		client: c,
		scheme: scheme,
	}
}

// GetClient returns the underlying controller-runtime client.
// This is useful when you need direct access to the client for operations
// not covered by the convenience methods.
func (c *Client) GetClient() client.Client {
	return c.client
}

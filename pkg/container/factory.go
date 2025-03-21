package container

import (
	"context"
)

// Factory creates container runtimes
type Factory struct{}

// NewFactory creates a new container factory
func NewFactory() *Factory {
	return &Factory{}
}

// Create creates a container runtime
func (f *Factory) Create(ctx context.Context) (Runtime, error) {
	// Try to create a container client
	client, err := NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return client, nil
}
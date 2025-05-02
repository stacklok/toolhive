// Package factory provides a factory for creating ToolHive API clients.
package factory

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/api/local"
	"github.com/StacklokLabs/toolhive/pkg/container"
	rt "github.com/StacklokLabs/toolhive/pkg/container/runtime"
)

// ClientType represents the type of client to create.
type ClientType string

const (
	// LocalClientType represents a local client.
	LocalClientType ClientType = "local"
	// RemoteClientType represents a remote client.
	RemoteClientType ClientType = "remote"
)

// Factory is a factory for creating ToolHive API clients.
type Factory struct {
	// clientType is the type of client to create
	clientType ClientType
	// runtime is the container runtime to use for container operations
	runtime rt.Runtime
	// debug indicates whether debug mode is enabled
	debug bool
}

// Option is a function that configures a Factory.
type Option func(*Factory) error

// WithClientType sets the type of client to create.
func WithClientType(clientType ClientType) Option {
	return func(f *Factory) error {
		f.clientType = clientType
		return nil
	}
}

// WithRuntime sets the container runtime to use.
func WithRuntime(runtime rt.Runtime) Option {
	return func(f *Factory) error {
		f.runtime = runtime
		return nil
	}
}

// WithDebug enables debug mode for the factory.
func WithDebug(debug bool) Option {
	return func(f *Factory) error {
		f.debug = debug
		return nil
	}
}

// New creates a new Factory with the provided options.
func New(opts ...Option) (*Factory, error) {
	// Create a new factory with default values
	f := &Factory{
		clientType: LocalClientType,
		debug:      false,
	}

	// Apply options
	for _, opt := range opts {
		if err := opt(f); err != nil {
			return nil, fmt.Errorf("failed to apply factory option: %w", err)
		}
	}

	return f, nil
}

// Create creates a new ToolHive API client with the factory's configuration.
func (f *Factory) Create(ctx context.Context) (api.Client, error) {
	// If no runtime was provided, create a default one
	if f.runtime == nil {
		runtime, err := container.NewFactory().Create(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create container runtime: %w", err)
		}
		f.runtime = runtime
	}

	// Create the client based on the client type
	switch f.clientType {
	case LocalClientType:
		return local.New(ctx, local.WithRuntime(f.runtime), local.WithDebug(f.debug))
	case RemoteClientType:
		return nil, fmt.Errorf("remote client not implemented yet")
	default:
		return nil, fmt.Errorf("unknown client type: %s", f.clientType)
	}
}

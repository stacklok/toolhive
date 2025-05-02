// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/container"
	rt "github.com/StacklokLabs/toolhive/pkg/container/runtime"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

// Client is the local implementation of the api.Client interface.
type Client struct {
	// runtime is the container runtime to use for container operations
	runtime rt.Runtime
	// debug indicates whether debug mode is enabled
	debug bool
	// serverAPI is the implementation of the ServerAPI interface
	serverAPI api.ServerAPI
	// secretAPI is the implementation of the SecretAPI interface
	secretAPI api.SecretAPI
	// configAPI is the implementation of the ConfigAPI interface
	configAPI api.ConfigAPI
	// versionAPI is the implementation of the VersionAPI interface
	versionAPI api.VersionAPI
}

// Option is a function that configures a Client.
type Option func(*Client) error

// WithDebug enables debug mode for the client.
func WithDebug(debug bool) Option {
	return func(c *Client) error {
		c.debug = debug
		return nil
	}
}

// WithRuntime sets the container runtime to use.
func WithRuntime(runtime rt.Runtime) Option {
	return func(c *Client) error {
		c.runtime = runtime
		return nil
	}
}

// New creates a new local ToolHive API client with the provided options.
func New(ctx context.Context, opts ...Option) (api.Client, error) {
	// Create a new client with default values
	c := &Client{
		debug: false,
	}

	// Apply options
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("failed to apply client option: %w", err)
		}
	}

	// If no runtime was provided, create a default one
	if c.runtime == nil {
		runtime, err := container.NewFactory().Create(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create container runtime: %w", err)
		}
		c.runtime = runtime
	}

	// Initialize API implementations
	c.serverAPI = NewServer(c.runtime, c.debug)
	c.secretAPI = NewSecret(c.debug)
	c.configAPI = NewConfig(c.debug)
	c.versionAPI = NewVersion(c.debug)

	return c, nil
}

// Server returns the ServerAPI for managing MCP servers.
func (c *Client) Server() api.ServerAPI {
	return c.serverAPI
}

// Secret returns the SecretAPI for managing secrets.
func (c *Client) Secret() api.SecretAPI {
	return c.secretAPI
}

// Config returns the ConfigAPI for managing configuration.
func (c *Client) Config() api.ConfigAPI {
	return c.configAPI
}

// Version returns the VersionAPI for getting version information.
func (c *Client) Version() api.VersionAPI {
	return c.versionAPI
}

// Close releases any resources held by the client.
func (*Client) Close() error {
	// Currently, there are no resources to release
	return nil
}

// LogDebug logs a debug message if debug mode is enabled.
func (c *Client) LogDebug(format string, args ...interface{}) {
	if c.debug {
		logger.Log.Infof(format, args...)
	}
}

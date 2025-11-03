// Package transport provides utilities for handling different transport modes
// for communication between the client and MCP server.
package transport

import (
	"github.com/stacklok/toolhive/pkg/transport/errors"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Factory creates transports
type Factory struct{}

// NewFactory creates a new transport factory
func NewFactory() *Factory {
	return &Factory{}
}

// Option is a function that configures a transport
type Option func(types.Transport) error

// WithContainerName returns an option that sets the container name on a transport
func WithContainerName(containerName string) Option {
	return func(t types.Transport) error {
		if setter, ok := t.(interface{ setContainerName(string) }); ok {
			setter.setContainerName(containerName)
		}
		return nil
	}
}

// WithTargetURI returns an option that sets the target URI on a transport
func WithTargetURI(targetURI string) Option {
	return func(t types.Transport) error {
		if setter, ok := t.(interface{ setTargetURI(string) }); ok {
			setter.setTargetURI(targetURI)
		}
		return nil
	}
}

// Create creates a transport based on the provided configuration
func (*Factory) Create(config types.Config, opts ...Option) (types.Transport, error) {
	var tr types.Transport

	switch config.Type {
	case types.TransportTypeStdio:
		tr = NewStdioTransport(
			config.Host, config.ProxyPort, config.Deployer, config.Debug, config.TrustProxyHeaders,
			config.PrometheusHandler, config.Middlewares...,
		)
		tr.(*StdioTransport).SetProxyMode(config.ProxyMode)
	case types.TransportTypeSSE:
		tr = NewHTTPTransport(
			types.TransportTypeSSE,
			config.Host,
			config.ProxyPort,
			config.TargetPort,
			config.Deployer,
			config.Debug,
			config.TargetHost,
			config.AuthInfoHandler,
			config.PrometheusHandler,
			config.Middlewares...,
		)
	case types.TransportTypeStreamableHTTP:
		tr = NewHTTPTransport(
			types.TransportTypeStreamableHTTP,
			config.Host,
			config.ProxyPort,
			config.TargetPort,
			config.Deployer,
			config.Debug,
			config.TargetHost,
			config.AuthInfoHandler,
			config.PrometheusHandler,
			config.Middlewares...,
		)
	case types.TransportTypeInspector:
		// HTTP transport is not implemented yet
		return nil, errors.ErrUnsupportedTransport
	default:
		return nil, errors.ErrUnsupportedTransport
	}

	// Apply options to the transport
	for _, opt := range opts {
		if err := opt(tr); err != nil {
			return nil, err
		}
	}

	return tr, nil
}

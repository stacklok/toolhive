// Package transport provides utilities for handling different transport modes
// for communication between the client and MCP server.
package transport

import (
	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/transport/errors"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Factory creates transports
type Factory struct {
	logger *zap.SugaredLogger
}

// NewFactory creates a new transport factory
func NewFactory(logger *zap.SugaredLogger) *Factory {
	return &Factory{logger}
}

// Create creates a transport based on the provided configuration
func (f *Factory) Create(config types.Config) (types.Transport, error) {
	switch config.Type {
	case types.TransportTypeStdio:
		tr := NewStdioTransport(
			config.Host, config.ProxyPort, config.Deployer, config.Debug, config.PrometheusHandler, f.logger, config.Middlewares...,
		)
		tr.SetProxyMode(config.ProxyMode)
		return tr, nil
	case types.TransportTypeSSE:
		return NewHTTPTransport(
			types.TransportTypeSSE,
			config.Host,
			config.ProxyPort,
			config.TargetPort,
			config.Deployer,
			config.Debug,
			config.TargetHost,
			config.AuthInfoHandler,
			config.PrometheusHandler,
			f.logger,
			config.Middlewares...,
		), nil
	case types.TransportTypeStreamableHTTP:
		return NewHTTPTransport(
			types.TransportTypeStreamableHTTP,
			config.Host,
			config.ProxyPort,
			config.TargetPort,
			config.Deployer,
			config.Debug,
			config.TargetHost,
			config.AuthInfoHandler,
			config.PrometheusHandler,
			f.logger,
			config.Middlewares...,
		), nil
	case types.TransportTypeInspector:
		// HTTP transport is not implemented yet
		return nil, errors.ErrUnsupportedTransport
	default:
		return nil, errors.ErrUnsupportedTransport
	}
}

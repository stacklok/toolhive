// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/auth"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/transport/proxy/transparent"
	"github.com/StacklokLabs/toolhive/pkg/transport/types"
)

// Proxy proxies to a running MCP server.
func (s *Server) Proxy(ctx context.Context, name string, opts *api.ProxyOptions) error {
	// Get the container info for the specified name
	container, err := s.getContainerInfo(ctx, name)
	if err != nil {
		return err
	}

	// Get the target port from the container labels if not provided
	targetPort := opts.TargetPort
	if targetPort == 0 {
		port, err := labels.GetPort(container.Labels)
		if err != nil {
			return fmt.Errorf("failed to get port from container labels: %v", err)
		}
		targetPort = port
	}

	// Get the target host from options or use default
	targetHost := opts.TargetHost
	if targetHost == "" {
		targetHost = "localhost"
	}

	// Get the port to listen on from options or use default
	port := opts.Port
	if port == 0 {
		port = targetPort // Use the same port as the target port
	}

	// Create a target URI
	targetURI := fmt.Sprintf("http://%s:%d", targetHost, targetPort)

	// Create middlewares slice
	var middlewares []types.Middleware

	// Check if OIDC validation is enabled
	if opts.OIDCConfig != nil {
		s.logDebug("OIDC validation enabled")

		// Create JWT validator
		jwtValidator, err := auth.NewJWTValidator(ctx, *opts.OIDCConfig)
		if err != nil {
			return fmt.Errorf("failed to create JWT validator: %v", err)
		}

		// Add JWT validation middleware
		middlewares = append(middlewares, jwtValidator.Middleware)
	} else {
		s.logDebug("OIDC validation disabled")
	}

	// Create a transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(port, name, targetURI, middlewares...)

	// Start the proxy
	s.logDebug("Setting up transparent proxy to forward from host port %d to %s", port, targetURI)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	s.logDebug("Transparent proxy started for server %s on port %d -> %s", name, port, targetURI)
	return nil
}

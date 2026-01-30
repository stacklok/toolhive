// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package helpers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/telemetry"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

// NewBackend creates a test backend with sensible defaults.
// Use functional options to customize.
func NewBackend(id string, opts ...func(*vmcptypes.Backend)) vmcptypes.Backend {
	b := vmcptypes.Backend{
		ID:            id,
		Name:          id,
		BaseURL:       "http://localhost:8080/mcp",
		TransportType: "streamable-http",
		HealthStatus:  vmcptypes.BackendHealthy,
		Metadata:      make(map[string]string),
	}
	for _, opt := range opts {
		opt(&b)
	}
	return b
}

// WithURL sets the backend URL.
func WithURL(url string) func(*vmcptypes.Backend) {
	return func(b *vmcptypes.Backend) {
		b.BaseURL = url
	}
}

// WithAuth configures authentication with a typed auth strategy.
func WithAuth(authConfig *authtypes.BackendAuthStrategy) func(*vmcptypes.Backend) {
	return func(b *vmcptypes.Backend) {
		b.AuthConfig = authConfig
	}
}

// WithMetadata adds a metadata key-value pair.
func WithMetadata(key, value string) func(*vmcptypes.Backend) {
	return func(b *vmcptypes.Backend) {
		b.Metadata[key] = value
	}
}

// VMCPServerOption is a functional option for configuring a vMCP test server.
type VMCPServerOption func(*vmcpServerConfig)

// vmcpServerConfig holds configuration for creating a test vMCP server.
type vmcpServerConfig struct {
	conflictStrategy  string
	prefixFormat      string
	workflowDefs      map[string]*composer.WorkflowDefinition
	telemetryProvider *telemetry.Provider
}

// WithPrefixConflictResolution configures prefix-based conflict resolution.
func WithPrefixConflictResolution(format string) VMCPServerOption {
	return func(c *vmcpServerConfig) {
		c.conflictStrategy = "prefix"
		c.prefixFormat = format
	}
}

// WithWorkflowDefinitions configures composite tool workflow definitions.
func WithWorkflowDefinitions(defs map[string]*composer.WorkflowDefinition) VMCPServerOption {
	return func(c *vmcpServerConfig) {
		c.workflowDefs = defs
	}
}

// WithTelemetryProvider configures the telemetry provider.
func WithTelemetryProvider(provider *telemetry.Provider) VMCPServerOption {
	return func(c *vmcpServerConfig) {
		c.telemetryProvider = provider
	}
}

// getFreePort returns an available TCP port on localhost.
// This is used for parallel test execution to avoid port conflicts.
func getFreePort(tb testing.TB) int {
	tb.Helper()

	// Listen on port 0 to get a random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err, "failed to get free port")
	defer func() {
		// Error ignored in test cleanup
		_ = listener.Close()
	}()

	// Extract the port number from the listener's address
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		tb.Fatalf("failed to get TCP address from listener")
	}
	return addr.Port
}

// NewVMCPServer creates a vMCP server for testing with sensible defaults.
// The server is automatically started and will be ready when this function returns.
// Use functional options to customize behavior.
//
// Example:
//
//	server := testkit.NewVMCPServer(ctx, t, backends,
//	    testkit.WithPrefixConflictResolution("{workload}_"),
//	)
//	defer server.Shutdown(ctx)
func NewVMCPServer(
	ctx context.Context, tb testing.TB, backends []vmcptypes.Backend, opts ...VMCPServerOption,
) *vmcpserver.Server {
	tb.Helper()

	// Default configuration
	config := &vmcpServerConfig{
		conflictStrategy: "prefix",
		prefixFormat:     "{workload}_",
	}

	// Apply options
	for _, opt := range opts {
		opt(config)
	}

	// Create outgoing auth registry with all strategies registered
	outgoingRegistry, err := factory.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	require.NoError(tb, err)

	// Create backend client
	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	require.NoError(tb, err)

	// Create conflict resolver based on strategy
	var conflictResolver aggregator.ConflictResolver
	switch config.conflictStrategy {
	case "prefix":
		conflictResolver = aggregator.NewPrefixConflictResolver(config.prefixFormat)
	default:
		conflictResolver = aggregator.NewPrefixConflictResolver(config.prefixFormat)
	}

	// Create aggregator
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, nil, nil)

	// Create discovery manager
	discoveryMgr, err := discovery.NewManager(agg)
	require.NoError(tb, err)

	// Create router
	rtr := router.NewDefaultRouter()

	// Create immutable backend registry for tests (backends don't change during test execution)
	backendRegistry := vmcptypes.NewImmutableRegistry(backends)

	// Create vMCP server with test-specific defaults
	vmcpServer, err := vmcpserver.New(ctx, &vmcpserver.Config{
		Name:              "test-vmcp",
		Version:           "1.0.0",
		Host:              "127.0.0.1",
		Port:              getFreePort(tb), // Get a random available port for parallel test execution
		AuthMiddleware:    auth.AnonymousMiddleware,
		TelemetryProvider: config.telemetryProvider,
	}, rtr, backendClient, discoveryMgr, backendRegistry, config.workflowDefs)
	require.NoError(tb, err, "failed to create vMCP server")

	// Start server automatically
	// Use the passed-in context to ensure proper cancellation propagation
	go func() {
		if err := vmcpServer.Start(ctx); err != nil {
			select {
			case <-ctx.Done():
				// Context cancelled, ignore error
			default:
				tb.Errorf("vMCP server error: %v", err)
			}
		}
	}()

	// Wait for server to be ready (with 5 second timeout)
	select {
	case <-vmcpServer.Ready():
		tb.Logf("vMCP server ready at: http://%s/mcp", vmcpServer.Address())
	case <-time.After(5 * time.Second):
		tb.Fatal("vMCP server failed to start within 5 seconds")
	}

	return vmcpServer
}

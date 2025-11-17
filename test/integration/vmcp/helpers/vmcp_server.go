package helpers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/env"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
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
		AuthMetadata:  make(map[string]any),
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

// WithAuth configures authentication.
func WithAuth(strategy string, metadata map[string]any) func(*vmcptypes.Backend) {
	return func(b *vmcptypes.Backend) {
		b.AuthStrategy = strategy
		b.AuthMetadata = metadata
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
	conflictStrategy string
	prefixFormat     string
}

// WithPrefixConflictResolution configures prefix-based conflict resolution.
func WithPrefixConflictResolution(format string) VMCPServerOption {
	return func(c *vmcpServerConfig) {
		c.conflictStrategy = "prefix"
		c.prefixFormat = format
	}
}

// getFreePort returns an available TCP port on localhost.
// This is used for parallel test execution to avoid port conflicts.
func getFreePort(tb testing.TB) int {
	tb.Helper()

	// Listen on port 0 to get a random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err, "failed to get free port")
	defer listener.Close()

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

	// Create outgoing auth registry and register strategies used by backends
	outgoingRegistry, err := factory.NewOutgoingAuthRegistry(ctx, nil, &env.OSReader{})
	require.NoError(tb, err)

	// Scan backends to determine which strategies need to be registered
	// This is needed because we pass nil config to NewOutgoingAuthRegistry (which only registers unauthenticated)
	// but backends may use other strategies like header_injection
	strategyTypes := make(map[string]struct{})
	for _, backend := range backends {
		if backend.AuthStrategy != "" && backend.AuthStrategy != "unauthenticated" {
			strategyTypes[backend.AuthStrategy] = struct{}{}
		}
	}

	// Register additional strategies found in backends
	for strategyType := range strategyTypes {
		var strategy vmcpauth.Strategy
		switch strategyType {
		case strategies.StrategyTypeHeaderInjection:
			strategy = strategies.NewHeaderInjectionStrategy()
		case strategies.StrategyTypeTokenExchange:
			strategy = strategies.NewTokenExchangeStrategy(&env.OSReader{})
		default:
			tb.Fatalf("unknown auth strategy type: %s", strategyType)
		}

		err = outgoingRegistry.RegisterStrategy(strategyType, strategy)
		require.NoError(tb, err, "failed to register strategy %s", strategyType)
	}

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
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, nil)

	// Create discovery manager
	discoveryMgr, err := discovery.NewManager(agg)
	require.NoError(tb, err)

	// Create router
	rtr := router.NewDefaultRouter()

	// Create vMCP server with test-specific defaults
	vmcpServer, err := vmcpserver.New(&vmcpserver.Config{
		Name:           "test-vmcp",
		Version:        "1.0.0",
		Host:           "127.0.0.1",
		Port:           getFreePort(tb), // Get a random available port for parallel test execution
		AuthMiddleware: auth.AnonymousMiddleware,
	}, rtr, backendClient, discoveryMgr, backends, nil) // nil for workflowDefs in tests
	require.NoError(tb, err, "failed to create vMCP server")

	// Start server automatically
	// Use the passed-in context to ensure proper cancellation propagation
	go func() {
		if err := vmcpServer.Start(ctx); err != nil && ctx.Err() == nil {
			// Only report error if context wasn't cancelled
			tb.Errorf("vMCP server error: %v", err)
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

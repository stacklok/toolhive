// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package helpers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/telemetry"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpcore "github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
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
	conflictStrategy   string
	prefixFormat       string
	workflowDefs       map[string]*composer.WorkflowDefinition
	telemetryProvider  *telemetry.Provider
	passthroughHeaders []string
	sessionTTL         time.Duration
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

// WithPassthroughHeaders sets the vMCP server's PassthroughHeaders allowlist,
// matching the production wiring in pkg/vmcp/cli/serve.go.
//
// When this option is used, NewVMCPServer passes the header names to
// vmcpserver.ServerConfig.PassthroughHeaders, which installs
// headerforward.CaptureMiddleware so allowlisted headers are captured into the
// request context and forwarded to backends.
func WithPassthroughHeaders(headers ...string) VMCPServerOption {
	return func(c *vmcpServerConfig) {
		c.passthroughHeaders = headers
	}
}

// WithSessionTTL overrides the server's session time-to-live (default 30m).
// A short TTL is useful for sliding-TTL / eviction regression tests. A zero
// value leaves the default in place.
func WithSessionTTL(ttl time.Duration) VMCPServerOption {
	return func(c *vmcpServerConfig) {
		c.sessionTTL = ttl
	}
}

// getFreePort returns an available TCP port on localhost.
func getFreePort(tb testing.TB) int {
	tb.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err, "failed to get free port")
	defer func() { _ = listener.Close() }()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		tb.Fatalf("failed to get TCP address from listener")
	}
	return addr.Port
}

// NewVMCPServer creates a vMCP server for testing using the Serve path (core.New +
// Serve). The server is automatically started and ready when this function returns.
// Use functional options to customize behavior.
//
// The Serve path is used (rather than the legacy server.New path) so that
// integration tests exercise the production code path that routes tool/resource
// calls through core.VMCP. This is required for testing per-request
// passthrough header forwarding (#5560).
func NewVMCPServer(
	ctx context.Context, tb testing.TB, backends []vmcptypes.Backend, opts ...VMCPServerOption,
) *vmcpserver.Server {
	tb.Helper()

	config := &vmcpServerConfig{
		conflictStrategy: "prefix",
		prefixFormat:     "{workload}_",
	}
	for _, opt := range opts {
		opt(config)
	}

	outgoingRegistry, err := vmcpauth.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	require.NoError(tb, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	require.NoError(tb, err)

	var conflictResolver aggregator.ConflictResolver
	switch config.conflictStrategy {
	case "prefix":
		conflictResolver = aggregator.NewPrefixConflictResolver(config.prefixFormat)
	default:
		conflictResolver = aggregator.NewPrefixConflictResolver(config.prefixFormat)
	}

	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, nil, nil)
	rtr := router.NewSessionRouter(&vmcptypes.RoutingTable{})
	backendRegistry := vmcptypes.NewImmutableRegistry(backends)

	// Build the core VMCP — the single authoritative aggregation on the Serve path.
	coreVMCP, err := vmcpcore.New(&vmcpcore.Config{
		Aggregator:        agg,
		Router:            rtr,
		BackendRegistry:   backendRegistry,
		BackendClient:     backendClient,
		WorkflowDefs:      config.workflowDefs,
		TelemetryProvider: config.telemetryProvider,
	})
	require.NoError(tb, err, "failed to create core VMCP")

	// The session factory must NOT use WithAggregator on the Serve path: the core
	// is the single source of truth for capability aggregation. A factory with its
	// own aggregator would produce a second, divergent capability set that the Serve
	// path discards — exactly the double-aggregation AC2 forbids.
	sessionFactory := vmcpsession.NewSessionFactory(outgoingRegistry)

	serverCfg := &vmcpserver.ServerConfig{
		Name:               "test-vmcp",
		Version:            "1.0.0",
		Host:               "127.0.0.1",
		Port:               getFreePort(tb),
		EndpointPath:       "/mcp",
		SessionTTL:         30 * time.Minute,
		AuthMiddleware:     auth.AnonymousMiddleware,
		PassthroughHeaders: config.passthroughHeaders,
		BackendRegistry:    backendRegistry,
		TelemetryProvider:  config.telemetryProvider,
		SessionManagerConfig: &sessionmanager.FactoryConfig{
			Base: sessionFactory,
		},
	}
	if config.sessionTTL > 0 {
		serverCfg.SessionTTL = config.sessionTTL
	}

	vmcpServer, err := vmcpserver.Serve(ctx, coreVMCP, serverCfg)
	require.NoError(tb, err, "failed to create vMCP server")

	go func() {
		if err := vmcpServer.Start(ctx); err != nil {
			select {
			case <-ctx.Done():
			default:
				tb.Errorf("vMCP server error: %v", err)
			}
		}
	}()

	select {
	case <-vmcpServer.Ready():
		tb.Logf("vMCP server ready at: http://%s/mcp", vmcpServer.Address())
	case <-time.After(5 * time.Second):
		tb.Fatal("vMCP server failed to start within 5 seconds")
	}

	return vmcpServer
}

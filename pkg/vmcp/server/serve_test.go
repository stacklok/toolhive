// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// stubVMCP is a no-op core.VMCP for Serve tests. The Serve skeleton never invokes
// its capability methods; Close records invocation so the shutdown wiring can be
// asserted.
type stubVMCP struct {
	closed bool
}

var _ core.VMCP = (*stubVMCP)(nil)

func (*stubVMCP) ListTools(context.Context, *auth.Identity) ([]vmcp.Tool, error) { return nil, nil }
func (*stubVMCP) CallTool(
	context.Context, *auth.Identity, string, map[string]any, map[string]any,
) (*vmcp.ToolCallResult, error) {
	return nil, nil
}
func (*stubVMCP) ListResources(context.Context, *auth.Identity) ([]vmcp.Resource, error) {
	return nil, nil
}
func (*stubVMCP) ReadResource(context.Context, *auth.Identity, string) (*vmcp.ResourceReadResult, error) {
	return nil, nil
}
func (*stubVMCP) ListPrompts(context.Context, *auth.Identity) ([]vmcp.Prompt, error) { return nil, nil }
func (*stubVMCP) GetPrompt(
	context.Context, *auth.Identity, string, map[string]any,
) (*vmcp.PromptGetResult, error) {
	return nil, nil
}
func (*stubVMCP) LookupTool(context.Context, *auth.Identity, string) (*vmcp.Tool, error) {
	return nil, nil
}
func (*stubVMCP) LookupResource(context.Context, *auth.Identity, string) (*vmcp.Resource, error) {
	return nil, nil
}
func (*stubVMCP) LookupPrompt(context.Context, *auth.Identity, string) (*vmcp.Prompt, error) {
	return nil, nil
}
func (s *stubVMCP) Close() error { s.closed = true; return nil }

// stubWatcher is a non-nil Watcher for the drift-guard test; its behavior is not
// exercised there.
type stubWatcher struct{}

func (stubWatcher) WaitForCacheSync(context.Context) bool { return true }

// stubServeReporter is a non-nil vmcpstatus.Reporter for the drift-guard test.
type stubServeReporter struct{}

func (stubServeReporter) ReportStatus(context.Context, *vmcp.Status) error { return nil }
func (stubServeReporter) Start(context.Context) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

func TestServeAppliesTransportDefaults(t *testing.T) {
	t.Parallel()

	// Empty transport fields exercise every default; Port is left zero.
	cfg := &ServerConfig{SessionFactory: testMinimalFactory()}

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NotNil(t, srv.MCPServer())

	// Defaults mirror server.New and are applied to the server's own config,
	// leaving the caller's ServerConfig untouched.
	assert.Equal(t, "toolhive-vmcp", srv.config.Name)
	assert.Equal(t, "0.1.0", srv.config.Version)
	assert.Equal(t, "127.0.0.1", srv.config.Host)
	assert.Equal(t, "/mcp", srv.config.EndpointPath)
	assert.Equal(t, defaultSessionTTL, srv.config.SessionTTL)
	assert.Equal(t, 0, srv.config.Port) // Port 0 => OS-assigned

	assert.Empty(t, cfg.Host, "caller config must not be mutated")

	// Address reflects the defaulted host and unassigned port (no listener yet).
	assert.Equal(t, "127.0.0.1:0", srv.Address())
}

func TestServePreservesExplicitConfig(t *testing.T) {
	t.Parallel()

	cfg := &ServerConfig{
		Name:           "custom",
		Version:        "9.9.9",
		Host:           "0.0.0.0",
		Port:           8080,
		EndpointPath:   "/rpc",
		GroupRef:       "my-group",
		SessionFactory: testMinimalFactory(),
	}

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	assert.Equal(t, "custom", srv.config.Name)
	assert.Equal(t, "9.9.9", srv.config.Version)
	assert.Equal(t, "0.0.0.0", srv.config.Host)
	assert.Equal(t, 8080, srv.config.Port)
	assert.Equal(t, "/rpc", srv.config.EndpointPath)
	assert.Equal(t, "my-group", srv.config.GroupRef)
}

func TestServeHandlerRegistersUnauthenticatedRoutes(t *testing.T) {
	t.Parallel()

	cfg := &ServerConfig{SessionFactory: testMinimalFactory()}
	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)

	// The unauthenticated routes are direct mux entries and respond without the
	// not-yet-relocated authenticated MCP chain or its dependencies.
	for _, path := range []string{"/health", "/ping", "/readyz", "/status", "/api/backends/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equalf(t, http.StatusOK, rec.Code, "route %s", path)
	}

	// /metrics is only registered when a telemetry provider with a Prometheus
	// handler is configured; absent here the request falls through to the catch-all
	// MCP handler and must not return 200.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestServeStopClosesCore(t *testing.T) {
	t.Parallel()

	stub := &stubVMCP{}
	srv, err := Serve(context.Background(), stub, &ServerConfig{SessionFactory: testMinimalFactory()})
	require.NoError(t, err)

	// Stop on a never-started server still runs the shutdown funcs, which release
	// the injected core.
	require.NoError(t, srv.Stop(context.Background()))
	assert.True(t, stub.closed, "Serve must wire core.Close into the shutdown sequence")
}

func TestServeValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    core.VMCP
		cfg  *ServerConfig
	}{
		{
			name: "nil config",
			v:    &stubVMCP{},
			cfg:  nil,
		},
		{
			name: "nil vmcp",
			v:    nil,
			cfg:  &ServerConfig{SessionFactory: testMinimalFactory()},
		},
		{
			name: "nil session factory",
			v:    &stubVMCP{},
			cfg:  &ServerConfig{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, err := Serve(context.Background(), tc.v, tc.cfg)
			require.Error(t, err)
			assert.ErrorIs(t, err, vmcp.ErrInvalidConfig)
			assert.Nil(t, srv)
		})
	}
}

// TestBuildServeConfigMapsSharedFields guards the ServerConfig -> Config mapping
// against silent drift: every Config field except the documented intentionally-
// unmapped set must be populated by buildServeConfig. If a future field is added to
// Config and forgotten in buildServeConfig, this test fails (the field is zero).
// When a field is deliberately not mapped, add it to intentionallyUnmapped with a
// reason, mirroring the buildServeConfig doc comment.
func TestBuildServeConfigMapsSharedFields(t *testing.T) {
	t.Parallel()

	intentionallyUnmapped := map[string]struct{}{
		"AuthzMiddleware":     {}, // authenticated/authz chain relocated by #5441
		"HealthMonitorConfig": {}, // monitor injected pre-built via ServerConfig.HealthMonitor (A2)
	}

	// Every field set to a non-zero value so a dropped mapping surfaces as a zero
	// field on the resulting Config.
	src := &ServerConfig{
		Name:                    "n",
		Version:                 "v",
		GroupRef:                "g",
		Host:                    "h",
		Port:                    1,
		EndpointPath:            "/e",
		SessionTTL:              time.Second,
		AuthMiddleware:          func(h http.Handler) http.Handler { return h },
		AuthInfoHandler:         http.NewServeMux(),
		AuthServer:              &asrunner.EmbeddedAuthServer{},
		HealthMonitor:           &health.Monitor{},
		StatusReportingInterval: time.Second,
		StatusReporter:          stubServeReporter{},
		Watcher:                 stubWatcher{},
		SessionFactory:          testMinimalFactory(),
		SessionStorage:          &vmcpconfig.SessionStorageConfig{},
		OptimizerFactory: func(context.Context, []server.ServerTool) (optimizer.Optimizer, error) {
			return nil, nil
		},
		OptimizerConfig:   &optimizer.Config{},
		TelemetryProvider: &telemetry.Provider{},
		AuditConfig:       &audit.Config{},
	}

	got := reflect.ValueOf(*buildServeConfig(src))
	gotType := got.Type()
	for i := range gotType.NumField() {
		name := gotType.Field(i).Name
		if _, skip := intentionallyUnmapped[name]; skip {
			continue
		}
		assert.Falsef(t, got.Field(i).IsZero(), "Config.%s was not populated by buildServeConfig", name)
	}
}

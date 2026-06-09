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
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
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

// testMinimalSessionManagerConfig returns a minimal valid SessionManagerConfig for
// Serve tests that need a non-nil session-manager config but do not exercise session
// creation. Base is the only required FactoryConfig field; the minimal factory's
// MakeSessionWithID returns an error if a test accidentally triggers registration.
func testMinimalSessionManagerConfig() *sessionmanager.FactoryConfig {
	return &sessionmanager.FactoryConfig{Base: testMinimalFactory()}
}

// testMinimalServeConfig returns a minimal valid ServerConfig for Serve tests that do
// not exercise session creation: a non-nil SessionManagerConfig and an empty
// BackendRegistry, the two required collaborators Serve validates.
func testMinimalServeConfig() *ServerConfig {
	return &ServerConfig{
		SessionManagerConfig: testMinimalSessionManagerConfig(),
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	}
}

func TestServeAppliesTransportDefaults(t *testing.T) {
	t.Parallel()

	// Empty transport fields exercise every default; Port is left zero.
	cfg := testMinimalServeConfig()

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	require.NotNil(t, srv)
	require.NotNil(t, srv.MCPServer())

	// Defaults mirror server.New and are applied to the server's own config,
	// leaving the caller's ServerConfig untouched. Asserting against the shared
	// consts (not literals) keeps this test from being a third copy that can drift.
	assert.Equal(t, defaultServerName, srv.config.Name)
	assert.Equal(t, defaultServerVersion, srv.config.Version)
	assert.Equal(t, defaultHost, srv.config.Host)
	assert.Equal(t, defaultEndpointPath, srv.config.EndpointPath)
	assert.Equal(t, defaultSessionTTL, srv.config.SessionTTL)
	assert.Equal(t, 0, srv.config.Port) // Port 0 => OS-assigned

	assert.Empty(t, cfg.Host, "caller config must not be mutated")

	// Address reflects the defaulted host and unassigned port (no listener yet).
	assert.Equal(t, defaultHost+":0", srv.Address())
}

func TestServePreservesExplicitConfig(t *testing.T) {
	t.Parallel()

	// Distinct values per scalar field so a wrong-source mapping (e.g. Host:
	// cfg.GroupRef) is caught here — this test carries value-correctness for the
	// pass-through scalars that the presence-only drift guard cannot.
	cfg := &ServerConfig{
		Name:                    "custom",
		Version:                 "9.9.9",
		Host:                    "0.0.0.0",
		Port:                    8080,
		EndpointPath:            "/rpc",
		GroupRef:                "my-group",
		SessionTTL:              7 * time.Minute,
		StatusReportingInterval: 11 * time.Second,
		SessionManagerConfig:    testMinimalSessionManagerConfig(),
		BackendRegistry:         vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	}

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	assert.Equal(t, "custom", srv.config.Name)
	assert.Equal(t, "9.9.9", srv.config.Version)
	assert.Equal(t, "0.0.0.0", srv.config.Host)
	assert.Equal(t, 8080, srv.config.Port)
	assert.Equal(t, "/rpc", srv.config.EndpointPath)
	assert.Equal(t, "my-group", srv.config.GroupRef)
	assert.Equal(t, 7*time.Minute, srv.config.SessionTTL)
	assert.Equal(t, 11*time.Second, srv.config.StatusReportingInterval)
}

func TestServeHandlerRegistersUnauthenticatedRoutes(t *testing.T) {
	t.Parallel()

	cfg := testMinimalServeConfig()
	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)

	// These routes are registered as direct mux entries, so they bypass the
	// authenticated middleware chain that Handler still builds and mounts at "/"
	// (the chain itself is relocated under Serve by a later phase).
	for _, path := range []string{"/health", "/ping", "/readyz", "/status", "/api/backends/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equalf(t, http.StatusOK, rec.Code, "route %s", path)
	}

	// .well-known is always registered; with no AuthInfoHandler it returns a clean
	// JSON 404, distinct from the catch-all MCP handler's 406 on a bare GET.
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	// /metrics is only registered when a telemetry provider with a Prometheus
	// handler is configured; absent here the request falls through to the catch-all
	// MCP handler and must not return 200.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestServeHandlerRegistersMetricsWhenTelemetryEnabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName:                 "vmcp-serve-test",
		ServiceVersion:              "0.0.0",
		EnablePrometheusMetricsPath: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Shutdown(ctx) })

	srv, err := Serve(ctx, &stubVMCP{}, &ServerConfig{
		SessionManagerConfig: testMinimalSessionManagerConfig(),
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		TelemetryProvider:    provider,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	handler, err := srv.Handler(ctx)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServeStopClosesCore(t *testing.T) {
	t.Parallel()

	stub := &stubVMCP{}
	srv, err := Serve(context.Background(), stub, testMinimalServeConfig())
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
			cfg:  testMinimalServeConfig(),
		},
		{
			name: "nil session manager config",
			v:    &stubVMCP{},
			cfg:  &ServerConfig{BackendRegistry: vmcp.NewImmutableRegistry([]vmcp.Backend{})},
		},
		{
			name: "nil backend registry",
			v:    &stubVMCP{},
			cfg:  &ServerConfig{SessionManagerConfig: testMinimalSessionManagerConfig()},
		},
		{
			// Both nil: cfg is checked first, so this must fail cleanly (no panic
			// from dereferencing the nil cfg) rather than depending on check order.
			name: "nil config and nil vmcp",
			v:    nil,
			cfg:  nil,
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
//
// This is a PRESENCE check only — with every source field non-zero it cannot catch a
// wrong-source mapping or default-value drift. Value correctness is carried by
// TestServePreservesExplicitConfig (pass-through scalars) and
// TestServeAppliesTransportDefaults (the cmp.Or defaults).
func TestBuildServeConfigMapsSharedFields(t *testing.T) {
	t.Parallel()

	intentionallyUnmapped := map[string]struct{}{
		"AuthzMiddleware":     {}, // authenticated/authz chain relocated by #5441
		"HealthMonitorConfig": {}, // monitor injected pre-built via ServerConfig.HealthMonitor (A2)
		"StatusReporter":      {}, // set directly on Server; Config.StatusReporter only read by New
		"SessionFactory":      {}, // session manager built in Serve from ServerConfig.SessionManagerConfig
		"OptimizerFactory":    {}, // optimizer wiring carried on ServerConfig.SessionManagerConfig (FactoryConfig)
		"OptimizerConfig":     {}, // optimizer wiring carried on ServerConfig.SessionManagerConfig (FactoryConfig)
	}

	// Every field set to a non-zero value so a dropped mapping surfaces as a zero
	// field on the resulting Config. SessionManagerConfig and BackendRegistry are
	// ServerConfig-only (consumed directly by Serve, not mapped into Config), so they
	// are set for completeness but are not part of this destination-field assertion.
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
		BackendRegistry:         vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		SessionStorage:          &vmcpconfig.SessionStorageConfig{},
		SessionManagerConfig:    testMinimalSessionManagerConfig(),
		TelemetryProvider:       &telemetry.Provider{},
		AuditConfig:             &audit.Config{},
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

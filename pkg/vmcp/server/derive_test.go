// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

// stubStatusProvider is a non-nil health.StatusProvider for derivation tests; its
// behavior is not exercised — only its identity is asserted (it is passed through
// onto core.Config.HealthStatusProvider).
type stubStatusProvider struct{}

func (*stubStatusProvider) QueryBackendStatus(string) (vmcp.BackendHealthStatus, bool) {
	return vmcp.BackendHealthy, true
}

// populatedLegacyConfig returns a server.Config with every field deriveServerConfig
// reads set to a distinctive non-zero value, so a dropped or wrong-source mapping
// surfaces in the assertions below. AuthzMiddleware is set too, to prove derivation
// retains it on the legacy Config (vestigial) while never projecting it.
func populatedLegacyConfig() *Config {
	passthrough := func(h http.Handler) http.Handler { return h }
	return &Config{
		Name:                    "vmcp-name",
		Version:                 "9.9.9",
		GroupRef:                "grp",
		Host:                    "0.0.0.0",
		Port:                    7777,
		EndpointPath:            "/custom",
		SessionTTL:              17 * time.Minute,
		AuthMiddleware:          passthrough,
		AuthzMiddleware:         passthrough,
		AuthInfoHandler:         http.NewServeMux(),
		RateLimitMiddleware:     passthrough,
		AuthServer:              &asrunner.EmbeddedAuthServer{},
		TelemetryProvider:       &telemetry.Provider{},
		AuditConfig:             &audit.Config{},
		StatusReportingInterval: 11 * time.Second,
		Watcher:                 stubWatcher{},
		StatusReporter:          stubServeReporter{},
		SessionStorage:          &vmcpconfig.SessionStorageConfig{},
	}
}

func TestDeriveServerConfigProjectsTransportFields(t *testing.T) {
	t.Parallel()

	cfg := populatedLegacyConfig()
	healthMon := &health.Monitor{}
	registry := vmcp.NewImmutableRegistry([]vmcp.Backend{})
	smCfg := testMinimalSessionManagerConfig()

	got := deriveServerConfig(cfg, healthMon, registry, smCfg)

	// Scalars projected verbatim (cfg's values are all non-default, so cmp.Or returns them).
	assert.Equal(t, "vmcp-name", got.Name)
	assert.Equal(t, "9.9.9", got.Version)
	assert.Equal(t, "grp", got.GroupRef)
	assert.Equal(t, "0.0.0.0", got.Host)
	assert.Equal(t, 7777, got.Port)
	assert.Equal(t, "/custom", got.EndpointPath)
	assert.Equal(t, 17*time.Minute, got.SessionTTL)
	assert.Equal(t, 11*time.Second, got.StatusReportingInterval)

	// Func/handler/pointer fields projected by reference.
	assert.NotNil(t, got.AuthMiddleware)
	assert.NotNil(t, got.RateLimitMiddleware)
	assert.Same(t, cfg.AuthInfoHandler, got.AuthInfoHandler)
	assert.Same(t, cfg.AuthServer, got.AuthServer)
	assert.Same(t, cfg.SessionStorage, got.SessionStorage)
	assert.Equal(t, cfg.Watcher, got.Watcher)
	assert.Equal(t, cfg.StatusReporter, got.StatusReporter)

	// Cross-cutting fields shared with the core (R3).
	assert.Same(t, cfg.TelemetryProvider, got.TelemetryProvider)
	assert.Same(t, cfg.AuditConfig, got.AuditConfig)

	// Collaborators passed in (not on server.Config) — threaded through, not from cfg.
	assert.Same(t, healthMon, got.HealthMonitor)
	assert.Same(t, registry, got.BackendRegistry)
	assert.Same(t, smCfg, got.SessionManagerConfig)
}

func TestDeriveServerConfigAppliesTransportDefaults(t *testing.T) {
	t.Parallel()

	// Empty transport scalars exercise every default; Port stays zero (OS-assigned).
	got := deriveServerConfig(&Config{}, nil, nil, nil)

	assert.Equal(t, defaultServerName, got.Name)
	assert.Equal(t, defaultServerVersion, got.Version)
	assert.Equal(t, defaultHost, got.Host)
	assert.Equal(t, defaultEndpointPath, got.EndpointPath)
	assert.Equal(t, defaultSessionTTL, got.SessionTTL)
	assert.Equal(t, 0, got.Port)
}

func TestDeriveServerConfigPropagatesNilCrossCutting(t *testing.T) {
	t.Parallel()

	// A deliberately-disabled subsystem (nil provider/config/monitor) must stay nil —
	// derivation must not substitute a default or panic.
	got := deriveServerConfig(&Config{}, nil, nil, nil)

	assert.Nil(t, got.TelemetryProvider)
	assert.Nil(t, got.AuditConfig)
	assert.Nil(t, got.HealthMonitor)
	assert.Nil(t, got.BackendRegistry)
	assert.Nil(t, got.SessionManagerConfig)
}

// TestDeriveServerConfigMapsAllFields guards deriveServerConfig against silent drift:
// with every readable source field non-zero and every collaborator param non-nil, every
// ServerConfig field must be populated. If a future ServerConfig field is added and
// deriveServerConfig forgets it, this fails (the field is zero). Add a deliberately-
// unmapped field to the skip set with a reason, mirroring the doc comment.
func TestDeriveServerConfigMapsAllFields(t *testing.T) {
	t.Parallel()

	got := deriveServerConfig(
		populatedLegacyConfig(),
		&health.Monitor{},
		vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		testMinimalSessionManagerConfig(),
	)

	assertAllFieldsPopulated(t, *got, "ServerConfig", nil)
}

func TestDeriveCoreConfigAssemblesCollaborators(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	cfg := &Config{
		Name:              "core-name",
		TelemetryProvider: &telemetry.Provider{},
		AuditConfig:       &audit.Config{},
	}
	agg := aggmocks.NewMockAggregator(ctrl)
	rt := routermocks.NewMockRouter(ctrl)
	backendClient := vmcpmocks.NewMockBackendClient(ctrl)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	workflowDefs := map[string]*composer.WorkflowDefinition{"wf": {}}
	authzCfg := &authz.Config{}
	elicitation := vmcpmocks.NewMockElicitationRequester(ctrl)
	healthProvider := &stubStatusProvider{}

	got := deriveCoreConfig(cfg, agg, rt, backendClient, registry, workflowDefs, authzCfg, elicitation, healthProvider)

	// Collaborators passed through by identity.
	assert.Same(t, agg, got.Aggregator)
	assert.Same(t, rt, got.Router)
	assert.Same(t, backendClient, got.BackendClient)
	assert.Same(t, registry, got.BackendRegistry)
	assert.Same(t, authzCfg, got.Authz)
	assert.Same(t, elicitation, got.Elicitation)
	assert.Same(t, healthProvider, got.HealthStatusProvider)
	assert.Equal(t, workflowDefs, got.WorkflowDefs)

	// ServerName uses the raw cfg.Name for authz parity (no transport default applied).
	assert.Equal(t, "core-name", got.ServerName)

	// Cross-cutting fields shared with the transport (R3).
	assert.Same(t, cfg.TelemetryProvider, got.TelemetryProvider)
	assert.Same(t, cfg.AuditConfig, got.AuditConfig)
}

func TestDeriveCoreConfigUsesRawServerNameAndNilDefaults(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	// Empty Name must NOT be defaulted (authz keys on the real VirtualMCPServer name),
	// and nil admission/health inputs must propagate as nil (allow-all / no filtering).
	got := deriveCoreConfig(
		&Config{},
		aggmocks.NewMockAggregator(ctrl),
		routermocks.NewMockRouter(ctrl),
		vmcpmocks.NewMockBackendClient(ctrl),
		vmcpmocks.NewMockBackendRegistry(ctrl),
		nil, // workflowDefs
		nil, // authzCfg
		nil, // elicitation
		nil, // healthProvider
	)

	assert.Empty(t, got.ServerName)
	assert.Nil(t, got.Authz)
	assert.Nil(t, got.Elicitation)
	assert.Nil(t, got.HealthStatusProvider)
	assert.Nil(t, got.WorkflowDefs)
}

// TestDeriveCoreConfigMapsAllFields guards deriveCoreConfig against silent drift: with
// every cross-cutting source field non-zero and every collaborator non-nil, every
// core.Config field must be populated.
func TestDeriveCoreConfigMapsAllFields(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	cfg := &Config{
		Name:              "core-name",
		TelemetryProvider: &telemetry.Provider{},
		AuditConfig:       &audit.Config{},
	}

	got := deriveCoreConfig(
		cfg,
		aggmocks.NewMockAggregator(ctrl),
		routermocks.NewMockRouter(ctrl),
		vmcpmocks.NewMockBackendClient(ctrl),
		vmcpmocks.NewMockBackendRegistry(ctrl),
		map[string]*composer.WorkflowDefinition{"wf": {}},
		&authz.Config{},
		vmcpmocks.NewMockElicitationRequester(ctrl),
		&stubStatusProvider{},
	)

	assertAllFieldsPopulated(t, *got, "core.Config", nil)
}

// TestDeriveConfigsTreatInputAsReadOnly verifies neither helper mutates cfg. server.New
// applies its defaults by writing back onto cfg; the derivation helpers must not, so a
// caller's Config is unchanged after derivation (go-style: copy before mutating input).
func TestDeriveConfigsTreatInputAsReadOnly(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	authzFn := func(h http.Handler) http.Handler { return h }
	cfg := &Config{AuthzMiddleware: authzFn} // empty scalars: defaulting would mutate them.

	_ = deriveServerConfig(cfg, nil, nil, nil)
	_ = deriveCoreConfig(
		cfg,
		aggmocks.NewMockAggregator(ctrl),
		routermocks.NewMockRouter(ctrl),
		vmcpmocks.NewMockBackendClient(ctrl),
		vmcpmocks.NewMockBackendRegistry(ctrl),
		nil, nil, nil, nil,
	)

	// Transport defaults were NOT written back onto the caller's Config.
	assert.Empty(t, cfg.Name)
	assert.Empty(t, cfg.Version)
	assert.Empty(t, cfg.Host)
	assert.Empty(t, cfg.EndpointPath)
	assert.Zero(t, cfg.SessionTTL)
	// The vestigial AuthzMiddleware field is retained on the legacy Config (never cleared).
	assert.NotNil(t, cfg.AuthzMiddleware)
}

// assertAllFieldsPopulated asserts every field of the struct value v is non-zero,
// skipping any name in skip. typeName labels failures.
func assertAllFieldsPopulated(t *testing.T, v any, typeName string, skip map[string]struct{}) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		if _, skipped := skip[name]; skipped {
			continue
		}
		assert.Falsef(t, rv.Field(i).IsZero(), "%s.%s was not populated by derivation", typeName, name)
	}
}

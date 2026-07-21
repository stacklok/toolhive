// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/app"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	servermocks "github.com/stacklok/toolhive/pkg/vmcp/server/mocks"
)

// TestBuildServerConfig_InlineDiscovery covers the inline discovery path.
// BuildServerConfig must succeed and return a non-nil *server.ServerConfig and cleanup.
func TestBuildServerConfig_InlineDiscovery(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	srvCfg, cleanup, err := app.BuildServerConfig(t.Context(),
		cfg,
		app.WithVersion("test-0.0.1"),
		app.WithHost("127.0.0.1", 0),
		app.WithBackendRegistry(reg, nil),
	)
	require.NoError(t, err)
	require.NotNil(t, srvCfg)
	require.NotNil(t, cleanup)
	defer cleanup()

	// Transport defaults are applied.
	assert.Equal(t, "test-vmcp", srvCfg.Name)
	assert.NotEmpty(t, srvCfg.EndpointPath)
	assert.NotEmpty(t, srvCfg.SessionTTL)
}

// TestBuildServerConfig_OptionsApplied verifies that version, host, port, and
// session TTL options are reflected in the returned ServerConfig.
func TestBuildServerConfig_OptionsApplied(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()
	ttl := 15 * time.Minute

	srvCfg, cleanup, err := app.BuildServerConfig(t.Context(),
		cfg,
		app.WithVersion("1.2.3"),
		app.WithHost("0.0.0.0", 9999),
		app.WithSessionTTL(ttl),
		app.WithBackendRegistry(reg, nil),
	)
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, "1.2.3", srvCfg.Version)
	assert.Equal(t, "0.0.0.0", srvCfg.Host)
	assert.Equal(t, 9999, srvCfg.Port)
	assert.Equal(t, ttl, srvCfg.SessionTTL)
}

// TestBuildServerConfig_DiscoveredMode_RequiresInjectedRegistry verifies that
// BuildServerConfig returns an error for the Kubernetes discovered mode when no
// pre-built registry is provided, to prevent starting a duplicate K8s watcher.
func TestBuildServerConfig_DiscoveredMode_RequiresInjectedRegistry(t *testing.T) {
	t.Parallel()

	cfg := &vmcpconfig.Config{
		Name:  "test-vmcp",
		Group: "test-group",
		IncomingAuth: &vmcpconfig.IncomingAuthConfig{
			Type: vmcpconfig.IncomingAuthTypeAnonymous,
		},
		OutgoingAuth: &vmcpconfig.OutgoingAuthConfig{
			Source: "discovered",
		},
		Aggregation: &vmcpconfig.AggregationConfig{
			ConflictResolution: vmcp.ConflictStrategyPrefix,
			ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
		},
	}

	_, cleanup, err := app.BuildServerConfig(t.Context(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithBackendRegistry")
	assert.NotNil(t, cleanup)
}

// TestBuildServerConfig_BackendRegistryFromOpts verifies that WithBackendRegistry
// populates ServerConfig.BackendRegistry and ServerConfig.Watcher.
func TestBuildServerConfig_BackendRegistryFromOpts(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockWatcher := servermocks.NewMockWatcher(ctrl)

	cfg := minimalInlineConfig()

	srvCfg, cleanup, err := app.BuildServerConfig(t.Context(),
		cfg,
		app.WithBackendRegistry(reg, mockWatcher),
	)
	require.NoError(t, err)
	defer cleanup()

	// The injected registry and watcher must appear in ServerConfig.
	assert.Equal(t, vmcp.BackendRegistry(reg), srvCfg.BackendRegistry)
	assert.Equal(t, mockWatcher, srvCfg.Watcher)
}

// TestBuildServerConfig_SessionManagerConfigWired verifies that BuildServerConfig
// always wires a non-nil SessionManagerConfig with AdvertiseFromCore == true.
func TestBuildServerConfig_SessionManagerConfigWired(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	srvCfg, cleanup, err := app.BuildServerConfig(t.Context(), cfg, app.WithBackendRegistry(reg, nil))
	require.NoError(t, err)
	defer cleanup()

	require.NotNil(t, srvCfg.SessionManagerConfig)
	assert.True(t, srvCfg.SessionManagerConfig.AdvertiseFromCore,
		"AdvertiseFromCore must be true on the Serve path to prevent double-aggregation")
	assert.NotNil(t, srvCfg.SessionManagerConfig.Base, "session factory must be wired")
}

// TestBuildServerConfig_CleanupDoesNotPanic verifies that the returned cleanup func
// can be invoked repeatedly without panicking. For this minimal config no embedded
// auth server or rate-limit middleware is acquired, so cleanupFuncs is empty — the
// assertion is strictly no-panic, not single-invocation of a real Close.
func TestBuildServerConfig_CleanupDoesNotPanic(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	_, cleanup, err := app.BuildServerConfig(t.Context(), cfg, app.WithBackendRegistry(reg, nil))
	require.NoError(t, err)

	assert.NotPanics(t, cleanup)
	assert.NotPanics(t, cleanup)
}

// TestBuildServerConfig_StaticBackends verifies that a non-empty cfg.Backends causes
// BuildServerConfig to build its own registry (no WithBackendRegistry) and populate
// ServerConfig.BackendRegistry. Mirrors TestBuildCore_StaticBackends.
func TestBuildServerConfig_StaticBackends(t *testing.T) {
	t.Parallel()

	cfg := minimalInlineConfig()
	cfg.Backends = []vmcpconfig.StaticBackendConfig{
		{Name: "backend-one", URL: "http://127.0.0.1:9001/mcp", Transport: "streamable-http"},
	}

	srvCfg, cleanup, err := app.BuildServerConfig(t.Context(), cfg, app.WithVersion("test-0.0.1"))
	require.NoError(t, err)
	defer cleanup()

	require.NotNil(t, srvCfg)
	assert.NotNil(t, srvCfg.BackendRegistry, "static mode must populate BackendRegistry")
}

// TestBuildServerConfig_NilConfig verifies that BuildServerConfig returns an error
// for a nil config.
func TestBuildServerConfig_NilConfig(t *testing.T) {
	t.Parallel()

	_, cleanup, err := app.BuildServerConfig(t.Context(), nil)
	require.Error(t, err)
	require.NotNil(t, cleanup)
}

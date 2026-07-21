// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/app"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// minimalInlineConfig returns a vmcpconfig.Config sufficient to drive BuildCore
// without any external dependencies (no Kubernetes, no Docker).
func minimalInlineConfig() *vmcpconfig.Config {
	return &vmcpconfig.Config{
		Name:  "test-vmcp",
		Group: "test-group",
		IncomingAuth: &vmcpconfig.IncomingAuthConfig{
			Type: vmcpconfig.IncomingAuthTypeAnonymous,
		},
		OutgoingAuth: &vmcpconfig.OutgoingAuthConfig{
			Source: "inline",
		},
		Aggregation: &vmcpconfig.AggregationConfig{
			ConflictResolution: vmcp.ConflictStrategyPrefix,
			ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
		},
	}
}

// TestBuildCore_WithInjectedRegistry_SkipsDiscovery verifies that when
// WithBackendRegistry is provided, BuildCore uses it directly and skips
// resolveBackendRegistryForCore entirely. Using a mock registry (which would never be
// produced by discovery) proves the injected one is what the core consumes.
func TestBuildCore_WithInjectedRegistry_SkipsDiscovery(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	coreVMCP, cleanup, err := app.BuildCore(t.Context(),
		cfg,
		app.WithVersion("test-0.0.1"),
		app.WithBackendRegistry(reg, nil),
	)
	require.NoError(t, err)
	require.NotNil(t, coreVMCP)
	require.NotNil(t, cleanup)
	defer cleanup()
}

// TestBuildCore_StaticBackends covers the static mode where vmcpCfg.Backends is
// non-empty. BuildCore must build its own registry and return a non-nil VMCP.
// The test does not call ListTools because the static backend URL is not a live
// server — aggregation against a non-existent backend is an error, not silence.
func TestBuildCore_StaticBackends(t *testing.T) {
	t.Parallel()

	cfg := minimalInlineConfig()
	cfg.Backends = []vmcpconfig.StaticBackendConfig{
		{Name: "backend-one", URL: "http://127.0.0.1:9001/mcp", Transport: "streamable-http"},
	}

	coreVMCP, cleanup, err := app.BuildCore(t.Context(), cfg, app.WithVersion("test-0.0.1"))
	require.NoError(t, err)
	require.NotNil(t, coreVMCP)
	cleanup()
}

// TestBuildCore_CodeModeEnabledConstructs verifies that BuildCore succeeds when code
// mode is enabled, exercising the codemode.FromConfig + NewDecorator wiring added to
// match the legacy server.New path. The decorator's advertised execute_tool_script tool
// cannot be asserted here (ListTools requires at least one capability-returning backend,
// and BuildCore builds its own real HTTP backend client); the end-to-end codemode
// behavior is covered by test/e2e/thv-operator/virtualmcp/virtualmcp_codemode_test.go.
func TestBuildCore_CodeModeEnabledConstructs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()
	cfg.CodeMode = &vmcpconfig.CodeModeConfig{Enabled: true}

	coreVMCP, cleanup, err := app.BuildCore(t.Context(), cfg, app.WithBackendRegistry(reg, nil))
	require.NoError(t, err)
	require.NotNil(t, coreVMCP)
	cleanup()
}

// TestBuildCore_DiscoveredMode_RequiresBackendRegistry verifies that BuildCore
// returns an error for the Kubernetes discovered outgoingAuth source when no
// pre-built registry is injected. This mirrors BuildServerConfig and prevents two
// independent informer caches with no shared readiness handle.
func TestBuildCore_DiscoveredMode_RequiresBackendRegistry(t *testing.T) {
	t.Parallel()

	cfg := &vmcpconfig.Config{
		Name:  "test-vmcp",
		Group: "test-group",
		IncomingAuth: &vmcpconfig.IncomingAuthConfig{
			Type: vmcpconfig.IncomingAuthTypeAnonymous,
		},
		OutgoingAuth: &vmcpconfig.OutgoingAuthConfig{
			Source: vmcpconfig.OutgoingAuthSourceDiscovered,
		},
		Aggregation: &vmcpconfig.AggregationConfig{
			ConflictResolution: vmcp.ConflictStrategyPrefix,
			ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
		},
	}

	_, cleanup, err := app.BuildCore(t.Context(), cfg)
	require.Error(t, err)
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	assert.Contains(t, err.Error(), "WithBackendRegistry")
	assert.NotNil(t, cleanup) // cleanup must always be returned (even on error; it's noop)
}

// TestBuildCore_InjectedRegistryUsedByBothBuilds verifies that the same injected
// registry can back two independent BuildCore calls (each yielding a distinct, live
// core) without error. It asserts reuse of the injected instance, not that any
// collaborator is constructed exactly once — single construction is the Builder's job
// and is covered there; this is the standalone-primitive reuse path.
func TestBuildCore_InjectedRegistryUsedByBothBuilds(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()
	opts := []app.Option{app.WithBackendRegistry(reg, nil)}

	coreVMCP1, cleanup1, err := app.BuildCore(t.Context(), cfg, opts...)
	require.NoError(t, err)
	defer cleanup1()

	coreVMCP2, cleanup2, err := app.BuildCore(t.Context(), cfg, opts...)
	require.NoError(t, err)
	defer cleanup2()

	// Both cores are distinct, live objects.
	require.NotNil(t, coreVMCP1)
	require.NotNil(t, coreVMCP2)
}

// TestBuildCore_CleanupDoesNotPanic verifies that the returned cleanup func can be
// invoked repeatedly without panicking. The core's Close is sync.Once-guarded, so a
// second call is a no-op. (It does not assert resource release — no real resources are
// acquired for this minimal config; the assertion is strictly no-panic idempotency.)
func TestBuildCore_CleanupDoesNotPanic(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	_, cleanup, err := app.BuildCore(context.Background(), cfg, app.WithBackendRegistry(reg, nil))
	require.NoError(t, err)

	assert.NotPanics(t, cleanup)
	assert.NotPanics(t, cleanup)
}

// TestBuildCore_HealthMonitorConfig_InvalidThreshold verifies that BuildCore rejects a
// config whose health-check interval is set with an invalid unhealthy threshold. The
// config validator (now run at the top of BuildCore) catches this and BuildCore returns
// a vmcp.ErrInvalidConfig-wrapped error rather than proceeding.
func TestBuildCore_HealthMonitorConfig_InvalidThreshold(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()
	cfg.Operational = &vmcpconfig.OperationalConfig{
		FailureHandling: &vmcpconfig.FailureHandlingConfig{
			HealthCheckInterval: vmcpconfig.Duration(time.Second),
			UnhealthyThreshold:  0, // invalid: must be positive when the interval is set
		},
	}

	_, cleanup, err := app.BuildCore(t.Context(), cfg, app.WithBackendRegistry(reg, nil))
	require.Error(t, err)
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	assert.Contains(t, err.Error(), "unhealthyThreshold")
	assert.NotNil(t, cleanup)
}

// TestBuildCore_NilConfig verifies that BuildCore returns an error for a nil config.
func TestBuildCore_NilConfig(t *testing.T) {
	t.Parallel()

	_, cleanup, err := app.BuildCore(t.Context(), nil)
	require.Error(t, err)
	require.NotNil(t, cleanup) // noop, but must be non-nil
}

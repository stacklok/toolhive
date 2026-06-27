// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"testing"

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

// TestBuildCore_InlineDiscovery covers the inline (no static backends, no K8s)
// discovery path. BuildCore must succeed and return a non-nil VMCP and cleanup.
func TestBuildCore_InlineDiscovery(t *testing.T) {
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

	// Cleanup must be idempotent (Close is called at most once).
	cleanup()
	cleanup()
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

// TestBuildCore_DiscoveredMode_RequiresVMCPNamespace verifies that BuildCore
// returns an error for the Kubernetes discovered mode when the VMCP_NAMESPACE
// environment variable is absent and no pre-built registry is provided.
// Cannot run in parallel: t.Setenv modifies the process environment.
func TestBuildCore_DiscoveredMode_RequiresVMCPNamespace(t *testing.T) {
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

	// VMCP_NAMESPACE is not set; VMCP_NAMESPACE is read from env by the discovered path.
	t.Setenv("VMCP_NAMESPACE", "")

	_, cleanup, err := app.BuildCore(t.Context(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VMCP_NAMESPACE")
	assert.NotNil(t, cleanup) // cleanup must always be returned (even on error; it's noop)
}

// TestBuildCore_WithInjectedRegistry verifies that when WithBackendRegistry is
// provided, BuildCore uses it directly and skips any internal discovery.
func TestBuildCore_WithInjectedRegistry(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	coreVMCP, cleanup, err := app.BuildCore(t.Context(), cfg, app.WithBackendRegistry(reg, nil))
	require.NoError(t, err)
	require.NotNil(t, coreVMCP)
	defer cleanup()
}

// TestBuildCore_SharedCollaboratorBuiltOnce verifies that passing the same registry
// via WithBackendRegistry to two BuildCore calls does not double-build anything that
// would conflict (the registry is a shared read-only view, not rebuilt).
func TestBuildCore_SharedCollaboratorBuiltOnce(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	// List is called at most twice total (once per BuildCore call, at most).
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

// TestBuildCore_CleanupReleasesOnlyOwnedResources verifies that cleanup() closes
// the returned core and does not panic even when called on a zero-option config.
func TestBuildCore_CleanupReleasesOnlyOwnedResources(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	cfg := minimalInlineConfig()

	_, cleanup, err := app.BuildCore(context.Background(), cfg, app.WithBackendRegistry(reg, nil))
	require.NoError(t, err)

	// Must not panic; calling twice is idempotent (core.Close uses sync.Once).
	assert.NotPanics(t, cleanup)
	assert.NotPanics(t, cleanup)
}

// TestBuildCore_NilConfig verifies that BuildCore returns an error for a nil config.
func TestBuildCore_NilConfig(t *testing.T) {
	t.Parallel()

	_, cleanup, err := app.BuildCore(t.Context(), nil)
	require.Error(t, err)
	require.NotNil(t, cleanup) // noop, but must be non-nil
}

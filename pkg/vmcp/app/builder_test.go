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
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// markerDecorator is a no-op core.VMCP decorator used to prove Builder.Finish returns
// the decorated core rather than the inner one.
type markerDecorator struct{ core.VMCP }

// TestBuilder_NilConfig verifies Finish rejects a nil config and still returns a
// non-nil (noop) cleanup.
func TestBuilder_NilConfig(t *testing.T) {
	t.Parallel()

	_, _, cleanup, err := app.NewBuilder(t.Context(), nil).Finish()
	require.Error(t, err)
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	require.NotNil(t, cleanup)
}

// TestBuilder_InlineAssembles verifies the Builder assembles a working server and
// core for inline mode with an injected registry, and that its single cleanup func
// is safe to call repeatedly.
func TestBuilder_InlineAssembles(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	srv, coreVMCP, cleanup, err := app.NewBuilder(t.Context(), minimalInlineConfig(),
		app.WithVersion("test-0.0.1"),
		app.WithBackendRegistry(reg, nil),
	).Finish()
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NotNil(t, coreVMCP)
	require.NotNil(t, cleanup)

	// Stop the server (releases the session manager/storage goroutines started by
	// Serve) then run the builder cleanup; both must be safe and idempotent.
	require.NoError(t, srv.Stop(context.Background()))
	assert.NotPanics(t, cleanup)
	assert.NotPanics(t, cleanup)
}

// TestBuilder_DecorateApplied verifies the decorator is invoked and that Finish
// returns the decorated core (the RFC THV-0076 extension seam).
func TestBuilder_DecorateApplied(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	reg := vmcpmocks.NewMockBackendRegistry(ctrl)
	reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	invoked := false
	srv, gotCore, cleanup, err := app.NewBuilder(t.Context(), minimalInlineConfig(),
		app.WithBackendRegistry(reg, nil),
	).Decorate(func(inner core.VMCP) core.VMCP {
		invoked = true
		return &markerDecorator{VMCP: inner}
	}).Finish()
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()); cleanup() })

	assert.True(t, invoked, "decorator must be invoked")
	_, ok := gotCore.(*markerDecorator)
	assert.True(t, ok, "Finish must return the decorated core, not the inner one")
}

// TestBuilder_DiscoveredMode_RequiresRegistryOrNamespace verifies that, for the
// discovered source with no injected registry, the Builder tries to build the
// Kubernetes registry itself and fails fast when VMCP_NAMESPACE is unset.
// Cannot run in parallel: t.Setenv mutates the process environment.
func TestBuilder_DiscoveredMode_RequiresRegistryOrNamespace(t *testing.T) {
	t.Setenv("VMCP_NAMESPACE", "")

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

	_, _, cleanup, err := app.NewBuilder(t.Context(), cfg).Finish()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VMCP_NAMESPACE")
	require.NotNil(t, cleanup)
}

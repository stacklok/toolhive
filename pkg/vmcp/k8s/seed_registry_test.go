// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	workloadsmocks "github.com/stacklok/toolhive/pkg/vmcp/workloads/mocks"
)

// TestSeedRegistryFromDiscoverer covers the testable core of SyncRegistry: the
// per-workload conversion + upsert loop, its skip-on-error / skip-nil tolerance, and
// the seeded count. This is the "extra scrutiny" seam of the discovered-mode seeding
// fix — a regression here would silently reintroduce the transiently-empty-registry
// window the PR closes.
func TestSeedRegistryFromDiscoverer(t *testing.T) {
	t.Parallel()

	const group = "test-group"
	wl := func(name string) workloads.TypedWorkload {
		return workloads.TypedWorkload{Name: name, Type: workloads.WorkloadTypeMCPServer}
	}
	backend := func(id string) *vmcp.Backend {
		return &vmcp.Backend{ID: id, Name: id}
	}

	t.Run("list error is fatal and seeds nothing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		d := workloadsmocks.NewMockDiscoverer(ctrl)
		d.EXPECT().ListWorkloadsInGroup(gomock.Any(), group).Return(nil, errors.New("api down"))

		reg := vmcp.NewDynamicRegistry(nil)
		n, err := seedRegistryFromDiscoverer(context.Background(), d, group, reg)

		require.Error(t, err)
		assert.Equal(t, 0, n)
		assert.Equal(t, 0, reg.Count())
	})

	t.Run("empty group seeds nothing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		d := workloadsmocks.NewMockDiscoverer(ctrl)
		d.EXPECT().ListWorkloadsInGroup(gomock.Any(), group).Return(nil, nil)

		reg := vmcp.NewDynamicRegistry(nil)
		n, err := seedRegistryFromDiscoverer(context.Background(), d, group, reg)

		require.NoError(t, err)
		assert.Equal(t, 0, n)
		assert.Equal(t, 0, reg.Count())
	})

	t.Run("happy path seeds every accessible backend", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		d := workloadsmocks.NewMockDiscoverer(ctrl)
		d.EXPECT().ListWorkloadsInGroup(gomock.Any(), group).Return([]workloads.TypedWorkload{wl("a"), wl("b")}, nil)
		d.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), wl("a")).Return(backend("a"), nil)
		d.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), wl("b")).Return(backend("b"), nil)

		reg := vmcp.NewDynamicRegistry(nil)
		n, err := seedRegistryFromDiscoverer(context.Background(), d, group, reg)

		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, 2, reg.Count())
	})

	t.Run("conversion error skips that workload but seeds the rest", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		d := workloadsmocks.NewMockDiscoverer(ctrl)
		d.EXPECT().ListWorkloadsInGroup(gomock.Any(), group).Return([]workloads.TypedWorkload{wl("bad"), wl("good")}, nil)
		d.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), wl("bad")).Return(nil, errors.New("convert failed"))
		d.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), wl("good")).Return(backend("good"), nil)

		reg := vmcp.NewDynamicRegistry(nil)
		n, err := seedRegistryFromDiscoverer(context.Background(), d, group, reg)

		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Equal(t, 1, reg.Count())
	})

	t.Run("nil backend (not yet accessible) is skipped", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		d := workloadsmocks.NewMockDiscoverer(ctrl)
		d.EXPECT().ListWorkloadsInGroup(gomock.Any(), group).Return([]workloads.TypedWorkload{wl("pending"), wl("ready")}, nil)
		d.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), wl("pending")).Return(nil, nil)
		d.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), wl("ready")).Return(backend("ready"), nil)

		reg := vmcp.NewDynamicRegistry(nil)
		n, err := seedRegistryFromDiscoverer(context.Background(), d, group, reg)

		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Equal(t, 1, reg.Count())
	})
}

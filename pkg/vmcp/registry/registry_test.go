// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/registry"
)

// fakeRESTConfig is a non-connecting REST config. NewBackendWatcher builds a
// controller-runtime manager from it without contacting a cluster, so the
// constructor can be exercised without envtest/kubebuilder binaries.
func fakeRESTConfig() *rest.Config {
	return &rest.Config{Host: "https://localhost:6443"}
}

func TestNewKubernetesBackendRegistry_InvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		namespace     string
		group         string
		expectedError string
	}{
		{
			name:          "empty namespace",
			namespace:     "",
			group:         "default/test-group",
			expectedError: "namespace cannot be empty",
		},
		{
			name:          "empty group",
			namespace:     "default",
			group:         "",
			expectedError: "group cannot be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			reg, watcher, err := registry.NewKubernetesBackendRegistry(
				ctx, tc.namespace, tc.group, registry.WithRESTConfig(fakeRESTConfig()),
			)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectedError)
			assert.Nil(t, reg)
			assert.Nil(t, watcher)
		})
	}
}

// TestNewKubernetesBackendRegistry_DefaultUsesInClusterConfig verifies the
// default (no WithRESTConfig) path resolves the REST config via
// rest.InClusterConfig. The env vars are forced empty so the lookup
// deterministically reports "not in cluster" regardless of where the test runs.
func TestNewKubernetesBackendRegistry_DefaultUsesInClusterConfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg, watcher, err := registry.NewKubernetesBackendRegistry(ctx, "default", "default/test-group")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "in-cluster")
	assert.Nil(t, reg)
	assert.Nil(t, watcher)
}

// TestNewKubernetesBackendRegistry_StartsEmpty verifies the constructor returns a
// non-nil registry and readiness watcher, and that the registry starts empty —
// the watcher's initial informer sync (not the static-discovery path) populates
// it.
func TestNewKubernetesBackendRegistry_StartsEmpty(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg, watcher, err := registry.NewKubernetesBackendRegistry(
		ctx, "default", "default/test-group", registry.WithRESTConfig(fakeRESTConfig()),
	)
	require.NoError(t, err)
	require.NotNil(t, reg)
	require.NotNil(t, watcher)

	assert.Equal(t, 0, reg.Count())
	assert.Empty(t, reg.List(ctx))
}

// TestNewKubernetesBackendRegistry_ReturnsLiveRegistry verifies the returned
// registry is the live, mutable DynamicRegistry the watcher updates in place —
// the wiring that gives parity with cli.Serve's live add/remove behavior
// (AC: live updates). The watcher's CR→registry conversion itself is covered by
// pkg/vmcp/k8s's backend reconciler integration test.
func TestNewKubernetesBackendRegistry_ReturnsLiveRegistry(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg, _, err := registry.NewKubernetesBackendRegistry(
		ctx, "default", "default/test-group", registry.WithRESTConfig(fakeRESTConfig()),
	)
	require.NoError(t, err)

	dyn, ok := reg.(vmcp.DynamicRegistry)
	require.True(t, ok, "returned registry must be the DynamicRegistry the watcher mutates")

	require.NoError(t, dyn.Upsert(vmcp.Backend{ID: "backend-1", Name: "Backend 1"}))
	assert.Equal(t, 1, reg.Count(), "Upsert must be visible through the returned registry")

	require.NoError(t, dyn.Remove("backend-1"))
	assert.Equal(t, 0, reg.Count(), "Remove must be visible through the returned registry")
}

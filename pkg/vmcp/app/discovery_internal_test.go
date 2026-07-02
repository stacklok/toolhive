// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/vmcp"
	aggregatormocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
)

// TestRunDiscovery_ZeroBackends exercises the branch in runDiscovery where the
// discoverer succeeds but returns no backends. The function must return a non-error
// and an empty (non-nil) backend slice. (Ported from pkg/vmcp/cli/serve_test.go when
// the discovery logic moved into pkg/vmcp/app.)
func TestRunDiscovery_ZeroBackends(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	discoverer := aggregatormocks.NewMockBackendDiscoverer(ctrl)

	const groupRef = "test-group"
	discoverer.EXPECT().
		Discover(gomock.Any(), groupRef).
		Return([]vmcp.Backend{}, nil)

	backends, err := runDiscovery(t.Context(), groupRef, discoverer)

	require.NoError(t, err)
	assert.NotNil(t, backends)
	assert.Empty(t, backends)
}

// TestRunDiscovery_KubernetesGroupNotFound exercises the Kubernetes-specific branch in
// runDiscovery where ErrGroupNotFound is treated as a non-fatal condition. vMCP should
// start with zero backends and return nil error so it can begin serving before the
// MCPGroup CRD is created by the operator. (Ported from pkg/vmcp/cli/serve_test.go.)
func TestRunDiscovery_KubernetesGroupNotFound(t *testing.T) {
	// Cannot run in parallel: t.Setenv modifies the process environment.
	t.Setenv("TOOLHIVE_RUNTIME", "kubernetes")

	ctrl := gomock.NewController(t)
	discoverer := aggregatormocks.NewMockBackendDiscoverer(ctrl)

	const groupRef = "test-group"
	discoverer.EXPECT().
		Discover(gomock.Any(), groupRef).
		Return(nil, fmt.Errorf("wrapped: %w", groups.ErrGroupNotFound))

	backends, err := runDiscovery(t.Context(), groupRef, discoverer)

	require.NoError(t, err)
	assert.NotNil(t, backends)
	assert.Empty(t, backends)
}

// TestVMCPNamespace covers the VMCP_NAMESPACE fallback used to scope the rate limiter.
// (Ported from pkg/vmcp/cli/serve_test.go when vmcpNamespace moved into pkg/vmcp/app.)
func TestVMCPNamespace(t *testing.T) {
	t.Run("defaults to local", func(t *testing.T) {
		t.Setenv("VMCP_NAMESPACE", "")
		assert.Equal(t, "local", vmcpNamespace())
	})

	t.Run("uses environment value", func(t *testing.T) {
		t.Setenv("VMCP_NAMESPACE", "toolhive-system")
		assert.Equal(t, "toolhive-system", vmcpNamespace())
	})
}

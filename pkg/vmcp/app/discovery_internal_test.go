// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/vmcp"
	aggregatormocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
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

// TestDeriveHealthMonitorConfig covers the health-monitor config derivation: disabled
// when operational failure-handling is absent or the interval is unset, and the happy
// path where a valid config yields a MonitorConfig with the interval, threshold, and
// timeout (default or overridden) populated.
func TestDeriveHealthMonitorConfig(t *testing.T) {
	t.Parallel()

	defaults := health.DefaultConfig()

	t.Run("nil operational disables monitoring", func(t *testing.T) {
		t.Parallel()
		mc, err := deriveHealthMonitorConfig(&vmcpconfig.Config{})
		require.NoError(t, err)
		assert.Nil(t, mc)
	})

	t.Run("zero interval disables monitoring", func(t *testing.T) {
		t.Parallel()
		cfg := &vmcpconfig.Config{Operational: &vmcpconfig.OperationalConfig{
			FailureHandling: &vmcpconfig.FailureHandlingConfig{UnhealthyThreshold: 3},
		}}
		mc, err := deriveHealthMonitorConfig(cfg)
		require.NoError(t, err)
		assert.Nil(t, mc)
	})

	t.Run("happy path uses defaults for unset timeout", func(t *testing.T) {
		t.Parallel()
		cfg := &vmcpconfig.Config{Operational: &vmcpconfig.OperationalConfig{
			FailureHandling: &vmcpconfig.FailureHandlingConfig{
				HealthCheckInterval: vmcpconfig.Duration(30 * time.Second),
				UnhealthyThreshold:  3,
			},
		}}
		mc, err := deriveHealthMonitorConfig(cfg)
		require.NoError(t, err)
		require.NotNil(t, mc)
		assert.Equal(t, 30*time.Second, mc.CheckInterval)
		assert.Equal(t, 3, mc.UnhealthyThreshold)
		assert.Equal(t, defaults.Timeout, mc.Timeout)
		assert.Equal(t, defaults.DegradedThreshold, mc.DegradedThreshold)
		assert.Nil(t, mc.CircuitBreaker)
	})

	t.Run("happy path honors explicit timeout and circuit breaker", func(t *testing.T) {
		t.Parallel()
		cfg := &vmcpconfig.Config{Operational: &vmcpconfig.OperationalConfig{
			FailureHandling: &vmcpconfig.FailureHandlingConfig{
				HealthCheckInterval: vmcpconfig.Duration(30 * time.Second),
				HealthCheckTimeout:  vmcpconfig.Duration(5 * time.Second),
				UnhealthyThreshold:  2,
				CircuitBreaker: &vmcpconfig.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 4,
					Timeout:          vmcpconfig.Duration(time.Minute),
				},
			},
		}}
		mc, err := deriveHealthMonitorConfig(cfg)
		require.NoError(t, err)
		require.NotNil(t, mc)
		assert.Equal(t, 5*time.Second, mc.Timeout)
		require.NotNil(t, mc.CircuitBreaker)
		assert.True(t, mc.CircuitBreaker.Enabled)
		assert.Equal(t, 4, mc.CircuitBreaker.FailureThreshold)
		assert.Equal(t, time.Minute, mc.CircuitBreaker.Timeout)
	})
}

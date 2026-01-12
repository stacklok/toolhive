// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

func TestDeterminePhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		summary       health.Summary
		totalBackends int
		expectedPhase vmcp.Phase
	}{
		{
			name:          "no backends - pending",
			summary:       health.Summary{},
			totalBackends: 0,
			expectedPhase: vmcp.PhasePending,
		},
		{
			name: "all healthy - ready",
			summary: health.Summary{
				Total:   3,
				Healthy: 3,
			},
			totalBackends: 3,
			expectedPhase: vmcp.PhaseReady,
		},
		{
			name: "some degraded - degraded",
			summary: health.Summary{
				Total:    3,
				Healthy:  2,
				Degraded: 1,
			},
			totalBackends: 3,
			expectedPhase: vmcp.PhaseDegraded,
		},
		{
			name: "some unhealthy - degraded",
			summary: health.Summary{
				Total:     3,
				Healthy:   2,
				Unhealthy: 1,
			},
			totalBackends: 3,
			expectedPhase: vmcp.PhaseDegraded,
		},
		{
			name: "all unhealthy - failed",
			summary: health.Summary{
				Total:     3,
				Unhealthy: 3,
			},
			totalBackends: 3,
			expectedPhase: vmcp.PhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := &Server{}
			phase := srv.determinePhase(tt.summary, tt.totalBackends)
			assert.Equal(t, tt.expectedPhase, phase)
		})
	}
}

func TestBuildDiscoveredBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		backends         []vmcp.Backend
		expectedCount    int
		validateBackends func(t *testing.T, discovered []vmcp.DiscoveredBackend)
	}{
		{
			name:          "empty backends",
			backends:      []vmcp.Backend{},
			expectedCount: 0,
			validateBackends: func(t *testing.T, discovered []vmcp.DiscoveredBackend) {
				t.Helper()
				assert.Nil(t, discovered)
			},
		},
		{
			name: "single healthy backend",
			backends: []vmcp.Backend{
				{
					ID:           "backend-1",
					Name:         "Backend 1",
					BaseURL:      "http://backend1:8080",
					HealthStatus: vmcp.BackendHealthy,
				},
			},
			expectedCount: 1,
			validateBackends: func(t *testing.T, discovered []vmcp.DiscoveredBackend) {
				t.Helper()
				require.Len(t, discovered, 1)
				assert.Equal(t, "Backend 1", discovered[0].Name)
				assert.Equal(t, "http://backend1:8080", discovered[0].URL)
				assert.Equal(t, "ready", discovered[0].Status)
			},
		},
		{
			name: "multiple backends with different health states",
			backends: []vmcp.Backend{
				{
					ID:           "backend-1",
					Name:         "Backend 1",
					BaseURL:      "http://backend1:8080",
					HealthStatus: vmcp.BackendHealthy,
				},
				{
					ID:           "backend-2",
					Name:         "Backend 2",
					BaseURL:      "http://backend2:8080",
					HealthStatus: vmcp.BackendDegraded,
				},
				{
					ID:           "backend-3",
					Name:         "Backend 3",
					BaseURL:      "http://backend3:8080",
					HealthStatus: vmcp.BackendUnhealthy,
				},
			},
			expectedCount: 3,
			validateBackends: func(t *testing.T, discovered []vmcp.DiscoveredBackend) {
				t.Helper()
				require.Len(t, discovered, 3)
				assert.Equal(t, "ready", discovered[0].Status)
				assert.Equal(t, "degraded", discovered[1].Status)
				assert.Equal(t, "unavailable", discovered[2].Status)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := &Server{}
			discovered := srv.buildDiscoveredBackends(tt.backends)

			assert.Len(t, discovered, tt.expectedCount)
			if tt.validateBackends != nil {
				tt.validateBackends(t, discovered)
			}
		})
	}
}

func TestBuildConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		phase             vmcp.Phase
		totalBackends     int
		validateCondition func(t *testing.T, conditions []vmcp.Condition)
	}{
		{
			name:          "pending phase - no backends",
			phase:         vmcp.PhasePending,
			totalBackends: 0,
			validateCondition: func(t *testing.T, conditions []vmcp.Condition) {
				t.Helper()
				require.Len(t, conditions, 2)

				// BackendsDiscovered should be False
				backendsCond := conditions[0]
				assert.Equal(t, vmcp.ConditionTypeBackendsDiscovered, backendsCond.Type)
				assert.Equal(t, metav1.ConditionFalse, backendsCond.Status)

				// Ready should be False
				readyCond := conditions[1]
				assert.Equal(t, vmcp.ConditionTypeReady, readyCond.Type)
				assert.Equal(t, metav1.ConditionFalse, readyCond.Status)
				assert.Equal(t, vmcp.ReasonServerStarting, readyCond.Reason)
			},
		},
		{
			name:          "ready phase - backends discovered",
			phase:         vmcp.PhaseReady,
			totalBackends: 3,
			validateCondition: func(t *testing.T, conditions []vmcp.Condition) {
				t.Helper()
				require.Len(t, conditions, 2)

				// BackendsDiscovered should be True
				backendsCond := conditions[0]
				assert.Equal(t, vmcp.ConditionTypeBackendsDiscovered, backendsCond.Type)
				assert.Equal(t, metav1.ConditionTrue, backendsCond.Status)

				// Ready should be True
				readyCond := conditions[1]
				assert.Equal(t, vmcp.ConditionTypeReady, readyCond.Type)
				assert.Equal(t, metav1.ConditionTrue, readyCond.Status)
				assert.Equal(t, vmcp.ReasonServerReady, readyCond.Reason)
			},
		},
		{
			name:          "degraded phase",
			phase:         vmcp.PhaseDegraded,
			totalBackends: 3,
			validateCondition: func(t *testing.T, conditions []vmcp.Condition) {
				t.Helper()
				require.Len(t, conditions, 2)

				readyCond := conditions[1]
				assert.Equal(t, vmcp.ConditionTypeReady, readyCond.Type)
				assert.Equal(t, metav1.ConditionTrue, readyCond.Status)
				assert.Equal(t, vmcp.ReasonServerDegraded, readyCond.Reason)
			},
		},
		{
			name:          "failed phase",
			phase:         vmcp.PhaseFailed,
			totalBackends: 3,
			validateCondition: func(t *testing.T, conditions []vmcp.Condition) {
				t.Helper()
				require.Len(t, conditions, 2)

				readyCond := conditions[1]
				assert.Equal(t, vmcp.ConditionTypeReady, readyCond.Type)
				assert.Equal(t, metav1.ConditionFalse, readyCond.Status)
				assert.Equal(t, vmcp.ReasonServerFailed, readyCond.Reason)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := &Server{}
			conditions := srv.buildConditions(tt.phase, health.Summary{}, tt.totalBackends)

			if tt.validateCondition != nil {
				tt.validateCondition(t, conditions)
			}
		})
	}
}

func TestBuildMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		phase           vmcp.Phase
		totalBackends   int
		expectedMessage string
	}{
		{
			name:            "no backends",
			phase:           vmcp.PhasePending,
			totalBackends:   0,
			expectedMessage: "Waiting for backend discovery",
		},
		{
			name:            "ready",
			phase:           vmcp.PhaseReady,
			totalBackends:   3,
			expectedMessage: "All backends healthy",
		},
		{
			name:            "degraded",
			phase:           vmcp.PhaseDegraded,
			totalBackends:   3,
			expectedMessage: "Some backends unhealthy or degraded",
		},
		{
			name:            "failed",
			phase:           vmcp.PhaseFailed,
			totalBackends:   3,
			expectedMessage: "All backends unhealthy",
		},
		{
			name:            "pending with backends",
			phase:           vmcp.PhasePending,
			totalBackends:   3,
			expectedMessage: "Starting up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := &Server{}
			message := srv.buildMessage(tt.phase, health.Summary{}, tt.totalBackends)
			assert.Equal(t, tt.expectedMessage, message)
		})
	}
}

func TestBuildCurrentStatus(t *testing.T) {
	t.Parallel()

	// Create a mock backend registry
	mockRegistry := &mockBackendRegistry{
		backends: []vmcp.Backend{
			{
				ID:           "backend-1",
				Name:         "Backend 1",
				BaseURL:      "http://backend1:8080",
				HealthStatus: vmcp.BackendHealthy,
			},
		},
	}

	srv := &Server{
		backendRegistry: mockRegistry,
		healthMonitor:   nil, // No health monitoring
	}

	ctx := context.Background()
	status := srv.buildCurrentStatus(ctx)

	// Verify status structure
	assert.NotNil(t, status)
	assert.Equal(t, vmcp.PhaseReady, status.Phase)
	assert.Len(t, status.DiscoveredBackends, 1)
	assert.Len(t, status.Conditions, 2)
	assert.False(t, status.Timestamp.IsZero())

	// Verify discovered backend
	backend := status.DiscoveredBackends[0]
	assert.Equal(t, "Backend 1", backend.Name)
	assert.Equal(t, "http://backend1:8080", backend.URL)
	assert.Equal(t, "ready", backend.Status)
}

// mockBackendRegistry is a simple mock for testing
type mockBackendRegistry struct {
	backends []vmcp.Backend
}

func (m *mockBackendRegistry) List(_ context.Context) []vmcp.Backend {
	return m.backends
}

func (*mockBackendRegistry) Get(_ context.Context, _ string) *vmcp.Backend {
	return nil
}

func (m *mockBackendRegistry) Count() int {
	return len(m.backends)
}

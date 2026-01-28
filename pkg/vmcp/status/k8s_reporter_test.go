// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestNewK8sReporter_Validation tests parameter validation in NewK8sReporter.
func TestNewK8sReporter_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		restConfig  *rest.Config
		serverName  string
		namespace   string
		expectError string
	}{
		{
			name:        "nil rest config",
			restConfig:  nil,
			serverName:  "test-server",
			namespace:   "default",
			expectError: "restConfig cannot be nil",
		},
		{
			name:        "empty name",
			restConfig:  &rest.Config{},
			serverName:  "",
			namespace:   "default",
			expectError: "name cannot be empty",
		},
		{
			name:        "empty namespace",
			restConfig:  &rest.Config{},
			serverName:  "test-server",
			namespace:   "",
			expectError: "namespace cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter, err := NewK8sReporter(tt.restConfig, tt.serverName, tt.namespace)
			assert.Error(t, err)
			assert.Nil(t, reporter)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

// TestK8sReporter_ReportStatus_NilStatus tests that nil status is handled gracefully.
func TestK8sReporter_ReportStatus_NilStatus(t *testing.T) {
	t.Parallel()

	reporter, fakeClient := createTestReporter(t, "test-server", "default")
	createTestVirtualMCPServer(t, fakeClient, "test-server", "default")

	ctx := context.Background()
	err := reporter.ReportStatus(ctx, nil)
	assert.NoError(t, err, "nil status should be handled gracefully")
}

// TestK8sReporter_ReportStatus_Success tests successful status updates.
func TestK8sReporter_ReportStatus_Success(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		phase          vmcptypes.Phase
		expectedPhase  mcpv1alpha1.VirtualMCPServerPhase
		backendCount   int
		conditionCount int
	}{
		{
			name:           "ready phase with backends",
			phase:          vmcptypes.PhaseReady,
			expectedPhase:  mcpv1alpha1.VirtualMCPServerPhaseReady,
			backendCount:   2,
			conditionCount: 2,
		},
		{
			name:           "degraded phase",
			phase:          vmcptypes.PhaseDegraded,
			expectedPhase:  mcpv1alpha1.VirtualMCPServerPhaseDegraded,
			backendCount:   1,
			conditionCount: 1,
		},
		{
			name:           "failed phase",
			phase:          vmcptypes.PhaseFailed,
			expectedPhase:  mcpv1alpha1.VirtualMCPServerPhaseFailed,
			backendCount:   0,
			conditionCount: 1,
		},
		{
			name:           "pending phase",
			phase:          vmcptypes.PhasePending,
			expectedPhase:  mcpv1alpha1.VirtualMCPServerPhasePending,
			backendCount:   0,
			conditionCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter, fakeClient := createTestReporter(t, "test-server", "default")
			vmcpServer := createTestVirtualMCPServer(t, fakeClient, "test-server", "default")

			// Create test status
			status := &vmcptypes.Status{
				Phase:     tt.phase,
				Message:   "Test message",
				Timestamp: time.Now(),
			}

			// Add backends if specified
			for i := 0; i < tt.backendCount; i++ {
				status.DiscoveredBackends = append(status.DiscoveredBackends, vmcptypes.DiscoveredBackend{
					Name:            fmt.Sprintf("backend-%d", i+1),
					URL:             "http://backend:8080",
					Status:          vmcptypes.BackendHealthy.ToCRDStatus(),
					LastHealthCheck: metav1.Now(),
				})
			}
			// Set backend count to match number of healthy backends
			status.BackendCount = tt.backendCount

			// Add conditions if specified
			for i := 0; i < tt.conditionCount; i++ {
				status.Conditions = append(status.Conditions, metav1.Condition{
					Type:               fmt.Sprintf("Condition%d", i+1),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "TestReason",
					Message:            "Test condition message",
				})
			}

			ctx := context.Background()
			err := reporter.ReportStatus(ctx, status)
			require.NoError(t, err)

			// Verify the status was updated
			updated := &mcpv1alpha1.VirtualMCPServer{}
			err = fakeClient.Get(ctx, types.NamespacedName{
				Name:      "test-server",
				Namespace: "default",
			}, updated)
			require.NoError(t, err)

			// Verify phase conversion
			assert.Equal(t, tt.expectedPhase, updated.Status.Phase)

			// Verify message
			assert.Equal(t, "Test message", updated.Status.Message)

			// Verify backend count
			assert.Equal(t, tt.backendCount, updated.Status.BackendCount)
			assert.Len(t, updated.Status.DiscoveredBackends, tt.backendCount)

			// Verify conditions
			assert.Len(t, updated.Status.Conditions, tt.conditionCount)

			// Verify observed generation
			assert.Equal(t, vmcpServer.Generation, updated.Status.ObservedGeneration)
		})
	}
}

// TestK8sReporter_ReportStatus_BackendConversion tests backend conversion.
func TestK8sReporter_ReportStatus_BackendConversion(t *testing.T) {
	t.Parallel()

	reporter, fakeClient := createTestReporter(t, "test-server", "default")
	createTestVirtualMCPServer(t, fakeClient, "test-server", "default")

	now := metav1.Now()
	status := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseReady,
		Timestamp: time.Now(),
		DiscoveredBackends: []vmcptypes.DiscoveredBackend{
			{
				Name:            "backend-1",
				URL:             "http://backend-1:8080",
				Status:          vmcptypes.BackendHealthy.ToCRDStatus(),
				AuthConfigRef:   "auth-config-1",
				AuthType:        "oauth2",
				LastHealthCheck: now,
				Message:         "Healthy",
			},
			{
				Name:            "backend-2",
				URL:             "http://backend-2:8080",
				Status:          vmcptypes.BackendDegraded.ToCRDStatus(),
				LastHealthCheck: now,
				Message:         "Slow response times",
			},
		},
	}

	ctx := context.Background()
	err := reporter.ReportStatus(ctx, status)
	require.NoError(t, err)

	// Verify backends were converted correctly
	updated := &mcpv1alpha1.VirtualMCPServer{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)

	require.Len(t, updated.Status.DiscoveredBackends, 2)

	// Verify first backend
	backend1 := updated.Status.DiscoveredBackends[0]
	assert.Equal(t, "backend-1", backend1.Name)
	assert.Equal(t, "http://backend-1:8080", backend1.URL)
	assert.Equal(t, "ready", backend1.Status)
	assert.Equal(t, "auth-config-1", backend1.AuthConfigRef)
	assert.Equal(t, "oauth2", backend1.AuthType)
	// Compare timestamps with second precision (Kubernetes metav1.Time truncates to seconds)
	assert.True(t, backend1.LastHealthCheck.Truncate(time.Second).Equal(now.Truncate(time.Second)),
		"LastHealthCheck timestamps should match at second precision")
	assert.Equal(t, "Healthy", backend1.Message)

	// Verify second backend
	backend2 := updated.Status.DiscoveredBackends[1]
	assert.Equal(t, "backend-2", backend2.Name)
	assert.Equal(t, "degraded", backend2.Status)
	assert.Equal(t, "Slow response times", backend2.Message)
}

// TestK8sReporter_ReportStatus_ServerNotFound tests error handling when server doesn't exist.
func TestK8sReporter_ReportStatus_ServerNotFound(t *testing.T) {
	t.Parallel()

	reporter, _ := createTestReporter(t, "nonexistent-server", "default")

	status := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseReady,
		Timestamp: time.Now(),
	}

	ctx := context.Background()
	err := reporter.ReportStatus(ctx, status)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get VirtualMCPServer")
}

// TestK8sReporter_ReportStatus_ConcurrentUpdates tests that concurrent updates work correctly
// with retry logic.
func TestK8sReporter_ReportStatus_ConcurrentUpdates(t *testing.T) {
	t.Parallel()

	reporter, fakeClient := createTestReporter(t, "test-server", "default")
	createTestVirtualMCPServer(t, fakeClient, "test-server", "default")

	ctx := context.Background()

	// Simulate rapid successive updates (which could cause conflicts in a real scenario).
	// The retry logic ensures these all succeed.
	for i := range 5 {
		status := &vmcptypes.Status{
			Phase:     vmcptypes.PhaseReady,
			Message:   fmt.Sprintf("Update %d", i+1),
			Timestamp: time.Now(),
			DiscoveredBackends: []vmcptypes.DiscoveredBackend{
				{
					Name:            fmt.Sprintf("backend-%d", i+1),
					URL:             "http://backend:8080",
					Status:          vmcptypes.BackendHealthy.ToCRDStatus(),
					LastHealthCheck: metav1.Now(),
				},
			},
			BackendCount: 1, // One healthy backend
		}

		err := reporter.ReportStatus(ctx, status)
		require.NoError(t, err, "Update %d should succeed with retry logic", i+1)
	}

	// Verify the final state has the last update
	updated := &mcpv1alpha1.VirtualMCPServer{}
	err := fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)

	assert.Equal(t, "Update 5", updated.Status.Message)
	assert.Equal(t, 1, updated.Status.BackendCount)
	assert.Equal(t, "backend-5", updated.Status.DiscoveredBackends[0].Name)
}

// TestK8sReporter_ReportStatus_ConditionUpdates tests that conditions are properly updated.
// Note: LastTransitionTime preservation semantics are handled by meta.SetStatusCondition,
// which is tested by the Kubernetes project. We verify that conditions are correctly
// updated with the expected Status, Reason, and Message fields.
func TestK8sReporter_ReportStatus_ConditionUpdates(t *testing.T) {
	t.Parallel()

	reporter, fakeClient := createTestReporter(t, "test-server", "default")
	createTestVirtualMCPServer(t, fakeClient, "test-server", "default")
	ctx := context.Background()

	// First report: Ready condition with Status True
	status1 := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseReady,
		Message:   "All backends healthy",
		Timestamp: time.Now(),
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "AllBackendsHealthy",
				Message:            "All backends are healthy",
			},
		},
		BackendCount: 2,
	}

	err := reporter.ReportStatus(ctx, status1)
	require.NoError(t, err)

	// Verify condition was created
	updated := &mcpv1alpha1.VirtualMCPServer{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	assert.Equal(t, "Ready", updated.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, updated.Status.Conditions[0].Status)
	assert.Equal(t, "AllBackendsHealthy", updated.Status.Conditions[0].Reason)
	assert.Equal(t, "All backends are healthy", updated.Status.Conditions[0].Message)

	// Second report: update message while keeping Status True
	status2 := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseReady,
		Message:   "Still healthy",
		Timestamp: time.Now(),
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "AllBackendsHealthy",
				Message:            "All backends are still healthy",
			},
		},
		BackendCount: 2,
	}

	err = reporter.ReportStatus(ctx, status2)
	require.NoError(t, err)

	// Verify message was updated
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, updated.Status.Conditions[0].Status)
	assert.Equal(t, "All backends are still healthy", updated.Status.Conditions[0].Message)

	// Third report: change Status to False
	status3 := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseFailed,
		Message:   "No healthy backends",
		Timestamp: time.Now(),
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
				Reason:             "NoHealthyBackends",
				Message:            "No healthy backends available",
			},
		},
		BackendCount: 0,
	}

	err = reporter.ReportStatus(ctx, status3)
	require.NoError(t, err)

	// Verify Status was changed
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)
	require.Len(t, updated.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionFalse, updated.Status.Conditions[0].Status)
	assert.Equal(t, "NoHealthyBackends", updated.Status.Conditions[0].Reason)
	assert.Equal(t, "No healthy backends available", updated.Status.Conditions[0].Message)
}

// TestK8sReporter_ReportStatus_RemovesStaleConditions tests that conditions
// no longer present in status are removed from the resource.
func TestK8sReporter_ReportStatus_RemovesStaleConditions(t *testing.T) {
	t.Parallel()

	reporter, fakeClient := createTestReporter(t, "test-server", "default")
	createTestVirtualMCPServer(t, fakeClient, "test-server", "default")
	ctx := context.Background()

	// First report: system is degraded with both Ready and Degraded conditions
	status1 := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseDegraded,
		Message:   "Some backends unhealthy",
		Timestamp: time.Now(),
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
				Reason:             "SomeBackendsUnhealthy",
				Message:            "2/3 backends healthy",
			},
			{
				Type:               "Degraded",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "BackendsDegraded",
				Message:            "1 backend degraded",
			},
		},
		BackendCount: 2,
	}

	err := reporter.ReportStatus(ctx, status1)
	require.NoError(t, err)

	// Verify both conditions exist
	updated := &mcpv1alpha1.VirtualMCPServer{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)
	assert.Len(t, updated.Status.Conditions, 2, "Should have Ready and Degraded conditions")

	hasDegraded := false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Degraded" {
			hasDegraded = true
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
		}
	}
	assert.True(t, hasDegraded, "Degraded condition should be present")

	// Second report: system recovered, only Ready condition (no Degraded)
	status2 := &vmcptypes.Status{
		Phase:     vmcptypes.PhaseReady,
		Message:   "All backends healthy",
		Timestamp: time.Now(),
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "AllBackendsHealthy",
				Message:            "All 3 backends healthy",
			},
		},
		BackendCount: 3,
	}

	err = reporter.ReportStatus(ctx, status2)
	require.NoError(t, err)

	// Verify Degraded condition was removed
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      "test-server",
		Namespace: "default",
	}, updated)
	require.NoError(t, err)
	assert.Len(t, updated.Status.Conditions, 1, "Should have only Ready condition")

	hasReady := false
	hasDegraded = false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Ready" {
			hasReady = true
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
		}
		if cond.Type == "Degraded" {
			hasDegraded = true
		}
	}
	assert.True(t, hasReady, "Ready condition should be present")
	assert.False(t, hasDegraded, "Degraded condition should be removed after recovery")
}

// TestK8sReporter_Start tests the Start lifecycle method.
func TestK8sReporter_Start(t *testing.T) {
	t.Parallel()

	reporter, _ := createTestReporter(t, "test-server", "default")
	ctx := context.Background()

	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Shutdown should be idempotent
	err = shutdown(ctx)
	assert.NoError(t, err)

	err = shutdown(ctx)
	assert.NoError(t, err)
}

// TestK8sReporter_FullLifecycle tests the full reporter lifecycle.
func TestK8sReporter_FullLifecycle(t *testing.T) {
	t.Parallel()

	reporter, fakeClient := createTestReporter(t, "test-server", "default")
	createTestVirtualMCPServer(t, fakeClient, "test-server", "default")
	ctx := context.Background()

	// Start reporter
	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Report status multiple times
	for range 3 {
		status := &vmcptypes.Status{
			Phase:     vmcptypes.PhaseReady,
			Message:   "Operational",
			Timestamp: time.Now(),
		}
		err := reporter.ReportStatus(ctx, status)
		assert.NoError(t, err)
	}

	// Shutdown
	err = shutdown(ctx)
	assert.NoError(t, err)
}

// TestConvertPhase tests phase conversion logic.
func TestConvertPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         vmcptypes.Phase
		expectedPhase mcpv1alpha1.VirtualMCPServerPhase
	}{
		{
			name:          "ready phase",
			input:         vmcptypes.PhaseReady,
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseReady,
		},
		{
			name:          "degraded phase",
			input:         vmcptypes.PhaseDegraded,
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseDegraded,
		},
		{
			name:          "failed phase",
			input:         vmcptypes.PhaseFailed,
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhaseFailed,
		},
		{
			name:          "pending phase",
			input:         vmcptypes.PhasePending,
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
		{
			name:          "unknown phase defaults to pending",
			input:         vmcptypes.Phase("unknown"),
			expectedPhase: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertPhase(tt.input)
			assert.Equal(t, tt.expectedPhase, result)
		})
	}
}

// TestK8sReporter_ImplementsInterface verifies K8sReporter implements Reporter.
func TestK8sReporter_ImplementsInterface(t *testing.T) {
	t.Parallel()

	var _ Reporter = (*K8sReporter)(nil)
}

// createTestReporter creates a K8sReporter with a fake Kubernetes client for testing.
//
//nolint:unparam // namespace parameter provides flexibility for future tests
func createTestReporter(t *testing.T, name, namespace string) (*K8sReporter, client.Client) {
	t.Helper()

	// Create scheme with VirtualMCPServer types
	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	// Create fake client with status subresource support
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&mcpv1alpha1.VirtualMCPServer{}).
		Build()

	// Create reporter with fake client
	reporter := &K8sReporter{
		client:    fakeClient,
		name:      name,
		namespace: namespace,
	}

	return reporter, fakeClient
}

// createTestVirtualMCPServer creates a test VirtualMCPServer resource.
//
//nolint:unparam // name parameter provides flexibility for future tests
func createTestVirtualMCPServer(t *testing.T, fakeClient client.Client, name, namespace string) *mcpv1alpha1.VirtualMCPServer {
	t.Helper()

	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
			},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
		},
	}

	ctx := context.Background()
	err := fakeClient.Create(ctx, vmcpServer)
	require.NoError(t, err)

	return vmcpServer
}

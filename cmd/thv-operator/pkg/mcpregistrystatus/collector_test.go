package mcpregistrystatus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewStatusManager(t *testing.T) {
	t.Parallel()

	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "default",
		},
	}

	statusManager := NewStatusManager(registry)

	assert.NotNil(t, statusManager)
	sc := statusManager.(*StatusCollector)
	assert.Equal(t, registry, sc.mcpRegistry)
	assert.False(t, sc.hasChanges)
	assert.Empty(t, sc.conditions)
}

func TestStatusCollector_SetPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		phase mcpv1alpha1.MCPRegistryPhase
	}{
		{
			name:  "set pending phase",
			phase: mcpv1alpha1.MCPRegistryPhasePending,
		},
		{
			name:  "set ready phase",
			phase: mcpv1alpha1.MCPRegistryPhaseReady,
		},
		{
			name:  "set failed phase",
			phase: mcpv1alpha1.MCPRegistryPhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := &mcpv1alpha1.MCPRegistry{}
			collector := NewStatusManager(registry).(*StatusCollector)

			collector.SetPhase(tt.phase)

			assert.True(t, collector.hasChanges)
			assert.NotNil(t, collector.phase)
			assert.Equal(t, tt.phase, *collector.phase)
		})
	}
}

func TestStatusCollector_SetMessage(t *testing.T) {
	t.Parallel()

	registry := &mcpv1alpha1.MCPRegistry{}
	collector := NewStatusManager(registry).(*StatusCollector)
	testMessage := "Test message"

	collector.SetMessage(testMessage)

	assert.True(t, collector.hasChanges)
	assert.NotNil(t, collector.message)
	assert.Equal(t, testMessage, *collector.message)
}

func TestStatusCollector_SetAPIReadyCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reason    string
		message   string
		status    metav1.ConditionStatus
		expectKey string
	}{
		{
			name:      "API ready condition true",
			reason:    "APIReady",
			message:   "API is ready",
			status:    metav1.ConditionTrue,
			expectKey: mcpv1alpha1.ConditionAPIReady,
		},
		{
			name:      "API ready condition false",
			reason:    "APINotReady",
			message:   "API is not ready",
			status:    metav1.ConditionFalse,
			expectKey: mcpv1alpha1.ConditionAPIReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := &mcpv1alpha1.MCPRegistry{}
			collector := NewStatusManager(registry).(*StatusCollector)

			collector.SetAPIReadyCondition(tt.reason, tt.message, tt.status)

			assert.True(t, collector.hasChanges)
			assert.Contains(t, collector.conditions, tt.expectKey)

			condition := collector.conditions[tt.expectKey]
			assert.Equal(t, tt.expectKey, condition.Type)
			assert.Equal(t, tt.reason, condition.Reason)
			assert.Equal(t, tt.message, condition.Message)
			assert.Equal(t, tt.status, condition.Status)
		})
	}
}

func TestStatusCollector_SetSyncStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		phase        mcpv1alpha1.SyncPhase
		message      string
		attemptCount int
		lastSyncTime *metav1.Time
		lastSyncHash string
		serverCount  int
	}{
		{
			name:         "sync status idle",
			phase:        mcpv1alpha1.SyncPhaseIdle,
			message:      "No sync required",
			attemptCount: 0,
			lastSyncTime: &metav1.Time{Time: metav1.Now().Time},
			lastSyncHash: "abc123",
			serverCount:  5,
		},
		{
			name:         "sync status syncing",
			phase:        mcpv1alpha1.SyncPhaseSyncing,
			message:      "Sync in progress",
			attemptCount: 1,
			lastSyncTime: nil,
			lastSyncHash: "",
			serverCount:  0,
		},
		{
			name:         "sync status failed",
			phase:        mcpv1alpha1.SyncPhaseFailed,
			message:      "Sync failed: connection timeout",
			attemptCount: 3,
			lastSyncTime: &metav1.Time{Time: metav1.Now().Time},
			lastSyncHash: "old-hash",
			serverCount:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := &mcpv1alpha1.MCPRegistry{}
			collector := NewStatusManager(registry).(*StatusCollector)

			collector.SetSyncStatus(tt.phase, tt.message, tt.attemptCount, tt.lastSyncTime, tt.lastSyncHash, tt.serverCount)

			assert.True(t, collector.hasChanges)
			assert.NotNil(t, collector.syncStatus)

			syncStatus := collector.syncStatus
			assert.Equal(t, tt.phase, syncStatus.Phase)
			assert.Equal(t, tt.message, syncStatus.Message)
			assert.Equal(t, tt.attemptCount, syncStatus.AttemptCount)
			assert.Equal(t, tt.lastSyncTime, syncStatus.LastSyncTime)
			assert.Equal(t, tt.lastSyncHash, syncStatus.LastSyncHash)
			assert.Equal(t, tt.serverCount, syncStatus.ServerCount)
			// LastAttempt should be set to now
			assert.NotNil(t, syncStatus.LastAttempt)
		})
	}
}

func TestStatusCollector_SetAPIStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		phase    mcpv1alpha1.APIPhase
		message  string
		endpoint string
	}{
		{
			name:     "API status ready",
			phase:    mcpv1alpha1.APIPhaseReady,
			message:  "API is ready",
			endpoint: "http://test-api.default.svc.cluster.local:8080",
		},
		{
			name:     "API status deploying",
			phase:    mcpv1alpha1.APIPhaseDeploying,
			message:  "API is deploying",
			endpoint: "",
		},
		{
			name:     "API status error",
			phase:    mcpv1alpha1.APIPhaseError,
			message:  "Deployment failed",
			endpoint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := &mcpv1alpha1.MCPRegistry{}
			collector := NewStatusManager(registry).(*StatusCollector)

			collector.SetAPIStatus(tt.phase, tt.message, tt.endpoint)

			assert.True(t, collector.hasChanges)
			assert.NotNil(t, collector.apiStatus)

			apiStatus := collector.apiStatus
			assert.Equal(t, tt.phase, apiStatus.Phase)
			assert.Equal(t, tt.message, apiStatus.Message)
			assert.Equal(t, tt.endpoint, apiStatus.Endpoint)
		})
	}
}

func TestStatusCollector_SetAPIStatus_ReadySince(t *testing.T) {
	t.Parallel()

	t.Run("sets ReadySince when becoming ready", func(t *testing.T) {
		t.Parallel()
		registry := &mcpv1alpha1.MCPRegistry{
			Status: mcpv1alpha1.MCPRegistryStatus{
				APIStatus: &mcpv1alpha1.APIStatus{
					Phase: mcpv1alpha1.APIPhaseDeploying,
				},
			},
		}
		collector := NewStatusManager(registry).(*StatusCollector)

		collector.SetAPIStatus(mcpv1alpha1.APIPhaseReady, "API is ready", "http://test.com")

		assert.NotNil(t, collector.apiStatus.ReadySince)
	})

	t.Run("preserves ReadySince when already ready", func(t *testing.T) {
		t.Parallel()
		readySince := metav1.Now()
		registry := &mcpv1alpha1.MCPRegistry{
			Status: mcpv1alpha1.MCPRegistryStatus{
				APIStatus: &mcpv1alpha1.APIStatus{
					Phase:      mcpv1alpha1.APIPhaseReady,
					ReadySince: &readySince,
				},
			},
		}
		collector := NewStatusManager(registry).(*StatusCollector)

		collector.SetAPIStatus(mcpv1alpha1.APIPhaseReady, "API is ready", "http://test.com")

		assert.Equal(t, &readySince, collector.apiStatus.ReadySince)
	})

	t.Run("clears ReadySince when not ready", func(t *testing.T) {
		t.Parallel()
		registry := &mcpv1alpha1.MCPRegistry{}
		collector := NewStatusManager(registry).(*StatusCollector)

		collector.SetAPIStatus(mcpv1alpha1.APIPhaseError, "API failed", "")

		assert.Nil(t, collector.apiStatus.ReadySince)
	})
}

func TestStatusCollector_Apply(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create scheme and fake client
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create test registry
	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "default",
		},
		Status: mcpv1alpha1.MCPRegistryStatus{
			Phase:   mcpv1alpha1.MCPRegistryPhasePending,
			Message: "Initial state",
		},
	}

	// Create registry in fake client
	require.NoError(t, k8sClient.Create(ctx, registry))

	t.Run("applies no changes when hasChanges is false", func(t *testing.T) {
		t.Parallel()
		collector := NewStatusManager(registry).(*StatusCollector)

		err := collector.Apply(ctx, k8sClient)

		assert.NoError(t, err)
	})

	t.Run("verifies hasChanges behavior", func(t *testing.T) {
		t.Parallel()
		collector := NewStatusManager(registry).(*StatusCollector)

		// Initially no changes
		assert.False(t, collector.hasChanges)

		// Setting a value should mark as having changes
		collector.SetPhase(mcpv1alpha1.MCPRegistryPhaseReady)
		assert.True(t, collector.hasChanges)
	})

	t.Run("verifies status field collection", func(t *testing.T) {
		t.Parallel()
		collector := NewStatusManager(registry).(*StatusCollector)

		// Set various status fields
		collector.SetPhase(mcpv1alpha1.MCPRegistryPhaseReady)
		collector.SetMessage("Registry is ready")
		collector.SetSyncStatus(mcpv1alpha1.SyncPhaseComplete, "Sync complete", 0, &metav1.Time{}, "hash123", 5)
		collector.SetAPIStatus(mcpv1alpha1.APIPhaseReady, "API ready", "http://test-api.default.svc.cluster.local:8080")
		collector.SetAPIReadyCondition("APIReady", "API is ready", metav1.ConditionTrue)

		// Verify all fields are collected
		assert.True(t, collector.hasChanges)
		assert.NotNil(t, collector.phase)
		assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseReady, *collector.phase)
		assert.NotNil(t, collector.message)
		assert.Equal(t, "Registry is ready", *collector.message)
		assert.NotNil(t, collector.syncStatus)
		assert.Equal(t, mcpv1alpha1.SyncPhaseComplete, collector.syncStatus.Phase)
		assert.NotNil(t, collector.apiStatus)
		assert.Equal(t, mcpv1alpha1.APIPhaseReady, collector.apiStatus.Phase)
		assert.Equal(t, "http://test-api.default.svc.cluster.local:8080", collector.apiStatus.Endpoint)
		assert.Len(t, collector.conditions, 1)
		assert.Contains(t, collector.conditions, mcpv1alpha1.ConditionAPIReady)
	})
}

func TestStatusCollector_NoChanges(t *testing.T) {
	t.Parallel()

	registry := &mcpv1alpha1.MCPRegistry{}
	collector := NewStatusManager(registry).(*StatusCollector)

	// Initially no changes
	assert.False(t, collector.hasChanges)

	// After setting values, should have changes
	collector.SetPhase(mcpv1alpha1.MCPRegistryPhaseReady)
	assert.True(t, collector.hasChanges)
}

func TestStatusCollector_MultipleConditions(t *testing.T) {
	t.Parallel()

	registry := &mcpv1alpha1.MCPRegistry{}
	collector := NewStatusManager(registry).(*StatusCollector)

	// Add condition
	collector.SetAPIReadyCondition("APIReady", "API is ready", metav1.ConditionTrue)

	// Should have the condition
	assert.Len(t, collector.conditions, 1)
	assert.Contains(t, collector.conditions, mcpv1alpha1.ConditionAPIReady)
}

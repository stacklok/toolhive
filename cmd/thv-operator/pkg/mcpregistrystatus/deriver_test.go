package mcpregistrystatus

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewDefaultStatusDeriver(t *testing.T) {
	t.Parallel()

	deriver := NewDefaultStatusDeriver()
	assert.NotNil(t, deriver)
	assert.IsType(t, &DefaultStatusDeriver{}, deriver)
}

func TestDeriveOverallStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		syncStatus      *mcpv1alpha1.SyncStatus
		apiStatus       *mcpv1alpha1.APIStatus
		expectedPhase   mcpv1alpha1.MCPRegistryPhase
		expectedMessage string
		description     string
	}{
		{
			name: "sync failed - highest priority",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase:   mcpv1alpha1.SyncPhaseFailed,
				Message: "source unreachable",
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseReady,
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhaseFailed,
			expectedMessage: "Sync failed: source unreachable",
			description:     "Sync failure should take precedence over API ready state",
		},
		{
			name: "API error when sync is complete",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: mcpv1alpha1.SyncPhaseComplete,
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase:   mcpv1alpha1.APIPhaseError,
				Message: "deployment failed",
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhaseFailed,
			expectedMessage: "API deployment failed: deployment failed",
			description:     "API error should result in failed phase",
		},
		{
			name: "sync in progress",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: mcpv1alpha1.SyncPhaseSyncing,
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseDeploying,
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhaseSyncing,
			expectedMessage: "Registry data synchronization in progress",
			description:     "Syncing phase should be shown when sync is in progress",
		},
		{
			name: "both sync and API ready",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: mcpv1alpha1.SyncPhaseComplete,
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseReady,
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhaseReady,
			expectedMessage: "Registry is ready and API is serving requests",
			description:     "Both components ready should result in ready phase",
		},
		{
			name: "sync complete, API deploying",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: mcpv1alpha1.SyncPhaseComplete,
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseDeploying,
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhasePending,
			expectedMessage: "Registry data synced, API deployment in progress",
			description:     "Complete sync with deploying API should be pending",
		},
		{
			name: "sync complete, API status missing",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: mcpv1alpha1.SyncPhaseComplete,
			},
			apiStatus:       nil,
			expectedPhase:   mcpv1alpha1.MCPRegistryPhasePending,
			expectedMessage: "Registry data synced, API deployment pending",
			description:     "Complete sync without API status should be pending",
		},
		{
			name:            "both statuses nil",
			syncStatus:      nil,
			apiStatus:       nil,
			expectedPhase:   mcpv1alpha1.MCPRegistryPhasePending,
			expectedMessage: "Registry initialization in progress",
			description:     "No status information should default to pending",
		},
		{
			name:       "sync nil, API ready",
			syncStatus: nil,
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseReady,
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhasePending,
			expectedMessage: "Registry initialization in progress",
			description:     "Missing sync status should default to pending even with ready API",
		},
		{
			name: "sync complete, API unknown phase",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: mcpv1alpha1.SyncPhaseComplete,
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: "UnknownPhase",
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhasePending,
			expectedMessage: "Registry data synced, API deployment pending",
			description:     "Unknown API phase should be treated as not ready",
		},
		{
			name: "sync with unknown phase",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: "UnknownSyncPhase",
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseReady,
			},
			expectedPhase:   mcpv1alpha1.MCPRegistryPhasePending,
			expectedMessage: "Registry initialization in progress",
			description:     "Unknown sync phase should default to pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deriver := &DefaultStatusDeriver{}
			phase, message := deriver.DeriveOverallStatus(tt.syncStatus, tt.apiStatus)

			assert.Equal(t, tt.expectedPhase, phase, tt.description)
			assert.Equal(t, tt.expectedMessage, message, tt.description)
		})
	}
}

func TestDeriveOverallStatus_PriorityOrdering(t *testing.T) {
	t.Parallel()

	// Test that sync failures take precedence over API errors
	syncStatus := &mcpv1alpha1.SyncStatus{
		Phase:   mcpv1alpha1.SyncPhaseFailed,
		Message: "sync failed",
	}
	apiStatus := &mcpv1alpha1.APIStatus{
		Phase:   mcpv1alpha1.APIPhaseError,
		Message: "api failed",
	}

	deriver := &DefaultStatusDeriver{}
	phase, message := deriver.DeriveOverallStatus(syncStatus, apiStatus)

	assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseFailed, phase)
	assert.Contains(t, message, "Sync failed")
	assert.NotContains(t, message, "API deployment failed")
}

func TestDeriveOverallStatus_SyncingTakesPrecedence(t *testing.T) {
	t.Parallel()

	// Test that syncing takes precedence over API ready state
	syncStatus := &mcpv1alpha1.SyncStatus{
		Phase: mcpv1alpha1.SyncPhaseSyncing,
	}
	apiStatus := &mcpv1alpha1.APIStatus{
		Phase: mcpv1alpha1.APIPhaseReady,
	}

	deriver := &DefaultStatusDeriver{}
	phase, message := deriver.DeriveOverallStatus(syncStatus, apiStatus)

	assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseSyncing, phase)
	assert.Equal(t, "Registry data synchronization in progress", message)
}

func TestDeriveOverallStatus_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		syncStatus  *mcpv1alpha1.SyncStatus
		apiStatus   *mcpv1alpha1.APIStatus
		description string
	}{
		{
			name: "empty sync status with empty phase",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase: "",
			},
			apiStatus: &mcpv1alpha1.APIStatus{
				Phase: mcpv1alpha1.APIPhaseReady,
			},
			description: "Empty sync phase should be handled gracefully",
		},
		{
			name: "sync status with whitespace message",
			syncStatus: &mcpv1alpha1.SyncStatus{
				Phase:   mcpv1alpha1.SyncPhaseFailed,
				Message: "   ",
			},
			apiStatus:   nil,
			description: "Whitespace in error message should be preserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Should not panic and should return valid phase/message
			deriver := &DefaultStatusDeriver{}
			phase, message := deriver.DeriveOverallStatus(tt.syncStatus, tt.apiStatus)

			assert.NotEmpty(t, phase, tt.description)
			assert.NotEmpty(t, message, tt.description)
		})
	}
}

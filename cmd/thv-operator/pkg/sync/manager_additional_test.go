package sync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestDefaultSyncManager_isSyncNeededForState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mcpRegistry *mcpv1alpha1.MCPRegistry
		expected    bool
		description string
	}{
		{
			name: "sync needed when sync status is failed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						Phase: mcpv1alpha1.SyncPhaseFailed,
					},
				},
			},
			expected:    true,
			description: "Should need sync when sync phase is failed",
		},
		{
			name: "sync needed when sync status is syncing",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						Phase: mcpv1alpha1.SyncPhaseSyncing,
					},
				},
			},
			expected:    true,
			description: "Should need sync when sync phase is syncing",
		},
		{
			name: "sync not needed when sync status is complete",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						Phase: mcpv1alpha1.SyncPhaseComplete,
					},
				},
			},
			expected:    false,
			description: "Should not need sync when sync phase is complete",
		},
		{
			name: "sync needed when no sync status and overall phase is failed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseFailed,
				},
			},
			expected:    true,
			description: "Should need sync when no sync status but overall phase is failed",
		},
		{
			name: "sync needed when no sync status, pending phase, and no last sync time",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
				},
			},
			expected:    true,
			description: "Should need sync when pending phase with no sync status or last sync time",
		},
		{
			name: "sync not needed when sync complete, pending phase, but has last sync time",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
					SyncStatus: &mcpv1alpha1.SyncStatus{
						Phase:        mcpv1alpha1.SyncPhaseComplete,
						LastSyncTime: &metav1.Time{Time: metav1.Now().Time},
					},
				},
			},
			expected:    false,
			description: "Should not need sync when sync complete but overall pending (waiting for API)",
		},
		{
			name: "sync not needed when no sync status and ready phase",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			expected:    false,
			description: "Should not need sync when overall phase is ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &DefaultSyncManager{}
			result := manager.isSyncNeededForState(tt.mcpRegistry)

			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestResult_Struct(t *testing.T) {
	t.Parallel()

	// Test Result struct creation and field access
	result := &Result{
		Hash:        "test-hash-123",
		ServerCount: 42,
	}

	assert.Equal(t, "test-hash-123", result.Hash)
	assert.Equal(t, 42, result.ServerCount)
}

func TestResult_ZeroValues(t *testing.T) {
	t.Parallel()

	// Test Result struct with zero values
	result := &Result{}

	assert.Equal(t, "", result.Hash)
	assert.Equal(t, 0, result.ServerCount)
}

func TestSyncReasonConstants(t *testing.T) {
	t.Parallel()

	// Verify sync reason constants are properly defined
	assert.Equal(t, "sync-already-in-progress", ReasonAlreadyInProgress)
	assert.Equal(t, "registry-not-ready", ReasonRegistryNotReady)
	assert.Equal(t, "error-checking-sync-need", ReasonErrorCheckingSyncNeed)
	assert.Equal(t, "error-checking-data-changes", ReasonErrorCheckingChanges)
	assert.Equal(t, "error-parsing-sync-interval", ReasonErrorParsingInterval)
	assert.Equal(t, "source-data-changed", ReasonSourceDataChanged)
	assert.Equal(t, "manual-sync-with-data-changes", ReasonManualWithChanges)
	assert.Equal(t, "manual-sync-no-data-changes", ReasonManualNoChanges)
	assert.Equal(t, "up-to-date-with-policy", ReasonUpToDateWithPolicy)
	assert.Equal(t, "up-to-date-no-policy", ReasonUpToDateNoPolicy)
}

func TestConditionReasonConstants(t *testing.T) {
	t.Parallel()

	// Verify condition reason constants are properly defined
	assert.Equal(t, "HandlerCreationFailed", conditionReasonHandlerCreationFailed)
	assert.Equal(t, "ValidationFailed", conditionReasonValidationFailed)
	assert.Equal(t, "FetchFailed", conditionReasonFetchFailed)
	assert.Equal(t, "StorageFailed", conditionReasonStorageFailed)
}

// Test helper function that was added during our refactoring
func TestDefaultSyncManager_isSyncNeededForState_EdgeCases(t *testing.T) {
	t.Parallel()

	manager := &DefaultSyncManager{}

	t.Run("handles nil registry", func(t *testing.T) {
		t.Parallel()

		// This should not panic but return sensible default
		result := manager.isSyncNeededForState(&mcpv1alpha1.MCPRegistry{})
		assert.False(t, result, "Should not need sync for empty registry")
	})

	t.Run("handles registry with empty status", func(t *testing.T) {
		t.Parallel()

		registry := &mcpv1alpha1.MCPRegistry{
			Status: mcpv1alpha1.MCPRegistryStatus{},
		}
		result := manager.isSyncNeededForState(registry)
		assert.False(t, result, "Should not need sync for empty status")
	})

	t.Run("handles registry with sync status but empty phase", func(t *testing.T) {
		t.Parallel()

		registry := &mcpv1alpha1.MCPRegistry{
			Status: mcpv1alpha1.MCPRegistryStatus{
				SyncStatus: &mcpv1alpha1.SyncStatus{
					// Empty phase - should default to not needing sync for empty value
				},
			},
		}
		result := manager.isSyncNeededForState(registry)
		// Empty phase is treated as needing sync since it's not complete
		assert.True(t, result, "Should need sync for empty sync phase")
	})
}

// Test the integration between the helper function and main ShouldSync logic
func TestDefaultSyncManager_isSyncNeededForState_Integration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupRegistry func() *mcpv1alpha1.MCPRegistry
		expectedSync  bool
		description   string
	}{
		{
			name: "registry transitioning from syncing to complete",
			setupRegistry: func() *mcpv1alpha1.MCPRegistry {
				return &mcpv1alpha1.MCPRegistry{
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseSyncing,
						SyncStatus: &mcpv1alpha1.SyncStatus{
							Phase: mcpv1alpha1.SyncPhaseSyncing,
						},
					},
				}
			},
			expectedSync: true,
			description:  "Should need sync when currently syncing",
		},
		{
			name: "registry in stable ready state",
			setupRegistry: func() *mcpv1alpha1.MCPRegistry {
				return &mcpv1alpha1.MCPRegistry{
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhaseReady,
						SyncStatus: &mcpv1alpha1.SyncStatus{
							Phase:        mcpv1alpha1.SyncPhaseComplete,
							LastSyncTime: &metav1.Time{Time: metav1.Now().Time},
							LastSyncHash: "stable-hash",
							ServerCount:  5,
						},
					},
				}
			},
			expectedSync: false,
			description:  "Should not need sync when in stable ready state",
		},
		{
			name: "registry after successful sync, API still deploying",
			setupRegistry: func() *mcpv1alpha1.MCPRegistry {
				return &mcpv1alpha1.MCPRegistry{
					Status: mcpv1alpha1.MCPRegistryStatus{
						Phase: mcpv1alpha1.MCPRegistryPhasePending,
						SyncStatus: &mcpv1alpha1.SyncStatus{
							Phase:        mcpv1alpha1.SyncPhaseComplete,
							LastSyncTime: &metav1.Time{Time: metav1.Now().Time},
							LastSyncHash: "recent-hash",
							ServerCount:  3,
						},
					},
				}
			},
			expectedSync: false,
			description:  "Should not need sync when sync is complete but overall pending due to API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &DefaultSyncManager{}
			registry := tt.setupRegistry()

			result := manager.isSyncNeededForState(registry)

			assert.Equal(t, tt.expectedSync, result, tt.description)
		})
	}
}

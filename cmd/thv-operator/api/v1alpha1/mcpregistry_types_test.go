package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPRegistry_DeriveOverallPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		syncStatus    *SyncStatus
		apiStatus     *APIStatus
		expectedPhase MCPRegistryPhase
		description   string
	}{
		// No status set (initial state)
		{
			name:          "no_status_set",
			syncStatus:    nil,
			apiStatus:     nil,
			expectedPhase: MCPRegistryPhasePending,
			description:   "Default to pending when no status is set",
		},

		// Sync Failed cases
		{
			name: "sync_failed_api_notstarted",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseFailed,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseNotStarted,
			},
			expectedPhase: MCPRegistryPhaseFailed,
			description:   "Sync failed should result in Failed regardless of API status",
		},
		{
			name: "sync_failed_api_ready",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseFailed,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseReady,
			},
			expectedPhase: MCPRegistryPhaseFailed,
			description:   "Sync failed should result in Failed even when API is ready",
		},

		// Sync Syncing cases
		{
			name: "sync_syncing_api_notstarted",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseSyncing,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseNotStarted,
			},
			expectedPhase: MCPRegistryPhaseSyncing,
			description:   "Sync in progress should result in Syncing regardless of API status",
		},
		{
			name: "sync_syncing_api_ready",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseSyncing,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseReady,
			},
			expectedPhase: MCPRegistryPhaseSyncing,
			description:   "Sync in progress should result in Syncing even when API is ready",
		},

		// Sync Complete + API combinations
		{
			name: "sync_complete_api_ready",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseComplete,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseReady,
			},
			expectedPhase: MCPRegistryPhaseReady,
			description:   "Sync complete + API ready should result in Ready",
		},
		{
			name: "sync_complete_api_error",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseComplete,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseError,
			},
			expectedPhase: MCPRegistryPhaseFailed,
			description:   "Sync complete + API error should result in Failed",
		},
		{
			name: "sync_complete_api_notstarted",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseComplete,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseNotStarted,
			},
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync complete + API not started should result in Pending",
		},
		{
			name: "sync_complete_api_deploying",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseComplete,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseDeploying,
			},
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync complete + API deploying should result in Pending",
		},
		{
			name: "sync_complete_api_unhealthy",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseComplete,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseUnhealthy,
			},
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync complete + API unhealthy should result in Pending",
		},

		// Sync Idle + API combinations (key test cases for the recent fix)
		{
			name: "sync_idle_api_ready",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseIdle,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseReady,
			},
			expectedPhase: MCPRegistryPhaseReady,
			description:   "Sync idle + API ready should result in Ready (fixed behavior)",
		},
		{
			name: "sync_idle_api_error",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseIdle,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseError,
			},
			expectedPhase: MCPRegistryPhaseFailed,
			description:   "Sync idle + API error should result in Failed",
		},
		{
			name: "sync_idle_api_notstarted",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseIdle,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseNotStarted,
			},
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync idle + API not started should result in Pending",
		},
		{
			name: "sync_idle_api_deploying",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseIdle,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseDeploying,
			},
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync idle + API deploying should result in Pending",
		},
		{
			name: "sync_idle_api_unhealthy",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseIdle,
			},
			apiStatus: &APIStatus{
				Phase: APIPhaseUnhealthy,
			},
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync idle + API unhealthy should result in Pending",
		},

		// Partial status combinations (one nil, one set)
		{
			name:          "sync_complete_api_nil",
			syncStatus:    &SyncStatus{Phase: SyncPhaseComplete},
			apiStatus:     nil,
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync complete + API nil should result in Pending (API defaults to NotStarted)",
		},
		{
			name:          "sync_nil_api_ready",
			syncStatus:    nil,
			apiStatus:     &APIStatus{Phase: APIPhaseReady},
			expectedPhase: MCPRegistryPhaseReady,
			description:   "Sync nil + API ready should result in Ready (sync defaults to Idle, which is treated as valid)",
		},

		// Edge case: sync idle with nil API (common in real scenarios)
		{
			name: "sync_idle_api_nil",
			syncStatus: &SyncStatus{
				Phase: SyncPhaseIdle,
			},
			apiStatus:     nil,
			expectedPhase: MCPRegistryPhasePending,
			description:   "Sync idle + API nil should result in Pending (API defaults to NotStarted)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create MCPRegistry with the specified status
			registry := &MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Status: MCPRegistryStatus{
					SyncStatus: tt.syncStatus,
					APIStatus:  tt.apiStatus,
				},
			}

			// Call DeriveOverallPhase and verify result
			actualPhase := registry.DeriveOverallPhase()

			assert.Equal(t, tt.expectedPhase, actualPhase,
				"Test case: %s\nDescription: %s\nSync phase: %v\nAPI phase: %v",
				tt.name, tt.description,
				getSyncPhaseString(tt.syncStatus),
				getAPIPhaseString(tt.apiStatus))
		})
	}
}

// Helper function to get sync phase as string for better test output
func getSyncPhaseString(syncStatus *SyncStatus) string {
	if syncStatus == nil {
		return "nil (defaults to Idle)"
	}
	return string(syncStatus.Phase)
}

// Helper function to get API phase as string for better test output
func getAPIPhaseString(apiStatus *APIStatus) string {
	if apiStatus == nil {
		return "nil (defaults to NotStarted)"
	}
	return string(apiStatus.Phase)
}

// TestMCPRegistry_DeriveOverallPhase_EdgeCases tests additional edge cases and regression scenarios
func TestMCPRegistry_DeriveOverallPhase_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("regression_test_idle_ready_becomes_ready", func(t *testing.T) {
		t.Parallel()
		// This is the specific regression test for the bug fix where
		// syncPhase=Idle + apiPhase=Ready was incorrectly returning Pending
		registry := &MCPRegistry{
			Status: MCPRegistryStatus{
				SyncStatus: &SyncStatus{Phase: SyncPhaseIdle},
				APIStatus:  &APIStatus{Phase: APIPhaseReady},
			},
		}

		phase := registry.DeriveOverallPhase()
		assert.Equal(t, MCPRegistryPhaseReady, phase,
			"The specific case syncPhase=Idle + apiPhase=Ready should result in Ready phase")
	})

	t.Run("all_api_phases_with_failed_sync", func(t *testing.T) {
		t.Parallel()
		// Verify that sync failed always results in overall failed regardless of API phase
		apiPhases := []APIPhase{
			APIPhaseNotStarted,
			APIPhaseDeploying,
			APIPhaseReady,
			APIPhaseUnhealthy,
			APIPhaseError,
		}

		for _, apiPhase := range apiPhases {
			t.Run(string(apiPhase), func(t *testing.T) {
				t.Parallel()
				registry := &MCPRegistry{
					Status: MCPRegistryStatus{
						SyncStatus: &SyncStatus{Phase: SyncPhaseFailed},
						APIStatus:  &APIStatus{Phase: apiPhase},
					},
				}

				phase := registry.DeriveOverallPhase()
				assert.Equal(t, MCPRegistryPhaseFailed, phase,
					"Sync failed should always result in Failed phase regardless of API phase: %s", apiPhase)
			})
		}
	})

	t.Run("all_api_phases_with_syncing", func(t *testing.T) {
		t.Parallel()
		// Verify that sync in progress always results in overall syncing regardless of API phase
		apiPhases := []APIPhase{
			APIPhaseNotStarted,
			APIPhaseDeploying,
			APIPhaseReady,
			APIPhaseUnhealthy,
			APIPhaseError,
		}

		for _, apiPhase := range apiPhases {
			t.Run(string(apiPhase), func(t *testing.T) {
				t.Parallel()
				registry := &MCPRegistry{
					Status: MCPRegistryStatus{
						SyncStatus: &SyncStatus{Phase: SyncPhaseSyncing},
						APIStatus:  &APIStatus{Phase: apiPhase},
					},
				}

				phase := registry.DeriveOverallPhase()
				assert.Equal(t, MCPRegistryPhaseSyncing, phase,
					"Sync in progress should always result in Syncing phase regardless of API phase: %s", apiPhase)
			})
		}
	})
}

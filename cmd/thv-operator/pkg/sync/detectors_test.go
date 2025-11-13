package sync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
)

func TestDefaultManualSyncChecker_IsManualSyncRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		mcpRegistry       *mcpv1alpha1.MCPRegistry
		expectedRequested bool
		expectedReason    string
	}{
		{
			name: "no annotations",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			expectedRequested: false,
			expectedReason:    ManualSyncReasonNoAnnotations,
		},
		{
			name: "no sync trigger annotation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other-annotation": "value",
					},
				},
			},
			expectedRequested: false,
			expectedReason:    ManualSyncReasonNoTrigger,
		},
		{
			name: "empty sync trigger annotation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						mcpregistrystatus.SyncTriggerAnnotation: "",
					},
				},
			},
			expectedRequested: false,
			expectedReason:    ManualSyncReasonNoTrigger,
		},
		{
			name: "manual sync already processed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						mcpregistrystatus.SyncTriggerAnnotation: "trigger-123",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					LastManualSyncTrigger: "trigger-123", // Same as annotation
				},
			},
			expectedRequested: false,
			expectedReason:    ManualSyncReasonAlreadyProcessed,
		},
		{
			name: "manual sync requested with new trigger",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						mcpregistrystatus.SyncTriggerAnnotation: "trigger-456",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					LastManualSyncTrigger: "trigger-123", // Different from annotation
				},
			},
			expectedRequested: true,
			expectedReason:    ManualSyncReasonRequested,
		},
		{
			name: "manual sync requested with no previous trigger",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						mcpregistrystatus.SyncTriggerAnnotation: "first-trigger",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					LastManualSyncTrigger: "", // No previous trigger
				},
			},
			expectedRequested: true,
			expectedReason:    ManualSyncReasonRequested,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &DefaultManualSyncChecker{}
			requested, reason := checker.IsManualSyncRequested(tt.mcpRegistry)

			assert.Equal(t, tt.expectedRequested, requested, "Manual sync request detection should match expected")
			assert.Equal(t, tt.expectedReason, reason, "Manual sync reason should match expected")
		})
	}
}

func TestDefaultAutomaticSyncChecker_IsIntervalSyncNeeded(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name                 string
		mcpRegistry          *mcpv1alpha1.MCPRegistry
		expectedSyncNeeded   bool
		expectedNextTimeFunc func(time.Time) bool // Function to verify nextSyncTime
		expectError          bool
	}{
		{
			name: "invalid interval format",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Spec: mcpv1alpha1.MCPRegistrySpec{
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "invalid-duration",
					},
				},
			},
			expectedSyncNeeded: false,
			expectError:        true,
		},
		{
			name: "no last sync time - sync needed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Spec: mcpv1alpha1.MCPRegistrySpec{
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "1h",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncTime: nil,
					},
				},
			},
			expectedSyncNeeded: true,
			expectedNextTimeFunc: func(nextTime time.Time) bool {
				// Should be approximately now + 1 hour
				expected := now.Add(time.Hour)
				return nextTime.After(expected.Add(-time.Minute)) && nextTime.Before(expected.Add(time.Minute))
			},
			expectError: false,
		},
		{
			name: "last sync time in past - sync needed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Spec: mcpv1alpha1.MCPRegistrySpec{
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "30m",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncTime: &metav1.Time{Time: now.Add(-time.Hour)}, // 1 hour ago
					},
				},
			},
			expectedSyncNeeded: true,
			expectedNextTimeFunc: func(nextTime time.Time) bool {
				// Should be approximately now + 30 minutes
				expected := now.Add(30 * time.Minute)
				return nextTime.After(expected.Add(-time.Minute)) && nextTime.Before(expected.Add(time.Minute))
			},
			expectError: false,
		},
		{
			name: "last sync time recent - sync not needed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Spec: mcpv1alpha1.MCPRegistrySpec{
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "1h",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastAttempt:  &metav1.Time{Time: now.Add(-30 * time.Minute)}, // 30 minutes ago
						LastSyncTime: &metav1.Time{Time: now.Add(-30 * time.Minute)}, // 30 minutes ago
					},
				},
			},
			expectedSyncNeeded: false,
			expectedNextTimeFunc: func(nextTime time.Time) bool {
				// Should be approximately now + 30 minutes (lastSync + 1h)
				expected := now.Add(30 * time.Minute)
				return nextTime.After(expected.Add(-time.Minute)) && nextTime.Before(expected.Add(time.Minute))
			},
			expectError: false,
		},
		{
			name: "last sync time exactly at interval - sync needed",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				Spec: mcpv1alpha1.MCPRegistrySpec{
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "1h",
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncTime: &metav1.Time{Time: now.Add(-time.Hour)}, // Exactly 1 hour ago
					},
				},
			},
			expectedSyncNeeded: true,
			expectedNextTimeFunc: func(nextTime time.Time) bool {
				// Should be approximately now + 1 hour
				expected := now.Add(time.Hour)
				return nextTime.After(expected.Add(-time.Minute)) && nextTime.Before(expected.Add(time.Minute))
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &DefaultAutomaticSyncChecker{}
			syncNeeded, nextSyncTime, err := checker.IsIntervalSyncNeeded(tt.mcpRegistry)

			assert.Equal(t, tt.expectedSyncNeeded, syncNeeded, "Sync needed result should match expected")

			if tt.expectError {
				assert.Error(t, err, "Expected an error")
			} else {
				assert.NoError(t, err, "Should not have an error")

				if tt.expectedNextTimeFunc != nil {
					assert.True(t, tt.expectedNextTimeFunc(nextSyncTime),
						"Next sync time should be within expected range. Got: %v", nextSyncTime)
				}

				// Verify nextSyncTime is always in the future (this was the bug we fixed)
				assert.True(t, nextSyncTime.After(time.Now()),
					"Next sync time should always be in the future. Got: %v", nextSyncTime)
			}
		})
	}
}

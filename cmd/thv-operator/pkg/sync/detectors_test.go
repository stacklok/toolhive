package sync

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

func TestDefaultDataChangeDetector_IsDataChanged(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name            string
		mcpRegistry     *mcpv1alpha1.MCPRegistry
		configMap       *corev1.ConfigMap
		expectedChanged bool
		expectError     bool
	}{
		{
			name: "data changed when no last sync hash",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-configmap",
							Key:  "registry.json",
						},
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncHash: "", // No hash means data changed
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-configmap",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": "test data",
				},
			},
			expectedChanged: true,
			expectError:     false,
		},
		{
			name: "data unchanged when hash matches",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-configmap",
							Key:  "registry.json",
						},
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncHash: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", // SHA256 of "test"
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-configmap",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": "test", // Same content
				},
			},
			expectedChanged: false,
			expectError:     false,
		},
		{
			name: "data changed when hash differs",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-configmap",
							Key:  "registry.json",
						},
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncHash: "old-hash",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-configmap",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": "new data",
				},
			},
			expectedChanged: true,
			expectError:     false,
		},
		{
			name: "error when configmap not found",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "missing-configmap",
							Key:  "registry.json",
						},
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					SyncStatus: &mcpv1alpha1.SyncStatus{
						LastSyncHash: "some-hash",
					},
				},
			},
			configMap:       nil,  // ConfigMap doesn't exist
			expectedChanged: true, // Should return true on error
			expectError:     true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.mcpRegistry}
			if tt.configMap != nil {
				objects = append(objects, tt.configMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
			detector := &DefaultDataChangeDetector{
				sourceHandlerFactory: sourceHandlerFactory,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			changed, err := detector.IsDataChanged(ctx, tt.mcpRegistry)

			assert.Equal(t, tt.expectedChanged, changed, "Data change detection result should match expected")

			if tt.expectError {
				assert.Error(t, err, "Expected an error")
			} else {
				assert.NoError(t, err, "Should not have an error")
			}
		})
	}
}

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

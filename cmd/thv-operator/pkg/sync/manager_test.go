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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

func TestNewDefaultSyncManager(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
	storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)

	syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

	assert.NotNil(t, syncManager)
	assert.IsType(t, &DefaultSyncManager{}, syncManager)
}

func TestDefaultSyncManager_ShouldSync(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name               string
		mcpRegistry        *mcpv1alpha1.MCPRegistry
		configMap          *corev1.ConfigMap
		expectedSyncNeeded bool
		expectedReason     string
		expectedNextTime   bool // whether nextSyncTime should be set
	}{
		{
			name: "sync needed when registry is in pending state",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
				},
			},
			configMap:          nil,
			expectedSyncNeeded: true,
			expectedReason:     ReasonRegistryNotReady,
			expectedNextTime:   false,
		},
		{
			name: "sync not needed when already syncing",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseSyncing,
				},
			},
			configMap:          nil,
			expectedSyncNeeded: false,
			expectedReason:     ReasonAlreadyInProgress,
			expectedNextTime:   false,
		},
		{
			name: "sync needed when no last sync hash",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
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
					Phase:        mcpv1alpha1.MCPRegistryPhaseReady,
					LastSyncHash: "", // No hash means data changed
				},
			},
			configMap:          nil,
			expectedSyncNeeded: true,
			expectedReason:     ReasonSourceDataChanged,
			expectedNextTime:   false,
		},
		{
			name: "manual sync requested with new trigger value",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
					Annotations: map[string]string{
						"toolhive.stacklok.dev/sync-trigger": "manual-sync-123",
					},
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
					Phase:                 mcpv1alpha1.MCPRegistryPhaseReady,
					LastSyncHash:          "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", // SHA256 of "test"
					LastManualSyncTrigger: "old-trigger",
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-configmap",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": "test", // This will produce the same hash as above
				},
			},
			expectedSyncNeeded: true,
			expectedReason:     ReasonManualNoChanges, // No data changes but manual trigger
			expectedNextTime:   false,
		},
	}

	for _, tt := range tests {
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
			storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)
			syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			syncNeeded, reason, nextSyncTime, err := syncManager.ShouldSync(ctx, tt.mcpRegistry)

			// We expect some errors for ConfigMap not found, but that's okay for this test
			if tt.expectedSyncNeeded {
				assert.True(t, syncNeeded, "Expected sync to be needed")
				assert.Equal(t, tt.expectedReason, reason, "Expected specific sync reason")
			} else {
				assert.False(t, syncNeeded, "Expected sync not to be needed")
				assert.Equal(t, tt.expectedReason, reason, "Expected specific sync reason")
				assert.NoError(t, err, "Should not have error when sync not needed")
			}

			if tt.expectedNextTime {
				assert.NotNil(t, nextSyncTime, "Expected next sync time to be set")
			} else {
				assert.Nil(t, nextSyncTime, "Expected next sync time to be nil")
			}
		})
	}
}

func TestDefaultSyncManager_PerformSync(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name               string
		mcpRegistry        *mcpv1alpha1.MCPRegistry
		sourceConfigMap    *corev1.ConfigMap
		existingStorageCM  *corev1.ConfigMap
		expectedError      bool
		expectedPhase      mcpv1alpha1.MCPRegistryPhase
		errorContains      string
		validateConditions bool
	}{
		{
			name: "successful sync with valid data",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "source-configmap",
							Key:  "registry.json",
						},
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
				},
			},
			sourceConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "source-configmap",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": `{"version": "1.0.0", "last_updated": "2023-01-01T00:00:00Z", "servers": {"test-server": {"name": "test-server", "description": "Test server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["test_tool"], "image": "test/image:latest"}}, "remoteServers": {}}`,
				},
			},
			expectedError:      false,
			expectedPhase:      mcpv1alpha1.MCPRegistryPhaseReady,
			validateConditions: true,
		},
		{
			name: "sync fails when source configmap not found",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
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
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
				},
			},
			sourceConfigMap:    nil,
			expectedError:      false, // PerformSync handles errors internally and sets phase
			expectedPhase:      mcpv1alpha1.MCPRegistryPhaseFailed,
			errorContains:      "",
			validateConditions: false,
		},
		{
			name: "sync with manual trigger annotation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
					Annotations: map[string]string{
						"toolhive.stacklok.dev/sync-trigger": "manual-123",
					},
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "source-configmap",
							Key:  "registry.json",
						},
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			sourceConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "source-configmap",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": `{"version": "1.0.0", "last_updated": "2023-01-01T00:00:00Z", "servers": {}, "remoteServers": {}}`,
				},
			},
			expectedError:      false,
			expectedPhase:      mcpv1alpha1.MCPRegistryPhaseReady,
			validateConditions: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.mcpRegistry}
			if tt.sourceConfigMap != nil {
				objects = append(objects, tt.sourceConfigMap)
			}
			if tt.existingStorageCM != nil {
				objects = append(objects, tt.existingStorageCM)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1alpha1.MCPRegistry{}).
				Build()

			sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
			storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)
			syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := syncManager.PerformSync(ctx, tt.mcpRegistry)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			// Verify the result
			assert.NotNil(t, result)

			// Check the registry object directly since PerformSync modifies it in place
			assert.Equal(t, tt.expectedPhase, tt.mcpRegistry.Status.Phase)

			if tt.validateConditions {
				// Check that success conditions are set when sync succeeds
				if tt.expectedPhase == mcpv1alpha1.MCPRegistryPhaseReady {
					assert.Len(t, tt.mcpRegistry.Status.Conditions, 3)

					// Verify manual sync trigger is processed if annotation exists
					if tt.mcpRegistry.Annotations != nil {
						if triggerValue := tt.mcpRegistry.Annotations["toolhive.stacklok.dev/sync-trigger"]; triggerValue != "" {
							assert.Equal(t, triggerValue, tt.mcpRegistry.Status.LastManualSyncTrigger)
						}
					}
				}
			}
		})
	}
}

func TestDefaultSyncManager_UpdateManualSyncTriggerOnly(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name                 string
		mcpRegistry          *mcpv1alpha1.MCPRegistry
		expectedError        bool
		expectedTriggerValue string
	}{
		{
			name: "update manual sync trigger with annotation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
					Annotations: map[string]string{
						"toolhive.stacklok.dev/sync-trigger": "manual-trigger-123",
					},
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			expectedError:        false,
			expectedTriggerValue: "manual-trigger-123",
		},
		{
			name: "no trigger annotation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
				Status: mcpv1alpha1.MCPRegistryStatus{
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
				},
			},
			expectedError:        false,
			expectedTriggerValue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.mcpRegistry}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1alpha1.MCPRegistry{}).
				Build()

			sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
			storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)
			syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := syncManager.UpdateManualSyncTriggerOnly(ctx, tt.mcpRegistry)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}

			// Check the registry object directly since UpdateManualSyncTriggerOnly modifies it in place
			assert.Equal(t, tt.expectedTriggerValue, tt.mcpRegistry.Status.LastManualSyncTrigger)
		})
	}
}

func TestDefaultSyncManager_Delete(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name             string
		mcpRegistry      *mcpv1alpha1.MCPRegistry
		storageConfigMap *corev1.ConfigMap
		expectedError    bool
	}{
		{
			name: "delete with existing storage configmap",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
			},
			storageConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"registry.json": `{"version": "1.0.0", "servers": {}}`,
				},
			},
			expectedError: false,
		},
		{
			name: "delete with no storage configmap (should succeed)",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
			},
			storageConfigMap: nil,
			expectedError:    false, // Delete should be idempotent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.mcpRegistry}
			if tt.storageConfigMap != nil {
				objects = append(objects, tt.storageConfigMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
			storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)
			syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := syncManager.Delete(ctx, tt.mcpRegistry)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify storage ConfigMap was deleted if it existed
				if tt.storageConfigMap != nil {
					configMap := &corev1.ConfigMap{}
					err = fakeClient.Get(ctx, types.NamespacedName{
						Name:      tt.storageConfigMap.Name,
						Namespace: tt.storageConfigMap.Namespace,
					}, configMap)
					assert.Error(t, err) // Should get not found error
				}
			}
		})
	}
}

func TestDefaultSyncManager_updatePhase(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
			UID:       types.UID("test-uid"),
		},
		Status: mcpv1alpha1.MCPRegistryStatus{
			Phase: mcpv1alpha1.MCPRegistryPhasePending,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(mcpRegistry).
		WithStatusSubresource(&mcpv1alpha1.MCPRegistry{}).
		Build()

	sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
	storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)
	syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := syncManager.updatePhase(ctx, mcpRegistry, mcpv1alpha1.MCPRegistryPhaseSyncing, "Test message")
	assert.NoError(t, err)

	// Verify the phase was updated - check the modified object directly
	// since the method modifies in place
	assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseSyncing, mcpRegistry.Status.Phase)
	assert.Equal(t, "Test message", mcpRegistry.Status.Message)
}

func TestDefaultSyncManager_updatePhaseFailedWithCondition(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
			UID:       types.UID("test-uid"),
		},
		Status: mcpv1alpha1.MCPRegistryStatus{
			Phase: mcpv1alpha1.MCPRegistryPhasePending,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(mcpRegistry).
		WithStatusSubresource(&mcpv1alpha1.MCPRegistry{}).
		Build()

	sourceHandlerFactory := sources.NewSourceHandlerFactory(fakeClient)
	storageManager := sources.NewConfigMapStorageManager(fakeClient, scheme)
	syncManager := NewDefaultSyncManager(fakeClient, scheme, sourceHandlerFactory, storageManager)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := syncManager.updatePhaseFailedWithCondition(
		ctx,
		mcpRegistry,
		"Test failure message",
		mcpv1alpha1.ConditionSourceAvailable,
		"TestFailure",
		"Test condition message",
	)
	assert.NoError(t, err)

	// Verify the phase and condition were updated - check the modified object directly
	// since the method modifies in place after refreshing from client
	assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseFailed, mcpRegistry.Status.Phase)
	assert.Equal(t, "Test failure message", mcpRegistry.Status.Message)

	// Check condition was set
	require.Len(t, mcpRegistry.Status.Conditions, 1)
	condition := mcpRegistry.Status.Conditions[0]
	assert.Equal(t, mcpv1alpha1.ConditionSourceAvailable, condition.Type)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, "TestFailure", condition.Reason)
	assert.Equal(t, "Test condition message", condition.Message)
}

func TestIsManualSync(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason   string
		expected bool
	}{
		{ReasonManualWithChanges, true},
		{ReasonManualNoChanges, true},
		{ReasonSourceDataChanged, false},
		{ReasonRegistryNotReady, false},
		{ReasonUpToDateWithPolicy, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.reason, func(t *testing.T) {
			t.Parallel()
			result := IsManualSync(tt.reason)
			assert.Equal(t, tt.expected, result)
		})
	}
}

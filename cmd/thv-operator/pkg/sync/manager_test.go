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
					Phase: mcpv1alpha1.MCPRegistryPhaseReady,
					SyncStatus: &mcpv1alpha1.SyncStatus{
						Phase:        mcpv1alpha1.SyncPhaseComplete, // Registry has completed sync
						LastSyncHash: "",                            // No hash means data changed
					},
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
						SyncTriggerAnnotation: "manual-sync-123",
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
					LastManualSyncTrigger: "old-trigger",
					SyncStatus: &mcpv1alpha1.SyncStatus{
						Phase:        mcpv1alpha1.SyncPhaseComplete,                                      // Registry has completed sync
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

	intPtr := func(i int) *int { return &i }

	tests := []struct {
		name                string
		mcpRegistry         *mcpv1alpha1.MCPRegistry
		sourceConfigMap     *corev1.ConfigMap
		existingStorageCM   *corev1.ConfigMap
		expectedError       bool
		expectedPhase       mcpv1alpha1.MCPRegistryPhase
		expectedServerCount *int // nil means don't validate
		errorContains       string
		validateConditions  bool
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
			expectedError:       false,
			expectedPhase:       mcpv1alpha1.MCPRegistryPhasePending,
			expectedServerCount: intPtr(1), // 1 server in the registry data
			validateConditions:  false,
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
			sourceConfigMap:     nil,
			expectedError:       true,                                // PerformSync now returns errors for controller to handle
			expectedPhase:       mcpv1alpha1.MCPRegistryPhasePending, // Phase is not changed by PerformSync, only by controller
			expectedServerCount: nil,                                 // Don't validate server count for failed sync
			errorContains:       "",
			validateConditions:  false,
		},
		{
			name: "sync with manual trigger annotation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
					Annotations: map[string]string{
						SyncTriggerAnnotation: "manual-123",
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
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
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
			expectedError:       false,
			expectedPhase:       mcpv1alpha1.MCPRegistryPhasePending,
			expectedServerCount: intPtr(0), // 0 servers in the registry data
			validateConditions:  false,
		},
		{
			name: "successful sync with name filtering",
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
					Filter: &mcpv1alpha1.RegistryFilter{
						NameFilters: &mcpv1alpha1.NameFilter{
							Include: []string{"test-*"},
							Exclude: []string{"*-excluded"},
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
					"registry.json": `{"version": "1.0.0", "last_updated": "2023-01-01T00:00:00Z", "servers": {"test-server": {"name": "test-server", "description": "Test server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["test_tool"], "image": "test/image:latest"}, "excluded-server": {"name": "excluded-server", "description": "Excluded server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["test_tool"], "image": "test/image:latest"}, "other-server": {"name": "other-server", "description": "Other server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["test_tool"], "image": "test/image:latest"}}, "remoteServers": {}}`,
				},
			},
			expectedError:       false,
			expectedPhase:       mcpv1alpha1.MCPRegistryPhasePending,
			expectedServerCount: intPtr(1), // 1 server after filtering (test-server matches include, others excluded/don't match)
			validateConditions:  false,
		},
		{
			name: "successful sync with tag filtering",
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
					Filter: &mcpv1alpha1.RegistryFilter{
						Tags: &mcpv1alpha1.TagFilter{
							Include: []string{"database"},
							Exclude: []string{"deprecated"},
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
					"registry.json": `{"version": "1.0.0", "last_updated": "2023-01-01T00:00:00Z", "servers": {"db-server": {"name": "db-server", "description": "Database server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["db_tool"], "tags": ["database", "sql"], "image": "db/image:latest"}, "old-db-server": {"name": "old-db-server", "description": "Old database server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["db_tool"], "tags": ["database", "deprecated"], "image": "db/image:old"}, "web-server": {"name": "web-server", "description": "Web server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["web_tool"], "tags": ["web"], "image": "web/image:latest"}}, "remoteServers": {}}`,
				},
			},
			expectedError:       false,
			expectedPhase:       mcpv1alpha1.MCPRegistryPhasePending,
			expectedServerCount: intPtr(1), // 1 server after filtering (db-server has "database" tag, old-db-server excluded by "deprecated", web-server doesn't have "database")
			validateConditions:  false,
		},
		{
			name: "successful sync with combined name and tag filtering",
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
					Filter: &mcpv1alpha1.RegistryFilter{
						NameFilters: &mcpv1alpha1.NameFilter{
							Include: []string{"prod-*"},
						},
						Tags: &mcpv1alpha1.TagFilter{
							Include: []string{"production"},
							Exclude: []string{"experimental"},
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
					"registry.json": `{"version": "1.0.0", "last_updated": "2023-01-01T00:00:00Z", "servers": {"prod-db": {"name": "prod-db", "description": "Production database", "tier": "Official", "status": "Active", "transport": "stdio", "tools": ["db_tool"], "tags": ["database", "production"], "image": "db/image:prod"}, "prod-web": {"name": "prod-web", "description": "Production web server", "tier": "Official", "status": "Active", "transport": "stdio", "tools": ["web_tool"], "tags": ["web", "production"], "image": "web/image:prod"}, "prod-experimental": {"name": "prod-experimental", "description": "Experimental prod server", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["exp_tool"], "tags": ["production", "experimental"], "image": "exp/image:latest"}, "dev-db": {"name": "dev-db", "description": "Development database", "tier": "Community", "status": "Active", "transport": "stdio", "tools": ["db_tool"], "tags": ["database", "development"], "image": "db/image:dev"}}, "remoteServers": {}}`,
				},
			},
			expectedError:       false,
			expectedPhase:       mcpv1alpha1.MCPRegistryPhasePending,
			expectedServerCount: intPtr(2), // 2 servers after filtering (prod-db and prod-web match name pattern and have "production" tag, prod-experimental excluded by "experimental", dev-db doesn't match "prod-*")
			validateConditions:  false,
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

			result, syncResult, syncErr := syncManager.PerformSync(ctx, tt.mcpRegistry)

			if tt.expectedError {
				assert.NotNil(t, syncErr)
				if tt.errorContains != "" {
					assert.Contains(t, syncErr.Error(), tt.errorContains)
				}
			} else {
				assert.Nil(t, syncErr)
			}

			// Verify the result
			assert.NotNil(t, result)

			// Check the registry object directly since PerformSync modifies it in place
			assert.Equal(t, tt.expectedPhase, tt.mcpRegistry.Status.Phase)

			// Validate server count if expected
			if tt.expectedServerCount != nil && syncResult != nil {
				assert.Equal(t, *tt.expectedServerCount, syncResult.ServerCount, "ServerCount should match expected value after sync")
			}

			if tt.validateConditions {
				// Check that conditions are NOT set by sync manager (they're handled by controller now)
				assert.Len(t, tt.mcpRegistry.Status.Conditions, 0, "Sync manager should not set conditions")
			}

			// Verify manual sync trigger is processed if annotation exists (this is still done by sync manager)
			if tt.mcpRegistry.Annotations != nil {
				if triggerValue := tt.mcpRegistry.Annotations[SyncTriggerAnnotation]; triggerValue != "" {
					assert.Equal(t, triggerValue, tt.mcpRegistry.Status.LastManualSyncTrigger)
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
						SyncTriggerAnnotation: "manual-trigger-123",
					},
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
					Phase: mcpv1alpha1.MCPRegistryPhasePending,
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
		t.Run(tt.reason, func(t *testing.T) {
			t.Parallel()
			result := IsManualSync(tt.reason)
			assert.Equal(t, tt.expected, result)
		})
	}
}

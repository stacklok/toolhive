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

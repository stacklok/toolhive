package configmap

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

const (
	testExistingUID = "existing-uid"
)

// Mock checksum implementation for testing
type mockChecksum struct {
	hasChanged bool
}

func (*mockChecksum) ComputeConfigMapChecksum(_ *corev1.ConfigMap) string {
	return "mock-checksum"
}

func (m *mockChecksum) ConfigMapChecksumHasChanged(_, _ *corev1.ConfigMap) bool {
	return m.hasChanged
}

func TestUpsertRunConfigMap_Create(t *testing.T) {
	t.Parallel()

	// Helper function to create test scheme
	createTestScheme := func() *runtime.Scheme {
		testScheme := runtime.NewScheme()
		_ = corev1.AddToScheme(testScheme)
		_ = mcpv1alpha1.AddToScheme(testScheme)
		return testScheme
	}

	// Helper function to create test MCPServer
	createTestMCPServer := func() *mcpv1alpha1.MCPServer {
		return &mcpv1alpha1.MCPServer{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "toolhive.stacklok.dev/v1alpha1",
				Kind:       "MCPServer",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-mcp-server",
				Namespace: "test-namespace",
				UID:       "test-uid",
			},
		}
	}

	// Helper function to create test ConfigMap
	createTestConfigMap := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
			},
			Data: map[string]string{
				"runconfig.json": "test-runconfig",
			},
		}
	}

	t.Run("ConfigMap already exists", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		existingConfigMap := createTestConfigMap()
		existingConfigMap.Annotations = map[string]string{
			checksum.ContentChecksumAnnotation: "same-checksum",
		}
		newConfigMap := createTestConfigMap()
		newConfigMap.Annotations = map[string]string{
			checksum.ContentChecksumAnnotation: "same-checksum",
		}
		scheme := createTestScheme()

		// Create fake client with existing ConfigMap
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingConfigMap).
			Build()

		// Use mock checksum that reports no change
		checksumManager := &mockChecksum{hasChanged: false}
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, newConfigMap)

		// Assert
		require.NoError(t, err)

		// Verify that the ConfigMap wasn't modified
		var result corev1.ConfigMap
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      existingConfigMap.Name,
			Namespace: existingConfigMap.Namespace,
		}, &result)
		require.NoError(t, err)
		// Should not have owner references since we didn't create it
		assert.Len(t, result.OwnerReferences, 0)
	})

	t.Run("ConfigMap doesn't exist - successful creation", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		configMap := createTestConfigMap()
		scheme := createTestScheme()

		// Create fake client without any existing objects
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		checksumManager := checksum.NewRunConfigConfigMapChecksum()
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, configMap)

		// Assert
		require.NoError(t, err)

		// Verify that the ConfigMap was created with proper owner reference
		var result corev1.ConfigMap
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      configMap.Name,
			Namespace: configMap.Namespace,
		}, &result)
		require.NoError(t, err)

		// Verify owner reference was set
		require.Len(t, result.OwnerReferences, 1)
		assert.Equal(t, mcpServer.Name, result.OwnerReferences[0].Name)
		assert.Equal(t, "MCPServer", result.OwnerReferences[0].Kind)
		assert.Equal(t, "toolhive.stacklok.dev/v1alpha1", result.OwnerReferences[0].APIVersion)
		assert.True(t, *result.OwnerReferences[0].Controller)
	})

	t.Run("ConfigMap doesn't exist - SetControllerReference fails", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		configMap := createTestConfigMap()

		// Create a scheme without MCPServer registered to cause SetControllerReference to fail
		brokenScheme := runtime.NewScheme()
		_ = corev1.AddToScheme(brokenScheme)
		// Intentionally not adding MCPServer to scheme

		fakeClient := fake.NewClientBuilder().
			WithScheme(brokenScheme).
			Build()

		checksumManager := checksum.NewRunConfigConfigMapChecksum()
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, brokenScheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, configMap)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to set controller reference while creating RunConfig ConfigMap")
	})

	t.Run("ConfigMap doesn't exist - Create fails", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		configMap := createTestConfigMap()
		scheme := createTestScheme()

		// Create fake client with interceptor to simulate Create failure
		createError := errors.New("create failed due to some error")
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					// Return NotFound for Get
					return apierrors.NewNotFound(schema.GroupResource{
						Group:    "v1",
						Resource: "configmaps",
					}, key.Name)
				},
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					// Simulate Create failure
					return createError
				},
			}).
			Build()

		checksumManager := checksum.NewRunConfigConfigMapChecksum()
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, configMap)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create RunConfig ConfigMap")
		assert.Contains(t, err.Error(), createError.Error())
	})

	t.Run("Get returns error that is not NotFound", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		configMap := createTestConfigMap()
		scheme := createTestScheme()

		// Create fake client with interceptor to simulate Get failure
		getError := errors.New("network error or permission denied")
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					// Return a non-NotFound error
					return getError
				},
			}).
			Build()

		checksumManager := checksum.NewRunConfigConfigMapChecksum()
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, configMap)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get RunConfig ConfigMap")
		assert.Contains(t, err.Error(), getError.Error())
	})

	t.Run("Nil MCPServer returns error", func(t *testing.T) {
		t.Parallel()
		// Setup
		configMap := createTestConfigMap()
		scheme := createTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		checksumManager := checksum.NewRunConfigConfigMapChecksum()
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), nil, configMap)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot create RunConfig ConfigMap because MCPServer object is nil")
	})

	t.Run("Nil ConfigMap returns error", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		scheme := createTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		checksumManager := checksum.NewRunConfigConfigMapChecksum()
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, nil)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot create RunConfig ConfigMap because ConfigMap object is nil")
	})
}

func TestUpsertRunConfigMap_Update(t *testing.T) {
	t.Parallel()

	// Helper function to create test scheme
	createTestScheme := func() *runtime.Scheme {
		testScheme := runtime.NewScheme()
		_ = corev1.AddToScheme(testScheme)
		_ = mcpv1alpha1.AddToScheme(testScheme)
		return testScheme
	}

	// Helper function to create test MCPServer
	createTestMCPServer := func() *mcpv1alpha1.MCPServer {
		return &mcpv1alpha1.MCPServer{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "toolhive.stacklok.dev/v1alpha1",
				Kind:       "MCPServer",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-mcp-server",
				Namespace: "test-namespace",
				UID:       "test-uid",
			},
		}
	}

	// Helper function to create test ConfigMap with checksum
	createTestConfigMapWithChecksum := func(checksumValue string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
				Annotations: map[string]string{
					checksum.ContentChecksumAnnotation: checksumValue,
				},
			},
			Data: map[string]string{
				"runconfig.json": "test-runconfig",
			},
		}
	}

	t.Run("Existing ConfigMap with same checksum - no update", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		existingConfigMap := createTestConfigMapWithChecksum("checksum123")
		desiredConfigMap := createTestConfigMapWithChecksum("checksum123")
		scheme := createTestScheme()

		// Create fake client with existing ConfigMap
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingConfigMap).
			Build()

		// Mock checksum that reports no change
		checksumManager := &mockChecksum{hasChanged: false}
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, desiredConfigMap)

		// Assert
		require.NoError(t, err)

		// Verify that the ConfigMap wasn't updated
		var result corev1.ConfigMap
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      existingConfigMap.Name,
			Namespace: existingConfigMap.Namespace,
		}, &result)
		require.NoError(t, err)
		assert.Equal(t, "checksum123", result.Annotations[checksum.ContentChecksumAnnotation])
	})

	t.Run("Existing ConfigMap with different checksum - SetControllerReference fails", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		existingConfigMap := createTestConfigMapWithChecksum("old-checksum")
		existingConfigMap.ResourceVersion = "1"
		existingConfigMap.UID = testExistingUID
		desiredConfigMap := createTestConfigMapWithChecksum("new-checksum")

		// Create a scheme without MCPServer registered to cause SetControllerReference to fail
		brokenScheme := runtime.NewScheme()
		_ = corev1.AddToScheme(brokenScheme)
		// Intentionally not adding MCPServer to scheme

		// Create fake client with existing ConfigMap
		fakeClient := fake.NewClientBuilder().
			WithScheme(brokenScheme).
			WithObjects(existingConfigMap).
			Build()

		// Mock checksum that reports change
		checksumManager := &mockChecksum{hasChanged: true}
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, brokenScheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, desiredConfigMap)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to set controller reference while updating RunConfig ConfigMap")
	})

	t.Run("Existing ConfigMap with different checksum - Update fails", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		existingConfigMap := createTestConfigMapWithChecksum("old-checksum")
		existingConfigMap.ResourceVersion = "1"
		existingConfigMap.UID = testExistingUID
		desiredConfigMap := createTestConfigMapWithChecksum("new-checksum")
		scheme := createTestScheme()

		// Create fake client with interceptor to simulate Update failure
		updateError := errors.New("update failed due to conflict")
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingConfigMap).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					// Simulate Update failure
					return updateError
				},
			}).
			Build()

		// Mock checksum that reports change
		checksumManager := &mockChecksum{hasChanged: true}
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, desiredConfigMap)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to update RunConfig ConfigMap")
		assert.Contains(t, err.Error(), updateError.Error())
	})

	t.Run("Existing ConfigMap with different checksum - successful update", func(t *testing.T) {
		t.Parallel()
		// Setup
		mcpServer := createTestMCPServer()
		existingConfigMap := createTestConfigMapWithChecksum("old-checksum")
		existingConfigMap.ResourceVersion = "1"
		existingConfigMap.UID = testExistingUID

		desiredConfigMap := createTestConfigMapWithChecksum("new-checksum")
		desiredConfigMap.Data = map[string]string{
			"runconfig.json": "updated-runconfig-content",
		}
		scheme := createTestScheme()

		// Create fake client with existing ConfigMap
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingConfigMap).
			Build()

		// Mock checksum that reports change
		checksumManager := &mockChecksum{hasChanged: true}
		runConfigConfigMap := NewRunConfigConfigMap(fakeClient, scheme, checksumManager)

		// Execute
		err := runConfigConfigMap.UpsertRunConfigMap(context.TODO(), mcpServer, desiredConfigMap)

		// Assert
		require.NoError(t, err)

		// Verify that the ConfigMap was updated with new content and owner reference
		var result corev1.ConfigMap
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      desiredConfigMap.Name,
			Namespace: desiredConfigMap.Namespace,
		}, &result)
		require.NoError(t, err)

		// Verify owner reference was set
		require.Len(t, result.OwnerReferences, 1)
		assert.Equal(t, mcpServer.Name, result.OwnerReferences[0].Name)
		assert.Equal(t, "MCPServer", result.OwnerReferences[0].Kind)
		assert.Equal(t, "toolhive.stacklok.dev/v1alpha1", result.OwnerReferences[0].APIVersion)
		assert.True(t, *result.OwnerReferences[0].Controller)

		// Verify content was updated
		assert.Equal(t, "updated-runconfig-content", result.Data["runconfig.json"])
		assert.Equal(t, "new-checksum", result.Annotations[checksum.ContentChecksumAnnotation])

		// Verify UID was preserved (ResourceVersion will be incremented by k8s)
		assert.Equal(t, types.UID(testExistingUID), result.UID)
		// ResourceVersion should have been updated (not the same as original)
		assert.NotEqual(t, "1", result.ResourceVersion)
	})
}

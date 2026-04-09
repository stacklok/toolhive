// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8smeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
	registryapimocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/mocks"
)

// toRawJSONSlice marshals each item to JSON and wraps it in apiextensionsv1.JSON
// so tests can construct []apiextensionsv1.JSON fields from typed Go structs.
func toRawJSONSlice[T any](t *testing.T, items []T) []apiextensionsv1.JSON {
	t.Helper()
	result := make([]apiextensionsv1.JSON, len(items))
	for i, item := range items {
		data, err := json.Marshal(item)
		require.NoError(t, err)
		result[i] = apiextensionsv1.JSON{Raw: data}
	}
	return result
}

// newMCPRegistryTestScheme creates a runtime scheme with all required API groups registered.
func newMCPRegistryTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))
	return s
}

// newMCPRegistryWithFinalizer creates an MCPRegistry with the controller finalizer
// and a minimal valid spec (one source) so it passes reconciler validation.
func newMCPRegistryWithFinalizer(name, namespace string) *mcpv1alpha1.MCPRegistry { //nolint:unparam
	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{"mcpregistry.toolhive.stacklok.dev/finalizer"},
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
				{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
					Key:                  "registry.json",
				}},
			},
		},
	}
}

func TestMCPRegistryReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	const (
		registryName      = "test-registry"
		registryNamespace = "default"
	)

	tests := []struct {
		name           string
		setup          func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry)
		configureMocks func(mock *registryapimocks.MockManager)
		expResult      ctrl.Result
		expErr         error
		assertRegistry func(t *testing.T, fakeClient client.Client)
	}{
		{
			name: "resource_not_found",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, &mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{Name: registryName, Namespace: registryNamespace},
				}
			},
			configureMocks: func(_ *registryapimocks.MockManager) {},
			expResult:      ctrl.Result{},
			expErr:         nil,
		},
		{
			name: "adds_finalizer_on_first_reconcile",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := &mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{Name: registryName, Namespace: registryNamespace},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
							{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
								Key:                  "registry.json",
							}},
						},
					},
				}
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(_ *registryapimocks.MockManager) {
				// Returns early after adding finalizer — no API calls.
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				assert.Contains(t, updated.Finalizers, "mcpregistry.toolhive.stacklok.dev/finalizer")
			},
		},
		{
			// finalizeMCPRegistry sets Status.Phase=Terminating then the finalizer is removed.
			// A second dummy finalizer keeps the object alive so we can verify both effects.
			name: "handles_deletion_with_finalizer_sets_terminating_status",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				now := metav1.NewTime(time.Now())
				mcpRegistry := &mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      registryName,
						Namespace: registryNamespace,
						Finalizers: []string{
							"mcpregistry.toolhive.stacklok.dev/finalizer",
							"other.finalizer/dummy", // keeps object alive after controller finalizer is removed
						},
						DeletionTimestamp: &now,
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
							{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
								Key:                  "registry.json",
							}},
						},
					},
				}
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(_ *registryapimocks.MockManager) {
				// finalizeMCPRegistry does not call registryAPIManager.
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseTerminating, updated.Status.Phase)
				assert.NotContains(t, updated.Finalizers, "mcpregistry.toolhive.stacklok.dev/finalizer")
			},
		},
		{
			name: "handles_deletion_without_controller_finalizer",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				// The fake client requires at least one finalizer for objects with DeletionTimestamp.
				// Use a non-controller finalizer so the controller skips its finalize path.
				now := metav1.NewTime(time.Now())
				mcpRegistry := &mcpv1alpha1.MCPRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:              registryName,
						Namespace:         registryNamespace,
						Finalizers:        []string{"other.finalizer/test"},
						DeletionTimestamp: &now,
					},
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
							{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
								Key:                  "registry.json",
							}},
						},
					},
				}
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(_ *registryapimocks.MockManager) {},
			expResult:      ctrl.Result{},
			expErr:         nil,
		},
		{
			// validateAndUpdatePodTemplateStatus returns false → Reconcile returns early without error,
			// and the PodTemplateValid condition is set to False with phase Failed.
			name: "invalid_podtemplatespec_blocks_reconcile",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				mcpRegistry.Spec.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(`{"spec": {"containers": "invalid"}}`),
				}
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(_ *registryapimocks.MockManager) {
				// No API calls — returns before reaching API reconcile.
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionPodTemplateValid)
				require.NotNil(t, cond, "PodTemplateValid condition must be set")
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseFailed, updated.Status.Phase)
			},
		},
		{
			// validateAndUpdatePodTemplateStatus returns true → reconcile proceeds, setting the
			// PodTemplateValid condition to True and continuing to the API reconcile path.
			name: "valid_podtemplatespec_proceeds_to_api_reconcile",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				mcpRegistry.Spec.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(`{"spec": {"containers": [{"name": "main"}]}}`),
				}
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(mock *registryapimocks.MockManager) {
				mock.EXPECT().ReconcileAPIService(gomock.Any(), gomock.Any()).Return(nil)
				mock.EXPECT().GetAPIStatus(gomock.Any(), gomock.Any()).Return(true, int32(1))
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionPodTemplateValid)
				require.NotNil(t, cond, "PodTemplateValid condition must be set")
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
			},
		},
		{
			name: "api_reconcile_error",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(mock *registryapimocks.MockManager) {
				mock.EXPECT().ReconcileAPIService(gomock.Any(), gomock.Any()).Return(
					&registryapi.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
				)
				// reconcileErr != nil → IsAPIReady and GetReadyReplicas are never called.
			},
			expResult: ctrl.Result{},
			expErr:    &registryapi.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
		},
		{
			// updateRegistryStatus sets Phase=Pending when API is not ready.
			// Reconcile also schedules a requeue because IsAPIReady returns false.
			name: "api_reconcile_success_api_not_ready",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(mock *registryapimocks.MockManager) {
				mock.EXPECT().ReconcileAPIService(gomock.Any(), gomock.Any()).Return(nil)
				mock.EXPECT().GetAPIStatus(gomock.Any(), gomock.Any()).Return(false, int32(0))
			},
			expResult: ctrl.Result{RequeueAfter: 30 * time.Second},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhasePending, updated.Status.Phase)
				assert.Equal(t, int32(0), updated.Status.ReadyReplicas)
			},
		},
		{
			// updateRegistryStatus sets Phase=Running when API is ready.
			// No requeue because IsAPIReady returns true.
			name: "api_reconcile_success_api_ready",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(mock *registryapimocks.MockManager) {
				mock.EXPECT().ReconcileAPIService(gomock.Any(), gomock.Any()).Return(nil)
				mock.EXPECT().GetAPIStatus(gomock.Any(), gomock.Any()).Return(true, int32(1))
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseReady, updated.Status.Phase)
				assert.Equal(t, int32(1), updated.Status.ReadyReplicas)
			},
		},
		{
			// When ReconcileAPIService fails, updateRegistryStatus sets Phase=Failed
			// and the Ready condition to False with the structured error reason.
			name: "api_reconcile_error_updates_failed_status",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(mock *registryapimocks.MockManager) {
				mock.EXPECT().ReconcileAPIService(gomock.Any(), gomock.Any()).Return(
					&registryapi.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
				)
				// reconcileErr != nil → IsAPIReady and GetReadyReplicas are never called.
			},
			expResult: ctrl.Result{},
			expErr:    &registryapi.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseFailed, updated.Status.Phase)
				assert.Equal(t, "deploy failed", updated.Status.Message)
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				require.NotNil(t, cond, "Ready condition must be set")
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, "DeployFailed", cond.Reason)
			},
		},
		{
			// When the API is ready, the URL should follow the in-cluster format
			// and the Ready condition should be True.
			name: "api_reconcile_success_api_ready_checks_endpoint_and_condition",
			setup: func(t *testing.T, s *runtime.Scheme) (*fake.ClientBuilder, *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				mcpRegistry := newMCPRegistryWithFinalizer(registryName, registryNamespace)
				builder := fake.NewClientBuilder().
					WithScheme(s).
					WithObjects(mcpRegistry).
					WithStatusSubresource(&mcpv1alpha1.MCPRegistry{})
				return builder, mcpRegistry
			},
			configureMocks: func(mock *registryapimocks.MockManager) {
				mock.EXPECT().ReconcileAPIService(gomock.Any(), gomock.Any()).Return(nil)
				mock.EXPECT().GetAPIStatus(gomock.Any(), gomock.Any()).Return(true, int32(2))
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				assert.Equal(t, "http://test-registry-api.default:8080", updated.Status.URL)
				assert.Equal(t, int32(2), updated.Status.ReadyReplicas)
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				require.NotNil(t, cond, "Ready condition must be set")
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// arrange
			ctx := log.IntoContext(t.Context(), log.Log)
			s := newMCPRegistryTestScheme(t)

			builder, mcpRegistry := tt.setup(t, s)
			fakeClient := builder.Build()

			mockCtrl := gomock.NewController(t)
			mockAPIManager := registryapimocks.NewMockManager(mockCtrl)
			tt.configureMocks(mockAPIManager)

			r := &MCPRegistryReconciler{
				Client:             fakeClient,
				Scheme:             s,
				registryAPIManager: mockAPIManager,
			}

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      mcpRegistry.Name,
					Namespace: mcpRegistry.Namespace,
				},
			}

			// act
			result, err := r.Reconcile(ctx, req)

			// assert
			assert.Equal(t, tt.expResult, result)
			assert.Equal(t, tt.expErr, err)
			if tt.assertRegistry != nil {
				tt.assertRegistry(t, fakeClient)
			}
		})
	}
}

func TestValidateNewPathSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    mcpv1alpha1.MCPRegistrySpec
		wantErr string
	}{
		{
			name: "valid new path with configYAML and no legacy fields",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
			},
		},
		{
			name: "valid legacy path with sources and no configYAML",
			spec: mcpv1alpha1.MCPRegistrySpec{
				Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
					{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "registry.json",
					}},
				},
			},
		},
		{
			name: "mutual exclusivity configYAML plus sources",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
					{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "registry.json",
					}},
				},
			},
			wantErr: "mutually exclusive",
		},
		{
			name: "mutual exclusivity configYAML plus databaseConfig",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML:     "sources:\n  - name: default\n",
				DatabaseConfig: &mcpv1alpha1.MCPRegistryDatabaseConfig{Host: "pg"},
			},
			wantErr: "mutually exclusive",
		},
		{
			name: "mutual exclusivity configYAML plus authConfig",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				AuthConfig: &mcpv1alpha1.MCPRegistryAuthConfig{Mode: mcpv1alpha1.MCPRegistryAuthModeAnonymous},
			},
			wantErr: "mutually exclusive",
		},
		{
			name:    "neither path specified",
			spec:    mcpv1alpha1.MCPRegistrySpec{},
			wantErr: "either configYAML or sources must be specified",
		},
		{
			name: "volumes without configYAML",
			spec: mcpv1alpha1.MCPRegistrySpec{
				Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
					{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "registry.json",
					}},
				},
				Volumes: toRawJSONSlice(t, []corev1.Volume{
					{Name: "extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}),
			},
			wantErr: "volumes and volumeMounts require configYAML",
		},
		{
			name: "volumeMounts without configYAML",
			spec: mcpv1alpha1.MCPRegistrySpec{
				Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
					{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "registry.json",
					}},
				},
				VolumeMounts: toRawJSONSlice(t, []corev1.VolumeMount{
					{Name: "extra", MountPath: "/extra"},
				}),
			},
			wantErr: "volumes and volumeMounts require configYAML",
		},
		{
			name: "pgpassSecretRef without configYAML",
			spec: mcpv1alpha1.MCPRegistrySpec{
				Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
					{Name: "test", ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "registry.json",
					}},
				},
				PGPassSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "pgpass"},
					Key:                  ".pgpass",
				},
			},
			wantErr: "pgpassSecretRef requires configYAML",
		},
		{
			name: "pgpassSecretRef with empty name",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				PGPassSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ""},
					Key:                  ".pgpass",
				},
			},
			wantErr: "pgpassSecretRef.name is required",
		},
		{
			name: "pgpassSecretRef with empty key",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				PGPassSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
					Key:                  "",
				},
			},
			wantErr: "pgpassSecretRef.key is required",
		},
		{
			name: "reserved volume name registry-server-config in spec volumes",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				Volumes: toRawJSONSlice(t, []corev1.Volume{
					{Name: registryapi.RegistryServerConfigVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}),
			},
			wantErr: "reserved by the operator",
		},
		{
			name: "reserved volume name pgpass-secret when pgpassSecretRef is set",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				PGPassSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
					Key:                  ".pgpass",
				},
				Volumes: toRawJSONSlice(t, []corev1.Volume{
					{Name: registryapi.PGPassSecretVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}),
			},
			wantErr: "reserved by the operator",
		},
		{
			name: "non-reserved volume name passes",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				Volumes: toRawJSONSlice(t, []corev1.Volume{
					{Name: "my-custom-volume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}),
			},
		},
		{
			name: "reserved volume name pgpass-secret when pgpassSecretRef is NOT set passes",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				Volumes: toRawJSONSlice(t, []corev1.Volume{
					{Name: registryapi.PGPassSecretVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}),
			},
			// pgpass-secret is only reserved when pgpassSecretRef is set
		},
		{
			name: "reserved volume name registry-server-config in PodTemplateSpec",
			spec: func() mcpv1alpha1.MCPRegistrySpec {
				pts := corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Volumes: []corev1.Volume{
							{
								Name: registryapi.RegistryServerConfigVolumeName,
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
						},
						Containers: []corev1.Container{
							{Name: "registry-api"},
						},
					},
				}
				raw, _ := json.Marshal(pts)
				return mcpv1alpha1.MCPRegistrySpec{
					ConfigYAML:      "sources:\n  - name: default\n",
					PodTemplateSpec: &runtime.RawExtension{Raw: raw},
				}
			}(),
			wantErr: "reserved by the operator",
		},
		{
			name: "init container setup-pgpass in PodTemplateSpec when pgpassSecretRef is set",
			spec: func() mcpv1alpha1.MCPRegistrySpec {
				pts := corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						InitContainers: []corev1.Container{
							{Name: registryapi.PGPassInitContainerName, Image: "busybox"},
						},
						Containers: []corev1.Container{
							{Name: "registry-api"},
						},
					},
				}
				raw, _ := json.Marshal(pts)
				return mcpv1alpha1.MCPRegistrySpec{
					ConfigYAML: "sources:\n  - name: default\n",
					PGPassSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "my-pgpass"},
						Key:                  ".pgpass",
					},
					PodTemplateSpec: &runtime.RawExtension{Raw: raw},
				}
			}(),
			wantErr: "reserved by the operator when pgpassSecretRef is set",
		},
		{
			name: "mount path collision from PodTemplateSpec container mounts",
			spec: func() mcpv1alpha1.MCPRegistrySpec {
				pts := corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "registry-api",
								VolumeMounts: []corev1.VolumeMount{
									{Name: "user-vol", MountPath: "/config"},
								},
							},
						},
					},
				}
				raw, _ := json.Marshal(pts)
				return mcpv1alpha1.MCPRegistrySpec{
					ConfigYAML:      "sources:\n  - name: default\n",
					PodTemplateSpec: &runtime.RawExtension{Raw: raw},
				}
			}(),
			wantErr: "duplicate mount path '/config'",
		},
		{
			name: "duplicate mount path in spec volumeMounts",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				VolumeMounts: toRawJSONSlice(t, []corev1.VolumeMount{
					{Name: "vol-a", MountPath: "/data/files"},
					{Name: "vol-b", MountPath: "/data/files"},
				}),
			},
			wantErr: "duplicate mount path",
		},
		{
			name: "mount path collision with operator-reserved config path",
			spec: mcpv1alpha1.MCPRegistrySpec{
				ConfigYAML: "sources:\n  - name: default\n",
				VolumeMounts: toRawJSONSlice(t, []corev1.VolumeMount{
					{Name: "my-vol", MountPath: "/config"},
				}),
			},
			wantErr: "duplicate mount path '/config'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "default",
				},
				Spec: tt.spec,
			}

			err := validateNewPathSpec(mcpRegistry)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

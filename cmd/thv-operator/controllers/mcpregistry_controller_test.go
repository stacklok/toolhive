// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8smeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	registryapimocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/mocks"
)

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

// newMCPRegistryWithFinalizer creates an MCPRegistry with the controller finalizer already set.
func newMCPRegistryWithFinalizer(name, namespace string) *mcpv1alpha1.MCPRegistry { //nolint:unparam
	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: []string{"mcpregistry.toolhive.stacklok.dev/finalizer"},
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
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionRegistryPodTemplateValid)
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
				mock.EXPECT().IsAPIReady(gomock.Any(), gomock.Any()).Return(true).Times(2)
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionRegistryPodTemplateValid)
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
					&mcpregistrystatus.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
				)
				// err != nil in Reconcile → IsAPIReady is never called.
			},
			expResult: ctrl.Result{},
			expErr:    &mcpregistrystatus.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
		},
		{
			// applyStatusUpdates writes APIPhaseDeploying; deriveOverallStatus sets PhasePending.
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
				// Called twice: once in the success branch, once in the requeue check.
				mock.EXPECT().IsAPIReady(gomock.Any(), gomock.Any()).Return(false).Times(2)
			},
			expResult: ctrl.Result{RequeueAfter: 30 * time.Second},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				require.NotNil(t, updated.Status.APIStatus)
				assert.Equal(t, mcpv1alpha1.APIPhaseDeploying, updated.Status.APIStatus.Phase)
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhasePending, updated.Status.Phase)
			},
		},
		{
			// applyStatusUpdates writes APIPhaseReady; deriveOverallStatus sets PhaseReady.
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
				// Called twice: once in the success branch, once in the requeue check.
				mock.EXPECT().IsAPIReady(gomock.Any(), gomock.Any()).Return(true).Times(2)
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				require.NotNil(t, updated.Status.APIStatus)
				assert.Equal(t, mcpv1alpha1.APIPhaseReady, updated.Status.APIStatus.Phase)
				assert.Equal(t, mcpv1alpha1.MCPRegistryPhaseReady, updated.Status.Phase)
			},
		},
		{
			// When ReconcileAPIService fails, applyStatusUpdates should still persist
			// APIStatus.Phase=Error and set the APIReady condition to False.
			name: "api_reconcile_error_updates_api_error_status",
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
					&mcpregistrystatus.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
				)
				// err != nil → IsAPIReady is never called.
			},
			expResult: ctrl.Result{},
			expErr:    &mcpregistrystatus.Error{Message: "deploy failed", ConditionReason: "DeployFailed"},
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				require.NotNil(t, updated.Status.APIStatus)
				assert.Equal(t, mcpv1alpha1.APIPhaseError, updated.Status.APIStatus.Phase)
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionAPIReady)
				require.NotNil(t, cond, "APIReady condition must be set")
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
			},
		},
		{
			// When the API is ready, the endpoint in APIStatus should follow the in-cluster
			// URL format and the APIReady condition should be True.
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
				// Called twice: once in the success branch, once in the requeue check.
				mock.EXPECT().IsAPIReady(gomock.Any(), gomock.Any()).Return(true).Times(2)
			},
			expResult: ctrl.Result{},
			expErr:    nil,
			assertRegistry: func(t *testing.T, fakeClient client.Client) {
				t.Helper()
				var updated mcpv1alpha1.MCPRegistry
				require.NoError(t, fakeClient.Get(t.Context(),
					types.NamespacedName{Name: registryName, Namespace: registryNamespace}, &updated))
				require.NotNil(t, updated.Status.APIStatus)
				assert.Equal(t, "http://test-registry-api.default:8080", updated.Status.APIStatus.Endpoint)
				cond := k8smeta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionAPIReady)
				require.NotNil(t, cond, "APIReady condition must be set")
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

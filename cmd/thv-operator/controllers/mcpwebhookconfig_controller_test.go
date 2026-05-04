// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestMCPWebhookConfigReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		webhookConfig     *mcpv1alpha1.MCPWebhookConfig
		existingMCPServer *mcpv1beta1.MCPServer
		expectFinalizer   bool
		expectHash        bool
	}{
		{
			name: "new webhook config without references",
			webhookConfig: &mcpv1alpha1.MCPWebhookConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPWebhookConfigSpec{
					Validating: []mcpv1beta1.WebhookSpec{
						{
							Name: "test-validate",
							URL:  "https://test.example.com",
						},
					},
				},
			},
			expectFinalizer: true,
			expectHash:      true,
		},
		{
			name: "webhook config with referencing mcpserver",
			webhookConfig: &mcpv1alpha1.MCPWebhookConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPWebhookConfigSpec{
					Mutating: []mcpv1beta1.WebhookSpec{
						{
							Name: "test-mutate",
							URL:  "https://test.example.com",
						},
					},
				},
			},
			existingMCPServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: "test-image",
					WebhookConfigRef: &mcpv1beta1.WebhookConfigRef{
						Name: "test-webhook-config",
					},
				},
			},
			expectFinalizer: true,
			expectHash:      true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []client.Object{tt.webhookConfig}
			if tt.existingMCPServer != nil {
				objs = append(objs, tt.existingMCPServer)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPWebhookConfig{}).
				Build()

			r := &MCPWebhookConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.webhookConfig.Name,
					Namespace: tt.webhookConfig.Namespace,
				},
			}

			// First pass adds finalizer
			result, err := r.Reconcile(ctx, req)
			require.NoError(t, err)

			if result.RequeueAfter > 0 {
				result, err = r.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, time.Duration(0), result.RequeueAfter)
			}

			var updatedConfig mcpv1alpha1.MCPWebhookConfig
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
			require.NoError(t, err)

			if tt.expectFinalizer {
				assert.Contains(t, updatedConfig.Finalizers, WebhookConfigFinalizerName)
			}
			if tt.expectHash {
				assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
			}
			if tt.existingMCPServer != nil {
				assert.Contains(t, updatedConfig.Status.ReferencingWorkloads, mcpv1beta1.WorkloadReference{
					Kind: mcpv1beta1.WorkloadKindMCPServer,
					Name: tt.existingMCPServer.Name,
				})

				var updatedServer mcpv1beta1.MCPServer
				require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{
					Name:      tt.existingMCPServer.Name,
					Namespace: tt.existingMCPServer.Namespace,
				}, &updatedServer))
				assert.Equal(t, updatedConfig.Status.ConfigHash,
					updatedServer.Annotations["toolhive.stacklok.dev/webhookconfig-hash"])
			}
		})
	}

	t.Run("resource not found", func(t *testing.T) {
		t.Parallel()
		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
		require.NoError(t, mcpv1beta1.AddToScheme(scheme))
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &MCPWebhookConfigReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "does-not-exist",
				Namespace: "default",
			},
		}
		result, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("invalid spec sets Valid condition false", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
		require.NoError(t, mcpv1beta1.AddToScheme(scheme))

		webhookConfig := &mcpv1alpha1.MCPWebhookConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "invalid-webhook-config",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPWebhookConfigSpec{
				Validating: []mcpv1beta1.WebhookSpec{{
					Name: "invalid-http",
					URL:  "http://policy.example.com",
				}},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(webhookConfig).
			WithStatusSubresource(&mcpv1alpha1.MCPWebhookConfig{}).
			Build()

		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: webhookConfig.Name, Namespace: webhookConfig.Namespace}}

		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		if result.RequeueAfter > 0 {
			result, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
		}

		var updated mcpv1alpha1.MCPWebhookConfig
		require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updated))
		condition := findWebhookConfigCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Contains(t, condition.Message, "must use HTTPS")
	})
}

func TestMCPWebhookConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	webhookConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Finalizers: []string{WebhookConfigFinalizerName},
			DeletionTimestamp: &metav1.Time{
				Time: time.Now(),
			},
		},
	}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: "test-image",
			WebhookConfigRef: &mcpv1beta1.WebhookConfigRef{
				Name: "test-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(webhookConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPWebhookConfig{}).
		Build()

	r := &MCPWebhookConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	result, err := r.handleDeletion(ctx, webhookConfig)
	require.NoError(t, err)
	assert.Equal(t, webhookConfigDeletionRequeueDelay, result.RequeueAfter)

	blockedConfig := &mcpv1alpha1.MCPWebhookConfig{}
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(webhookConfig), blockedConfig))
	cond := meta.FindStatusCondition(blockedConfig.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "ReferencedByWorkloads", cond.Reason)
	assert.NotEmpty(t, blockedConfig.Status.ReferencingWorkloads)

	// Delete server and try again
	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	_, err = r.handleDeletion(ctx, blockedConfig)
	assert.NoError(t, err, "Should delete successfully after reference removed")

	deletedConfig := &mcpv1alpha1.MCPWebhookConfig{}
	err = fakeClient.Get(ctx, client.ObjectKeyFromObject(webhookConfig), deletedConfig)
	if apierrors.IsNotFound(err) {
		return
	}
	require.NoError(t, err)
	assert.Empty(t, deletedConfig.Finalizers)
	assert.Nil(t, meta.FindStatusCondition(deletedConfig.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked))
	assert.Empty(t, deletedConfig.Status.ReferencingWorkloads)
}

func TestMCPWebhookConfigReconciler_handleDeletionClearsBlockedCondition(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	webhookConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Finalizers: []string{WebhookConfigFinalizerName},
		},
		Status: mcpv1beta1.MCPWebhookConfigStatus{
			ReferencingWorkloads: []mcpv1beta1.WorkloadReference{{
				Kind: mcpv1beta1.WorkloadKindMCPServer,
				Name: "server1",
			}},
			Conditions: []metav1.Condition{{
				Type:   mcpv1beta1.ConditionTypeDeletionBlocked,
				Status: metav1.ConditionTrue,
				Reason: "ReferencedByWorkloads",
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(webhookConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPWebhookConfig{}).
		Build()

	r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}
	ctx := t.Context()

	result, err := r.handleDeletion(ctx, webhookConfig)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	clearedConfig := &mcpv1alpha1.MCPWebhookConfig{}
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(webhookConfig), clearedConfig))
	assert.Empty(t, clearedConfig.Finalizers)
	assert.Nil(t, meta.FindStatusCondition(clearedConfig.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked))
	assert.Empty(t, clearedConfig.Status.ReferencingWorkloads)
}

func TestMCPWebhookConfigReconciler_mapMCPServerToWebhookConfig(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	server := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			WebhookConfigRef: &mcpv1beta1.WebhookConfigRef{Name: "current-config"},
		},
	}
	currentConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "current-config", Namespace: "default"},
		Status: mcpv1beta1.MCPWebhookConfigStatus{
			ReferencingWorkloads: []mcpv1beta1.WorkloadReference{{
				Kind: mcpv1beta1.WorkloadKindMCPServer,
				Name: server.Name,
			}},
		},
	}
	staleConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "stale-config", Namespace: "default"},
		Status: mcpv1beta1.MCPWebhookConfigStatus{
			ReferencingWorkloads: []mcpv1beta1.WorkloadReference{{
				Kind: mcpv1beta1.WorkloadKindMCPServer,
				Name: server.Name,
			}},
		},
	}

	t.Run("current WebhookConfigRef returns one request", func(t *testing.T) {
		t.Parallel()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server).
			Build()
		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}

		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: "current-config", Namespace: "default"},
		}}, r.mapMCPServerToWebhookConfig(ctx, server))
	})

	t.Run("stale ReferencingWorkloads returns owning config request", func(t *testing.T) {
		t.Parallel()

		serverWithoutRef := server.DeepCopy()
		serverWithoutRef.Spec.WebhookConfigRef = nil
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(serverWithoutRef, staleConfig).
			Build()
		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}

		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: "stale-config", Namespace: "default"},
		}}, r.mapMCPServerToWebhookConfig(ctx, serverWithoutRef))
	})

	t.Run("current and stale references are returned without duplicates", func(t *testing.T) {
		t.Parallel()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server, currentConfig, staleConfig).
			Build()
		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}

		require.ElementsMatch(t, []reconcile.Request{
			{NamespacedName: types.NamespacedName{Name: "current-config", Namespace: "default"}},
			{NamespacedName: types.NamespacedName{Name: "stale-config", Namespace: "default"}},
		}, r.mapMCPServerToWebhookConfig(ctx, server))
	})

	t.Run("wrong object type returns nil", func(t *testing.T) {
		t.Parallel()

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"}}

		assert.Nil(t, r.mapMCPServerToWebhookConfig(ctx, pod))
	})

	t.Run("list failure returns partial current-ref request", func(t *testing.T) {
		t.Parallel()

		listErr := errors.New("simulated list failure")
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(
					ctx context.Context,
					c client.WithWatch,
					list client.ObjectList,
					opts ...client.ListOption,
				) error {
					if _, ok := list.(*mcpv1alpha1.MCPWebhookConfigList); ok {
						return listErr
					}
					return c.List(ctx, list, opts...)
				},
			}).
			Build()
		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}

		require.ElementsMatch(t, []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: "current-config", Namespace: "default"},
		}}, r.mapMCPServerToWebhookConfig(ctx, server))
	})
}

func TestMCPWebhookConfigReconciler_updateReferencingWorkloads(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	webhookConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-config", Namespace: "default"},
	}
	referencingServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: "test-image",
			WebhookConfigRef: &mcpv1beta1.WebhookConfigRef{
				Name: webhookConfig.Name,
			},
		},
	}
	otherServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "other-server", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: "test-image",
			WebhookConfigRef: &mcpv1beta1.WebhookConfigRef{
				Name: "other-config",
			},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(webhookConfig, referencingServer, otherServer).
		WithStatusSubresource(&mcpv1alpha1.MCPWebhookConfig{}).
		Build()
	r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}

	result, err := r.updateReferencingWorkloads(ctx, webhookConfig)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	var updated mcpv1alpha1.MCPWebhookConfig
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(webhookConfig), &updated))
	assert.Equal(t, []mcpv1beta1.WorkloadReference{{
		Kind: mcpv1beta1.WorkloadKindMCPServer,
		Name: referencingServer.Name,
	}}, updated.Status.ReferencingWorkloads)

	result, err = r.updateReferencingWorkloads(ctx, &updated)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	var unchanged mcpv1alpha1.MCPWebhookConfig
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(webhookConfig), &unchanged))
	assert.Equal(t, updated.Status.ReferencingWorkloads, unchanged.Status.ReferencingWorkloads)
}

func TestMCPWebhookConfigReconciler_updateReferencingWorkloadsListError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	webhookConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-config", Namespace: "default"},
	}
	listErr := errors.New("simulated MCPServer list failure")
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(webhookConfig).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(
				ctx context.Context,
				c client.WithWatch,
				list client.ObjectList,
				opts ...client.ListOption,
			) error {
				if _, ok := list.(*mcpv1beta1.MCPServerList); ok {
					return listErr
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()
	r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}

	result, err := r.updateReferencingWorkloads(ctx, webhookConfig)
	require.ErrorIs(t, err, listErr)
	assert.Equal(t, reconcile.Result{}, result)
}

func findWebhookConfigCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

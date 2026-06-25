// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
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
			existingMCPServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithWebhookConfigRef("test-webhook-config"),
			),
			expectFinalizer: true,
			expectHash:      true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			scheme := testutil.NewScheme(t)

			objs := []client.Object{tt.webhookConfig}
			if tt.existingMCPServer != nil {
				objs = append(objs, tt.existingMCPServer)
			}
			fakeClient := withWebhookConfigRefIndex(fake.NewClientBuilder().WithScheme(scheme)).
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
		scheme := testutil.NewScheme(t)
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

		scheme := testutil.NewScheme(t)

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

	scheme := testutil.NewScheme(t)

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

	mcpServer := v1beta1test.NewMCPServer("server1", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithWebhookConfigRef("test-config"),
	)

	fakeClient := withWebhookConfigRefIndex(fake.NewClientBuilder().WithScheme(scheme)).
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
}

func TestMCPWebhookConfigReconciler_handleDeletionClearsBlockedCondition(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	webhookConfig := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Finalizers: []string{WebhookConfigFinalizerName},
		},
		Status: mcpv1beta1.MCPWebhookConfigStatus{
			Conditions: []metav1.Condition{{
				Type:   mcpv1beta1.ConditionTypeDeletionBlocked,
				Status: metav1.ConditionTrue,
				Reason: "ReferencedByWorkloads",
			}},
		},
	}

	fakeClient := withWebhookConfigRefIndex(fake.NewClientBuilder().WithScheme(scheme)).
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
}

func findWebhookConfigCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestMCPWebhookConfigReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		webhookConfig     *mcpv1beta1.MCPWebhookConfig
		existingMCPServer *mcpv1beta1.MCPServer
		expectFinalizer   bool
		expectHash        bool
	}{
		{
			name: "new webhook config without references",
			webhookConfig: &mcpv1beta1.MCPWebhookConfig{
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
			webhookConfig: &mcpv1beta1.MCPWebhookConfig{
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
			ctx := context.Background()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []client.Object{tt.webhookConfig}
			if tt.existingMCPServer != nil {
				objs = append(objs, tt.existingMCPServer)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPWebhookConfig{}).
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

			var updatedConfig mcpv1beta1.MCPWebhookConfig
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
			}
		})
	}

	t.Run("resource not found", func(t *testing.T) {
		t.Parallel()
		scheme := runtime.NewScheme()
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
		result, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("invalid spec sets Valid condition false", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1beta1.AddToScheme(scheme))

		webhookConfig := &mcpv1beta1.MCPWebhookConfig{
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
			WithStatusSubresource(&mcpv1beta1.MCPWebhookConfig{}).
			Build()

		r := &MCPWebhookConfigReconciler{Client: fakeClient, Scheme: scheme}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: webhookConfig.Name, Namespace: webhookConfig.Namespace}}

		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		if result.RequeueAfter > 0 {
			result, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
		}

		var updated mcpv1beta1.MCPWebhookConfig
		require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updated))
		condition := findCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Contains(t, condition.Message, "must use HTTPS")
	})
}

func TestMCPWebhookConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	webhookConfig := &mcpv1beta1.MCPWebhookConfig{
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
		WithStatusSubresource(&mcpv1beta1.MCPWebhookConfig{}).
		Build()

	r := &MCPWebhookConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := context.Background()
	result, err := r.handleDeletion(ctx, webhookConfig)
	require.NoError(t, err)
	assert.Equal(t, webhookConfigDeletionRequeueDelay, result.RequeueAfter)

	// Delete server and try again
	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	_, err = r.handleDeletion(ctx, webhookConfig)
	assert.NoError(t, err, "Should delete successfully after reference removed")
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

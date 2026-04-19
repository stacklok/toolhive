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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPWebhookConfigReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		webhookConfig     *mcpv1alpha1.MCPWebhookConfig
		existingMCPServer *mcpv1alpha1.MCPServer
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
				Spec: mcpv1alpha1.MCPWebhookConfigSpec{
					Validating: []mcpv1alpha1.WebhookSpec{
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
				Spec: mcpv1alpha1.MCPWebhookConfigSpec{
					Mutating: []mcpv1alpha1.WebhookSpec{
						{
							Name: "test-mutate",
							URL:  "https://test.example.com",
						},
					},
				},
			},
			existingMCPServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{
						Name: "test-webhook-config",
					},
				},
			},
			expectFinalizer: true,
			expectHash:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
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
				assert.Contains(t, updatedConfig.Status.ReferencingServers, tt.existingMCPServer.Name)
			}
		})
	}

	t.Run("resource not found", func(t *testing.T) {
		t.Parallel()
		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
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
}

func TestMCPWebhookConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{
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

	ctx := context.Background()
	_, err := r.handleDeletion(ctx, webhookConfig)
	assert.Error(t, err, "Should not delete while referenced by server")

	// Delete server and try again
	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	_, err = r.handleDeletion(ctx, webhookConfig)
	assert.NoError(t, err, "Should delete successfully after reference removed")
}

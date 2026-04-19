// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleWebhookConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		mcpServer           *mcpv1alpha1.MCPServer
		webhookConfig       *mcpv1alpha1.MCPWebhookConfig
		expectError         bool
		expectErrorContains string
		expectHash          string
		expectHashCleared   bool
	}{
		{
			name: "no ref clears previously stored hash",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec:       mcpv1alpha1.MCPServerSpec{Image: "img"},
				Status:     mcpv1alpha1.MCPServerStatus{WebhookConfigHash: "old-hash"},
			},
			expectHashCleared: true,
		},
		{
			name: "referenced config not found returns error",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:            "img",
					WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "missing"},
				},
			},
			expectError:         true,
			expectErrorContains: "not found",
		},
		{
			name: "valid config sets hash",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:            "img",
					WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "cfg"},
				},
			},
			webhookConfig: &mcpv1alpha1.MCPWebhookConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
				Spec:       mcpv1alpha1.MCPWebhookConfigSpec{},
				Status: mcpv1alpha1.MCPWebhookConfigStatus{
					ConfigHash: "new-hash",
				},
			},
			expectHash: "new-hash",
		},
		{
			name: "detects hash change",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:            "img",
					WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "cfg"},
				},
				Status: mcpv1alpha1.MCPServerStatus{WebhookConfigHash: "old-hash"},
			},
			webhookConfig: &mcpv1alpha1.MCPWebhookConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
				Spec:       mcpv1alpha1.MCPWebhookConfigSpec{},
				Status: mcpv1alpha1.MCPWebhookConfigStatus{
					ConfigHash: "newer-hash",
				},
			},
			expectHash: "newer-hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []runtime.Object{tt.mcpServer}
			if tt.webhookConfig != nil {
				objs = append(objs, tt.webhookConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
				Build()

			r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			err := r.handleWebhookConfig(ctx, tt.mcpServer)

			if tt.expectError {
				assert.Error(t, err)
				if tt.expectErrorContains != "" {
					assert.Contains(t, err.Error(), tt.expectErrorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectHash != "" {
				assert.Equal(t, tt.expectHash, tt.mcpServer.Status.WebhookConfigHash)
			}
			if tt.expectHashCleared {
				assert.Empty(t, tt.mcpServer.Status.WebhookConfigHash)
			}
		})
	}
}

func TestMCPServerReconciler_mapWebhookConfigToServers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	servers := []runtime.Object{
		&mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "test-config"},
			},
		},
		&mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s2", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "other-config"},
			},
		},
		&mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: "other-ns"},
			Spec: mcpv1alpha1.MCPServerSpec{
				WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "test-config"},
			},
		},
		&mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "s4", Namespace: "default"},
			Spec:       mcpv1alpha1.MCPServerSpec{}, // No reference
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(servers...).
		Build()

	r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	t.Run("valid WebhookConfig", func(t *testing.T) {
		t.Parallel()
		config := &mcpv1alpha1.MCPWebhookConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default"},
		}

		reqs := r.mapWebhookConfigToServers(ctx, config)
		require.Len(t, reqs, 1)
		assert.Equal(t, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "s1", Namespace: "default"},
		}, reqs[0])
	})

	t.Run("wrong object type", func(t *testing.T) {
		t.Parallel()
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
		}
		reqs := r.mapWebhookConfigToServers(ctx, pod)
		assert.Nil(t, reqs)
	})
}

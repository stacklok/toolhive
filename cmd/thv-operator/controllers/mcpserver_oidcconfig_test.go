// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleOIDCConfig(t *testing.T) {
	t.Parallel()

	// validOIDCCondition is a helper to build a Ready=True condition slice.
	validOIDCCondition := []metav1.Condition{{
		Type: mcpv1alpha1.ConditionTypeOIDCConfigReady, Status: metav1.ConditionTrue, Reason: mcpv1alpha1.ConditionReasonOIDCConfigValid,
	}}

	tests := []struct {
		name                    string
		mcpServer               *mcpv1alpha1.MCPServer
		oidcConfig              *mcpv1alpha1.MCPOIDCConfig
		expectError             bool
		expectErrorContains     string
		expectHash              string
		expectHashCleared       bool
		expectConditionStatus   *metav1.ConditionStatus
		expectConditionReason   string
		expectReferencingServer bool
	}{
		{
			name: "no ref clears previously stored hash",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec:       mcpv1alpha1.MCPServerSpec{Image: "img"},
				Status:     mcpv1alpha1.MCPServerStatus{OIDCConfigHash: "old"},
			},
			expectHashCleared: true,
		},
		{
			name: "referenced config not found sets NotFound condition",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "missing", Audience: "aud"},
				},
			},
			expectError:           true,
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1alpha1.ConditionReasonOIDCConfigRefNotFound,
		},
		{
			name: "config with Ready=False sets NotValid condition",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "bad", Audience: "aud"},
				},
			},
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x"},
				},
				Status: mcpv1alpha1.MCPOIDCConfigStatus{
					Conditions: []metav1.Condition{{
						Type: mcpv1alpha1.ConditionTypeOIDCConfigReady, Status: metav1.ConditionFalse, Reason: mcpv1alpha1.ConditionReasonOIDCConfigInvalid,
						Message: "missing fields",
					}},
				},
			},
			expectError:           true,
			expectErrorContains:   "not ready",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1alpha1.ConditionReasonOIDCConfigRefNotValid,
		},
		{
			name: "valid config sets hash, condition, and referencing server",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "ok", Audience: "aud"},
				},
			},
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "default"},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x", ClientID: "c"},
				},
				Status: mcpv1alpha1.MCPOIDCConfigStatus{
					ConfigHash: "hash-123",
					Conditions: validOIDCCondition,
				},
			},
			expectHash:              "hash-123",
			expectConditionStatus:   conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason:   mcpv1alpha1.ConditionReasonOIDCConfigRefValid,
			expectReferencingServer: true,
		},
		{
			name: "detects config hash change",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
				},
				Status: mcpv1alpha1.MCPServerStatus{OIDCConfigHash: "old-hash"},
			},
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
				Status: mcpv1alpha1.MCPOIDCConfigStatus{
					ConfigHash: "new-hash",
					Conditions: validOIDCCondition,
				},
			},
			expectHash:              "new-hash",
			expectConditionStatus:   conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason:   mcpv1alpha1.ConditionReasonOIDCConfigRefValid,
			expectReferencingServer: true,
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
			if tt.oidcConfig != nil {
				objs = append(objs, tt.oidcConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(
					&mcpv1alpha1.MCPServer{},
					&mcpv1alpha1.MCPOIDCConfig{},
				).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			err := reconciler.handleOIDCConfig(ctx, tt.mcpServer)

			if tt.expectError {
				assert.Error(t, err)
				if tt.expectErrorContains != "" {
					assert.Contains(t, err.Error(), tt.expectErrorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectHash != "" {
				assert.Equal(t, tt.expectHash, tt.mcpServer.Status.OIDCConfigHash)
			}
			if tt.expectHashCleared {
				assert.Empty(t, tt.mcpServer.Status.OIDCConfigHash)
			}

			if tt.expectConditionStatus != nil {
				var found bool
				for _, cond := range tt.mcpServer.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionOIDCConfigRefValidated {
						found = true
						assert.Equal(t, string(*tt.expectConditionStatus), string(cond.Status))
						assert.Equal(t, tt.expectConditionReason, cond.Reason)
						break
					}
				}
				assert.True(t, found, "expected %s condition", mcpv1alpha1.ConditionOIDCConfigRefValidated)
			}

			if tt.expectReferencingServer && tt.oidcConfig != nil {
				var updated mcpv1alpha1.MCPOIDCConfig
				require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(tt.oidcConfig), &updated))
				assert.Contains(t, updated.Status.ReferencingServers, tt.mcpServer.Name)
			}
		})
	}
}

func TestMCPServerReconciler_updateOIDCConfigReferencingServers(t *testing.T) {
	t.Parallel()

	t.Run("adds new server name", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		cfg := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
			Status:     mcpv1alpha1.MCPOIDCConfigStatus{ReferencingServers: []string{"existing"}},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).
			WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
		r := newTestMCPServerReconciler(fc, scheme, kubernetes.PlatformKubernetes)

		require.NoError(t, r.updateOIDCConfigReferencingServers(ctx, cfg, "new"))
		assert.ElementsMatch(t, []string{"existing", "new"}, cfg.Status.ReferencingServers)
	})

	t.Run("does not duplicate existing name", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		cfg := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
			Status:     mcpv1alpha1.MCPOIDCConfigStatus{ReferencingServers: []string{"existing"}},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).
			WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
		r := newTestMCPServerReconciler(fc, scheme, kubernetes.PlatformKubernetes)

		require.NoError(t, r.updateOIDCConfigReferencingServers(ctx, cfg, "existing"))
		assert.Len(t, cfg.Status.ReferencingServers, 1)
	})
}

func TestMCPOIDCConfigReconciler_handleDeletion_BlocksWhenReferenced(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	cfg := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default",
			Finalizers: []string{OIDCConfigFinalizerName}, DeletionTimestamp: &now,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x"},
		},
	}
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "referencing", Namespace: "default"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:         "img",
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cfg, server).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
	r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

	result, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)

	assert.Greater(t, result.RequeueAfter, time.Duration(0), "should requeue while referenced")
	assert.Contains(t, cfg.Finalizers, OIDCConfigFinalizerName, "finalizer must remain")
}

func TestMCPOIDCConfigReconciler_handleDeletion_AllowsWhenNotReferenced(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	cfg := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default",
			Finalizers: []string{OIDCConfigFinalizerName}, DeletionTimestamp: &now,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x"},
		},
	}
	// Unrelated server -- does NOT reference this config
	unrelated := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec:       mcpv1alpha1.MCPServerSpec{Image: "img"},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cfg, unrelated).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
	r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

	result, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)

	assert.Equal(t, time.Duration(0), result.RequeueAfter, "should not requeue")
	assert.NotContains(t, cfg.Finalizers, OIDCConfigFinalizerName, "finalizer should be removed")
}

func TestMCPOIDCConfigReconciler_handleDeletion_IgnoresCrossNamespaceRef(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	cfg := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "ns-a",
			Finalizers: []string{OIDCConfigFinalizerName}, DeletionTimestamp: &now,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x"},
		},
	}
	// Server in a DIFFERENT namespace referencing same config name
	crossNS := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns-b"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:         "img",
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cfg, crossNS).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
	r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

	result, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)

	assert.Equal(t, time.Duration(0), result.RequeueAfter)
	assert.NotContains(t, cfg.Finalizers, OIDCConfigFinalizerName,
		"cross-namespace refs should not block deletion")
}

// conditionStatusPtr returns a pointer to a metav1.ConditionStatus value.
func conditionStatusPtr(s metav1.ConditionStatus) *metav1.ConditionStatus {
	return &s
}

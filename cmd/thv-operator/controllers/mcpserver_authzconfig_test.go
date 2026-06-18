// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// authzConfigForTest builds an MCPAuthzConfig with the given validity and hash.
func authzConfigForTest(name string, valid bool, hash string) *mcpv1beta1.MCPAuthzConfig {
	status := metav1.ConditionFalse
	if valid {
		status = metav1.ConditionTrue
	}
	return &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(`{"policies":["permit(principal, action, resource);"],"entities_json":"[]"}`)},
		},
		Status: mcpv1beta1.MCPAuthzConfigStatus{
			ConfigHash: hash,
			Conditions: []metav1.Condition{{
				Type:   mcpv1beta1.ConditionTypeAuthzConfigValid,
				Status: status,
				Reason: "Test",
			}},
		},
	}
}

func TestMCPServerReconciler_handleAuthzConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		mcpServer             *mcpv1beta1.MCPServer
		authzConfig           *mcpv1beta1.MCPAuthzConfig
		expectError           bool
		expectHash            string
		expectHashCleared     bool
		expectConditionStatus *metav1.ConditionStatus
		expectConditionReason string
		expectConditionGone   bool
	}{
		{
			name: "no ref clears stored hash and condition",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec:       mcpv1beta1.MCPServerSpec{Image: "img"},
				Status: mcpv1beta1.MCPServerStatus{
					AuthzConfigHash: "old",
					Conditions: []metav1.Condition{{
						Type:   mcpv1beta1.ConditionAuthzConfigRefValidated,
						Status: metav1.ConditionTrue,
						Reason: mcpv1beta1.ConditionReasonAuthzConfigRefValid,
					}},
				},
			},
			expectHashCleared:   true,
			expectConditionGone: true,
		},
		{
			name: "referenced config not found sets NotFound condition",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:          "img",
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "missing"},
				},
			},
			expectError:           true,
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1beta1.ConditionReasonAuthzConfigRefNotFound,
		},
		{
			name: "referenced config not valid sets NotValid condition",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:          "img",
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "bad"},
				},
			},
			authzConfig:           authzConfigForTest("bad", false, ""),
			expectError:           true,
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1beta1.ConditionReasonAuthzConfigRefNotValid,
		},
		{
			name: "valid ref sets condition True and tracks hash",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:          "img",
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "ok"},
				},
			},
			authzConfig:           authzConfigForTest("ok", true, "hash-123"),
			expectHash:            "hash-123",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason: mcpv1beta1.ConditionReasonAuthzConfigRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []runtime.Object{tt.mcpServer}
			if tt.authzConfig != nil {
				objs = append(objs, tt.authzConfig)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPServer{}, &mcpv1beta1.MCPAuthzConfig{}).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			err := reconciler.handleAuthzConfig(ctx, tt.mcpServer)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectHash != "" {
				assert.Equal(t, tt.expectHash, tt.mcpServer.Status.AuthzConfigHash)
			}
			if tt.expectHashCleared {
				assert.Empty(t, tt.mcpServer.Status.AuthzConfigHash)
			}

			cond := meta.FindStatusCondition(tt.mcpServer.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
			if tt.expectConditionGone {
				assert.Nil(t, cond, "AuthzConfigRefValidated condition must be cleared when the ref is removed")
			}
			if tt.expectConditionStatus != nil {
				require.NotNil(t, cond, "expected AuthzConfigRefValidated condition")
				assert.Equal(t, *tt.expectConditionStatus, cond.Status)
				assert.Equal(t, tt.expectConditionReason, cond.Reason)
			}
		})
	}
}

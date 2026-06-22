// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestMCPRemoteProxyReconciler_handleAuthzConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		proxy                 *mcpv1beta1.MCPRemoteProxy
		authzConfig           *mcpv1beta1.MCPAuthzConfig
		expectError           bool
		expectErrContains     string
		expectHash            string
		expectHashCleared     bool
		expectConditionStatus *metav1.ConditionStatus
		expectConditionReason string
		expectConditionGone   bool
	}{
		{
			name: "no ref clears stored hash and condition",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
				Status: mcpv1beta1.MCPRemoteProxyStatus{
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
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "missing"},
				},
			},
			expectError:           true,
			expectErrContains:     "not found",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1beta1.ConditionReasonAuthzConfigRefNotFound,
		},
		{
			name: "referenced config not valid sets NotValid condition",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "bad"},
				},
			},
			authzConfig:           authzConfigForTest("bad", false, ""),
			expectError:           true,
			expectErrContains:     "not valid",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1beta1.ConditionReasonAuthzConfigRefNotValid,
		},
		{
			name: "valid ref sets condition True and tracks hash",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
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

			objs := []runtime.Object{tt.proxy}
			if tt.authzConfig != nil {
				objs = append(objs, tt.authzConfig)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}, &mcpv1beta1.MCPAuthzConfig{}).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.handleAuthzConfig(ctx, tt.proxy)
			if tt.expectError {
				assert.Error(t, err)
				if tt.expectErrContains != "" {
					assert.ErrorContains(t, err, tt.expectErrContains)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectHash != "" {
				assert.Equal(t, tt.expectHash, tt.proxy.Status.AuthzConfigHash)
			}
			if tt.expectHashCleared {
				assert.Empty(t, tt.proxy.Status.AuthzConfigHash)
			}

			cond := meta.FindStatusCondition(tt.proxy.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
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

// TestMCPRemoteProxyReconciler_handleAuthzConfig_Transitions exercises the stateful
// bookkeeping (needsUpdate from the prior condition, and recovery) that the
// static single-state cases above do not: valid -> invalid -> valid.
func TestMCPRemoteProxyReconciler_handleAuthzConfig_Transitions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	authzConfig := authzConfigForTest("cfg", true, "h1")
	proxy := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "cfg"},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(proxy, authzConfig).
		WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}, &mcpv1beta1.MCPAuthzConfig{}).
		Build()
	r := &MCPRemoteProxyReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	condStatus := func() *metav1.Condition {
		return meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
	}

	// valid -> condition True + hash tracked
	require.NoError(t, r.handleAuthzConfig(ctx, proxy))
	require.NotNil(t, condStatus())
	assert.Equal(t, metav1.ConditionTrue, condStatus().Status)
	assert.Equal(t, "h1", proxy.Status.AuthzConfigHash)

	// flip the referenced config to invalid -> condition transitions to False/NotValid
	authzConfig.Status.Conditions = []metav1.Condition{{
		Type: mcpv1beta1.ConditionTypeAuthzConfigValid, Status: metav1.ConditionFalse, Reason: "Invalidated",
	}}
	require.NoError(t, fakeClient.Status().Update(ctx, authzConfig))

	assert.Error(t, r.handleAuthzConfig(ctx, proxy))
	require.NotNil(t, condStatus())
	assert.Equal(t, metav1.ConditionFalse, condStatus().Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonAuthzConfigRefNotValid, condStatus().Reason)

	// recover the config -> condition transitions back to True
	authzConfig.Status.Conditions = []metav1.Condition{{
		Type: mcpv1beta1.ConditionTypeAuthzConfigValid, Status: metav1.ConditionTrue, Reason: "Test",
	}}
	require.NoError(t, fakeClient.Status().Update(ctx, authzConfig))

	require.NoError(t, r.handleAuthzConfig(ctx, proxy))
	require.NotNil(t, condStatus())
	assert.Equal(t, metav1.ConditionTrue, condStatus().Status)
}

// TestMapAuthzConfigToMCPRemoteProxy verifies that only proxies referencing the
// changed MCPAuthzConfig are enqueued for reconciliation.
func TestMapAuthzConfigToMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	proxy1 := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy1", Namespace: "default"},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "shared-authz"},
		},
	}
	proxy2 := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy2", Namespace: "default"},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "other-authz"},
		},
	}
	proxy3 := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy3", Namespace: "default"},
		Spec:       mcpv1beta1.MCPRemoteProxySpec{}, // no ref
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(proxy1, proxy2, proxy3).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()

	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-authz", Namespace: "default"},
	}

	requests := reconciler.mapAuthzConfigToMCPRemoteProxy(ctx, authzConfig)

	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Name: "proxy1", Namespace: "default"}, requests[0].NamespacedName)
}

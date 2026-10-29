// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

func TestVirtualMCPServerValidateCABundleRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configure  func(*mcpv1beta1.VirtualMCPServer, *mcpv1beta1.MCPOIDCConfig, *corev1.ConfigMap)
		omitOIDC   bool
		omitMap    bool
		readError  string
		remove     bool
		preserve   bool
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "existing ConfigMap and explicit key",
			wantStatus: metav1.ConditionTrue,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid,
		},
		{
			name: "empty key defaults to ca.crt",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef.ConfigMapRef.Key = ""
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid,
		},
		{
			name: "custom key",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, cm *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef.ConfigMapRef.Key = "custom.pem"
				cm.Data = map[string]string{"custom.pem": "certificate"}
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid,
		},
		{
			name: "key in binaryData is projected too",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, _ *mcpv1beta1.MCPOIDCConfig, cm *corev1.ConfigMap) {
				cm.Data = nil
				cm.BinaryData = map[string][]byte{"ca.crt": []byte("certificate")}
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid,
		},
		{
			name:       "missing ConfigMap",
			omitMap:    true,
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefNotFound,
		},
		{
			name: "ConfigMap in another namespace is not accepted",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, _ *mcpv1beta1.MCPOIDCConfig, cm *corev1.ConfigMap) {
				cm.Namespace = "other"
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefNotFound,
		},
		{
			name: "missing selected key",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef.ConfigMapRef.Key = "missing.pem"
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid,
		},
		{
			name: "missing ConfigMap selector",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef = &mcpv1beta1.CABundleSource{}
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid,
		},
		{
			name: "empty ConfigMap name",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef.ConfigMapRef.Name = ""
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid,
		},
		{
			name: "ConfigMap name cannot fit in OIDC volume name",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef.ConfigMapRef.Name = strings.Repeat("a", 49)
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid,
		},
		{
			name: "removed incoming auth clears condition",
			configure: func(vmcp *mcpv1beta1.VirtualMCPServer, _ *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				vmcp.Spec.IncomingAuth = nil
			},
			remove: true,
		},
		{
			name: "removed OIDC reference clears condition",
			configure: func(vmcp *mcpv1beta1.VirtualMCPServer, _ *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				vmcp.Spec.IncomingAuth.OIDCConfigRef = nil
			},
			remove: true,
		},
		{
			name: "removed CA reference clears condition",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Inline.CABundleRef = nil
			},
			remove: true,
		},
		{
			name: "non-inline OIDC config clears condition",
			configure: func(_ *mcpv1beta1.VirtualMCPServer, oidc *mcpv1beta1.MCPOIDCConfig, _ *corev1.ConfigMap) {
				oidc.Spec.Type = mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount
				oidc.Spec.Inline = nil
				oidc.Spec.KubernetesServiceAccount = &mcpv1beta1.KubernetesServiceAccountOIDCConfig{}
			},
			remove: true,
		},
		{
			name:     "unresolved OIDC config leaves condition to authoritative ref handler",
			omitOIDC: true,
			preserve: true,
		},
		{
			name:      "transient OIDC read preserves condition",
			readError: "oidc",
			preserve:  true,
		},
		{
			name:      "transient ConfigMap read preserves condition",
			readError: "configmap",
			preserve:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vmcp := v1beta1test.NewVirtualMCPServer("vmcp", testCABundleNamespace,
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
					Type: "oidc",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name: testProxyOIDCConfigName, Audience: "audience",
					},
				}),
			)
			vmcp.Generation = 3
			oldCondition := metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeVirtualMCPServerCABundleRefValidated,
				Status: metav1.ConditionTrue, Reason: mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid,
				Message: "previous validation", ObservedGeneration: 2,
				LastTransitionTime: metav1.NewTime(time.Unix(100, 0)),
			}
			otherCondition := metav1.Condition{
				Type: "UnrelatedCondition", Status: metav1.ConditionTrue, Reason: "Unchanged",
				Message: "preserve other conditions", LastTransitionTime: metav1.NewTime(time.Unix(100, 0)),
			}
			vmcp.Status.Conditions = []metav1.Condition{oldCondition, otherCondition}
			vmcp.Status.Phase = mcpv1beta1.VirtualMCPServerPhaseReady
			oidc := newOIDCConfigWithCABundle(configMapCABundleRef(testCABundleConfigMapName, "ca.crt"))
			cm := caBundleConfigMap()
			if tt.configure != nil {
				tt.configure(vmcp, oidc, cm)
			}
			scheme := testutil.NewScheme(t)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmcp).
				WithStatusSubresource(&mcpv1beta1.VirtualMCPServer{}).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						_, isOIDC := obj.(*mcpv1beta1.MCPOIDCConfig)
						_, isConfigMap := obj.(*corev1.ConfigMap)
						if (tt.readError == "oidc" && isOIDC) || (tt.readError == "configmap" && isConfigMap) {
							return apierrors.NewServiceUnavailable("temporary API outage")
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})
			if !tt.omitOIDC {
				builder = builder.WithObjects(oidc)
			}
			if !tt.omitMap {
				builder = builder.WithObjects(cm)
			}
			c := builder.Build()
			r := &VirtualMCPServerReconciler{Client: c, Scheme: scheme}
			key := client.ObjectKeyFromObject(vmcp)
			var firstResourceVersion string
			for pass := range 2 {
				current := &mcpv1beta1.VirtualMCPServer{}
				require.NoError(t, c.Get(t.Context(), key, current))
				statusManager := virtualmcpserverstatus.NewStatusManager(current)
				err := r.validateCABundleRef(t.Context(), current, statusManager)
				if tt.readError == "configmap" {
					require.Error(t, err)
					assert.True(t, apierrors.IsServiceUnavailable(err), "transient reads must request a retry")
				} else {
					require.NoError(t, err, "invalid or missing CA references remain advisory")
				}
				require.NoError(t, r.applyStatusUpdates(t.Context(), current, statusManager))
				persisted := &mcpv1beta1.VirtualMCPServer{}
				require.NoError(t, c.Get(t.Context(), key, persisted))
				condition := meta.FindStatusCondition(persisted.Status.Conditions,
					mcpv1beta1.ConditionTypeVirtualMCPServerCABundleRefValidated)
				switch {
				case tt.remove:
					assert.Nil(t, condition)
				case tt.preserve:
					require.NotNil(t, condition)
					assert.Equal(t, oldCondition, *condition)
				default:
					require.NotNil(t, condition)
					assert.Equal(t, tt.wantStatus, condition.Status)
					assert.Equal(t, tt.wantReason, condition.Reason)
					assert.Equal(t, vmcp.Generation, condition.ObservedGeneration)
					assert.NotEmpty(t, condition.Message)
				}
				assert.Equal(t, &otherCondition, meta.FindStatusCondition(persisted.Status.Conditions, otherCondition.Type))
				assert.Equal(t, mcpv1beta1.VirtualMCPServerPhaseReady, persisted.Status.Phase,
					"CA validation is advisory and must not change phase")
				if pass == 0 {
					firstResourceVersion = persisted.ResourceVersion
				} else {
					assert.Equal(t, firstResourceVersion, persisted.ResourceVersion, "unchanged validation must not write status")
				}
			}
		})
	}
}

func TestMapOIDCCABundleConfigMapToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	newOIDC := func(name, namespace, configMap string) *mcpv1beta1.MCPOIDCConfig {
		oidc := newOIDCConfigWithCABundle(configMapCABundleRef(configMap, "ca.crt"))
		oidc.Name, oidc.Namespace = name, namespace
		return oidc
	}
	newVMCP := func(name, namespace, oidc string) *mcpv1beta1.VirtualMCPServer {
		return v1beta1test.NewVirtualMCPServer(name, namespace,
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidc},
			}))
	}
	scheme := testutil.NewScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&mcpv1beta1.MCPOIDCConfig{}, oidcConfigCABundleConfigMapIndex, indexOIDCConfigByCABundleConfigMap).
		WithIndex(&mcpv1beta1.VirtualMCPServer{}, virtualMCPServerOIDCConfigRefIndex, indexVirtualMCPServerByOIDCConfigRef).
		WithObjects(
			newOIDC("oidc-a", "default", "shared-ca"), newOIDC("oidc-b", "default", "shared-ca"),
			newOIDC("oidc-unrelated", "default", "other-ca"), newOIDC("oidc-other-ns", "other", "shared-ca"),
			newVMCP("vmcp-a", "default", "oidc-a"), newVMCP("vmcp-b", "default", "oidc-b"),
			newVMCP("vmcp-unrelated", "default", "oidc-unrelated"), newVMCP("vmcp-other-ns", "other", "oidc-other-ns"),
			newVMCP("vmcp-cross-namespace", "default", "oidc-other-ns"),
			v1beta1test.NewVirtualMCPServer("vmcp-without-auth", "default"),
		).Build()
	r := &VirtualMCPServerReconciler{Client: c, Scheme: scheme}
	tests := []struct {
		name string
		obj  client.Object
		want []types.NamespacedName
	}{
		{
			name: "shared CA reaches all namespace-local OIDC consumers",
			obj:  &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-ca", Namespace: "default"}},
			want: []types.NamespacedName{{Name: "vmcp-a", Namespace: "default"}, {Name: "vmcp-b", Namespace: "default"}},
		},
		{
			name: "matching name in another namespace is isolated",
			obj:  &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-ca", Namespace: "other"}},
			want: []types.NamespacedName{{Name: "vmcp-other-ns", Namespace: "other"}},
		},
		{
			name: "unreferenced ConfigMap reaches no consumers",
			obj:  &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: "default"}},
		},
		{
			name: "non-ConfigMap event is ignored",
			obj:  &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-ca", Namespace: "default"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requests := r.mapOIDCCABundleConfigMapToVirtualMCPServer(t.Context(), tt.obj)
			got := make([]types.NamespacedName, 0, len(requests))
			for _, request := range requests {
				got = append(got, request.NamespacedName)
			}
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

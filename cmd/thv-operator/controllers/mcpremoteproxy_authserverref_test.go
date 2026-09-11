// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestMCPRemoteProxyReconciler_handleAuthServerRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		proxy           func() *mcpv1beta1.MCPRemoteProxy
		authConfig      func() *mcpv1beta1.MCPExternalAuthConfig
		expectError     bool
		errContains     string
		expectHash      string
		conditionStatus metav1.ConditionStatus
		conditionReason string
	}{
		{
			name: "nil authServerRef removes condition and clears hash",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return v1beta1test.NewMCPRemoteProxy("proxy", "default",
					v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
					v1beta1test.WithRemoteProxyStatus(mcpv1beta1.MCPRemoteProxyStatus{
						AuthServerConfigHash: "old-hash",
					}),
				)
			},
			expectHash: "",
		},
		{
			name: "unsupported kind sets InvalidKind condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return v1beta1test.NewMCPRemoteProxy("proxy", "default",
					v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
					v1beta1test.WithRemoteProxyAuthServerRef("Secret", "foo"),
				)
			},
			expectError:     true,
			errContains:     "unsupported authServerRef kind",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefInvalidKind,
		},
		{
			name: "not found sets NotFound condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return v1beta1test.NewMCPRemoteProxy("proxy", "default",
					v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
					v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "missing"),
				)
			},
			expectError:     true,
			errContains:     "not found",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefNotFound,
		},
		{
			name: "wrong type sets InvalidType condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return v1beta1test.NewMCPRemoteProxy("proxy", "default",
					v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
					v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "sts-config"),
				)
			},
			authConfig: func() *mcpv1beta1.MCPExternalAuthConfig {
				return &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "sts-config", Namespace: "default"},
					Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
						Type: mcpv1beta1.ExternalAuthTypeAWSSts,
						AWSSts: &mcpv1beta1.AWSStsConfig{
							Region: "us-east-1",
						},
					},
				}
			},
			expectError:     true,
			errContains:     "only embeddedAuthServer is supported",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefInvalidType,
		},
		{
			name: "multi-upstream sets MultiUpstream condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return v1beta1test.NewMCPRemoteProxy("proxy", "default",
					v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
					v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "multi"),
				)
			},
			authConfig: func() *mcpv1beta1.MCPExternalAuthConfig {
				return &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
					Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
						Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
							Issuer: "https://auth.example.com",
							UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
								{Name: "a", Type: mcpv1beta1.UpstreamProviderTypeOIDC, OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://a.com", ClientID: "a"}},
								{Name: "b", Type: mcpv1beta1.UpstreamProviderTypeOIDC, OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://b.com", ClientID: "b"}},
							},
						},
					},
					Status: mcpv1beta1.MCPExternalAuthConfigStatus{ConfigHash: "multi-hash"},
				}
			},
			expectError:     true,
			errContains:     "only 1 is supported",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefMultiUpstream,
		},
		{
			name: "valid ref sets Valid condition and updates hash",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return v1beta1test.NewMCPRemoteProxy("proxy", "default",
					v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
					v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "valid"),
				)
			},
			authConfig: func() *mcpv1beta1.MCPExternalAuthConfig {
				return &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: "default"},
					Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
						Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
							Issuer:                       "https://auth.example.com",
							AuthorizationEndpointBaseURL: "https://auth.example.com",
							SigningKeySecretRefs:         []mcpv1beta1.SecretKeyRef{{Name: "key", Key: "pem"}},
							HMACSecretRefs:               []mcpv1beta1.SecretKeyRef{{Name: "hmac", Key: "secret"}},
						},
					},
					Status: mcpv1beta1.MCPExternalAuthConfigStatus{ConfigHash: "valid-hash"},
				}
			},
			expectHash:      "valid-hash",
			conditionStatus: metav1.ConditionTrue,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			proxy := tt.proxy()
			objs := []client.Object{proxy}
			if tt.authConfig != nil {
				objs = append(objs, tt.authConfig())
			}

			reconciler, _ := newTestMCPRemoteProxyReconciler(t, objs...)
			err := reconciler.handleAuthServerRef(ctx, proxy)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectHash, proxy.Status.AuthServerConfigHash)
			}

			cond := meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
			if tt.conditionStatus != "" {
				require.NotNil(t, cond, "AuthServerRefValidated condition should be present")
				assert.Equal(t, tt.conditionStatus, cond.Status)
				assert.Equal(t, tt.conditionReason, cond.Reason)
			} else {
				assert.Nil(t, cond, "AuthServerRefValidated condition should be removed")
			}
		})
	}
}

func TestMCPRemoteProxyReconciler_handleInvalidEmbeddedAuthServerConfigExternalAuthConfigRef(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"),
		v1beta1test.WithRemoteProxyStatus(mcpv1beta1.MCPRemoteProxyStatus{
			Conditions: []metav1.Condition{{
				Type:   mcpv1beta1.ConditionTypeReady,
				Status: metav1.ConditionTrue,
			}},
		}),
	)
	reconciler, _ := newTestMCPRemoteProxyReconciler(t, proxy)

	require.NoError(t, reconciler.handleInvalidEmbeddedAuthServerConfig(t.Context(), proxy,
		&ctrlutil.InvalidEmbeddedAuthServerConfigError{
			Err:    stderrors.New("invalid delegate client"),
			Source: ctrlutil.EmbeddedAuthServerConfigSourceExternalAuthConfigRef,
		},
	))

	validated := meta.FindStatusCondition(proxy.Status.Conditions,
		mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated)
	require.NotNil(t, validated)
	assert.Equal(t, metav1.ConditionFalse, validated.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonInvalidConfig, validated.Reason)
	ready := meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionTypeReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
}

func TestMCPRemoteProxyReconciler_InvalidExternalAuthConfigSteadyState(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"),
	)
	authConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{ClientID: "delegate"}},
			},
		},
	}
	reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, proxy, authConfig)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: proxy.Name, Namespace: proxy.Namespace}}

	result, err := reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	actual := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, actual))
	initial := actual.DeepCopy()

	result, err = reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, actual))
	assert.Equal(t, initial.Status, actual.Status)
}

// authServerRefProxyCABundleConfig returns an embedded auth server config whose
// only defect is whatever state the referenced "bundle" ConfigMap is left in.
func authServerRefProxyCABundleConfig() *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: "signing-key", Key: "private.pem"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: "hmac-secret", Key: "hmac"}},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
					Name: "upstream",
					Type: mcpv1beta1.UpstreamProviderTypeOIDC,
					OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
						IssuerURL: "https://idp.example.com",
						ClientID:  "client",
						CABundleRef: &mcpv1beta1.CABundleSource{ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "bundle"}, Key: "ca.crt",
						}},
					},
				}},
			},
		},
	}
}

// A bundle ConfigMap without the key it names is a content error no retry can
// fix, so it must be recorded on AuthServerRefValidated and not requeued.
func TestMCPRemoteProxyReconciler_AuthServerRefInvalidCABundleIsTerminal(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}
	reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, proxy, authServerRefProxyCABundleConfig(), bundle)

	result, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(proxy)})
	require.NoError(t, err, "a terminal CA bundle failure must not requeue")
	assert.Zero(t, result)

	actual := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(proxy), actual))
	assert.Equal(t, mcpv1beta1.MCPRemoteProxyPhaseFailed, actual.Status.Phase)
	condition := meta.FindStatusCondition(
		actual.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonInvalidCABundle, condition.Reason)
	assert.Nil(t, meta.FindStatusCondition(
		actual.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated),
		"a bundle reached through authServerRef must not be recorded against externalAuthConfigRef")
}

// The bundle is repaired without touching the MCPRemoteProxy or the referenced
// config, so neither generation nor the config hash changes.
func TestMCPRemoteProxyReconciler_AuthServerRefCABundleRepairRestoresTrue(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}
	reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, proxy, authServerRefProxyCABundleConfig(), bundle)

	require.Error(t, reconciler.handleAuthServerRef(t.Context(), proxy))
	broken := meta.FindStatusCondition(
		proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
	require.NotNil(t, broken)
	require.Equal(t, mcpv1beta1.ConditionReasonInvalidCABundle, broken.Reason)

	repaired := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(bundle), repaired))
	repaired.Data = map[string]string{"ca.crt": string(mapperTestCertificatePEM(t))}
	require.NoError(t, fakeClient.Update(t.Context(), repaired))

	require.NoError(t, reconciler.handleAuthServerRef(t.Context(), proxy))
	healed := meta.FindStatusCondition(
		proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
	require.NotNil(t, healed)
	assert.Equal(t, metav1.ConditionTrue, healed.Status,
		"repairing the ConfigMap must clear the CA bundle failure")
	assert.Equal(t, mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefValid, healed.Reason)
}

// A ConfigMap read that fails for reasons unrelated to its content is transient
// and must requeue rather than be painted as a terminal spec error.
func TestMCPRemoteProxyReconciler_AuthServerRefCABundleGetErrorRequeues(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(proxy, authServerRefProxyCABundleConfig(),
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}).
		WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == "bundle" {
					return apierrors.NewServiceUnavailable("apiserver is having a moment")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	reconciler := &MCPRemoteProxyReconciler{Client: fakeClient, Scheme: scheme}

	err := reconciler.handleAuthServerRef(t.Context(), proxy)
	require.Error(t, err, "a transient read failure must surface so the caller requeues")
	condition := meta.FindStatusCondition(
		proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
	if condition != nil {
		assert.NotEqual(t, mcpv1beta1.ConditionReasonInvalidCABundle, condition.Reason,
			"a transient read failure must not be recorded as a terminal bundle error")
	}
}

// An unchanged terminal failure must be re-derived to the identical condition,
// LastTransitionTime included.
func TestMCPRemoteProxyReconciler_AuthServerRefInvalidCABundleSteadyState(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}
	reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, proxy, authServerRefProxyCABundleConfig(), bundle)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(proxy)}

	_, err := reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	first := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(proxy), first))

	_, err = reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	second := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(proxy), second))

	firstCondition := meta.FindStatusCondition(
		first.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
	secondCondition := meta.FindStatusCondition(
		second.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
	require.NotNil(t, firstCondition)
	require.NotNil(t, secondCondition)
	assert.Equal(t, *firstCondition, *secondCondition,
		"re-deriving an unchanged terminal CA failure must leave the condition untouched")
}

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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleAuthServerRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		mcpServer       func() *mcpv1beta1.MCPServer
		authConfig      func() *mcpv1beta1.MCPExternalAuthConfig
		expectError     bool
		errContains     string
		expectHash      string
		conditionStatus metav1.ConditionStatus
		conditionReason string
	}{
		{
			name: "nil authServerRef removes condition and clears hash",
			mcpServer: func() *mcpv1beta1.MCPServer {
				return v1beta1test.NewMCPServer("server", "default",
					v1beta1test.WithImage("test"),
					v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{AuthServerConfigHash: "old-hash"}),
				)
			},
			expectHash: "",
		},
		{
			name: "unsupported kind sets InvalidKind condition",
			mcpServer: func() *mcpv1beta1.MCPServer {
				return v1beta1test.NewMCPServer("server", "default",
					v1beta1test.WithImage("test"),
					v1beta1test.WithAuthServerRef("Secret", "foo"),
				)
			},
			expectError:     true,
			errContains:     "unsupported authServerRef kind",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonAuthServerRefInvalidKind,
		},
		{
			name: "not found sets NotFound condition",
			mcpServer: func() *mcpv1beta1.MCPServer {
				return v1beta1test.NewMCPServer("server", "default",
					v1beta1test.WithImage("test"),
					v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "missing"),
				)
			},
			expectError:     true,
			errContains:     "not found",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonAuthServerRefNotFound,
		},
		{
			name: "wrong type sets InvalidType condition",
			mcpServer: func() *mcpv1beta1.MCPServer {
				return v1beta1test.NewMCPServer("server", "default",
					v1beta1test.WithImage("test"),
					v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "sts-config"),
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
			conditionReason: mcpv1beta1.ConditionReasonAuthServerRefInvalidType,
		},
		{
			name: "multi-upstream sets MultiUpstream condition",
			mcpServer: func() *mcpv1beta1.MCPServer {
				return v1beta1test.NewMCPServer("server", "default",
					v1beta1test.WithImage("test"),
					v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "multi"),
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
			conditionReason: mcpv1beta1.ConditionReasonAuthServerRefMultiUpstream,
		},
		{
			name: "valid ref sets Valid condition and updates hash",
			mcpServer: func() *mcpv1beta1.MCPServer {
				return v1beta1test.NewMCPServer("server", "default",
					v1beta1test.WithImage("test"),
					v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "valid"),
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
			conditionReason: mcpv1beta1.ConditionReasonAuthServerRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			scheme := testutil.NewScheme(t)

			server := tt.mcpServer()
			objs := []runtime.Object{server}
			if tt.authConfig != nil {
				objs = append(objs, tt.authConfig())
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPServer{}).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
			err := reconciler.handleAuthServerRef(ctx, server)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectHash, server.Status.AuthServerConfigHash)
			}

			cond := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
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

func TestMCPServerReconciler_handleInvalidEmbeddedAuthServerConfig(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(server).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	err := reconciler.handleInvalidEmbeddedAuthServerConfig(
		t.Context(), server,
		&ctrlutil.InvalidEmbeddedAuthServerConfigError{
			Err:    stderrors.New("invalid delegate client"),
			Source: ctrlutil.EmbeddedAuthServerConfigSourceAuthServerRef,
		},
	)
	require.NoError(t, err)

	assert.Equal(t, mcpv1beta1.MCPServerPhaseFailed, server.Status.Phase)
	assert.Equal(t, server.Generation, server.Status.ObservedGeneration)
	ready := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonNotReady, ready.Reason)
	validated := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	require.NotNil(t, validated)
	assert.Equal(t, metav1.ConditionFalse, validated.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonAuthServerRefInvalidConfig, validated.Reason)

	persisted := &mcpv1beta1.MCPServer{}
	key := types.NamespacedName{Name: server.Name, Namespace: server.Namespace}
	require.NoError(t, fakeClient.Get(t.Context(), key, persisted))
	resourceVersion := persisted.ResourceVersion
	require.NoError(t, reconciler.handleInvalidEmbeddedAuthServerConfig(t.Context(), persisted,
		&ctrlutil.InvalidEmbeddedAuthServerConfigError{
			Err:    stderrors.New("invalid delegate client"),
			Source: ctrlutil.EmbeddedAuthServerConfigSourceAuthServerRef,
		},
	))
	require.NoError(t, fakeClient.Get(t.Context(), key, persisted))
	assert.Equal(t, resourceVersion, persisted.ResourceVersion)
}

func TestMCPServerReconciler_handleInvalidEmbeddedAuthServerConfigExternalAuthConfigRef(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithExternalAuthConfigRef("external-auth"),
	)
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(server).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	err := reconciler.handleInvalidEmbeddedAuthServerConfig(t.Context(), server,
		&ctrlutil.InvalidEmbeddedAuthServerConfigError{
			Err:    stderrors.New("invalid delegate client"),
			Source: ctrlutil.EmbeddedAuthServerConfigSourceExternalAuthConfigRef,
		})
	require.NoError(t, err)

	assert.Nil(t, meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated))
	validated := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
	require.NotNil(t, validated)
	assert.Equal(t, metav1.ConditionFalse, validated.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonInvalidConfig, validated.Reason)
}

func TestMCPServerReconciler_InvalidEmbeddedAuthServerConfigSteadyState(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"),
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
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(server, authConfig).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: server.Name, Namespace: server.Namespace}}

	result, err := reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	actual := &mcpv1beta1.MCPServer{}
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, actual))
	initial := actual.DeepCopy()

	result, err = reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, actual))
	assert.Equal(t, initial.Status, actual.Status)
}

// authServerRefCABundleConfig returns an embedded auth server config whose only
// defect is whatever state the referenced "bundle" ConfigMap is left in.
func authServerRefCABundleConfig() *mcpv1beta1.MCPExternalAuthConfig {
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
func TestMCPServerReconciler_AuthServerRefInvalidCABundleIsTerminal(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(server, authServerRefCABundleConfig(), bundle).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	result, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(server)})
	require.NoError(t, err, "a terminal CA bundle failure must not requeue")
	assert.Zero(t, result)

	actual := &mcpv1beta1.MCPServer{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(server), actual))
	assert.Equal(t, mcpv1beta1.MCPServerPhaseFailed, actual.Status.Phase)
	condition := meta.FindStatusCondition(actual.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonInvalidCABundle, condition.Reason)
	assert.Nil(t,
		meta.FindStatusCondition(actual.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated),
		"a bundle reached through authServerRef must not be recorded against externalAuthConfigRef")
}

// The bundle is repaired without touching the MCPServer or the referenced
// config, so neither generation nor the config hash changes.
func TestMCPServerReconciler_AuthServerRefCABundleRepairRestoresTrue(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(server, authServerRefCABundleConfig(), bundle).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	require.Error(t, reconciler.handleAuthServerRef(t.Context(), server))
	broken := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	require.NotNil(t, broken)
	require.Equal(t, mcpv1beta1.ConditionReasonInvalidCABundle, broken.Reason)

	repaired := &corev1.ConfigMap{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(bundle), repaired))
	repaired.Data = map[string]string{"ca.crt": string(mapperTestCertificatePEM(t))}
	require.NoError(t, fakeClient.Update(t.Context(), repaired))

	require.NoError(t, reconciler.handleAuthServerRef(t.Context(), server))
	healed := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	require.NotNil(t, healed)
	assert.Equal(t, metav1.ConditionTrue, healed.Status,
		"repairing the ConfigMap must clear the CA bundle failure")
	assert.Equal(t, mcpv1beta1.ConditionReasonAuthServerRefValid, healed.Reason)
}

// A ConfigMap read that fails for reasons unrelated to its content is transient
// and must requeue rather than be painted as a terminal spec error.
func TestMCPServerReconciler_AuthServerRefCABundleGetErrorRequeues(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(server, authServerRefCABundleConfig(),
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == "bundle" {
					return apierrors.NewServiceUnavailable("apiserver is having a moment")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	err := reconciler.handleAuthServerRef(t.Context(), server)
	require.Error(t, err, "a transient read failure must surface so the caller requeues")
	condition := meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	if condition != nil {
		assert.NotEqual(t, mcpv1beta1.ConditionReasonInvalidCABundle, condition.Reason,
			"a transient read failure must not be recorded as a terminal bundle error")
	}
}

// An unchanged terminal failure must be re-derived to the identical condition,
// LastTransitionTime included. Asserted on the condition rather than the
// object's ResourceVersion: other writers in Reconcile issue unconditional
// no-op status patches, so the ResourceVersion advances regardless.
func TestMCPServerReconciler_AuthServerRefInvalidCABundleSteadyState(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"),
	)
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"}}
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(server, authServerRefCABundleConfig(), bundle).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(server)}

	_, err := reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	first := &mcpv1beta1.MCPServer{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(server), first))

	_, err = reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	second := &mcpv1beta1.MCPServer{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(server), second))

	firstCondition := meta.FindStatusCondition(first.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	secondCondition := meta.FindStatusCondition(second.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
	require.NotNil(t, firstCondition)
	require.NotNil(t, secondCondition)
	assert.Equal(t, *firstCondition, *secondCondition,
		"re-deriving an unchanged terminal CA failure must leave the condition untouched")
	assert.Equal(t, first.Status.Phase, second.Status.Phase)
}

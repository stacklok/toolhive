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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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
				return &mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
					Spec:       mcpv1beta1.MCPRemoteProxySpec{RemoteURL: "https://remote.example.com"},
					Status: mcpv1beta1.MCPRemoteProxyStatus{
						AuthServerConfigHash: "old-hash",
					},
				}
			},
			expectHash: "",
		},
		{
			name: "unsupported kind sets InvalidKind condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return &mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:     "https://remote.example.com",
						AuthServerRef: &mcpv1beta1.AuthServerRef{Kind: "Secret", Name: "foo"},
					},
				}
			},
			expectError:     true,
			errContains:     "unsupported authServerRef kind",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefInvalidKind,
		},
		{
			name: "not found sets NotFound condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return &mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:     "https://remote.example.com",
						AuthServerRef: &mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "missing"},
					},
				}
			},
			expectError:     true,
			errContains:     "not found",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefNotFound,
		},
		{
			name: "wrong type sets InvalidType condition",
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				return &mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:     "https://remote.example.com",
						AuthServerRef: &mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "sts-config"},
					},
				}
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
				return &mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:     "https://remote.example.com",
						AuthServerRef: &mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "multi"},
					},
				}
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
				return &mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:     "https://remote.example.com",
						AuthServerRef: &mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "valid"},
					},
				}
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

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			proxy := tt.proxy()
			objs := []runtime.Object{proxy}
			if tt.authConfig != nil {
				objs = append(objs, tt.authConfig())
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}
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

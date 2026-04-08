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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleAuthServerRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		mcpServer       func() *mcpv1alpha1.MCPServer
		authConfig      func() *mcpv1alpha1.MCPExternalAuthConfig
		expectError     bool
		errContains     string
		expectHash      string
		conditionStatus metav1.ConditionStatus
		conditionReason string
	}{
		{
			name: "nil authServerRef removes condition and clears hash",
			mcpServer: func() *mcpv1alpha1.MCPServer {
				return &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
					Spec:       mcpv1alpha1.MCPServerSpec{Image: "test"},
					Status: mcpv1alpha1.MCPServerStatus{
						AuthServerConfigHash: "old-hash",
					},
				}
			},
			expectHash: "",
		},
		{
			name: "unsupported kind sets InvalidKind condition",
			mcpServer: func() *mcpv1alpha1.MCPServer {
				return &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:         "test",
						AuthServerRef: &mcpv1alpha1.AuthServerRef{Kind: "Secret", Name: "foo"},
					},
				}
			},
			expectError:     true,
			errContains:     "unsupported authServerRef kind",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1alpha1.ConditionReasonAuthServerRefInvalidKind,
		},
		{
			name: "not found sets NotFound condition",
			mcpServer: func() *mcpv1alpha1.MCPServer {
				return &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:         "test",
						AuthServerRef: &mcpv1alpha1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "missing"},
					},
				}
			},
			expectError:     true,
			errContains:     "not found",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1alpha1.ConditionReasonAuthServerRefNotFound,
		},
		{
			name: "wrong type sets InvalidType condition",
			mcpServer: func() *mcpv1alpha1.MCPServer {
				return &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:         "test",
						AuthServerRef: &mcpv1alpha1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "sts-config"},
					},
				}
			},
			authConfig: func() *mcpv1alpha1.MCPExternalAuthConfig {
				return &mcpv1alpha1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "sts-config", Namespace: "default"},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeAWSSts,
						AWSSts: &mcpv1alpha1.AWSStsConfig{
							Region: "us-east-1",
						},
					},
				}
			},
			expectError:     true,
			errContains:     "only embeddedAuthServer is supported",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1alpha1.ConditionReasonAuthServerRefInvalidType,
		},
		{
			name: "multi-upstream sets MultiUpstream condition",
			mcpServer: func() *mcpv1alpha1.MCPServer {
				return &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:         "test",
						AuthServerRef: &mcpv1alpha1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "multi"},
					},
				}
			},
			authConfig: func() *mcpv1alpha1.MCPExternalAuthConfig {
				return &mcpv1alpha1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
							Issuer: "https://auth.example.com",
							UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
								{Name: "a", Type: mcpv1alpha1.UpstreamProviderTypeOIDC, OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{IssuerURL: "https://a.com", ClientID: "a"}},
								{Name: "b", Type: mcpv1alpha1.UpstreamProviderTypeOIDC, OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{IssuerURL: "https://b.com", ClientID: "b"}},
							},
						},
					},
					Status: mcpv1alpha1.MCPExternalAuthConfigStatus{ConfigHash: "multi-hash"},
				}
			},
			expectError:     true,
			errContains:     "only 1 is supported",
			conditionStatus: metav1.ConditionFalse,
			conditionReason: mcpv1alpha1.ConditionReasonAuthServerRefMultiUpstream,
		},
		{
			name: "valid ref sets Valid condition and updates hash",
			mcpServer: func() *mcpv1alpha1.MCPServer {
				return &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default"},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image:         "test",
						AuthServerRef: &mcpv1alpha1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "valid"},
					},
				}
			},
			authConfig: func() *mcpv1alpha1.MCPExternalAuthConfig {
				return &mcpv1alpha1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: "default"},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
							Issuer:                       "https://auth.example.com",
							AuthorizationEndpointBaseURL: "https://auth.example.com",
							SigningKeySecretRefs:         []mcpv1alpha1.SecretKeyRef{{Name: "key", Key: "pem"}},
							HMACSecretRefs:               []mcpv1alpha1.SecretKeyRef{{Name: "hmac", Key: "secret"}},
						},
					},
					Status: mcpv1alpha1.MCPExternalAuthConfigStatus{ConfigHash: "valid-hash"},
				}
			},
			expectHash:      "valid-hash",
			conditionStatus: metav1.ConditionTrue,
			conditionReason: mcpv1alpha1.ConditionReasonAuthServerRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			server := tt.mcpServer()
			objs := []runtime.Object{server}
			if tt.authConfig != nil {
				objs = append(objs, tt.authConfig())
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
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

			cond := meta.FindStatusCondition(server.Status.Conditions, mcpv1alpha1.ConditionTypeAuthServerRefValidated)
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

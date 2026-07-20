// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestXAAConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &XAAConverter{}
	assert.Equal(t, authtypes.StrategyTypeXAA, converter.StrategyType())
}

func TestXAAConverter_ConvertToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1beta1.MCPExternalAuthConfig
		wantStrategy *authtypes.BackendAuthStrategy
		wantErr      bool
		errContains  string
	}{
		{
			name: "full xaa config",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL: "https://idp.example.com/token",
						IDPClientID: "idp-client",
						IDPClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "idp-secret",
							Key:  "client-secret",
						},
						TargetTokenURL: "https://target.example.com/token",
						TargetClientID: "target-client",
						TargetClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "target-secret",
							Key:  "client-secret",
						},
						TargetAudience:      "https://target.example.com",
						TargetResource:      "https://mcp.example.com",
						Scopes:              []string{"openid", "profile"},
						SubjectProviderName: "github",
						SubjectTokenType:    "urn:ietf:params:oauth:token-type:id_token",
					},
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:         "https://idp.example.com/token",
					IDPClientID:         "idp-client",
					TargetTokenURL:      "https://target.example.com/token",
					TargetClientID:      "target-client",
					TargetAudience:      "https://target.example.com",
					TargetResource:      "https://mcp.example.com",
					Scopes:              []string{"openid", "profile"},
					SubjectProviderName: "github",
					SubjectTokenType:    "urn:ietf:params:oauth:token-type:id_token",
				},
			},
		},
		{
			name: "minimal required fields",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL:    "https://idp.example.com/token",
						TargetTokenURL: "https://target.example.com/token",
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:    "https://idp.example.com/token",
					TargetTokenURL: "https://target.example.com/token",
					TargetAudience: "https://target.example.com",
					TargetResource: "https://mcp.example.com",
				},
			},
		},
		{
			name: "nil xaa config",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA:  nil,
				},
			},
			wantErr:     true,
			errContains: "xaa config is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := &XAAConverter{}
			strategy, err := converter.ConvertToStrategy(tt.externalAuth)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStrategy, strategy)
		})
	}
}

func TestXAAConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		externalAuth  *mcpv1beta1.MCPExternalAuthConfig
		setupSecrets  func(client.Client) error
		inputStrategy *authtypes.BackendAuthStrategy
		wantStrategy  *authtypes.BackendAuthStrategy
		wantErr       bool
		errContains   string
	}{
		{
			name: "resolves both IdP and target secrets",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL: "https://idp.example.com/token",
						IDPClientID: "idp-client",
						IDPClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "idp-secret",
							Key:  "client-secret",
						},
						TargetTokenURL: "https://target.example.com/token",
						TargetClientID: "target-client",
						TargetClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "target-secret",
							Key:  "client-secret",
						},
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			setupSecrets: func(k8sClient client.Client) error {
				idpSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "idp-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"client-secret": []byte("idp-secret-value"),
					},
				}
				targetSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "target-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"client-secret": []byte("target-secret-value"),
					},
				}
				if err := k8sClient.Create(context.Background(), idpSecret); err != nil {
					return err
				}
				return k8sClient.Create(context.Background(), targetSecret)
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:    "https://idp.example.com/token",
					IDPClientID:    "idp-client",
					TargetTokenURL: "https://target.example.com/token",
					TargetClientID: "target-client",
					TargetAudience: "https://target.example.com",
					TargetResource: "https://mcp.example.com",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:        "https://idp.example.com/token",
					IDPClientID:        "idp-client",
					IDPClientSecret:    "idp-secret-value",
					TargetTokenURL:     "https://target.example.com/token",
					TargetClientID:     "target-client",
					TargetClientSecret: "target-secret-value",
					TargetAudience:     "https://target.example.com",
					TargetResource:     "https://mcp.example.com",
				},
			},
		},
		{
			name: "resolves only IdP secret when target has none",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL: "https://idp.example.com/token",
						IDPClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "idp-secret",
							Key:  "client-secret",
						},
						TargetTokenURL: "https://target.example.com/token",
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			setupSecrets: func(k8sClient client.Client) error {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "idp-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"client-secret": []byte("idp-secret-value"),
					},
				}
				return k8sClient.Create(context.Background(), secret)
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:    "https://idp.example.com/token",
					TargetTokenURL: "https://target.example.com/token",
					TargetAudience: "https://target.example.com",
					TargetResource: "https://mcp.example.com",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:     "https://idp.example.com/token",
					IDPClientSecret: "idp-secret-value",
					TargetTokenURL:  "https://target.example.com/token",
					TargetAudience:  "https://target.example.com",
					TargetResource:  "https://mcp.example.com",
				},
			},
		},
		{
			name: "no-op when no secret refs configured",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL:    "https://idp.example.com/token",
						TargetTokenURL: "https://target.example.com/token",
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:    "https://idp.example.com/token",
					TargetTokenURL: "https://target.example.com/token",
					TargetAudience: "https://target.example.com",
					TargetResource: "https://mcp.example.com",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA: &authtypes.XAAConfig{
					IDPTokenURL:    "https://idp.example.com/token",
					TargetTokenURL: "https://target.example.com/token",
					TargetAudience: "https://target.example.com",
					TargetResource: "https://mcp.example.com",
				},
			},
		},
		{
			name: "nil strategy",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA:  &mcpv1beta1.XAASpec{},
				},
			},
			inputStrategy: nil,
			wantErr:       true,
			errContains:   "xaa strategy is nil",
		},
		{
			name: "nil xaa config in external auth",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA:  nil,
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA:  &authtypes.XAAConfig{},
			},
			wantErr:     true,
			errContains: "xaa config is nil",
		},
		{
			name: "missing IdP secret",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL: "https://idp.example.com/token",
						IDPClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "nonexistent-secret",
							Key:  "client-secret",
						},
						TargetTokenURL: "https://target.example.com/token",
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA:  &authtypes.XAAConfig{},
			},
			wantErr:     true,
			errContains: "failed to resolve IdP client secret",
		},
		{
			name: "missing key in target secret",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeXAA,
					XAA: &mcpv1beta1.XAASpec{
						IDPTokenURL:    "https://idp.example.com/token",
						TargetTokenURL: "https://target.example.com/token",
						TargetClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "target-secret",
							Key:  "wrong-key",
						},
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			setupSecrets: func(k8sClient client.Client) error {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "target-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"client-secret": []byte("target-secret-value"),
					},
				}
				return k8sClient.Create(context.Background(), secret)
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeXAA,
				XAA:  &authtypes.XAAConfig{},
			},
			wantErr:     true,
			errContains: "failed to resolve target client secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with schemes
			scheme := runtime.NewScheme()
			_ = mcpv1beta1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			// Setup secrets if provided
			if tt.setupSecrets != nil {
				err := tt.setupSecrets(fakeClient)
				require.NoError(t, err, "failed to setup secrets")
			}

			converter := &XAAConverter{}
			strategy, err := converter.ResolveSecrets(
				context.Background(),
				tt.externalAuth,
				fakeClient,
				tt.externalAuth.Namespace,
				tt.inputStrategy,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStrategy, strategy)
		})
	}
}

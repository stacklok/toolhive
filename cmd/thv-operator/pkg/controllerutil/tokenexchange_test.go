// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	return scheme
}

func TestAddTokenExchangeConfig(t *testing.T) {
	t.Parallel()

	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"client-secret": []byte("super-secret"),
		},
	}

	tests := []struct {
		name      string
		config    *mcpv1alpha1.MCPExternalAuthConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "standard RFC 8693 (no variant) adds option",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.example.com/token",
						ClientID: "client-id",
						Audience: "https://api.example.com",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
					},
				},
			},
		},
		{
			name: "entra variant adds option",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						Variant:  "entra",
						ClientID: "entra-client-id",
						Scopes:   []string{"https://graph.microsoft.com/.default"},
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Raw: &mcpv1alpha1.TokenExchangeRawConfig{
							Parameters: map[string]string{
								"tenantId": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
							},
						},
					},
				},
			},
		},
		{
			name: "raw variant adds option",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						Variant:  "raw",
						TokenURL: "https://auth.corp.internal/token",
						ClientID: "raw-client-id",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Raw: &mcpv1alpha1.TokenExchangeRawConfig{
							GrantTypeURN: "urn:corp:custom:grant-type:delegation",
							Parameters: map[string]string{
								"delegation_scope": "read:downstream",
								"target_service":   "backend-api",
							},
						},
					},
				},
			},
		},
		{
			name: "entra variant skips NormalizeTokenType",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						Variant:          "entra",
						ClientID:         "client-id",
						SubjectTokenType: "custom_type_not_normalized",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
					},
				},
			},
			// Should NOT error even though SubjectTokenType is not a valid RFC 8693 type,
			// because variant handlers handle their own token type semantics
		},
		{
			name: "nil token exchange config returns error",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:          mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: nil,
				},
			},
			expectErr: true,
			errMsg:    "token exchange configuration is nil",
		},
		{
			name: "missing secret returns error",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.example.com/token",
						Audience: "https://api.example.com",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "nonexistent-secret",
							Key:  "client-secret",
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "failed to get client secret",
		},
		{
			name: "missing secret key returns error",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.example.com/token",
						Audience: "https://api.example.com",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "nonexistent-key",
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "is missing key",
		},
		{
			name: "standard variant invalid subject token type returns error",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:         "https://sts.example.com/token",
						Audience:         "https://api.example.com",
						SubjectTokenType: "invalid_token_type",
					},
				},
			},
			expectErr: true,
			errMsg:    "invalid subject token type",
		},
		{
			name: "no client secret ref succeeds",
			config: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.example.com/token",
						ClientID: "client-id",
						Audience: "https://api.example.com",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := newTestScheme(t)
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(testSecret).
				Build()

			var options []runner.RunConfigBuilderOption

			err := addTokenExchangeConfig(
				context.Background(),
				fakeClient,
				"default",
				tt.config,
				&options,
			)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				return
			}

			require.NoError(t, err)
			assert.Len(t, options, 1, "should append exactly one RunConfigBuilderOption")
		})
	}
}

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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestHeaderInjectionConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &HeaderInjectionConverter{}
	assert.Equal(t, "header_injection", converter.StrategyType())
}

func TestHeaderInjectionConverter_ConvertToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		wantStrategy *authtypes.BackendAuthStrategy
		wantErr      bool
		errContains  string
	}{
		{
			name: "converts header injection config to strategy",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "api-secret",
							Key:  "key",
						},
					},
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
				},
			},
			wantErr: false,
		},
		{
			name: "nil header injection config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:            mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: nil,
				},
			},
			wantErr:     true,
			errContains: "header injection config is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := &HeaderInjectionConverter{}
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

func TestHeaderInjectionConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		externalAuth  *mcpv1alpha1.MCPExternalAuthConfig
		secret        *corev1.Secret
		inputStrategy *authtypes.BackendAuthStrategy
		wantStrategy  *authtypes.BackendAuthStrategy
		wantErr       bool
		errContains   string
	}{
		{
			name: "successful secret resolution",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "api-secret",
							Key:  "key",
						},
					},
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "api-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"key": []byte("secret-value-123"),
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "secret-value-123",
				},
			},
			wantErr: false,
		},
		{
			name: "missing secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "missing-secret",
							Key:  "key",
						},
					},
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
				},
			},
			wantErr:     true,
			errContains: "failed to get secret",
		},
		{
			name: "missing key in secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "api-secret",
							Key:  "missing-key",
						},
					},
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "api-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"key": []byte("secret-value"),
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
				},
			},
			wantErr:     true,
			errContains: "does not contain key",
		},
		{
			name: "nil strategy",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:            mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: nil,
				},
			},
			inputStrategy: nil,
			wantErr:       true,
			errContains:   "header injection strategy is nil",
		},
		{
			name: "nil header injection config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:            mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: nil,
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type:            authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: nil,
			},
			wantErr:     true,
			errContains: "header injection strategy is nil",
		},
		{
			name: "nil valueSecretRef",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName:     "X-API-Key",
						ValueSecretRef: nil,
					},
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
				},
			},
			wantErr:     true,
			errContains: "valueSecretRef is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = mcpv1alpha1.AddToScheme(scheme)

			// Add secret if provided
			var objects []runtime.Object
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			converter := &HeaderInjectionConverter{}
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

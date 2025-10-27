// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// TestGenerateTokenExchangeEnvVars tests the generateTokenExchangeEnvVars function
func TestGenerateTokenExchangeEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		expectError  bool
		errContains  string
		validate     func(*testing.T, []corev1.EnvVar)
	}{
		{
			name: "no external auth config reference",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "valid token exchange config generates env var",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "oauth-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oauth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client-id",
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "oauth-secret",
							Key:  "client-secret",
						},
						Audience: "api",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				require.Len(t, envVars, 1)
				assert.Equal(t, "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET", envVars[0].Name)
				require.NotNil(t, envVars[0].ValueFrom)
				require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
				assert.Equal(t, "oauth-secret", envVars[0].ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "client-secret", envVars[0].ValueFrom.SecretKeyRef.Key)
			},
		},
		{
			name: "unsupported auth type returns empty env vars",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "unsupported-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unsupported-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "unsupported",
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "nil token exchange spec returns empty env vars",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "nil-spec-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-spec-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:          mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: nil,
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "external auth config not found returns error",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
			errContains: "failed to get MCPExternalAuthConfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.mcpServer}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			ctx := t.Context()
			envVars, err := ctrlutil.GenerateTokenExchangeEnvVars(ctx, reconciler.Client, tt.mcpServer.Namespace, tt.mcpServer.Spec.ExternalAuthConfigRef, ctrlutil.GetExternalAuthConfigByName)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, envVars)
				}
			}
		})
	}
}

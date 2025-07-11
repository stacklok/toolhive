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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize logger for tests
	logger.Initialize()
}

func TestGenerateOIDCArgs(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		configMaps   []corev1.ConfigMap
		expectedArgs []string
	}{
		{
			name: "no OIDC config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectedArgs: nil,
		},
		{
			name: "kubernetes OIDC config with defaults",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://kubernetes.default.svc",
				"--oidc-audience=toolhive",
				"--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
				"--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token",
				"--jwks-allow-private-ip",
			},
		},
		{
			name: "kubernetes OIDC config with custom values",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
							Namespace: "custom-namespace",
							Audience:  "custom-audience",
							Issuer:    "https://custom.issuer.com",
							JWKSURL:   "https://custom.issuer.com/jwks",
						},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://custom.issuer.com",
				"--oidc-audience=custom-audience",
				"--oidc-jwks-url=https://custom.issuer.com/jwks",
				"--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
				"--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token",
				"--jwks-allow-private-ip",
			},
		},
		{
			name: "inline OIDC config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://accounts.google.com",
							Audience: "my-google-client",
							JWKSURL:  "https://www.googleapis.com/oauth2/v3/certs",
						},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://accounts.google.com",
				"--oidc-audience=my-google-client",
				"--oidc-jwks-url=https://www.googleapis.com/oauth2/v3/certs",
			},
		},
		{
			name: "configmap OIDC config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
							Name: "oidc-config",
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "oidc-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"issuer":   "https://accounts.google.com",
						"audience": "my-google-client",
						"jwksUrl":  "https://www.googleapis.com/oauth2/v3/certs",
						"clientId": "my-client-id",
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://accounts.google.com",
				"--oidc-audience=my-google-client",
				"--oidc-jwks-url=https://www.googleapis.com/oauth2/v3/certs",
				"--oidc-client-id=my-client-id",
			},
		},
		{
			name: "configmap OIDC config with partial data",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
							Name: "oidc-config",
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "oidc-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"issuer":   "https://accounts.google.com",
						"audience": "my-google-client",
						// jwksUrl and clientId are missing
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://accounts.google.com",
				"--oidc-audience=my-google-client",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with configmaps
			objs := make([]runtime.Object, len(tt.configMaps))
			for i, cm := range tt.configMaps {
				objs[i] = &cm
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			args := reconciler.generateOIDCArgs(ctx, tt.mcpServer)

			assert.Equal(t, tt.expectedArgs, args)
		})
	}
}

func TestGenerateKubernetesOIDCArgs(t *testing.T) {
	t.Parallel()

	reconciler := &MCPServerReconciler{}

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		expectedArgs []string
	}{
		{
			name: "nil kubernetes config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: nil,
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://kubernetes.default.svc",
				"--oidc-audience=toolhive",
				"--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
				"--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token",
				"--jwks-allow-private-ip",
			},
		},
		{
			name: "empty kubernetes config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://kubernetes.default.svc",
				"--oidc-audience=toolhive",
				"--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
				"--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token",
				"--jwks-allow-private-ip",
			},
		},
		{
			name: "custom service account only",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://kubernetes.default.svc",
				"--oidc-audience=toolhive",
				"--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
				"--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token",
				"--jwks-allow-private-ip",
			},
		},
		{
			name: "custom namespace only",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
							Namespace: "my-namespace",
						},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://kubernetes.default.svc",
				"--oidc-audience=toolhive",
				"--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
				"--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token",
				"--jwks-allow-private-ip",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := reconciler.generateKubernetesOIDCArgs(tt.mcpServer)
			assert.Equal(t, tt.expectedArgs, args)
		})
	}
}

func TestGenerateInlineOIDCArgs(t *testing.T) {
	t.Parallel()

	reconciler := &MCPServerReconciler{}

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		expectedArgs []string
	}{
		{
			name: "nil inline config",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:   mcpv1alpha1.OIDCConfigTypeInline,
						Inline: nil,
					},
				},
			},
			expectedArgs: nil,
		},
		{
			name: "empty inline config",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:   mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{},
					},
				},
			},
			expectedArgs: nil,
		},
		{
			name: "issuer only",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer: "https://accounts.google.com",
						},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://accounts.google.com",
			},
		},
		{
			name: "all fields",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://accounts.google.com",
							Audience: "my-audience",
							JWKSURL:  "https://www.googleapis.com/oauth2/v3/certs",
						},
					},
				},
			},
			expectedArgs: []string{
				"--oidc-issuer=https://accounts.google.com",
				"--oidc-audience=my-audience",
				"--oidc-jwks-url=https://www.googleapis.com/oauth2/v3/certs",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := reconciler.generateInlineOIDCArgs(tt.mcpServer)
			assert.Equal(t, tt.expectedArgs, args)
		})
	}
}

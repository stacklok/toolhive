package oidc

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
)

func TestResolve_KubernetesType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		expected  *OIDCConfig
	}{
		{
			name: "kubernetes with defaults",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Port: 8080,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: nil, // nil config should use defaults
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:             defaultK8sIssuer,
				Audience:           defaultK8sAudience,
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				ResourceURL:        "http://test-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "kubernetes with custom values",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-server",
					Namespace: "custom-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Port: 9090,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:        mcpv1alpha1.OIDCConfigTypeKubernetes,
						ResourceURL: "https://custom-resource.example.com",
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
							Issuer:           "https://custom-issuer.example.com",
							Audience:         "custom-audience",
							JWKSURL:          "https://custom-issuer.example.com/.well-known/jwks.json",
							IntrospectionURL: "https://custom-issuer.example.com/introspect",
							UseClusterAuth:   func(b bool) *bool { return &b }(true),
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:             "https://custom-issuer.example.com",
				Audience:           "custom-audience",
				JWKSURL:            "https://custom-issuer.example.com/.well-known/jwks.json",
				IntrospectionURL:   "https://custom-issuer.example.com/introspect",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				ResourceURL:        "https://custom-resource.example.com",
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "kubernetes with UseClusterAuth false",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-cluster-auth-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Port: 8080,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
							UseClusterAuth: func(b bool) *bool { return &b }(false),
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:             defaultK8sIssuer,
				Audience:           defaultK8sAudience,
				ResourceURL:        "http://no-cluster-auth-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP: false, // Should be false when UseClusterAuth is false
				// CA bundle and token paths should be empty
				ThvCABundlePath:   "",
				JWKSAuthTokenPath: "",
			},
		},
		{
			name: "nil oidc config returns nil",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: nil,
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			config, err := resolver.Resolve(context.Background(), tt.mcpServer)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, config)
		})
	}
}

func TestResolve_ConfigMapType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		configMap *corev1.ConfigMap
		expected  *OIDCConfig
		expectErr bool
	}{
		{
			name: "configmap with all values",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Port: 8080,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
							Name: "oidc-config",
						},
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":             "https://auth.example.com",
					"audience":           "test-audience",
					"jwksUrl":            "https://auth.example.com/.well-known/jwks.json",
					"introspectionUrl":   "https://auth.example.com/introspect",
					"clientId":           "test-client",
					"clientSecret":       "test-secret",
					"thvCABundlePath":    "/etc/ssl/ca.pem",
					"jwksAuthTokenPath":  "/etc/auth/token",
					"jwksAllowPrivateIP": "true",
				},
			},
			expected: &OIDCConfig{
				Issuer:             "https://auth.example.com",
				Audience:           "test-audience",
				JWKSURL:            "https://auth.example.com/.well-known/jwks.json",
				IntrospectionURL:   "https://auth.example.com/introspect",
				ClientID:           "test-client",
				ClientSecret:       "test-secret",
				ThvCABundlePath:    "/etc/ssl/ca.pem",
				JWKSAuthTokenPath:  "/etc/auth/token",
				ResourceURL:        "http://test-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "configmap with partial values",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "partial-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:        mcpv1alpha1.OIDCConfigTypeConfigMap,
						ResourceURL: "https://custom-resource.example.com",
						ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
							Name: "partial-config",
						},
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "partial-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":             "https://partial.example.com",
					"audience":           "partial-audience",
					"jwksAllowPrivateIP": "false", // explicitly false
				},
			},
			expected: &OIDCConfig{
				Issuer:             "https://partial.example.com",
				Audience:           "partial-audience",
				ResourceURL:        "https://custom-resource.example.com",
				JWKSAllowPrivateIP: false,
			},
		},
		{
			name: "configmap not found",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
							Name: "missing-config",
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "nil configmap ref returns nil",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:      mcpv1alpha1.OIDCConfigTypeConfigMap,
						ConfigMap: nil,
					},
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with ConfigMap if provided
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = mcpv1alpha1.AddToScheme(scheme)

			var objects []runtime.Object
			if tt.configMap != nil {
				objects = append(objects, tt.configMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			resolver := NewResolver(fakeClient)
			config, err := resolver.Resolve(context.Background(), tt.mcpServer)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, config)
			}
		})
	}
}

func TestResolve_InlineType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		expected  *OIDCConfig
	}{
		{
			name: "inline with all values",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Port: 8080,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:             "https://inline.example.com",
							Audience:           "inline-audience",
							JWKSURL:            "https://inline.example.com/.well-known/jwks.json",
							IntrospectionURL:   "https://inline.example.com/introspect",
							ClientID:           "inline-client",
							ClientSecret:       "inline-secret",
							ThvCABundlePath:    "/etc/ssl/inline-ca.pem",
							JWKSAuthTokenPath:  "/etc/auth/inline-token",
							JWKSAllowPrivateIP: true,
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:             "https://inline.example.com",
				Audience:           "inline-audience",
				JWKSURL:            "https://inline.example.com/.well-known/jwks.json",
				IntrospectionURL:   "https://inline.example.com/introspect",
				ClientID:           "inline-client",
				ClientSecret:       "inline-secret",
				ThvCABundlePath:    "/etc/ssl/inline-ca.pem",
				JWKSAuthTokenPath:  "/etc/auth/inline-token",
				ResourceURL:        "http://test-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "inline with custom resource URL",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-server",
					Namespace: "custom-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Port: 9090,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:        mcpv1alpha1.OIDCConfigTypeInline,
						ResourceURL: "https://custom-resource.example.com",
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://inline.example.com",
							Audience: "inline-audience",
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://inline.example.com",
				Audience:    "inline-audience",
				ResourceURL: "https://custom-resource.example.com",
			},
		},
		{
			name: "nil inline config returns nil",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:   mcpv1alpha1.OIDCConfigTypeInline,
						Inline: nil,
					},
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			config, err := resolver.Resolve(context.Background(), tt.mcpServer)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, config)
		})
	}
}

func TestResolve_UnknownType(t *testing.T) {
	t.Parallel()

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
				Type: "unknown-type",
			},
		},
	}

	resolver := NewResolver(nil)
	config, err := resolver.Resolve(context.Background(), mcpServer)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown OIDC config type")
	assert.Nil(t, config)
}

func TestResolve_NoClientForConfigMap(t *testing.T) {
	t.Parallel()

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
					Name: "test-config",
				},
			},
		},
	}

	resolver := NewResolver(nil) // No client provided
	config, err := resolver.Resolve(context.Background(), mcpServer)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes client is required")
	assert.Nil(t, config)
}

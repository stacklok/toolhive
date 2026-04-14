// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

// testResource is a minimal OIDCConfigurable implementation for testing.
type testResource struct {
	name       string
	namespace  string
	proxyPort  int32
	oidcConfig *mcpv1alpha1.OIDCConfigRef
}

func (t *testResource) GetName() string                           { return t.name }
func (t *testResource) GetNamespace() string                      { return t.namespace }
func (t *testResource) GetProxyPort() int32                       { return t.proxyPort }
func (t *testResource) GetOIDCConfig() *mcpv1alpha1.OIDCConfigRef { return t.oidcConfig }

func TestResolve_KubernetesType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource *testResource
		expected *OIDCConfig
	}{
		{
			name: "kubernetes with defaults",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
					Kubernetes: nil, // nil config should use defaults
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
			resource: &testResource{
				name:      "custom-server",
				namespace: "custom-ns",
				proxyPort: 9090,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
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
			resource: &testResource{
				name:      "no-cluster-auth-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeKubernetes,
					Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
						UseClusterAuth: func(b bool) *bool { return &b }(false),
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
			resource: &testResource{
				name:       "test-server",
				namespace:  "test-ns",
				oidcConfig: nil,
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			config, err := resolver.Resolve(context.Background(), tt.resource)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, config)
		})
	}
}

func TestResolve_ConfigMapType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resource  *testResource
		configMap *corev1.ConfigMap
		expected  *OIDCConfig
		expectErr bool
	}{
		{
			name: "configmap with all values",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "oidc-config",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":                          "https://auth.example.com",
					"audience":                        "test-audience",
					"jwksUrl":                         "https://auth.example.com/.well-known/jwks.json",
					"introspectionUrl":                "https://auth.example.com/introspect",
					"clientId":                        "test-client",
					"clientSecret":                    "test-secret",
					"thvCABundlePath":                 "/etc/ssl/ca.pem",
					"jwksAuthTokenPath":               "/etc/auth/token",
					"jwksAllowPrivateIP":              "true",
					"protectedResourceAllowPrivateIP": "true",
				},
			},
			expected: &OIDCConfig{
				Issuer:                          "https://auth.example.com",
				Audience:                        "test-audience",
				JWKSURL:                         "https://auth.example.com/.well-known/jwks.json",
				IntrospectionURL:                "https://auth.example.com/introspect",
				ClientID:                        "test-client",
				ClientSecret:                    "test-secret",
				ThvCABundlePath:                 "/etc/ssl/ca.pem",
				JWKSAuthTokenPath:               "/etc/auth/token",
				ResourceURL:                     "http://test-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP:              true,
				ProtectedResourceAllowPrivateIP: true,
			},
		},
		{
			name: "configmap with partial values",
			resource: &testResource{
				name:      "partial-server",
				namespace: "test-ns",
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type:        mcpv1alpha1.OIDCConfigTypeConfigMap,
					ResourceURL: "https://custom-resource.example.com",
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "partial-config",
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
			name: "configmap with jwksAllowPrivateIP independent of protectedResourceAllowPrivateIP",
			resource: &testResource{
				name:      "independent-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "independent-config",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "independent-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":             "https://auth.example.com",
					"audience":           "test-audience",
					"jwksAllowPrivateIP": "true",
					// protectedResourceAllowPrivateIP intentionally absent
				},
			},
			expected: &OIDCConfig{
				Issuer:                          "https://auth.example.com",
				Audience:                        "test-audience",
				ResourceURL:                     "http://independent-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP:              true,
				ProtectedResourceAllowPrivateIP: false,
			},
		},
		{
			name: "configmap with insecureAllowHTTP enabled",
			resource: &testResource{
				name:      "insecure-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "insecure-config",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "insecure-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":             "http://localhost:8080/realms/test",
					"audience":           "test-audience",
					"jwksAllowPrivateIP": "true",
					"insecureAllowHTTP":  "true",
				},
			},
			expected: &OIDCConfig{
				Issuer:             "http://localhost:8080/realms/test",
				Audience:           "test-audience",
				ResourceURL:        "http://insecure-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP: true,
				InsecureAllowHTTP:  true,
			},
		},
		{
			name: "configmap with scopes",
			resource: &testResource{
				name:      "scopes-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "scopes-config",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "scopes-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":   "https://auth.example.com",
					"audience": "test-audience",
					"scopes":   "https://www.googleapis.com/auth/drive.readonly,https://www.googleapis.com/auth/documents.readonly",
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://auth.example.com",
				Audience:    "test-audience",
				ResourceURL: "http://scopes-server.test-ns.svc.cluster.local:8080",
				Scopes: []string{
					"https://www.googleapis.com/auth/drive.readonly",
					"https://www.googleapis.com/auth/documents.readonly",
				},
			},
		},
		{
			name: "configmap with scopes containing whitespace",
			resource: &testResource{
				name:      "whitespace-scopes-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "whitespace-scopes-config",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "whitespace-scopes-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":   "https://auth.example.com",
					"audience": "test-audience",
					"scopes":   "scope1 , scope2,  scope3  ",
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://auth.example.com",
				Audience:    "test-audience",
				ResourceURL: "http://whitespace-scopes-server.test-ns.svc.cluster.local:8080",
				Scopes:      []string{"scope1", "scope2", "scope3"},
			},
		},
		{
			name: "configmap with empty scopes",
			resource: &testResource{
				name:      "empty-scopes-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "empty-scopes-config",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-scopes-config",
					Namespace: "test-ns",
				},
				Data: map[string]string{
					"issuer":   "https://auth.example.com",
					"audience": "test-audience",
					"scopes":   "",
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://auth.example.com",
				Audience:    "test-audience",
				ResourceURL: "http://empty-scopes-server.test-ns.svc.cluster.local:8080",
				Scopes:      nil,
			},
		},
		{
			name: "configmap not found",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "missing-config",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "nil configmap ref returns nil",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type:      mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: nil,
				},
			},
			expected: nil,
		},
		{
			name: "configmap with caBundleRef",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "oidc-config",
						CABundleRef: &mcpv1alpha1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "ca-bundle"},
							},
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
					"issuer":   "https://auth.example.com",
					"audience": "test-audience",
				},
			},
			expected: &OIDCConfig{
				Issuer:          "https://auth.example.com",
				Audience:        "test-audience",
				ResourceURL:     "http://test-server.test-ns.svc.cluster.local:8080",
				ThvCABundlePath: "/config/certs/ca-bundle/ca.crt",
			},
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
			config, err := resolver.Resolve(context.Background(), tt.resource)

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
		name     string
		resource *testResource
		expected *OIDCConfig
	}{
		{
			name: "inline with all values",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:           "https://inline.example.com",
						Audience:         "inline-audience",
						JWKSURL:          "https://inline.example.com/.well-known/jwks.json",
						IntrospectionURL: "https://inline.example.com/introspect",
						ClientID:         "inline-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "inline-secret",
							Key:  "client-secret",
						},
						CABundleRef: &mcpv1alpha1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "inline-ca"},
							},
						},
						JWKSAuthTokenPath:               "/etc/auth/inline-token",
						JWKSAllowPrivateIP:              true,
						ProtectedResourceAllowPrivateIP: true,
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:                          "https://inline.example.com",
				Audience:                        "inline-audience",
				JWKSURL:                         "https://inline.example.com/.well-known/jwks.json",
				IntrospectionURL:                "https://inline.example.com/introspect",
				ClientID:                        "inline-client",
				ThvCABundlePath:                 "/config/certs/inline-ca/ca.crt",
				JWKSAuthTokenPath:               "/etc/auth/inline-token",
				ResourceURL:                     "http://test-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP:              true,
				ProtectedResourceAllowPrivateIP: true,
			},
		},
		{
			name: "inline with custom resource URL",
			resource: &testResource{
				name:      "custom-server",
				namespace: "custom-ns",
				proxyPort: 9090,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type:        mcpv1alpha1.OIDCConfigTypeInline,
					ResourceURL: "https://custom-resource.example.com",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://inline.example.com",
						Audience: "inline-audience",
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
			name: "inline with insecureAllowHTTP enabled",
			resource: &testResource{
				name:      "insecure-inline-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:             "http://localhost:8080/realms/test",
						Audience:           "test-audience",
						JWKSURL:            "http://localhost:8080/realms/test/protocol/openid-connect/certs",
						JWKSAllowPrivateIP: true,
						InsecureAllowHTTP:  true,
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:             "http://localhost:8080/realms/test",
				Audience:           "test-audience",
				JWKSURL:            "http://localhost:8080/realms/test/protocol/openid-connect/certs",
				ResourceURL:        "http://insecure-inline-server.test-ns.svc.cluster.local:8080",
				JWKSAllowPrivateIP: true,
				InsecureAllowHTTP:  true,
			},
		},
		{
			name: "inline with protectedResourceAllowPrivateIP independent of jwksAllowPrivateIP",
			resource: &testResource{
				name:      "protected-resource-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:                          "https://auth.example.com",
						Audience:                        "test-audience",
						ProtectedResourceAllowPrivateIP: true,
						JWKSAllowPrivateIP:              false,
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:                          "https://auth.example.com",
				Audience:                        "test-audience",
				ResourceURL:                     "http://protected-resource-server.test-ns.svc.cluster.local:8080",
				ProtectedResourceAllowPrivateIP: true,
				JWKSAllowPrivateIP:              false,
			},
		},
		{
			name: "inline with scopes",
			resource: &testResource{
				name:      "scopes-inline-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://auth.example.com",
						Audience: "test-audience",
						Scopes:   []string{"openid", "profile", "email"},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://auth.example.com",
				Audience:    "test-audience",
				ResourceURL: "http://scopes-inline-server.test-ns.svc.cluster.local:8080",
				Scopes:      []string{"openid", "profile", "email"},
			},
		},
		{
			name: "nil inline config returns nil",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type:   mcpv1alpha1.OIDCConfigTypeInline,
					Inline: nil,
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			config, err := resolver.Resolve(context.Background(), tt.resource)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, config)
		})
	}
}

func TestResolve_UnknownType(t *testing.T) {
	t.Parallel()

	res := &testResource{
		name:      "test-server",
		namespace: "test-ns",
		oidcConfig: &mcpv1alpha1.OIDCConfigRef{
			Type: "unknown-type",
		},
	}

	resolver := NewResolver(nil)
	config, err := resolver.Resolve(context.Background(), res)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown OIDC config type")
	assert.Nil(t, config)
}

func TestResolve_NoClientForConfigMap(t *testing.T) {
	t.Parallel()

	res := &testResource{
		name:      "test-server",
		namespace: "test-ns",
		oidcConfig: &mcpv1alpha1.OIDCConfigRef{
			Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
				Name: "test-config",
			},
		},
	}

	resolver := NewResolver(nil) // No client provided
	config, err := resolver.Resolve(context.Background(), res)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes client is required")
	assert.Nil(t, config)
}

func TestResolve_InlineWithClientSecretRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource *testResource
		expected *OIDCConfig
	}{
		{
			name: "clientSecretRef set - clientSecret should be empty",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://example.com",
						Audience: "test-audience",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "oidc-secret",
							Key:  "client-secret",
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:       "https://example.com",
				Audience:     "test-audience",
				ClientID:     "test-client",
				ClientSecret: "", // Should be empty when ClientSecretRef is set
				ResourceURL:  "http://test-server.test-ns.svc.cluster.local:8080",
			},
		},
		{
			name: "clientSecretRef with clientID - clientSecret not in resolved config",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://example.com",
						Audience: "test-audience",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "oidc-secret",
							Key:  "client-secret",
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://example.com",
				Audience:    "test-audience",
				ClientID:    "test-client",
				ResourceURL: "http://test-server.test-ns.svc.cluster.local:8080",
			},
		},
		{
			name: "no clientSecretRef - clientSecret empty in resolved config",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://example.com",
						Audience: "test-audience",
						ClientID: "test-client",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:      "https://example.com",
				Audience:    "test-audience",
				ClientID:    "test-client",
				ResourceURL: "http://test-server.test-ns.svc.cluster.local:8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			config, err := resolver.Resolve(context.Background(), tt.resource)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, config)
		})
	}
}

func TestResolve_InlineWithCABundleRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resource  *testResource
		expected  *OIDCConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid caBundleRef",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://example.com",
						Audience: "test-audience",
						CABundleRef: &mcpv1alpha1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "ca-bundle"},
							},
						},
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:          "https://example.com",
				Audience:        "test-audience",
				ResourceURL:     "http://test-server.test-ns.svc.cluster.local:8080",
				ThvCABundlePath: "/config/certs/ca-bundle/ca.crt",
			},
		},
		{
			name: "caBundleRef without configMapRef fails",
			resource: &testResource{
				name:      "test-server",
				namespace: "test-ns",
				proxyPort: 8080,
				oidcConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: mcpv1alpha1.OIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:      "https://example.com",
						Audience:    "test-audience",
						CABundleRef: &mcpv1alpha1.CABundleSource{},
					},
				},
			},
			expectErr: true,
			errMsg:    "configMapRef must be specified in caBundleRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			config, err := resolver.Resolve(context.Background(), tt.resource)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, config)
			}
		})
	}
}

func TestParseCommaSeparatedList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "single value",
			input:    "scope1",
			expected: []string{"scope1"},
		},
		{
			name:     "multiple values",
			input:    "scope1,scope2,scope3",
			expected: []string{"scope1", "scope2", "scope3"},
		},
		{
			name:     "values with whitespace",
			input:    " scope1 , scope2 , scope3 ",
			expected: []string{"scope1", "scope2", "scope3"},
		},
		{
			name:     "values with URLs",
			input:    "https://www.googleapis.com/auth/drive.readonly,https://www.googleapis.com/auth/documents.readonly",
			expected: []string{"https://www.googleapis.com/auth/drive.readonly", "https://www.googleapis.com/auth/documents.readonly"},
		},
		{
			name:     "empty values filtered out",
			input:    "scope1,,scope2,  ,scope3",
			expected: []string{"scope1", "scope2", "scope3"},
		},
		{
			name:     "only commas and whitespace",
			input:    ", , ,  ",
			expected: nil,
		},
		{
			name:     "single whitespace-only value",
			input:    "   ",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseCommaSeparatedList(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

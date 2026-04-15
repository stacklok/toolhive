// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestResolveFromConfigRef_NilInputs(t *testing.T) {
	t.Parallel()

	resolver := NewResolver(nil)

	t.Run("nil ref", func(t *testing.T) {
		t.Parallel()
		result, err := resolver.ResolveFromConfigRef(
			t.Context(), nil, &mcpv1alpha1.MCPOIDCConfig{},
			"s", "ns", 8080,
		)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("nil config", func(t *testing.T) {
		t.Parallel()
		result, err := resolver.ResolveFromConfigRef(
			t.Context(),
			&mcpv1alpha1.MCPOIDCConfigReference{Name: "x", Audience: "a"},
			nil, "s", "ns", 8080,
		)
		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestResolveFromConfigRef_KubernetesServiceAccountType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      *mcpv1alpha1.MCPOIDCConfigReference
		oidcCfg  *mcpv1alpha1.MCPOIDCConfig
		expected *OIDCConfig
	}{
		{
			name: "audience and scopes from ref with explicit issuer",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "my-aud", Scopes: []string{"openid"},
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://kubernetes.default.svc", Audience: "my-aud",
				Scopes:             []string{"openid"},
				ResourceURL:        "http://srv.default.svc.cluster.local:8080",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "empty resourceUrl falls back to derived service URL",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "my-aud", Scopes: []string{"openid"},
				ResourceURL: "",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://kubernetes.default.svc", Audience: "my-aud",
				Scopes:             []string{"openid"},
				ResourceURL:        "http://srv.default.svc.cluster.local:8080",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "nil KSA config falls back to all defaults",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "aud",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:                     mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: nil,
				},
			},
			expected: &OIDCConfig{
				Issuer: defaultK8sIssuer, Audience: "aud",
				ResourceURL:        "http://srv.default.svc.cluster.local:8080",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "explicit resourceUrl overrides derived service URL",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "my-aud", Scopes: []string{"openid"},
				ResourceURL: "https://mcp-gateway.example.com/mcp",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://kubernetes.default.svc", Audience: "my-aud",
				Scopes:             []string{"openid"},
				ResourceURL:        "https://mcp-gateway.example.com/mcp",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "empty audience preserved for controller-level override",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "", Scopes: []string{"openid"},
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://kubernetes.default.svc", Audience: "",
				Scopes:             []string{"openid"},
				ResourceURL:        "http://srv.default.svc.cluster.local:8080",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "empty audience with explicit resourceUrl preserved for controller-level override",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "",
				ResourceURL: "https://mcp-gateway.example.com/mcp",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://kubernetes.default.svc", Audience: "",
				ResourceURL:        "https://mcp-gateway.example.com/mcp",
				ThvCABundlePath:    defaultK8sCABundlePath,
				JWKSAuthTokenPath:  defaultK8sTokenPath,
				JWKSAllowPrivateIP: true,
			},
		},
		{
			name: "UseClusterAuth false omits CA and token paths",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "k", Audience: "aud",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						Issuer:         "https://custom",
						UseClusterAuth: boolPtr(false),
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://custom", Audience: "aud",
				ResourceURL: "http://srv.default.svc.cluster.local:8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			result, err := resolver.ResolveFromConfigRef(
				t.Context(), tt.ref, tt.oidcCfg,
				"srv", "default", 8080,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveFromConfigRef_InlineType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      *mcpv1alpha1.MCPOIDCConfigReference
		oidcCfg  *mcpv1alpha1.MCPOIDCConfig
		expected *OIDCConfig
	}{
		{
			name: "audience and scopes from ref with shared inline config",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "i", Audience: "inline-aud", Scopes: []string{"openid", "email"},
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "gid",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://accounts.google.com", Audience: "inline-aud",
				ClientID:    "gid",
				ResourceURL: "http://srv.default.svc.cluster.local:8080",
				Scopes:      []string{"openid", "email"},
			},
		},
		{
			name: "protectedResourceAllowPrivateIP propagated from shared inline config",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "i", Audience: "inline-aud",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:                          "https://accounts.google.com",
						ClientID:                        "gid",
						ProtectedResourceAllowPrivateIP: true,
						JWKSAllowPrivateIP:              false,
					},
				},
			},
			expected: &OIDCConfig{
				Issuer:                          "https://accounts.google.com",
				Audience:                        "inline-aud",
				ClientID:                        "gid",
				ResourceURL:                     "http://srv.default.svc.cluster.local:8080",
				ProtectedResourceAllowPrivateIP: true,
				JWKSAllowPrivateIP:              false,
			},
		},
		{
			name: "explicit resourceUrl overrides derived service URL for inline config",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "i", Audience: "inline-aud", Scopes: []string{"openid"},
				ResourceURL: "https://mcp.corp.internal/tools",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "gid",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://accounts.google.com", Audience: "inline-aud",
				ClientID:    "gid",
				ResourceURL: "https://mcp.corp.internal/tools",
				Scopes:      []string{"openid"},
			},
		},
		{
			name: "empty audience preserved for controller-level override",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "i", Audience: "", Scopes: []string{"openid"},
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "gid",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://accounts.google.com", Audience: "",
				ClientID:    "gid",
				ResourceURL: "http://srv.default.svc.cluster.local:8080",
				Scopes:      []string{"openid"},
			},
		},
		{
			name: "empty audience with explicit resourceUrl preserved for controller-level override",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "i", Audience: "",
				ResourceURL: "https://mcp.corp.internal/tools",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "gid",
					},
				},
			},
			expected: &OIDCConfig{
				Issuer: "https://accounts.google.com", Audience: "",
				ClientID:    "gid",
				ResourceURL: "https://mcp.corp.internal/tools",
			},
		},
		{
			name: "nil inline config returns nil",
			ref: &mcpv1alpha1.MCPOIDCConfigReference{
				Name: "i", Audience: "aud",
			},
			oidcCfg: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline, Inline: nil,
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewResolver(nil)
			result, err := resolver.ResolveFromConfigRef(
				t.Context(), tt.ref, tt.oidcCfg,
				"srv", "default", 8080,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveFromConfigRef_UnknownType(t *testing.T) {
	t.Parallel()

	resolver := NewResolver(nil)
	result, err := resolver.ResolveFromConfigRef(
		t.Context(),
		&mcpv1alpha1.MCPOIDCConfigReference{Name: "x", Audience: "a"},
		&mcpv1alpha1.MCPOIDCConfig{
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{Type: "bad"},
		},
		"srv", "default", 8080,
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown MCPOIDCConfig type")
	assert.Nil(t, result)
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}

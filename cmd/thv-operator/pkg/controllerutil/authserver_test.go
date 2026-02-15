// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sptr "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/authserver"
	authrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestGenerateAuthServerVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		authConfig       *mcpv1alpha1.EmbeddedAuthServerConfig
		wantVolumes      int
		wantMounts       int
		wantSigningKeys  int
		wantHMACSecrets  int
		checkVolumePerms bool
		expectedPerm     int32
	}{
		{
			name:        "nil config returns empty slices",
			authConfig:  nil,
			wantVolumes: 0,
			wantMounts:  0,
		},
		{
			name: "single signing key and single HMAC secret",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key-secret", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			wantVolumes:      2,
			wantMounts:       2,
			wantSigningKeys:  1,
			wantHMACSecrets:  1,
			checkVolumePerms: true,
			expectedPerm:     0400,
		},
		{
			name: "multiple signing keys for rotation",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key-1", Key: "private.pem"},
					{Name: "signing-key-2", Key: "private.pem"},
					{Name: "signing-key-3", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			wantVolumes:      4, // 3 signing keys + 1 HMAC
			wantMounts:       4,
			wantSigningKeys:  3,
			wantHMACSecrets:  1,
			checkVolumePerms: true,
			expectedPerm:     0400,
		},
		{
			name: "multiple HMAC secrets for rotation",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret-1", Key: "hmac"},
					{Name: "hmac-secret-2", Key: "hmac"},
				},
			},
			wantVolumes:      3, // 1 signing key + 2 HMAC
			wantMounts:       3,
			wantSigningKeys:  1,
			wantHMACSecrets:  2,
			checkVolumePerms: true,
			expectedPerm:     0400,
		},
		{
			name: "empty signing keys list",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:               "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			wantVolumes:     1, // 0 signing keys + 1 HMAC
			wantMounts:      1,
			wantSigningKeys: 0,
			wantHMACSecrets: 1,
		},
		{
			name: "empty HMAC secrets list",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{},
			},
			wantVolumes:     1, // 1 signing key + 0 HMAC
			wantMounts:      1,
			wantSigningKeys: 1,
			wantHMACSecrets: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts := GenerateAuthServerVolumes(tt.authConfig)

			assert.Len(t, volumes, tt.wantVolumes)
			assert.Len(t, mounts, tt.wantMounts)

			if tt.wantVolumes == 0 {
				return
			}

			// Count signing key and HMAC volumes
			signingKeyCount := 0
			hmacSecretCount := 0
			for _, vol := range volumes {
				if len(vol.Name) > len(AuthServerKeysVolumePrefix) &&
					vol.Name[:len(AuthServerKeysVolumePrefix)] == AuthServerKeysVolumePrefix {
					signingKeyCount++
				}
				if len(vol.Name) > len(AuthServerHMACVolumePrefix) &&
					vol.Name[:len(AuthServerHMACVolumePrefix)] == AuthServerHMACVolumePrefix {
					hmacSecretCount++
				}
			}
			assert.Equal(t, tt.wantSigningKeys, signingKeyCount, "signing key volume count mismatch")
			assert.Equal(t, tt.wantHMACSecrets, hmacSecretCount, "HMAC secret volume count mismatch")

			// Check volume permissions
			if tt.checkVolumePerms {
				for _, vol := range volumes {
					require.NotNil(t, vol.Secret, "volume %s should be a secret volume", vol.Name)
					require.NotNil(t, vol.Secret.DefaultMode, "volume %s should have a default mode", vol.Name)
					assert.Equal(t, tt.expectedPerm, *vol.Secret.DefaultMode,
						"volume %s should have 0400 permissions", vol.Name)
				}
			}

			// Check mount paths
			for _, mount := range mounts {
				assert.True(t, mount.ReadOnly, "mount %s should be read-only", mount.Name)
				// Check signing key mounts
				if len(mount.Name) > len(AuthServerKeysVolumePrefix) &&
					mount.Name[:len(AuthServerKeysVolumePrefix)] == AuthServerKeysVolumePrefix {
					assert.Contains(t, mount.MountPath, AuthServerKeysMountPath,
						"signing key mount should be under keys directory")
				}
				// Check HMAC mounts
				if len(mount.Name) > len(AuthServerHMACVolumePrefix) &&
					mount.Name[:len(AuthServerHMACVolumePrefix)] == AuthServerHMACVolumePrefix {
					assert.Contains(t, mount.MountPath, AuthServerHMACMountPath,
						"HMAC mount should be under hmac directory")
				}
			}
		})
	}
}

func TestGenerateAuthServerEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authConfig *mcpv1alpha1.EmbeddedAuthServerConfig
		wantEnvVar bool
		wantName   string
	}{
		{
			name:       "nil config returns empty slice",
			authConfig: nil,
			wantEnvVar: false,
		},
		{
			name: "no upstream providers returns empty slice",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{},
			},
			wantEnvVar: false,
		},
		{
			name: "OIDC provider with client secret ref",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://auth.example.com/callback",
							ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "oidc-client-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
			wantEnvVar: true,
			wantName:   UpstreamClientSecretEnvVar,
		},
		{
			name: "OIDC provider without client secret ref (public client)",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://auth.example.com/callback",
							// No ClientSecretRef - public client using PKCE
						},
					},
				},
			},
			wantEnvVar: false,
		},
		{
			name: "OAuth2 provider with client secret ref",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1alpha1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1alpha1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							RedirectURI:           "https://auth.example.com/callback",
							ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "github-client-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
			wantEnvVar: true,
			wantName:   UpstreamClientSecretEnvVar,
		},
		{
			name: "OAuth2 provider without client secret ref",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1alpha1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1alpha1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							RedirectURI:           "https://auth.example.com/callback",
							// No ClientSecretRef
						},
					},
				},
			},
			wantEnvVar: false,
		},
		{
			name: "upstream provider with nil OIDCConfig",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name:       "test",
						Type:       mcpv1alpha1.UpstreamProviderTypeOIDC,
						OIDCConfig: nil, // Nil config
					},
				},
			},
			wantEnvVar: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envVars := GenerateAuthServerEnvVars(tt.authConfig)

			if !tt.wantEnvVar {
				assert.Empty(t, envVars)
				return
			}

			require.Len(t, envVars, 1)
			assert.Equal(t, tt.wantName, envVars[0].Name)
			require.NotNil(t, envVars[0].ValueFrom)
			require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
		})
	}
}

func TestGenerateAuthServerConfig(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		externalAuthRef *mcpv1alpha1.ExternalAuthConfigRef
		externalAuthCfg *mcpv1alpha1.MCPExternalAuthConfig
		wantVolumes     bool
		wantMounts      bool
		wantEnvVars     bool
		wantErr         bool
		errContains     string
	}{
		{
			name:            "nil external auth ref returns empty slices",
			externalAuthRef: nil,
			wantVolumes:     false,
			wantMounts:      false,
			wantEnvVars:     false,
			wantErr:         false,
		},
		{
			name: "non-embeddedAuthServer type returns empty slices",
			externalAuthRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "token-exchange-config",
			},
			externalAuthCfg: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "token-exchange-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://token.example.com/exchange",
						Audience: "my-audience",
					},
				},
			},
			wantVolumes: false,
			wantMounts:  false,
			wantEnvVars: false,
			wantErr:     false,
		},
		{
			name: "embeddedAuthServer type with valid config",
			externalAuthRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "embedded-auth-config",
			},
			externalAuthCfg: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "signing-key", Key: "private.pem"},
						},
						HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "hmac-secret", Key: "hmac"},
						},
						UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
							{
								Name: "okta",
								Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
									ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
										Name: "oidc-client-secret",
										Key:  "client-secret",
									},
								},
							},
						},
					},
				},
			},
			wantVolumes: true,
			wantMounts:  true,
			wantEnvVars: true,
			wantErr:     false,
		},
		{
			name: "embeddedAuthServer type with nil embedded config",
			externalAuthRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "bad-auth-config",
			},
			externalAuthCfg: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:               mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: nil, // Missing embedded config
				},
			},
			wantVolumes: false,
			wantMounts:  false,
			wantEnvVars: false,
			wantErr:     true,
			errContains: "embedded auth server configuration is nil",
		},
		{
			name: "non-existent external auth config",
			externalAuthRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "non-existent",
			},
			externalAuthCfg: nil, // No config to create
			wantVolumes:     false,
			wantMounts:      false,
			wantEnvVars:     false,
			wantErr:         true,
			errContains:     "failed to get MCPExternalAuthConfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Build fake client
			objects := []runtime.Object{}
			if tt.externalAuthCfg != nil {
				objects = append(objects, tt.externalAuthCfg)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			ctx := context.Background()
			volumes, mounts, envVars, err := GenerateAuthServerConfig(
				ctx, fakeClient, "default", tt.externalAuthRef,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)

			if tt.wantVolumes {
				assert.NotEmpty(t, volumes)
			} else {
				assert.Empty(t, volumes)
			}

			if tt.wantMounts {
				assert.NotEmpty(t, mounts)
			} else {
				assert.Empty(t, mounts)
			}

			if tt.wantEnvVars {
				assert.NotEmpty(t, envVars)
			} else {
				assert.Empty(t, envVars)
			}
		})
	}
}

func TestBuildEmbeddedAuthServerRunnerConfig(t *testing.T) {
	t.Parallel()

	// Default OIDC config used for most tests
	defaultOIDCConfig := &oidc.OIDCConfig{
		ResourceURL: "http://test-server.default.svc.cluster.local:8080",
		Scopes:      []string{"openid", "offline_access"},
	}

	tests := []struct {
		name       string
		authConfig *mcpv1alpha1.EmbeddedAuthServerConfig
		oidcConfig *oidc.OIDCConfig
		checkFunc  func(t *testing.T, config *authserver.RunConfig)
	}{
		{
			name: "basic config with allowed audiences and scopes from OIDC config",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			oidcConfig: defaultOIDCConfig,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				assert.Equal(t, authserver.CurrentSchemaVersion, config.SchemaVersion)
				assert.Equal(t, "https://auth.example.com", config.Issuer)
				require.NotNil(t, config.SigningKeyConfig)
				assert.Equal(t, AuthServerKeysMountPath, config.SigningKeyConfig.KeyDir)
				assert.Contains(t, config.SigningKeyConfig.SigningKeyFile, "key-0.pem")
				assert.Len(t, config.HMACSecretFiles, 1)
				// Verify AllowedAudiences and ScopesSupported from OIDC config
				assert.Equal(t, []string{"http://test-server.default.svc.cluster.local:8080"}, config.AllowedAudiences)
				assert.Equal(t, []string{"openid", "offline_access"}, config.ScopesSupported)
			},
		},
		{
			name: "multiple signing keys for rotation",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key-1", Key: "private.pem"},
					{Name: "signing-key-2", Key: "private.pem"},
					{Name: "signing-key-3", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			oidcConfig: defaultOIDCConfig,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.NotNil(t, config.SigningKeyConfig)
				assert.Contains(t, config.SigningKeyConfig.SigningKeyFile, "key-0.pem")
				assert.Len(t, config.SigningKeyConfig.FallbackKeyFiles, 2)
				assert.Contains(t, config.SigningKeyConfig.FallbackKeyFiles[0], "key-1.pem")
				assert.Contains(t, config.SigningKeyConfig.FallbackKeyFiles[1], "key-2.pem")
			},
		},
		{
			name: "with token lifespans",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				TokenLifespans: &mcpv1alpha1.TokenLifespanConfig{
					AccessTokenLifespan:  "30m",
					RefreshTokenLifespan: "168h",
					AuthCodeLifespan:     "5m",
				},
			},
			oidcConfig: defaultOIDCConfig,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.NotNil(t, config.TokenLifespans)
				assert.Equal(t, "30m", config.TokenLifespans.AccessTokenLifespan)
				assert.Equal(t, "168h", config.TokenLifespans.RefreshTokenLifespan)
				assert.Equal(t, "5m", config.TokenLifespans.AuthCodeLifespan)
			},
		},
		{
			name: "with OIDC upstream provider",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://auth.example.com/callback",
							Scopes:      []string{"openid", "profile"},
						},
					},
				},
			},
			oidcConfig: defaultOIDCConfig,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				upstream := config.Upstreams[0]
				assert.Equal(t, "okta", upstream.Name)
				assert.Equal(t, authserver.UpstreamProviderTypeOIDC, upstream.Type)
				require.NotNil(t, upstream.OIDCConfig)
				assert.Equal(t, "https://okta.example.com", upstream.OIDCConfig.IssuerURL)
				assert.Equal(t, "client-id", upstream.OIDCConfig.ClientID)
				assert.Equal(t, []string{"openid", "profile"}, upstream.OIDCConfig.Scopes)
			},
		},
		{
			name: "with OAuth2 upstream provider with userinfo config",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1alpha1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1alpha1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							RedirectURI:           "https://auth.example.com/callback",
							UserInfo: &mcpv1alpha1.UserInfoConfig{
								EndpointURL: "https://api.github.com/user",
								HTTPMethod:  "GET",
								AdditionalHeaders: map[string]string{
									"Accept": "application/vnd.github.v3+json",
								},
								FieldMapping: &mcpv1alpha1.UserInfoFieldMapping{
									SubjectFields: []string{"id", "login"},
									NameFields:    []string{"name", "login"},
									EmailFields:   []string{"email"},
								},
							},
						},
					},
				},
			},
			oidcConfig: defaultOIDCConfig,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				upstream := config.Upstreams[0]
				assert.Equal(t, "github", upstream.Name)
				assert.Equal(t, authserver.UpstreamProviderTypeOAuth2, upstream.Type)
				require.NotNil(t, upstream.OAuth2Config)
				assert.Equal(t, "https://github.com/login/oauth/authorize",
					upstream.OAuth2Config.AuthorizationEndpoint)
				require.NotNil(t, upstream.OAuth2Config.UserInfo)
				assert.Equal(t, "https://api.github.com/user",
					upstream.OAuth2Config.UserInfo.EndpointURL)
				assert.Equal(t, "GET", upstream.OAuth2Config.UserInfo.HTTPMethod)
				require.NotNil(t, upstream.OAuth2Config.UserInfo.FieldMapping)
				assert.Equal(t, []string{"id", "login"},
					upstream.OAuth2Config.UserInfo.FieldMapping.SubjectFields)
			},
		},
		{
			name: "with nil scopes uses auth server defaults",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "http://my-service.ns.svc.cluster.local:8080",
				Scopes:      nil, // nil scopes should be passed through
			},
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				assert.Equal(t, []string{"http://my-service.ns.svc.cluster.local:8080"}, config.AllowedAudiences)
				assert.Nil(t, config.ScopesSupported, "nil scopes should be passed through to use auth server defaults")
			},
		},
		{
			name: "with custom scopes from OIDC config",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "http://custom-service.ns.svc.cluster.local:9000",
				Scopes:      []string{"openid", "profile", "email", "custom:scope"},
			},
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				assert.Equal(t, []string{"http://custom-service.ns.svc.cluster.local:9000"}, config.AllowedAudiences)
				assert.Equal(t, []string{"openid", "profile", "email", "custom:scope"}, config.ScopesSupported)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			ctx := context.Background()
			config, err := buildEmbeddedAuthServerRunnerConfig(ctx, fakeClient, "default", "test-server", tt.authConfig, tt.oidcConfig)

			require.NoError(t, err)
			require.NotNil(t, config)
			tt.checkFunc(t, config)
		})
	}
}

func TestAddEmbeddedAuthServerConfigOptions_Validation(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	// Helper function to create a fresh external auth config for each test
	// This avoids data races when running subtests in parallel
	newExternalAuthConfig := func() *mcpv1alpha1.MCPExternalAuthConfig {
		return &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "embedded-auth-config",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
					Issuer: "https://auth.example.com",
					SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
						{Name: "signing-key", Key: "private.pem"},
					},
					HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
						{Name: "hmac-secret", Key: "hmac"},
					},
				},
			},
		}
	}

	tests := []struct {
		name        string
		oidcConfig  *oidc.OIDCConfig
		expectError bool
		errContains string
	}{
		{
			name:        "nil OIDC config returns error",
			oidcConfig:  nil,
			expectError: true,
			errContains: "OIDC config is required for embedded auth server",
		},
		{
			name: "empty ResourceURL returns error",
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "",
				Scopes:      []string{"openid"},
			},
			expectError: true,
			errContains: "OIDC config resourceUrl is required for embedded auth server",
		},
		{
			name: "valid OIDC config succeeds",
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      []string{"openid", "offline_access"},
			},
			expectError: false,
		},
		{
			name: "valid OIDC config with nil scopes succeeds",
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      nil,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(newExternalAuthConfig()).
				Build()

			ctx := context.Background()
			var options []runner.RunConfigBuilderOption

			err := AddEmbeddedAuthServerConfigOptions(
				ctx, fakeClient, "default", "test-server",
				&mcpv1alpha1.ExternalAuthConfigRef{Name: "embedded-auth-config"},
				tt.oidcConfig,
				&options,
			)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Len(t, options, 1, "Should have one embedded auth server config option")
			}
		})
	}
}

func TestVolumePathPatterns(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1alpha1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
			{Name: "key-0", Key: "private.pem"},
			{Name: "key-1", Key: "private.pem"},
		},
		HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
			{Name: "hmac-0", Key: "hmac"},
			{Name: "hmac-1", Key: "hmac"},
		},
	}

	volumes, mounts := GenerateAuthServerVolumes(authConfig)

	require.Len(t, volumes, 4)
	require.Len(t, mounts, 4)

	// Check signing key paths follow pattern
	assert.Equal(t, "/etc/toolhive/authserver/keys/key-0.pem", mounts[0].MountPath)
	assert.Equal(t, "/etc/toolhive/authserver/keys/key-1.pem", mounts[1].MountPath)

	// Check HMAC paths follow pattern
	assert.Equal(t, "/etc/toolhive/authserver/hmac/hmac-0", mounts[2].MountPath)
	assert.Equal(t, "/etc/toolhive/authserver/hmac/hmac-1", mounts[3].MountPath)
}

func TestGenerateAuthServerEnvVars_RedisCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		authConfig     *mcpv1alpha1.EmbeddedAuthServerConfig
		wantEnvVarLen  int
		wantRedisUser  bool
		wantRedisPass  bool
		wantUpstreamCS bool
	}{
		{
			name: "Redis storage with ACL credentials generates env vars",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{},
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeRedis,
					Redis: &mcpv1alpha1.RedisStorageConfig{
						SentinelConfig: &mcpv1alpha1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel:26379"},
						},
						ACLUserConfig: &mcpv1alpha1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "redis-creds",
								Key:  "username",
							},
							PasswordSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "redis-creds",
								Key:  "password",
							},
						},
					},
				},
			},
			wantEnvVarLen: 2,
			wantRedisUser: true,
			wantRedisPass: true,
		},
		{
			name: "Redis storage with upstream client secret generates all env vars",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
							IssuerURL: "https://okta.example.com",
							ClientID:  "client-id",
							ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "oidc-secret",
								Key:  "client-secret",
							},
						},
					},
				},
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeRedis,
					Redis: &mcpv1alpha1.RedisStorageConfig{
						SentinelConfig: &mcpv1alpha1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel:26379"},
						},
						ACLUserConfig: &mcpv1alpha1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "redis-creds",
								Key:  "username",
							},
							PasswordSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: "redis-creds",
								Key:  "password",
							},
						},
					},
				},
			},
			wantEnvVarLen:  3,
			wantRedisUser:  true,
			wantRedisPass:  true,
			wantUpstreamCS: true,
		},
		{
			name: "memory storage does not generate Redis env vars",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{},
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeMemory,
				},
			},
			wantEnvVarLen: 0,
		},
		{
			name: "nil storage does not generate Redis env vars",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{},
			},
			wantEnvVarLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envVars := GenerateAuthServerEnvVars(tt.authConfig)
			assert.Len(t, envVars, tt.wantEnvVarLen)

			envMap := make(map[string]corev1.EnvVar)
			for _, ev := range envVars {
				envMap[ev.Name] = ev
			}

			if tt.wantRedisUser {
				ev, ok := envMap[authrunner.RedisUsernameEnvVar]
				assert.True(t, ok, "expected Redis username env var")
				if ok {
					require.NotNil(t, ev.ValueFrom)
					require.NotNil(t, ev.ValueFrom.SecretKeyRef)
					assert.Equal(t, "redis-creds", ev.ValueFrom.SecretKeyRef.Name)
					assert.Equal(t, "username", ev.ValueFrom.SecretKeyRef.Key)
				}
			}

			if tt.wantRedisPass {
				ev, ok := envMap[authrunner.RedisPasswordEnvVar]
				assert.True(t, ok, "expected Redis password env var")
				if ok {
					require.NotNil(t, ev.ValueFrom)
					require.NotNil(t, ev.ValueFrom.SecretKeyRef)
					assert.Equal(t, "redis-creds", ev.ValueFrom.SecretKeyRef.Name)
					assert.Equal(t, "password", ev.ValueFrom.SecretKeyRef.Key)
				}
			}

			if tt.wantUpstreamCS {
				_, ok := envMap[UpstreamClientSecretEnvVar]
				assert.True(t, ok, "expected upstream client secret env var")
			}
		})
	}
}

func TestResolveSentinelAddrs(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, discoveryv1.AddToScheme(scheme))

	// Helper to create an EndpointSlice for a given service
	newEndpointSlice := func(name, namespace, serviceName string, ips []string) *discoveryv1.EndpointSlice {
		var endpoints []discoveryv1.Endpoint
		for _, ip := range ips {
			endpoints = append(endpoints, discoveryv1.Endpoint{
				Addresses:  []string{ip},
				Conditions: discoveryv1.EndpointConditions{Ready: k8sptr.To(true)},
			})
		}
		return &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{discoveryv1.LabelServiceName: serviceName},
			},
			Endpoints: endpoints,
		}
	}

	tests := []struct {
		name      string
		sentinel  *mcpv1alpha1.RedisSentinelConfig
		objects   []runtime.Object
		wantAddrs []string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "static addresses returned directly",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"10.0.0.1:26379", "10.0.0.2:26379"},
			},
			wantAddrs: []string{"10.0.0.1:26379", "10.0.0.2:26379"},
		},
		{
			name: "service discovery resolves endpoints",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1alpha1.SentinelServiceRef{
					Name: "redis-sentinel",
					Port: 26379,
				},
			},
			objects: []runtime.Object{
				newEndpointSlice("redis-sentinel-abc", "default", "redis-sentinel",
					[]string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}),
			},
			wantAddrs: []string{"10.0.0.1:26379", "10.0.0.2:26379", "10.0.0.3:26379"},
		},
		{
			name: "service discovery with default port",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1alpha1.SentinelServiceRef{
					Name: "redis-sentinel",
				},
			},
			objects: []runtime.Object{
				newEndpointSlice("redis-sentinel-abc", "default", "redis-sentinel",
					[]string{"10.0.0.1"}),
			},
			wantAddrs: []string{"10.0.0.1:26379"},
		},
		{
			name: "service discovery with custom namespace",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1alpha1.SentinelServiceRef{
					Name:      "redis-sentinel",
					Namespace: "redis-ns",
					Port:      26379,
				},
			},
			objects: []runtime.Object{
				newEndpointSlice("redis-sentinel-abc", "redis-ns", "redis-sentinel",
					[]string{"10.0.0.1"}),
			},
			wantAddrs: []string{"10.0.0.1:26379"},
		},
		{
			name: "no ready endpoints returns error",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1alpha1.SentinelServiceRef{
					Name: "redis-sentinel",
					Port: 26379,
				},
			},
			objects: []runtime.Object{
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "redis-sentinel-abc",
						Namespace: "default",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "redis-sentinel"},
					},
					Endpoints: []discoveryv1.Endpoint{},
				},
			},
			wantErr: true,
			errMsg:  "no ready addresses found",
		},
		{
			name: "neither addrs nor service returns error",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName: "mymaster",
			},
			wantErr: true,
			errMsg:  "either sentinelAddrs or sentinelService must be specified",
		},
		{
			name: "no EndpointSlices found returns error",
			sentinel: &mcpv1alpha1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1alpha1.SentinelServiceRef{
					Name: "non-existent",
					Port: 26379,
				},
			},
			wantErr: true,
			errMsg:  "no ready addresses found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.objects...).
				Build()

			ctx := context.Background()
			addrs, err := resolveSentinelAddrs(ctx, fakeClient, tt.sentinel, "default")

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantAddrs, addrs)
		})
	}
}

func TestBuildStorageRunConfig(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, discoveryv1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	tests := []struct {
		name        string
		authConfig  *mcpv1alpha1.EmbeddedAuthServerConfig
		objects     []runtime.Object
		wantNil     bool
		wantErr     bool
		errContains string
		checkFunc   func(t *testing.T, cfg *storage.RunConfig)
	}{
		{
			name: "nil storage returns nil (memory default)",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
			},
			wantNil: true,
		},
		{
			name: "memory storage returns nil",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeMemory,
				},
			},
			wantNil: true,
		},
		{
			name: "Redis storage with static addrs builds correctly",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeRedis,
					Redis: &mcpv1alpha1.RedisStorageConfig{
						SentinelConfig: &mcpv1alpha1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"10.0.0.1:26379"},
							DB:            2,
						},
						ACLUserConfig: &mcpv1alpha1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "s", Key: "p"},
						},
						DialTimeout:  "10s",
						ReadTimeout:  "5s",
						WriteTimeout: "5s",
					},
				},
			},
			checkFunc: func(t *testing.T, cfg *storage.RunConfig) {
				t.Helper()
				assert.Equal(t, string(storage.TypeRedis), cfg.Type)
				require.NotNil(t, cfg.RedisConfig)
				require.NotNil(t, cfg.RedisConfig.SentinelConfig)
				assert.Equal(t, "mymaster", cfg.RedisConfig.SentinelConfig.MasterName)
				assert.Equal(t, []string{"10.0.0.1:26379"}, cfg.RedisConfig.SentinelConfig.SentinelAddrs)
				assert.Equal(t, 2, cfg.RedisConfig.SentinelConfig.DB)
				assert.Equal(t, "aclUser", cfg.RedisConfig.AuthType)
				require.NotNil(t, cfg.RedisConfig.ACLUserConfig)
				assert.Equal(t, authrunner.RedisUsernameEnvVar, cfg.RedisConfig.ACLUserConfig.UsernameEnvVar)
				assert.Equal(t, authrunner.RedisPasswordEnvVar, cfg.RedisConfig.ACLUserConfig.PasswordEnvVar)
				assert.Equal(t, "10s", cfg.RedisConfig.DialTimeout)
				assert.Equal(t, "5s", cfg.RedisConfig.ReadTimeout)
				assert.Equal(t, "5s", cfg.RedisConfig.WriteTimeout)
				assert.Equal(t, "thv:auth:default:test-server:", cfg.RedisConfig.KeyPrefix)
			},
		},
		{
			name: "Redis storage with service discovery",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeRedis,
					Redis: &mcpv1alpha1.RedisStorageConfig{
						SentinelConfig: &mcpv1alpha1.RedisSentinelConfig{
							MasterName: "mymaster",
							SentinelService: &mcpv1alpha1.SentinelServiceRef{
								Name: "redis-sentinel",
								Port: 26379,
							},
						},
						ACLUserConfig: &mcpv1alpha1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			objects: []runtime.Object{
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "redis-sentinel-abc",
						Namespace: "default",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "redis-sentinel"},
					},
					Endpoints: []discoveryv1.Endpoint{
						{
							Addresses:  []string{"10.0.0.1"},
							Conditions: discoveryv1.EndpointConditions{Ready: k8sptr.To(true)},
						},
					},
				},
			},
			checkFunc: func(t *testing.T, cfg *storage.RunConfig) {
				t.Helper()
				assert.Equal(t, []string{"10.0.0.1:26379"}, cfg.RedisConfig.SentinelConfig.SentinelAddrs)
			},
		},
		{
			name: "Redis storage without redis config returns error",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeRedis,
				},
			},
			wantErr:     true,
			errContains: "redis config is required",
		},
		{
			name: "Redis storage without sentinel config returns error",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1alpha1.AuthServerStorageConfig{
					Type: mcpv1alpha1.AuthServerStorageTypeRedis,
					Redis: &mcpv1alpha1.RedisStorageConfig{
						ACLUserConfig: &mcpv1alpha1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "sentinel config is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.objects...).
				Build()

			ctx := context.Background()
			cfg, err := buildStorageRunConfig(ctx, fakeClient, "default", "test-server", tt.authConfig)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, cfg)
				return
			}

			require.NotNil(t, cfg)
			if tt.checkFunc != nil {
				tt.checkFunc(t, cfg)
			}
		})
	}
}

func TestBuildEmbeddedAuthServerRunnerConfig_WithRedisStorage(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	authConfig := &mcpv1alpha1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
			{Name: "signing-key", Key: "private.pem"},
		},
		HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
			{Name: "hmac-secret", Key: "hmac"},
		},
		Storage: &mcpv1alpha1.AuthServerStorageConfig{
			Type: mcpv1alpha1.AuthServerStorageTypeRedis,
			Redis: &mcpv1alpha1.RedisStorageConfig{
				SentinelConfig: &mcpv1alpha1.RedisSentinelConfig{
					MasterName:    "mymaster",
					SentinelAddrs: []string{"10.0.0.1:26379"},
				},
				ACLUserConfig: &mcpv1alpha1.RedisACLUserConfig{
					UsernameSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "redis-creds", Key: "username"},
					PasswordSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "redis-creds", Key: "password"},
				},
			},
		},
	}

	oidcConfig := &oidc.OIDCConfig{
		ResourceURL: "http://test-server.default.svc.cluster.local:8080",
		Scopes:      []string{"openid"},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx := context.Background()
	config, err := buildEmbeddedAuthServerRunnerConfig(ctx, fakeClient, "default", "my-mcp-server", authConfig, oidcConfig)

	require.NoError(t, err)
	require.NotNil(t, config)
	require.NotNil(t, config.Storage)
	assert.Equal(t, string(storage.TypeRedis), config.Storage.Type)
	require.NotNil(t, config.Storage.RedisConfig)
	assert.Equal(t, "mymaster", config.Storage.RedisConfig.SentinelConfig.MasterName)
	assert.Equal(t, authrunner.RedisUsernameEnvVar, config.Storage.RedisConfig.ACLUserConfig.UsernameEnvVar)
}

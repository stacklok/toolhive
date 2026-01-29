// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authserver"
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

	tests := []struct {
		name       string
		authConfig *mcpv1alpha1.EmbeddedAuthServerConfig
		checkFunc  func(t *testing.T, config *authserver.RunConfig)
	}{
		{
			name: "basic config",
			authConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				assert.Equal(t, authserver.CurrentSchemaVersion, config.SchemaVersion)
				assert.Equal(t, "https://auth.example.com", config.Issuer)
				require.NotNil(t, config.SigningKeyConfig)
				assert.Equal(t, AuthServerKeysMountPath, config.SigningKeyConfig.KeyDir)
				assert.Contains(t, config.SigningKeyConfig.SigningKeyFile, "key-0.pem")
				assert.Len(t, config.HMACSecretFiles, 1)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := buildEmbeddedAuthServerRunnerConfig(tt.authConfig)

			require.NotNil(t, config)
			tt.checkFunc(t, config)
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

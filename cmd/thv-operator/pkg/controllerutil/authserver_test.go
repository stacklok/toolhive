// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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
		authConfig       *mcpv1beta1.EmbeddedAuthServerConfig
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key-secret", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key-1", Key: "private.pem"},
					{Name: "signing-key-2", Key: "private.pem"},
					{Name: "signing-key-3", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{},
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

func TestGenerateAuthServerVolumes_RedisTLS(t *testing.T) {
	t.Parallel()

	baseAuthConfig := func(storageCfg *mcpv1beta1.AuthServerStorageConfig) *mcpv1beta1.EmbeddedAuthServerConfig {
		return &mcpv1beta1.EmbeddedAuthServerConfig{
			Issuer: "https://auth.example.com",
			SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
				{Name: "signing-key", Key: "private.pem"},
			},
			HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
				{Name: "hmac-secret", Key: "hmac"},
			},
			Storage: storageCfg,
		}
	}

	tests := []struct {
		name            string
		authConfig      *mcpv1beta1.EmbeddedAuthServerConfig
		wantTLSVolumes  int
		wantTLSMounts   int
		wantMasterVol   bool
		wantSentinelVol bool
	}{
		{
			name: "TLS enabled with CA cert creates volume",
			authConfig: baseAuthConfig(&mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeRedis,
				Redis: &mcpv1beta1.RedisStorageConfig{
					TLS: &mcpv1beta1.RedisTLSConfig{
						CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-ca", Key: "ca.crt"},
					},
				},
			}),
			wantTLSVolumes: 1,
			wantTLSMounts:  1,
			wantMasterVol:  true,
		},
		{
			name: "nil TLS produces no TLS volumes",
			authConfig: baseAuthConfig(&mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeRedis,
				Redis: &mcpv1beta1.RedisStorageConfig{
					TLS: nil,
				},
			}),
			wantTLSVolumes: 0,
			wantTLSMounts:  0,
		},
		{
			name: "TLS enabled without CA cert does NOT create volume",
			authConfig: baseAuthConfig(&mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeRedis,
				Redis: &mcpv1beta1.RedisStorageConfig{
					TLS: &mcpv1beta1.RedisTLSConfig{},
				},
			}),
			wantTLSVolumes: 0,
			wantTLSMounts:  0,
		},
		{
			name: "both master and sentinel TLS with CA certs create separate volumes",
			authConfig: baseAuthConfig(&mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeRedis,
				Redis: &mcpv1beta1.RedisStorageConfig{
					TLS: &mcpv1beta1.RedisTLSConfig{
						CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "master-ca", Key: "ca.crt"},
					},
					SentinelTLS: &mcpv1beta1.RedisTLSConfig{
						CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "sentinel-ca", Key: "ca.crt"},
					},
				},
			}),
			wantTLSVolumes:  2,
			wantTLSMounts:   2,
			wantMasterVol:   true,
			wantSentinelVol: true,
		},
		{
			name: "sentinel TLS only, master plaintext",
			authConfig: baseAuthConfig(&mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeRedis,
				Redis: &mcpv1beta1.RedisStorageConfig{
					TLS: nil,
					SentinelTLS: &mcpv1beta1.RedisTLSConfig{
						CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "sentinel-ca", Key: "ca.crt"},
					},
				},
			}),
			wantTLSVolumes:  1,
			wantTLSMounts:   1,
			wantSentinelVol: true,
		},
		{
			name:           "nil storage produces no TLS volumes",
			authConfig:     baseAuthConfig(nil),
			wantTLSVolumes: 0,
			wantTLSMounts:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts := GenerateAuthServerVolumes(tt.authConfig)

			// Count TLS-specific volumes
			tlsVolCount := 0
			tlsMountCount := 0
			hasMaster := false
			hasSentinel := false
			for _, vol := range volumes {
				if len(vol.Name) >= len(RedisTLSCACertVolumePrefix) &&
					vol.Name[:len(RedisTLSCACertVolumePrefix)] == RedisTLSCACertVolumePrefix {
					tlsVolCount++
					if vol.Name == RedisTLSCACertVolumePrefix+"master" {
						hasMaster = true
					}
					if vol.Name == RedisTLSCACertVolumePrefix+"sentinel" {
						hasSentinel = true
					}
					// Verify permissions
					require.NotNil(t, vol.Secret)
					require.NotNil(t, vol.Secret.DefaultMode)
					assert.Equal(t, int32(0400), *vol.Secret.DefaultMode)
				}
			}
			for _, mount := range mounts {
				if len(mount.Name) >= len(RedisTLSCACertVolumePrefix) &&
					mount.Name[:len(RedisTLSCACertVolumePrefix)] == RedisTLSCACertVolumePrefix {
					tlsMountCount++
					assert.True(t, mount.ReadOnly)
					assert.Contains(t, mount.MountPath, RedisTLSCACertMountPath)
				}
			}

			assert.Equal(t, tt.wantTLSVolumes, tlsVolCount, "TLS volume count")
			assert.Equal(t, tt.wantTLSMounts, tlsMountCount, "TLS mount count")
			if tt.wantMasterVol {
				assert.True(t, hasMaster, "expected master TLS volume")
			}
			if tt.wantSentinelVol {
				assert.True(t, hasSentinel, "expected sentinel TLS volume")
			}
		})
	}
}

func TestGenerateAuthServerEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		authConfig      *mcpv1beta1.EmbeddedAuthServerConfig
		wantEnvNames    []string
		wantSecretNames []string // parallel to wantEnvNames; asserts SecretKeyRef.Name
	}{
		{
			name:         "nil config returns empty slice",
			authConfig:   nil,
			wantEnvNames: nil,
		},
		{
			name: "no upstream providers returns empty slice",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{},
			},
			wantEnvNames: nil,
		},
		{
			name: "OIDC provider with client secret ref",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://auth.example.com/callback",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "oidc-client-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
			wantEnvNames: []string{UpstreamClientSecretEnvVar + "_OKTA"},
		},
		{
			name: "OIDC provider without client secret ref (public client)",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://auth.example.com/callback",
							// No ClientSecretRef - public client using PKCE
						},
					},
				},
			},
			wantEnvNames: nil,
		},
		{
			name: "OAuth2 provider with client secret ref",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							RedirectURI:           "https://auth.example.com/callback",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "github-client-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
			wantEnvNames: []string{UpstreamClientSecretEnvVar + "_GITHUB"},
		},
		{
			name: "OAuth2 provider without client secret ref",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							RedirectURI:           "https://auth.example.com/callback",
							// No ClientSecretRef
						},
					},
				},
			},
			wantEnvNames: nil,
		},
		{
			name: "upstream provider with nil OIDCConfig",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name:       "test",
						Type:       mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: nil, // Nil config
					},
				},
			},
			wantEnvNames: nil,
		},
		{
			name: "multiple upstream providers with client secrets get indexed env vars",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://okta.example.com",
							ClientID:  "client-id-0",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "okta-secret",
								Key:  "client-secret",
							},
						},
					},
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id-1",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "github-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
			wantEnvNames: []string{
				UpstreamClientSecretEnvVar + "_OKTA",
				UpstreamClientSecretEnvVar + "_GITHUB",
			},
			wantSecretNames: []string{"okta-secret", "github-secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envVars := GenerateAuthServerEnvVars(tt.authConfig)

			if len(tt.wantEnvNames) == 0 {
				assert.Empty(t, envVars)
				return
			}

			require.Len(t, envVars, len(tt.wantEnvNames))
			for i, wantName := range tt.wantEnvNames {
				assert.Equal(t, wantName, envVars[i].Name)
				require.NotNil(t, envVars[i].ValueFrom)
				require.NotNil(t, envVars[i].ValueFrom.SecretKeyRef)
				if len(tt.wantSecretNames) > i {
					assert.Equal(t, tt.wantSecretNames[i], envVars[i].ValueFrom.SecretKeyRef.Name)
				}
			}
		})
	}
}

func TestGenerateAuthServerConfigByName(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		configName      string
		externalAuthCfg *mcpv1beta1.MCPExternalAuthConfig
		wantVolumes     bool
		wantMounts      bool
		wantEnvVars     bool
		wantErr         bool
		errContains     string
	}{
		{
			name:       "non-embeddedAuthServer type returns empty slices",
			configName: "token-exchange-config",
			externalAuthCfg: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "token-exchange-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
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
			name:       "embeddedAuthServer type with valid config",
			configName: "embedded-auth-config",
			externalAuthCfg: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
							{Name: "signing-key", Key: "private.pem"},
						},
						HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
							{Name: "hmac-secret", Key: "hmac"},
						},
						UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
							{
								Name: "okta",
								Type: mcpv1beta1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
									ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
			name:       "embeddedAuthServer type with nil embedded config",
			configName: "bad-auth-config",
			externalAuthCfg: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type:               mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
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
			name:            "non-existent external auth config",
			configName:      "non-existent",
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
			volumes, mounts, envVars, err := GenerateAuthServerConfigByName(
				ctx, fakeClient, "default", tt.configName,
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

func TestBuildAuthServerRunConfig(t *testing.T) {
	t.Parallel()

	// Default audiences and scopes used for most tests
	defaultAudiences := []string{"http://test-server.default.svc.cluster.local:8080"}
	defaultScopes := []string{"openid", "offline_access"}

	defaultResourceURL := "http://test-server.default.svc.cluster.local:8080"

	tests := []struct {
		name             string
		authConfig       *mcpv1beta1.EmbeddedAuthServerConfig
		allowedAudiences []string
		scopesSupported  []string
		resourceURL      string
		checkFunc        func(t *testing.T, config *authserver.RunConfig)
	}{
		{
			name: "basic config with allowed audiences and scopes from OIDC config",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key-1", Key: "private.pem"},
					{Name: "signing-key-2", Key: "private.pem"},
					{Name: "signing-key-3", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				TokenLifespans: &mcpv1beta1.TokenLifespanConfig{
					AccessTokenLifespan:  "30m",
					RefreshTokenLifespan: "168h",
					AuthCodeLifespan:     "5m",
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.NotNil(t, config.TokenLifespans)
				assert.Equal(t, "30m", config.TokenLifespans.AccessTokenLifespan)
				assert.Equal(t, "168h", config.TokenLifespans.RefreshTokenLifespan)
				assert.Equal(t, "5m", config.TokenLifespans.AuthCodeLifespan)
			},
		},
		{
			name:        "with OIDC upstream provider",
			resourceURL: defaultResourceURL,
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://auth.example.com/callback",
							Scopes:      []string{"openid", "profile"},
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
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
			name:        "with OAuth2 upstream provider with userinfo config",
			resourceURL: defaultResourceURL,
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							RedirectURI:           "https://auth.example.com/callback",
							UserInfo: &mcpv1beta1.UserInfoConfig{
								EndpointURL: "https://api.github.com/user",
								HTTPMethod:  "GET",
								AdditionalHeaders: map[string]string{
									"Accept": "application/vnd.github.v3+json",
								},
								FieldMapping: &mcpv1beta1.UserInfoFieldMapping{
									SubjectFields: []string{"id", "login"},
									NameFields:    []string{"name", "login"},
									EmailFields:   []string{"email"},
								},
							},
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			allowedAudiences: []string{"http://my-service.ns.svc.cluster.local:8080"},
			scopesSupported:  nil, // nil scopes should be passed through
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				assert.Equal(t, []string{"http://my-service.ns.svc.cluster.local:8080"}, config.AllowedAudiences)
				assert.Nil(t, config.ScopesSupported, "nil scopes should be passed through to use auth server defaults")
			},
		},
		{
			name: "with custom scopes from OIDC config",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
			allowedAudiences: []string{"http://custom-service.ns.svc.cluster.local:9000"},
			scopesSupported:  []string{"openid", "profile", "email", "custom:scope"},
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				assert.Equal(t, []string{"http://custom-service.ns.svc.cluster.local:9000"}, config.AllowedAudiences)
				assert.Equal(t, []string{"openid", "profile", "email", "custom:scope"}, config.ScopesSupported)
			},
		},
		{
			name:        "with multiple upstream providers all are included",
			resourceURL: defaultResourceURL,
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "okta-client-id",
							RedirectURI: "https://auth.example.com/callback",
							Scopes:      []string{"openid", "profile"},
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "okta-secret",
								Key:  "client-secret",
							},
						},
					},
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "github-client-id",
							RedirectURI:           "https://auth.example.com/callback",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "github-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 2)

				// First upstream: okta OIDC with indexed env var
				okta := config.Upstreams[0]
				assert.Equal(t, "okta", okta.Name)
				assert.Equal(t, authserver.UpstreamProviderTypeOIDC, okta.Type)
				require.NotNil(t, okta.OIDCConfig)
				assert.Equal(t, "https://okta.example.com", okta.OIDCConfig.IssuerURL)
				assert.Equal(t, UpstreamClientSecretEnvVar+"_OKTA", okta.OIDCConfig.ClientSecretEnvVar)

				// Second upstream: github OAuth2 with indexed env var
				github := config.Upstreams[1]
				assert.Equal(t, "github", github.Name)
				assert.Equal(t, authserver.UpstreamProviderTypeOAuth2, github.Type)
				require.NotNil(t, github.OAuth2Config)
				assert.Equal(t, "https://github.com/login/oauth/authorize", github.OAuth2Config.AuthorizationEndpoint)
				assert.Equal(t, UpstreamClientSecretEnvVar+"_GITHUB", github.OAuth2Config.ClientSecretEnvVar)
			},
		},
		{
			name: "OIDC upstream propagates AdditionalAuthorizationParams",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "okta-client-id",
							RedirectURI: "https://auth.example.com/callback",
							Scopes:      []string{"openid", "profile"},
							AdditionalAuthorizationParams: map[string]string{
								"access_type": "offline",
							},
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				upstream := config.Upstreams[0]
				require.NotNil(t, upstream.OIDCConfig)
				assert.Equal(t, map[string]string{"access_type": "offline"},
					upstream.OIDCConfig.AdditionalAuthorizationParams)
			},
		},
		{
			name: "OAuth2 upstream propagates AdditionalAuthorizationParams",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "github-client-id",
							RedirectURI:           "https://auth.example.com/callback",
							AdditionalAuthorizationParams: map[string]string{
								"access_type": "offline",
							},
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				upstream := config.Upstreams[0]
				require.NotNil(t, upstream.OAuth2Config)
				assert.Equal(t, map[string]string{"access_type": "offline"},
					upstream.OAuth2Config.AdditionalAuthorizationParams)
			},
		},
		{
			name:        "OIDC upstream with empty redirectUri defaults to resourceURL/oauth/callback",
			resourceURL: "https://mcp.example.com",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://okta.example.com",
							ClientID:  "client-id",
							// RedirectURI intentionally omitted
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				require.NotNil(t, config.Upstreams[0].OIDCConfig)
				assert.Equal(t, "https://mcp.example.com/oauth/callback", config.Upstreams[0].OIDCConfig.RedirectURI)
			},
		},
		{
			name:        "OAuth2 upstream with empty redirectUri defaults to resourceURL/oauth/callback",
			resourceURL: "https://mcp.example.com",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "client-id",
							// RedirectURI intentionally omitted
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				require.NotNil(t, config.Upstreams[0].OAuth2Config)
				assert.Equal(t, "https://mcp.example.com/oauth/callback", config.Upstreams[0].OAuth2Config.RedirectURI)
			},
		},
		{
			name:        "explicit redirectUri is preserved when resourceURL is also set",
			resourceURL: "https://mcp.example.com",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL:   "https://okta.example.com",
							ClientID:    "client-id",
							RedirectURI: "https://custom.example.com/callback",
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				require.NotNil(t, config.Upstreams[0].OIDCConfig)
				assert.Equal(t, "https://custom.example.com/callback", config.Upstreams[0].OIDCConfig.RedirectURI)
			},
		},
		{
			name:        "resourceURL with trailing slash produces correct default redirectUri",
			resourceURL: "https://mcp.example.com/",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://okta.example.com",
							ClientID:  "client-id",
						},
					},
				},
			},
			allowedAudiences: defaultAudiences,
			scopesSupported:  defaultScopes,
			checkFunc: func(t *testing.T, config *authserver.RunConfig) {
				t.Helper()
				require.Len(t, config.Upstreams, 1)
				require.NotNil(t, config.Upstreams[0].OIDCConfig)
				assert.Equal(t, "https://mcp.example.com/oauth/callback", config.Upstreams[0].OIDCConfig.RedirectURI)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := BuildAuthServerRunConfig("default", "test-server", tt.authConfig, tt.allowedAudiences, tt.scopesSupported, tt.resourceURL)

			require.NoError(t, err)
			require.NotNil(t, config)
			tt.checkFunc(t, config)
		})
	}
}

func TestAddEmbeddedAuthServerConfigOptions_Validation(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	err := mcpv1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	// Helper function to create a fresh external auth config for each test
	// This avoids data races when running subtests in parallel
	newExternalAuthConfig := func() *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "embedded-auth-config",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: "https://auth.example.com",
					SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
						{Name: "signing-key", Key: "private.pem"},
					},
					HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
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
				Audience:    "http://test-server.default.svc.cluster.local:8080",
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      []string{"openid", "offline_access"},
			},
			expectError: false,
		},
		{
			name: "valid OIDC config with nil scopes succeeds",
			oidcConfig: &oidc.OIDCConfig{
				Audience:    "http://test-server.default.svc.cluster.local:8080",
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      nil,
			},
			expectError: false,
		},
		{
			name: "audience mismatch with resourceUrl returns error",
			oidcConfig: &oidc.OIDCConfig{
				Audience:    "https://different-audience.example.com",
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      []string{"openid"},
			},
			expectError: true,
			errContains: "must match resourceUrl",
		},
		{
			name: "empty audience returns specific error",
			oidcConfig: &oidc.OIDCConfig{
				Audience:    "",
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      []string{"openid"},
			},
			expectError: true,
			errContains: "audience is required when an embedded auth server is active",
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
				&mcpv1beta1.ExternalAuthConfigRef{Name: "embedded-auth-config"},
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

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
			{Name: "key-0", Key: "private.pem"},
			{Name: "key-1", Key: "private.pem"},
		},
		HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
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
		authConfig     *mcpv1beta1.EmbeddedAuthServerConfig
		wantEnvVarLen  int
		wantRedisUser  bool
		wantRedisPass  bool
		wantUpstreamCS bool
	}{
		{
			name: "Redis storage with ACL credentials generates env vars",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{},
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel:26379"},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "redis-creds",
								Key:  "username",
							},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "okta",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://okta.example.com",
							ClientID:  "client-id",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "oidc-secret",
								Key:  "client-secret",
							},
						},
					},
				},
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel:26379"},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "redis-creds",
								Key:  "username",
							},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{
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
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{},
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeMemory,
				},
			},
			wantEnvVarLen: 0,
		},
		{
			name: "nil storage does not generate Redis env vars",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:            "https://auth.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{},
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
				_, ok := envMap[UpstreamClientSecretEnvVar+"_OKTA"]
				assert.True(t, ok, "expected upstream client secret env var")
			}
		})
	}
}

func TestResolveSentinelAddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sentinel  *mcpv1beta1.RedisSentinelConfig
		wantAddrs []string
		wantErr   bool
		errMsg    string
	}{
		{
			name: "static addresses returned directly",
			sentinel: &mcpv1beta1.RedisSentinelConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"10.0.0.1:26379", "10.0.0.2:26379"},
			},
			wantAddrs: []string{"10.0.0.1:26379", "10.0.0.2:26379"},
		},
		{
			name: "service ref constructs DNS name with explicit port",
			sentinel: &mcpv1beta1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1beta1.SentinelServiceRef{
					Name: "redis-sentinel",
					Port: 26379,
				},
			},
			wantAddrs: []string{"redis-sentinel.default.svc.cluster.local:26379"},
		},
		{
			name: "service ref with default port",
			sentinel: &mcpv1beta1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1beta1.SentinelServiceRef{
					Name: "redis-sentinel",
				},
			},
			wantAddrs: []string{"redis-sentinel.default.svc.cluster.local:26379"},
		},
		{
			name: "service ref with custom namespace",
			sentinel: &mcpv1beta1.RedisSentinelConfig{
				MasterName: "mymaster",
				SentinelService: &mcpv1beta1.SentinelServiceRef{
					Name:      "redis-sentinel",
					Namespace: "redis-ns",
					Port:      26379,
				},
			},
			wantAddrs: []string{"redis-sentinel.redis-ns.svc.cluster.local:26379"},
		},
		{
			name: "neither addrs nor service returns error",
			sentinel: &mcpv1beta1.RedisSentinelConfig{
				MasterName: "mymaster",
			},
			wantErr: true,
			errMsg:  "either sentinelAddrs or sentinelService must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addrs, err := resolveSentinelAddrs(tt.sentinel, "default")

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

	tests := []struct {
		name        string
		authConfig  *mcpv1beta1.EmbeddedAuthServerConfig
		wantNil     bool
		wantErr     bool
		errContains string
		checkFunc   func(t *testing.T, cfg *storage.RunConfig)
	}{
		{
			name: "nil storage returns nil (memory default)",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
			},
			wantNil: true,
		},
		{
			name: "memory storage returns nil",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeMemory,
				},
			},
			wantNil: true,
		},
		{
			name: "Redis storage with static addrs builds correctly",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"10.0.0.1:26379"},
							DB:            2,
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "p"},
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
				assert.Equal(t, storage.AuthTypeACLUser, cfg.RedisConfig.AuthType)
				require.NotNil(t, cfg.RedisConfig.ACLUserConfig)
				assert.Equal(t, authrunner.RedisUsernameEnvVar, cfg.RedisConfig.ACLUserConfig.UsernameEnvVar)
				assert.Equal(t, authrunner.RedisPasswordEnvVar, cfg.RedisConfig.ACLUserConfig.PasswordEnvVar)
				assert.Equal(t, "10s", cfg.RedisConfig.DialTimeout)
				assert.Equal(t, "5s", cfg.RedisConfig.ReadTimeout)
				assert.Equal(t, "5s", cfg.RedisConfig.WriteTimeout)
				assert.Equal(t, "thv:auth:{default:test-server}:", cfg.RedisConfig.KeyPrefix)
			},
		},
		{
			name: "Redis storage with service discovery via DNS",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName: "mymaster",
							SentinelService: &mcpv1beta1.SentinelServiceRef{
								Name: "redis-sentinel",
								Port: 26379,
							},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			checkFunc: func(t *testing.T, cfg *storage.RunConfig) {
				t.Helper()
				assert.Equal(t, []string{"redis-sentinel.default.svc.cluster.local:26379"},
					cfg.RedisConfig.SentinelConfig.SentinelAddrs)
			},
		},
		{
			name: "Redis storage without redis config returns error",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
				},
			},
			wantErr:     true,
			errContains: "redis config is required",
		},
		{
			name: "Redis storage missing addr, sentinelConfig, and clusterConfig returns error",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "one of addr (standalone), sentinelConfig (Sentinel), or clusterConfig (Cluster) is required",
		},
		{
			name: "Redis storage with both addr and sentinelConfig returns error",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						Addr: "redis.example.com:6379",
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"10.0.0.1:26379"},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "mutually exclusive",
		},
		{
			name: "Redis storage with addr and clusterConfig returns error",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						Addr: "redis.example.com:6379",
						ClusterConfig: &mcpv1beta1.RedisClusterConfig{
							Addrs: []string{"node1.example.com:6379"},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "mutually exclusive",
		},
		{
			name: "Redis storage with sentinelConfig and clusterConfig returns error",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"10.0.0.1:26379"},
						},
						ClusterConfig: &mcpv1beta1.RedisClusterConfig{
							Addrs: []string{"node1.example.com:6379"},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "u"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "s", Key: "p"},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "mutually exclusive",
		},
		{
			name: "Redis cluster config builds correctly",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						ClusterConfig: &mcpv1beta1.RedisClusterConfig{
							Addrs: []string{"node1.example.com:6379", "node2.example.com:6379"},
						},
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				},
			},
			checkFunc: func(t *testing.T, cfg *storage.RunConfig) {
				t.Helper()
				assert.Equal(t, string(storage.TypeRedis), cfg.Type)
				require.NotNil(t, cfg.RedisConfig)
				require.NotNil(t, cfg.RedisConfig.ClusterConfig)
				assert.Equal(t, []string{"node1.example.com:6379", "node2.example.com:6379"},
					cfg.RedisConfig.ClusterConfig.Addrs)
				assert.Empty(t, cfg.RedisConfig.Addr)
				assert.Nil(t, cfg.RedisConfig.SentinelConfig)
				assert.Equal(t, storage.AuthTypeACLUser, cfg.RedisConfig.AuthType)
				require.NotNil(t, cfg.RedisConfig.ACLUserConfig)
				assert.Equal(t, authrunner.RedisUsernameEnvVar, cfg.RedisConfig.ACLUserConfig.UsernameEnvVar)
				assert.Equal(t, authrunner.RedisPasswordEnvVar, cfg.RedisConfig.ACLUserConfig.PasswordEnvVar)
				assert.Equal(t, "thv:auth:{default:test-server}:", cfg.RedisConfig.KeyPrefix)
			},
		},
		{
			name: "Redis storage with standalone addr builds correctly",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						Addr: "redis.example.com:6379",
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				},
			},
			checkFunc: func(t *testing.T, cfg *storage.RunConfig) {
				t.Helper()
				assert.Equal(t, string(storage.TypeRedis), cfg.Type)
				require.NotNil(t, cfg.RedisConfig)
				assert.Equal(t, "redis.example.com:6379", cfg.RedisConfig.Addr)
				assert.Nil(t, cfg.RedisConfig.SentinelConfig)
				assert.Equal(t, storage.AuthTypeACLUser, cfg.RedisConfig.AuthType)
				require.NotNil(t, cfg.RedisConfig.ACLUserConfig)
				assert.Equal(t, authrunner.RedisUsernameEnvVar, cfg.RedisConfig.ACLUserConfig.UsernameEnvVar)
				assert.Equal(t, authrunner.RedisPasswordEnvVar, cfg.RedisConfig.ACLUserConfig.PasswordEnvVar)
				assert.Equal(t, "thv:auth:{default:test-server}:", cfg.RedisConfig.KeyPrefix)
			},
		},
		{
			name: "Redis storage without ACL user config returns error",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"10.0.0.1:26379"},
						},
					},
				},
			},
			wantErr:     true,
			errContains: "ACL user config is required",
		},
		{
			name: "Redis standalone with password-only auth omits UsernameEnvVar",
			authConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						Addr: "memorystore.example.com:6379",
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				},
			},
			checkFunc: func(t *testing.T, cfg *storage.RunConfig) {
				t.Helper()
				assert.Equal(t, "memorystore.example.com:6379", cfg.RedisConfig.Addr)
				require.NotNil(t, cfg.RedisConfig.ACLUserConfig)
				assert.Empty(t, cfg.RedisConfig.ACLUserConfig.UsernameEnvVar)
				assert.Equal(t, authrunner.RedisPasswordEnvVar, cfg.RedisConfig.ACLUserConfig.PasswordEnvVar)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := buildStorageRunConfig("default", "test-server", tt.authConfig)

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

func TestBuildAuthServerRunConfig_WithRedisStorage(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
			{Name: "signing-key", Key: "private.pem"},
		},
		HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
			{Name: "hmac-secret", Key: "hmac"},
		},
		Storage: &mcpv1beta1.AuthServerStorageConfig{
			Type: mcpv1beta1.AuthServerStorageTypeRedis,
			Redis: &mcpv1beta1.RedisStorageConfig{
				SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
					MasterName:    "mymaster",
					SentinelAddrs: []string{"10.0.0.1:26379"},
				},
				ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
					UsernameSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-creds", Key: "username"},
					PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-creds", Key: "password"},
				},
			},
		},
	}

	config, err := BuildAuthServerRunConfig(
		"default", "my-mcp-server", authConfig,
		[]string{"http://test-server.default.svc.cluster.local:8080"},
		[]string{"openid"},
		"http://test-server.default.svc.cluster.local:8080",
	)

	require.NoError(t, err)
	require.NotNil(t, config)
	require.NotNil(t, config.Storage)
	assert.Equal(t, string(storage.TypeRedis), config.Storage.Type)
	require.NotNil(t, config.Storage.RedisConfig)
	assert.Equal(t, "mymaster", config.Storage.RedisConfig.SentinelConfig.MasterName)
	assert.Equal(t, authrunner.RedisUsernameEnvVar, config.Storage.RedisConfig.ACLUserConfig.UsernameEnvVar)
}

func TestAddAuthServerRefOptions(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	newValidEmbeddedAuthConfig := func() *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "auth-server-config",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:                       "https://auth.example.com",
					AuthorizationEndpointBaseURL: "https://auth.example.com",
					SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
						{Name: "signing-key", Key: "private.pem"},
					},
					HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
						{Name: "hmac-secret", Key: "hmac"},
					},
				},
			},
		}
	}

	newUnauthenticatedConfig := func() *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "unauth-config",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
			},
		}
	}

	validOIDCConfig := &oidc.OIDCConfig{
		Audience:    "https://mcp.example.com",
		ResourceURL: "https://mcp.example.com",
		Scopes:      []string{"openid"},
	}

	tests := []struct {
		name          string
		authServerRef *mcpv1beta1.AuthServerRef
		oidcConfig    *oidc.OIDCConfig
		objects       func() []runtime.Object
		wantErr       bool
		errContains   string
		wantOptions   int
	}{
		{
			name:          "nil ref returns nil",
			authServerRef: nil,
			oidcConfig:    validOIDCConfig,
			wantErr:       false,
			wantOptions:   0,
		},
		{
			name: "unsupported kind returns error",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "Foo",
				Name: "some-config",
			},
			oidcConfig:  validOIDCConfig,
			wantErr:     true,
			errContains: "unsupported authServerRef kind",
		},
		{
			name: "non-existent config returns error",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "non-existent",
			},
			oidcConfig:  validOIDCConfig,
			wantErr:     true,
			errContains: "failed to get MCPExternalAuthConfig",
		},
		{
			name: "wrong type returns error",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "unauth-config",
			},
			oidcConfig:  validOIDCConfig,
			objects:     func() []runtime.Object { return []runtime.Object{newUnauthenticatedConfig()} },
			wantErr:     true,
			errContains: "must reference a MCPExternalAuthConfig with type",
		},
		{
			name: "valid ref appends option",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-server-config",
			},
			oidcConfig:  validOIDCConfig,
			objects:     func() []runtime.Object { return []runtime.Object{newValidEmbeddedAuthConfig()} },
			wantErr:     false,
			wantOptions: 1,
		},
		{
			name: "nil OIDC config returns error for valid ref",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-server-config",
			},
			oidcConfig:  nil,
			objects:     func() []runtime.Object { return []runtime.Object{newValidEmbeddedAuthConfig()} },
			wantErr:     true,
			errContains: "OIDC config is required",
		},
		{
			name: "audience mismatch with resourceUrl returns error",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-server-config",
			},
			oidcConfig: &oidc.OIDCConfig{
				Audience:    "https://wrong-audience.example.com",
				ResourceURL: "https://mcp.example.com",
				Scopes:      []string{"openid"},
			},
			objects:     func() []runtime.Object { return []runtime.Object{newValidEmbeddedAuthConfig()} },
			wantErr:     true,
			errContains: "must match resourceUrl",
		},
		{
			name: "audience matching resourceUrl succeeds",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-server-config",
			},
			oidcConfig: &oidc.OIDCConfig{
				Audience:    "https://mcp.example.com",
				ResourceURL: "https://mcp.example.com",
				Scopes:      []string{"openid"},
			},
			objects:     func() []runtime.Object { return []runtime.Object{newValidEmbeddedAuthConfig()} },
			wantErr:     false,
			wantOptions: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.objects != nil {
				builder = builder.WithRuntimeObjects(tt.objects()...)
			}
			fakeClient := builder.Build()

			var options []runner.RunConfigBuilderOption
			err := AddAuthServerRefOptions(
				ctx, fakeClient, "default", "test-server",
				tt.authServerRef, tt.oidcConfig, &options,
			)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Len(t, options, tt.wantOptions)
			}
		})
	}
}

func TestValidateAndAddAuthServerRefOptions(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	newEmbeddedAuthConfig := func() *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "embedded-config",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:                       "https://auth.example.com",
					AuthorizationEndpointBaseURL: "https://auth.example.com",
					SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
						{Name: "signing-key", Key: "private.pem"},
					},
					HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
						{Name: "hmac-secret", Key: "hmac"},
					},
				},
			},
		}
	}

	newAWSStsConfig := func() *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "aws-sts-config",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeAWSSts,
				AWSSts: &mcpv1beta1.AWSStsConfig{
					Region: "us-east-1",
				},
			},
		}
	}

	validOIDC := &oidc.OIDCConfig{
		Audience:    "https://mcp.example.com",
		ResourceURL: "https://mcp.example.com",
		Scopes:      []string{"openid"},
	}

	tests := []struct {
		name                  string
		authServerRef         *mcpv1beta1.AuthServerRef
		externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef
		oidcConfig            *oidc.OIDCConfig
		objects               func() []runtime.Object
		wantErr               bool
		errContains           string
		wantOptions           int
	}{
		{
			name:                  "both nil is a no-op",
			authServerRef:         nil,
			externalAuthConfigRef: nil,
			oidcConfig:            validOIDC,
			wantErr:               false,
			wantOptions:           0,
		},
		{
			name: "authServerRef set with nil externalAuthConfigRef succeeds",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "embedded-config",
			},
			externalAuthConfigRef: nil,
			oidcConfig:            validOIDC,
			objects:               func() []runtime.Object { return []runtime.Object{newEmbeddedAuthConfig()} },
			wantErr:               false,
			wantOptions:           1,
		},
		{
			name: "both refs pointing to embeddedAuthServer returns conflict error",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "embedded-config",
			},
			externalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
				Name: "embedded-config",
			},
			oidcConfig:  validOIDC,
			objects:     func() []runtime.Object { return []runtime.Object{newEmbeddedAuthConfig()} },
			wantErr:     true,
			errContains: "conflict: both authServerRef and externalAuthConfigRef",
		},
		{
			name: "authServerRef embedded + externalAuthConfigRef awsSts succeeds",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "embedded-config",
			},
			externalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
				Name: "aws-sts-config",
			},
			oidcConfig:  validOIDC,
			objects:     func() []runtime.Object { return []runtime.Object{newEmbeddedAuthConfig(), newAWSStsConfig()} },
			wantErr:     false,
			wantOptions: 1,
		},
		{
			name: "non-NotFound fetch error for externalAuthConfigRef is returned",
			authServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "embedded-config",
			},
			externalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
				Name: "will-error",
			},
			oidcConfig:  validOIDC,
			objects:     func() []runtime.Object { return []runtime.Object{newEmbeddedAuthConfig()} },
			wantErr:     true,
			errContains: "failed to fetch externalAuthConfigRef for conflict validation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.objects != nil {
				builder = builder.WithRuntimeObjects(tt.objects()...)
			}

			// For the "non-NotFound fetch error" test case, inject a Get interceptor
			// that returns a transient error for the specific resource name.
			if tt.name == "non-NotFound fetch error for externalAuthConfigRef is returned" {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if key.Name == "will-error" {
							return fmt.Errorf("transient API error")
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})
			}

			fakeClient := builder.Build()

			var options []runner.RunConfigBuilderOption
			err := ValidateAndAddAuthServerRefOptions(
				ctx, fakeClient, "default", "test-server",
				tt.authServerRef, tt.externalAuthConfigRef,
				tt.oidcConfig, &options,
			)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Len(t, options, tt.wantOptions)
			}
		})
	}
}

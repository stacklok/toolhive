// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
)

func TestDefaultAuthManager_UnsetAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		updateErr error
		wantErr   bool
	}{
		{
			name:      "clears registry auth config on success",
			updateErr: nil,
			wantErr:   false,
		},
		{
			name:      "propagates error from UpdateConfig",
			updateErr: errUpdateFailed,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := configmocks.NewMockProvider(ctrl)

			// Capture the update function and verify it zeroes RegistryAuth.
			mockProvider.EXPECT().
				UpdateConfig(gomock.Any()).
				DoAndReturn(func(fn func(*config.Config) error) error {
					if tt.updateErr != nil {
						return tt.updateErr
					}
					cfg := &config.Config{
						RegistryAuth: config.RegistryAuth{
							Type: config.RegistryAuthTypeOAuth,
							OAuth: &config.RegistryOAuthConfig{
								Issuer:   "https://auth.example.com",
								ClientID: "my-client",
							},
						},
					}
					require.NoError(t, fn(cfg))
					// After the update function runs, RegistryAuth must be zero.
					require.Equal(t, config.RegistryAuth{}, cfg.RegistryAuth)
					return nil
				})

			mgr := NewAuthManager(mockProvider)
			err := mgr.UnsetAuth()

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultAuthManager_GetAuthInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		registryAuth      config.RegistryAuth
		wantAuthType      string
		wantHasCachedToks bool
	}{
		{
			name:              "returns empty when no auth configured",
			registryAuth:      config.RegistryAuth{},
			wantAuthType:      "",
			wantHasCachedToks: false,
		},
		{
			name: "returns oauth type without cached tokens when OAuth section has no ref",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantAuthType:      config.RegistryAuthTypeOAuth,
			wantHasCachedToks: false,
		},
		{
			name: "returns oauth type with cached tokens when CachedRefreshTokenRef is set",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:                "https://auth.example.com",
					ClientID:              "my-client",
					CachedRefreshTokenRef: "REGISTRY_OAUTH_aabbccdd",
				},
			},
			wantAuthType:      config.RegistryAuthTypeOAuth,
			wantHasCachedToks: true,
		},
		{
			name: "returns oauth type without cached tokens when OAuth section is nil",
			registryAuth: config.RegistryAuth{
				Type:  config.RegistryAuthTypeOAuth,
				OAuth: nil,
			},
			wantAuthType:      config.RegistryAuthTypeOAuth,
			wantHasCachedToks: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := configmocks.NewMockProvider(ctrl)

			mockProvider.EXPECT().
				GetConfig().
				Return(&config.Config{RegistryAuth: tt.registryAuth})

			mgr := NewAuthManager(mockProvider)
			authType, hasCachedToks := mgr.GetAuthInfo()

			require.Equal(t, tt.wantAuthType, authType)
			require.Equal(t, tt.wantHasCachedToks, hasCachedToks)
		})
	}
}

func TestDefaultAuthManager_GetAuthStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		registryAuth config.RegistryAuth
		wantStatus   string
		wantAuthType string
	}{
		{
			name:         "returns none when no auth configured",
			registryAuth: config.RegistryAuth{},
			wantStatus:   AuthStatusNone,
			wantAuthType: "",
		},
		{
			name: "returns configured when OAuth set but no cached tokens",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantStatus:   AuthStatusConfigured,
			wantAuthType: config.RegistryAuthTypeOAuth,
		},
		{
			name: "returns authenticated when OAuth set with cached tokens",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:                "https://auth.example.com",
					ClientID:              "my-client",
					CachedRefreshTokenRef: "REGISTRY_OAUTH_aabbccdd",
				},
			},
			wantStatus:   AuthStatusAuthenticated,
			wantAuthType: config.RegistryAuthTypeOAuth,
		},
		{
			name: "returns configured when OAuth section is nil",
			registryAuth: config.RegistryAuth{
				Type:  config.RegistryAuthTypeOAuth,
				OAuth: nil,
			},
			wantStatus:   AuthStatusConfigured,
			wantAuthType: config.RegistryAuthTypeOAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := configmocks.NewMockProvider(ctrl)

			mockProvider.EXPECT().
				GetConfig().
				Return(&config.Config{RegistryAuth: tt.registryAuth})

			mgr := NewAuthManager(mockProvider)
			status, authType := mgr.GetAuthStatus()

			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantAuthType, authType)
		})
	}
}

func TestDefaultAuthManager_GetOAuthPublicConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		registryAuth config.RegistryAuth
		wantConfig   *OAuthPublicConfig
	}{
		{
			name:         "returns nil when no auth configured",
			registryAuth: config.RegistryAuth{},
			wantConfig:   nil,
		},
		{
			name: "returns nil when type is oauth but OAuth section is nil",
			registryAuth: config.RegistryAuth{
				Type:  config.RegistryAuthTypeOAuth,
				OAuth: nil,
			},
			wantConfig: nil,
		},
		{
			name: "returns config with all fields populated",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
					Audience: "api://toolhive",
					Scopes:   []string{"openid", "profile"},
				},
			},
			wantConfig: &OAuthPublicConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client",
				Audience: "api://toolhive",
				Scopes:   []string{"openid", "profile"},
			},
		},
		{
			name: "returns config without optional fields",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantConfig: &OAuthPublicConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client",
			},
		},
		{
			name: "excludes cached token fields",
			registryAuth: config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:                "https://auth.example.com",
					ClientID:              "my-client",
					CachedRefreshTokenRef: "REGISTRY_OAUTH_aabbccdd",
				},
			},
			wantConfig: &OAuthPublicConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := configmocks.NewMockProvider(ctrl)

			mockProvider.EXPECT().
				GetConfig().
				Return(&config.Config{RegistryAuth: tt.registryAuth})

			mgr := NewAuthManager(mockProvider)
			got := mgr.GetOAuthPublicConfig()

			require.Equal(t, tt.wantConfig, got)
		})
	}
}

// errUpdateFailed is a sentinel error for testing UpdateConfig failure paths.
var errUpdateFailed = errSentinel("UpdateConfig failed")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

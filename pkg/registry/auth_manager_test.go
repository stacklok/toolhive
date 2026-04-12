// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
)

// configWithAuth returns a Config with a "default" registry that has the given auth.
func configWithAuth(regAuth *config.RegistryAuth) *config.Config {
	src := config.RegistrySource{
		Name:     "default",
		Type:     config.RegistrySourceTypeAPI,
		Location: "https://api.example.com",
	}
	if regAuth != nil {
		src.Auth = regAuth
	}
	return &config.Config{
		Registries:      []config.RegistrySource{src},
		DefaultRegistry: "default",
	}
}

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

			mockProvider.EXPECT().
				UpdateConfig(gomock.Any()).
				DoAndReturn(func(fn func(*config.Config)) error {
					if tt.updateErr != nil {
						return tt.updateErr
					}
					cfg := configWithAuth(&config.RegistryAuth{
						Type: config.RegistryAuthTypeOAuth,
						OAuth: &config.RegistryOAuthConfig{
							Issuer:   "https://auth.example.com",
							ClientID: "my-client",
						},
					})
					fn(cfg)
					// After the update function runs, Auth must be nil.
					src := cfg.FindRegistry("default")
					require.NotNil(t, src)
					require.Nil(t, src.Auth)
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
		cfg               *config.Config
		wantAuthType      string
		wantHasCachedToks bool
	}{
		{
			name:              "returns empty when no registries",
			cfg:               &config.Config{},
			wantAuthType:      "",
			wantHasCachedToks: false,
		},
		{
			name:              "returns empty when no auth configured",
			cfg:               configWithAuth(nil),
			wantAuthType:      "",
			wantHasCachedToks: false,
		},
		{
			name: "returns oauth type without cached tokens",
			cfg: configWithAuth(&config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			}),
			wantAuthType:      config.RegistryAuthTypeOAuth,
			wantHasCachedToks: false,
		},
		{
			name: "returns oauth type with cached tokens",
			cfg: configWithAuth(&config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:                "https://auth.example.com",
					ClientID:              "my-client",
					CachedRefreshTokenRef: "REGISTRY_OAUTH_aabbccdd",
				},
			}),
			wantAuthType:      config.RegistryAuthTypeOAuth,
			wantHasCachedToks: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := configmocks.NewMockProvider(ctrl)
			mockProvider.EXPECT().GetConfig().Return(tt.cfg)

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
		cfg          *config.Config
		wantStatus   string
		wantAuthType string
	}{
		{
			name:         "returns none when no auth",
			cfg:          configWithAuth(nil),
			wantStatus:   AuthStatusNone,
			wantAuthType: "",
		},
		{
			name: "returns configured when OAuth set but no cached tokens",
			cfg: configWithAuth(&config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			}),
			wantStatus:   AuthStatusConfigured,
			wantAuthType: config.RegistryAuthTypeOAuth,
		},
		{
			name: "returns authenticated when OAuth set with cached tokens",
			cfg: configWithAuth(&config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:                "https://auth.example.com",
					ClientID:              "my-client",
					CachedRefreshTokenRef: "REGISTRY_OAUTH_aabbccdd",
				},
			}),
			wantStatus:   AuthStatusAuthenticated,
			wantAuthType: config.RegistryAuthTypeOAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := configmocks.NewMockProvider(ctrl)
			mockProvider.EXPECT().GetConfig().Return(tt.cfg)

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
		name       string
		cfg        *config.Config
		wantConfig *OAuthPublicConfig
	}{
		{
			name:       "returns nil when no auth",
			cfg:        configWithAuth(nil),
			wantConfig: nil,
		},
		{
			name: "returns config with all fields populated",
			cfg: configWithAuth(&config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
					Audience: "api://toolhive",
					Scopes:   []string{"openid", "profile"},
				},
			}),
			wantConfig: &OAuthPublicConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client",
				Audience: "api://toolhive",
				Scopes:   []string{"openid", "profile"},
			},
		},
		{
			name: "excludes cached token fields",
			cfg: configWithAuth(&config.RegistryAuth{
				Type: config.RegistryAuthTypeOAuth,
				OAuth: &config.RegistryOAuthConfig{
					Issuer:                "https://auth.example.com",
					ClientID:              "my-client",
					CachedRefreshTokenRef: "REGISTRY_OAUTH_aabbccdd",
				},
			}),
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
			mockProvider.EXPECT().GetConfig().Return(tt.cfg)

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

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
)

// --- helpers ---

// oauthConfig returns a minimal valid OAuth config for tests.
func oauthConfig() *config.RegistryOAuthConfig {
	return &config.RegistryOAuthConfig{
		Issuer:   "https://auth.example.com",
		ClientID: "test-client",
	}
}

// configWithOAuth returns a Config that has OAuth fully configured on a "default" registry.
func configWithOAuth() *config.Config {
	return &config.Config{
		Registries: []config.RegistrySource{
			{
				Name:     "default",
				Type:     config.RegistrySourceTypeAPI,
				Location: "https://api.registry.example.com",
				Auth: &config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: oauthConfig(),
				},
			},
		},
		DefaultRegistry: "default",
	}
}

// --- findDefaultOAuthConfig ---

func TestFindDefaultOAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.Config
		wantNil bool
	}{
		{
			name:    "valid oauth on default registry",
			cfg:     configWithOAuth(),
			wantNil: false,
		},
		{
			name:    "no registries",
			cfg:     &config.Config{},
			wantNil: true,
		},
		{
			name: "registry exists but no auth",
			cfg: &config.Config{
				Registries: []config.RegistrySource{
					{Name: "default", Type: config.RegistrySourceTypeAPI, Location: "https://api.example.com"},
				},
				DefaultRegistry: "default",
			},
			wantNil: true,
		},
		{
			name: "registry with non-oauth auth type",
			cfg: &config.Config{
				Registries: []config.RegistrySource{
					{
						Name:     "default",
						Type:     config.RegistrySourceTypeAPI,
						Location: "https://api.example.com",
						Auth: &config.RegistryAuth{
							Type: "basic",
						},
					},
				},
				DefaultRegistry: "default",
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := findDefaultOAuthConfig(tt.cfg)
			if tt.wantNil {
				require.Nil(t, result)
			} else {
				require.NotNil(t, result)
			}
		})
	}
}

// --- registryURLFromConfig ---

func TestRegistryURLFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "returns default registry location",
			cfg: &config.Config{
				Registries: []config.RegistrySource{
					{Name: "default", Type: config.RegistrySourceTypeAPI, Location: "https://api.example.com"},
				},
				DefaultRegistry: "default",
			},
			want: "https://api.example.com",
		},
		{
			name: "falls back to embedded when no default set",
			cfg: &config.Config{
				Registries: []config.RegistrySource{
					{Name: "embedded", Type: config.RegistrySourceTypeFile, Location: "/tmp/embedded.json"},
				},
			},
			want: "/tmp/embedded.json",
		},
		{
			name: "empty when no registries",
			cfg:  &config.Config{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := registryURLFromConfig(tt.cfg)
			require.Equal(t, tt.want, got)
		})
	}
}

// --- checkMissingLoginConfig ---

func TestCheckMissingLoginConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *config.Config
		opts     LoginOptions
		wantErr  bool
		wantMsgs []string
	}{
		{
			name:    "all config present - no error",
			cfg:     configWithOAuth(),
			opts:    LoginOptions{},
			wantErr: false,
		},
		{
			name: "all opts provided when config empty - no error",
			cfg:  &config.Config{},
			opts: LoginOptions{
				RegistryURL: "https://api.example.com",
				Issuer:      "https://auth.example.com",
				ClientID:    "my-client",
			},
			wantErr: false,
		},
		{
			name:    "nothing configured and no opts - all three missing",
			cfg:     &config.Config{},
			opts:    LoginOptions{},
			wantErr: true,
			wantMsgs: []string{
				"--registry",
				"--issuer",
				"--client-id",
			},
		},
		{
			name: "registry configured but no oauth",
			cfg: &config.Config{
				Registries: []config.RegistrySource{
					{Name: "default", Type: config.RegistrySourceTypeAPI, Location: "https://api.example.com"},
				},
				DefaultRegistry: "default",
			},
			opts:    LoginOptions{},
			wantErr: true,
			wantMsgs: []string{
				"--issuer",
				"--client-id",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkMissingLoginConfig(tt.cfg, tt.opts)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorIs(t, err, ErrRegistryAuthRequired)
			for _, msg := range tt.wantMsgs {
				require.Contains(t, err.Error(), msg)
			}
		})
	}
}

// --- ensureRegistryURL ---

func TestEnsureRegistryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      LoginOptions
		setupMock func(m *configmocks.MockProvider)
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "empty opts - noop",
			opts:      LoginOptions{},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   false,
		},
		{
			name: "URL opts - clears auth then adds registry",
			opts: LoginOptions{RegistryURL: "https://registry.example.com/mcp.json"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().UpdateConfig(gomock.Any()).Return(nil)    // clear auth
				m.EXPECT().AddRegistry(gomock.Any()).Return(nil)     // add registry
				m.EXPECT().SetDefaultRegistry("default").Return(nil) // set default
			},
			wantErr: false,
		},
		{
			name: "UpdateConfig error when clearing auth",
			opts: LoginOptions{RegistryURL: "https://registry.example.com/mcp.json"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().UpdateConfig(gomock.Any()).Return(errors.New("disk full"))
			},
			wantErr: true,
			errMsg:  "clearing stale auth config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockCfg := configmocks.NewMockProvider(ctrl)
			tt.setupMock(mockCfg)

			err := ensureRegistryURL(mockCfg, tt.opts)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tt.errMsg != "" {
				require.Contains(t, err.Error(), tt.errMsg)
			}
		})
	}
}

// --- clearRegistryCache ---

func TestClearRegistryCache(t *testing.T) {
	t.Parallel()

	t.Run("empty URL is a noop", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, clearRegistryCache(""))
	})

	t.Run("no error when cache file does not exist", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, clearRegistryCache("https://no-cache-ever.test.example.com"))
	})
}

// --- Login error paths ---

func TestLogin_ConfigLoadError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(nil, errors.New("corrupt config"))

	err := Login(context.Background(), mockCfg, nil, LoginOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "loading config")
}

func TestLogin_RejectsFileOnlyRegistries(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(&config.Config{
		Registries: []config.RegistrySource{
			{Name: "local", Type: config.RegistrySourceTypeFile, Location: "/tmp/registry.json"},
		},
		DefaultRegistry: "local",
	}, nil)

	err := Login(context.Background(), mockCfg, nil, LoginOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported for local file registries")
}

func TestLogin_MissingConfig(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(&config.Config{}, nil)

	err := Login(context.Background(), mockCfg, nil, LoginOptions{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRegistryAuthRequired)
}

// --- Logout ---

func TestLogout(t *testing.T) {
	t.Parallel()

	t.Run("config load error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		mockCfg := configmocks.NewMockProvider(ctrl)
		mockCfg.EXPECT().LoadOrCreateConfig().Return(nil, errors.New("read error"))

		err := Logout(context.Background(), mockCfg, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "loading config")
	})

	t.Run("no oauth configured", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		mockCfg := configmocks.NewMockProvider(ctrl)
		mockCfg.EXPECT().LoadOrCreateConfig().Return(&config.Config{}, nil)

		err := Logout(context.Background(), mockCfg, nil)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrRegistryAuthRequired)
	})
}

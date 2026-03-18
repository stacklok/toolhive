// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// --- helpers ---

// oauthConfig returns a minimal valid OAuth config for tests.
func oauthConfig() *config.RegistryOAuthConfig {
	return &config.RegistryOAuthConfig{
		Issuer:   "https://auth.example.com",
		ClientID: "test-client",
	}
}

// configWithOAuth returns a Config that has OAuth fully configured.
func configWithOAuth() *config.Config {
	return &config.Config{
		RegistryApiUrl: "https://api.registry.example.com",
		RegistryAuth: config.RegistryAuth{
			Type:  config.RegistryAuthTypeOAuth,
			OAuth: oauthConfig(),
		},
	}
}

// --- validateOAuthConfig ---

func TestValidateOAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{
			name:    "valid oauth config",
			cfg:     configWithOAuth(),
			wantErr: false,
		},
		{
			name: "wrong auth type",
			cfg: &config.Config{
				RegistryAuth: config.RegistryAuth{
					Type:  "basic",
					OAuth: oauthConfig(),
				},
			},
			wantErr: true,
		},
		{
			name: "nil oauth pointer",
			cfg: &config.Config{
				RegistryAuth: config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: nil,
				},
			},
			wantErr: true,
		},
		{
			name:    "empty auth type and nil oauth",
			cfg:     &config.Config{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateOAuthConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrRegistryAuthRequired)
			} else {
				require.NoError(t, err)
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
			name: "prefers RegistryApiUrl",
			cfg: &config.Config{
				RegistryApiUrl: "https://api.example.com",
				RegistryUrl:    "https://static.example.com",
			},
			want: "https://api.example.com",
		},
		{
			name: "falls back to RegistryUrl",
			cfg: &config.Config{
				RegistryUrl: "https://static.example.com",
			},
			want: "https://static.example.com",
		},
		{
			name: "both empty returns empty string",
			cfg:  &config.Config{},
			want: "",
		},
		{
			name: "only RegistryApiUrl set",
			cfg: &config.Config{
				RegistryApiUrl: "https://api-only.example.com",
			},
			want: "https://api-only.example.com",
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
		name    string
		cfg     *config.Config
		opts    LoginOptions
		wantErr bool
		// If wantErr is true, wantMsgs lists substrings that must appear in the error.
		wantMsgs []string
	}{
		{
			name: "all config present - no error",
			cfg: &config.Config{
				RegistryApiUrl: "https://api.example.com",
				RegistryAuth: config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: oauthConfig(),
				},
			},
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
			name: "registry configured but no oauth and no opts",
			cfg: &config.Config{
				RegistryApiUrl: "https://api.example.com",
			},
			opts:    LoginOptions{},
			wantErr: true,
			wantMsgs: []string{
				"--issuer",
				"--client-id",
			},
		},
		{
			name:    "opts supply registry but not oauth",
			cfg:     &config.Config{},
			opts:    LoginOptions{RegistryURL: "https://r.example.com"},
			wantErr: true,
			wantMsgs: []string{
				"--issuer",
				"--client-id",
			},
		},
		{
			name:    "opts supply issuer only - missing registry and client-id",
			cfg:     &config.Config{},
			opts:    LoginOptions{Issuer: "https://auth.example.com"},
			wantErr: true,
			wantMsgs: []string{
				"--registry",
				"--client-id",
			},
		},
		{
			name: "RegistryUrl satisfies registry requirement",
			cfg: &config.Config{
				RegistryUrl: "https://static.example.com",
				RegistryAuth: config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: oauthConfig(),
				},
			},
			opts:    LoginOptions{},
			wantErr: false,
		},
		{
			name: "LocalRegistryPath satisfies registry requirement",
			cfg: &config.Config{
				LocalRegistryPath: "/tmp/registry.json",
				RegistryAuth: config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: oauthConfig(),
				},
			},
			opts:    LoginOptions{},
			wantErr: false,
		},
		{
			name: "oauth type set but OAuth pointer nil counts as missing",
			cfg: &config.Config{
				RegistryApiUrl: "https://api.example.com",
				RegistryAuth: config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: nil,
				},
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
		cfg       *config.Config
		opts      LoginOptions
		setupMock func(m *configmocks.MockProvider)
		wantErr   bool
		errMsg    string
	}{
		{
			name: "already has RegistryApiUrl - noop",
			cfg: &config.Config{
				RegistryApiUrl: "https://api.example.com",
			},
			opts:      LoginOptions{},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   false,
		},
		{
			name: "already has RegistryUrl - noop",
			cfg: &config.Config{
				RegistryUrl: "https://static.example.com",
			},
			opts:      LoginOptions{},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   false,
		},
		{
			name: "already has LocalRegistryPath - noop",
			cfg: &config.Config{
				LocalRegistryPath: "/path/to/registry.json",
			},
			opts:      LoginOptions{},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   false,
		},
		{
			name:      "no config and no opts - error",
			cfg:       &config.Config{},
			opts:      LoginOptions{},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   true,
			errMsg:    "no registry URL configured",
		},
		{
			name: "opts supply JSON URL - calls SetRegistryURL",
			cfg:  &config.Config{},
			opts: LoginOptions{RegistryURL: "https://registry.example.com/mcp.json"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().SetRegistryURL(gomock.Any(), false).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "opts supply file path - calls SetRegistryFile",
			cfg:  &config.Config{},
			opts: LoginOptions{RegistryURL: "file:///tmp/registry.json"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().SetRegistryFile("/tmp/registry.json").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "SetRegistryURL returns error",
			cfg:  &config.Config{},
			opts: LoginOptions{RegistryURL: "https://registry.example.com/mcp.json"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().SetRegistryURL(gomock.Any(), false).Return(errors.New("disk full"))
			},
			wantErr: true,
			errMsg:  "saving registry URL",
		},
		{
			name: "SetRegistryFile returns error",
			cfg:  &config.Config{},
			opts: LoginOptions{RegistryURL: "file:///tmp/registry.json"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().SetRegistryFile("/tmp/registry.json").Return(errors.New("permission denied"))
			},
			wantErr: true,
			errMsg:  "saving registry file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockCfg := configmocks.NewMockProvider(ctrl)
			tt.setupMock(mockCfg)

			err := ensureRegistryURL(tt.cfg, mockCfg, tt.opts)
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

// --- ensureOAuthConfig ---

func TestEnsureOAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		cfg              *config.Config
		opts             LoginOptions
		setupMock        func(m *configmocks.MockProvider)
		useOIDC          bool // whether to start the test OIDC server
		overrideScopes   []string
		overrideAudience string
		wantErr          bool
		errMsg           string
	}{
		{
			name:      "already configured - noop",
			cfg:       configWithOAuth(),
			opts:      LoginOptions{},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   false,
		},
		{
			name:      "no oauth and no issuer in opts",
			cfg:       &config.Config{},
			opts:      LoginOptions{ClientID: "my-client"},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   true,
			errMsg:    "OAuth config missing",
		},
		{
			name:      "no oauth and no client-id in opts",
			cfg:       &config.Config{},
			opts:      LoginOptions{Issuer: "https://auth.example.com"},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   true,
			errMsg:    "OAuth config missing",
		},
		{
			name: "OIDC discovery fails for bad issuer",
			cfg:  &config.Config{},
			opts: LoginOptions{
				Issuer:   "https://this-does-not-exist.invalid",
				ClientID: "my-client",
			},
			setupMock: func(_ *configmocks.MockProvider) {},
			wantErr:   true,
			errMsg:    "OIDC discovery failed",
		},
		{
			name:    "valid opts with OIDC server - saves config",
			cfg:     &config.Config{},
			useOIDC: true,
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().UpdateConfig(gomock.Any()).DoAndReturn(func(fn func(*config.Config)) error {
					c := &config.Config{}
					fn(c)
					// Verify the update function sets expected values.
					require.Equal(t, config.RegistryAuthTypeOAuth, c.RegistryAuth.Type)
					require.NotNil(t, c.RegistryAuth.OAuth)
					require.Equal(t, "my-client", c.RegistryAuth.OAuth.ClientID)
					require.Equal(t, []string{"openid", "offline_access"}, c.RegistryAuth.OAuth.Scopes)
					require.Equal(t, remote.DefaultCallbackPort, c.RegistryAuth.OAuth.CallbackPort)
					return nil
				})
			},
			wantErr: false,
		},
		{
			name:           "custom scopes override defaults",
			cfg:            &config.Config{},
			useOIDC:        true,
			overrideScopes: []string{"openid", "email"},
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().UpdateConfig(gomock.Any()).DoAndReturn(func(fn func(*config.Config)) error {
					c := &config.Config{}
					fn(c)
					require.Equal(t, []string{"openid", "email"}, c.RegistryAuth.OAuth.Scopes)
					return nil
				})
			},
			wantErr: false,
		},
		{
			name:             "audience is passed through",
			cfg:              &config.Config{},
			useOIDC:          true,
			overrideAudience: "api://my-api",
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().UpdateConfig(gomock.Any()).DoAndReturn(func(fn func(*config.Config)) error {
					c := &config.Config{}
					fn(c)
					require.Equal(t, "api://my-api", c.RegistryAuth.OAuth.Audience)
					return nil
				})
			},
			wantErr: false,
		},
		{
			name:    "UpdateConfig returns error",
			cfg:     &config.Config{},
			useOIDC: true,
			setupMock: func(m *configmocks.MockProvider) {
				m.EXPECT().UpdateConfig(gomock.Any()).Return(errors.New("permission denied"))
			},
			wantErr: true,
			errMsg:  "permission denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockCfg := configmocks.NewMockProvider(ctrl)
			tt.setupMock(mockCfg)

			opts := tt.opts
			if tt.useOIDC {
				srv := newOIDCTestServer(t)
				if opts.Issuer == "" {
					opts.Issuer = srv.URL
				}
				if opts.ClientID == "" {
					opts.ClientID = "my-client"
				}
				if len(tt.overrideScopes) > 0 {
					opts.Scopes = tt.overrideScopes
				}
				if tt.overrideAudience != "" {
					opts.Audience = tt.overrideAudience
				}
			}

			err := ensureOAuthConfig(context.Background(), tt.cfg, mockCfg, opts)
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
		// Use a URL that will not have a matching cache file.
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

func TestLogin_RejectsLocalRegistryPath(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(&config.Config{
		LocalRegistryPath: "/tmp/registry.json",
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

// TestLogout_DeletesCachedToken cannot be parallel because it uses t.Setenv.
func TestLogout_DeletesCachedToken(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := configWithOAuth()
	cfg.RegistryAuth.OAuth.CachedRefreshTokenRef = "my-token-ref"
	cfg.RegistryAuth.OAuth.CachedTokenExpiry = time.Now().Add(time.Hour)

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(cfg, nil)

	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	mockSecrets.EXPECT().DeleteSecret(gomock.Any(), "my-token-ref").Return(nil)
	// Derived key fallback: DeriveSecretKey(registryURL, issuer) differs from "my-token-ref".
	derivedKey := DeriveSecretKey(cfg.RegistryApiUrl, cfg.RegistryAuth.OAuth.Issuer)
	mockSecrets.EXPECT().DeleteSecret(gomock.Any(), derivedKey).Return(nil)

	mockCfg.EXPECT().UpdateConfig(gomock.Any()).DoAndReturn(func(fn func(*config.Config)) error {
		fn(cfg)
		require.Empty(t, cfg.RegistryAuth.OAuth.CachedRefreshTokenRef)
		require.True(t, cfg.RegistryAuth.OAuth.CachedTokenExpiry.IsZero())
		return nil
	})

	require.NoError(t, Logout(context.Background(), mockCfg, mockSecrets))
}

// TestLogout_NoCachedRefSkipsDelete cannot be parallel because it uses t.Setenv.
func TestLogout_NoCachedRefSkipsDelete(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := configWithOAuth()
	cfg.RegistryAuth.OAuth.CachedRefreshTokenRef = ""

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(cfg, nil)

	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	// No CachedRefreshTokenRef, but derived key fallback fires.
	derivedKey := DeriveSecretKey(cfg.RegistryApiUrl, cfg.RegistryAuth.OAuth.Issuer)
	mockSecrets.EXPECT().DeleteSecret(gomock.Any(), derivedKey).Return(nil)

	mockCfg.EXPECT().UpdateConfig(gomock.Any()).Return(nil)

	require.NoError(t, Logout(context.Background(), mockCfg, mockSecrets))
}

// TestLogout_DeleteSecretError cannot be parallel because it uses t.Setenv.
func TestLogout_DeleteSecretError(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := configWithOAuth()
	cfg.RegistryAuth.OAuth.CachedRefreshTokenRef = "token-ref"

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(cfg, nil)

	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	mockSecrets.EXPECT().DeleteSecret(gomock.Any(), "token-ref").Return(errors.New("vault locked"))

	err := Logout(context.Background(), mockCfg, mockSecrets)
	require.Error(t, err)
	require.Contains(t, err.Error(), "deleting cached token")
}

// TestLogout_UpdateConfigError cannot be parallel because it uses t.Setenv.
func TestLogout_UpdateConfigError(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := configWithOAuth()

	ctrl := gomock.NewController(t)
	mockCfg := configmocks.NewMockProvider(ctrl)
	mockCfg.EXPECT().LoadOrCreateConfig().Return(cfg, nil)

	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	// Derived key fallback fires since CachedRefreshTokenRef is empty.
	derivedKey := DeriveSecretKey(cfg.RegistryApiUrl, cfg.RegistryAuth.OAuth.Issuer)
	mockSecrets.EXPECT().DeleteSecret(gomock.Any(), derivedKey).Return(nil)

	mockCfg.EXPECT().UpdateConfig(gomock.Any()).Return(errors.New("write failed"))

	err := Logout(context.Background(), mockCfg, mockSecrets)
	require.Error(t, err)
	require.Contains(t, err.Error(), "write failed")
}

// --- resolvedTempDir helper ---

// resolvedTempDir creates a temp directory and resolves any symlinks in the
// path. On macOS, t.TempDir() often returns paths through /var which is a
// symlink to /private/var, causing issues with validators that reject symlinks.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/llmgateway"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestConfig_SetFields_Scopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		base          []string
		opts          []string
		wantStored    []string
		wantEffective []string
	}{
		{
			name: "nil leaves existing scopes", base: []string{"email"}, opts: nil,
			wantStored: []string{"email"}, wantEffective: []string{"email"},
		},
		{
			name: "offline_access comes first, then the extras", opts: []string{"toolhive:config:read"},
			wantStored:    []string{"offline_access", "toolhive:config:read"},
			wantEffective: []string{"offline_access", "toolhive:config:read"},
		},
		{
			name: "openid is not forced", opts: []string{"a"},
			wantStored: []string{"offline_access", "a"}, wantEffective: []string{"offline_access", "a"},
		},
		{
			name: "openid is kept when passed", opts: []string{"openid", "a"},
			wantStored:    []string{"offline_access", "openid", "a"},
			wantEffective: []string{"offline_access", "openid", "a"},
		},
		{
			name: "duplicates, a repeated offline_access and blanks are dropped", opts: []string{"offline_access", "a", "a", " "},
			wantStored: []string{"offline_access", "a"}, wantEffective: []string{"offline_access", "a"},
		},
		{
			name: "re-running replaces the earlier extras", base: []string{"offline_access", "old"}, opts: []string{"new"},
			wantStored: []string{"offline_access", "new"}, wantEffective: []string{"offline_access", "new"},
		},
		{
			name: "empty value stores nil so the defaults apply", base: []string{"offline_access", "old"}, opts: []string{},
			wantStored: nil, wantEffective: []string{"openid", "offline_access"},
		},
		{
			name: "blank-only value is a reset too", base: []string{"offline_access", "old"}, opts: []string{" "},
			wantStored: nil, wantEffective: []string{"openid", "offline_access"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{OIDC: OIDCConfig{Scopes: tt.base}}
			require.NoError(t, cfg.SetFields(SetOptions{Scopes: tt.opts}))
			assert.Equal(t, tt.wantStored, cfg.OIDC.Scopes)
			assert.Equal(t, tt.wantEffective, cfg.OIDC.EffectiveScopes())
		})
	}
}

func TestScopesChanged(t *testing.T) {
	t.Parallel()

	cfgWith := func(scopes ...string) Config { return Config{OIDC: OIDCConfig{Scopes: scopes}} }

	assert.False(t, scopesChanged(cfgWith(), cfgWith()))
	assert.False(t, scopesChanged(cfgWith(), cfgWith("offline_access", "openid")),
		"the same set as the defaults, in another order, is not a change")
	assert.True(t, scopesChanged(cfgWith(), cfgWith("offline_access", "openid", "a")))
	assert.True(t, scopesChanged(cfgWith("offline_access", "a"), cfgWith()))
}

// secretsFixture returns a secrets provider mock that lists one LLM token, and
// a SecretsProviderFunc handing it out. onDelete runs when the token is deleted.
func secretsFixture(t *testing.T, onDelete func()) SecretsProviderFunc {
	t.Helper()
	key := secrets.SystemKeyPrefix + string(secrets.ScopeLLM) + "_refresh_token"
	mock := secretsmocks.NewMockProvider(gomock.NewController(t))
	mock.EXPECT().Capabilities().Return(
		secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: true}).AnyTimes()
	mock.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{{Key: key}}, nil)
	mock.EXPECT().DeleteSecrets(gomock.Any(), []string{key}).DoAndReturn(
		func(context.Context, []string) error {
			if onDelete != nil {
				onDelete()
			}
			return nil
		})
	return func() (secrets.Provider, error) { return mock, nil }
}

// secretsNeverUsed fails the test if anything asks for the secrets provider.
func secretsNeverUsed(t *testing.T) SecretsProviderFunc {
	t.Helper()
	return func() (secrets.Provider, error) {
		t.Error("the secrets provider must not be used when the cached tokens are kept")
		return nil, errors.New("unexpected secrets provider use")
	}
}

func cachedTokenProvider() *stubConfigUpdater {
	p := configuredSetupProvider()
	p.cfg.OIDC.CachedRefreshTokenRef = "LLM_OAUTH_12345678"
	p.cfg.OIDC.CachedTokenExpiry = time.Now().Add(time.Hour)
	return p
}

func TestConfigSet_ScopeChange(t *testing.T) {
	t.Parallel()

	t.Run("a changed list persists, clears the tokens and the reference, and warns", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()
		getSecrets := secretsFixture(t, nil)
		var errOut bytes.Buffer

		err := ConfigSet(context.Background(), &errOut, provider, SetOptions{Scopes: []string{"openid", "a"}}, getSecrets)

		require.NoError(t, err)
		assert.Equal(t, []string{"offline_access", "openid", "a"}, provider.cfg.OIDC.Scopes)
		assert.Empty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
		assert.True(t, provider.cfg.OIDC.CachedTokenExpiry.IsZero())
		assert.Contains(t, errOut.String(), "Scopes changed")
		assert.Contains(t, errOut.String(), "Stop any running LLM proxy or token helper")
	})

	t.Run("the same scope set keeps the session", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()
		var errOut bytes.Buffer

		err := ConfigSet(context.Background(), &errOut, provider,
			SetOptions{Scopes: []string{"openid"}}, secretsNeverUsed(t))

		require.NoError(t, err)
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
		assert.Empty(t, errOut.String())
	})

	t.Run("no scopes flag keeps the session", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()

		err := ConfigSet(context.Background(), &bytes.Buffer{}, provider,
			SetOptions{Audience: "aud"}, secretsNeverUsed(t))

		require.NoError(t, err)
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	})

	t.Run("a rejected value keeps the tokens", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()
		provider.updateErr = errors.New("validation failed")

		err := ConfigSet(context.Background(), &bytes.Buffer{}, provider,
			SetOptions{Scopes: []string{"a"}}, secretsNeverUsed(t))

		require.ErrorContains(t, err, "validation failed")
	})

	t.Run("an invalid companion flag keeps the tokens", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()

		err := ConfigSet(context.Background(), &bytes.Buffer{}, provider,
			SetOptions{Scopes: []string{"a"}, GatewayURL: "http://not-https.example.com"}, secretsNeverUsed(t))

		require.Error(t, err)
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	})

	t.Run("a secrets provider failure is reported", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()

		err := ConfigSet(context.Background(), &bytes.Buffer{}, provider, SetOptions{Scopes: []string{"a"}},
			func() (secrets.Provider, error) { return nil, errors.New("keyring locked") })

		require.ErrorContains(t, err, "keyring locked")
	})
}

func TestSetup_ScopeChange(t *testing.T) {
	t.Parallel()

	run := func(gm *setupGatewayManager, provider *stubConfigUpdater, opts SetOptions, client string,
		login LoginFunc, getSecrets SecretsProviderFunc,
	) (string, error) {
		var out, errOut bytes.Buffer
		// anthropicPathPrefixSet=true skips the network probe.
		err := Setup(context.Background(), &out, &errOut, gm, provider, login, opts, "", true, client, false, getSecrets)
		return errOut.String(), err
	}
	cursor := func() *setupGatewayManager { return &setupGatewayManager{detected: []string{"cursor"}, mode: "proxy"} }

	t.Run("tokens are cleared before login so login cannot reuse the old session", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()
		deleted := false
		getSecrets := secretsFixture(t, func() { deleted = true })
		login := func(context.Context, *Config) error {
			assert.True(t, deleted, "the cached tokens must be gone before login runs")
			assert.Empty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
			return nil
		}

		errOut, err := run(cursor(), provider, SetOptions{Scopes: []string{"a"}}, "", login, getSecrets)

		require.NoError(t, err)
		assert.True(t, deleted)
		assert.Equal(t, []string{"offline_access", "a"}, provider.cfg.OIDC.Scopes)
		assert.Contains(t, errOut, "Scopes changed")
	})

	t.Run("an unchanged scope set keeps the session", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()

		_, err := run(cursor(), provider, SetOptions{Scopes: []string{"openid"}}, "",
			func(context.Context, *Config) error { return nil }, secretsNeverUsed(t))

		require.NoError(t, err)
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	})

	t.Run("invalid inline flags keep the tokens", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()

		_, err := run(cursor(), provider,
			SetOptions{Scopes: []string{"a"}, GatewayURL: "http://not-https.example.com"}, "",
			func(context.Context, *Config) error { return nil }, secretsNeverUsed(t))

		require.ErrorContains(t, err, "invalid inline flag values")
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	})

	t.Run("no tools to configure keeps the tokens", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()
		loginCalled := false

		_, err := run(&setupGatewayManager{mode: "proxy"}, provider, SetOptions{Scopes: []string{"a"}}, "",
			func(context.Context, *Config) error { loginCalled = true; return nil }, secretsNeverUsed(t))

		require.NoError(t, err)
		assert.False(t, loginCalled)
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
		assert.NotEqual(t, []string{"offline_access", "a"}, provider.cfg.OIDC.Scopes, "the scopes are not persisted either")
	})

	t.Run("a client that is not installed keeps the tokens", func(t *testing.T) {
		t.Parallel()
		provider := cachedTokenProvider()

		_, err := run(&setupGatewayManager{mode: llmgateway.ModeVSCode}, provider, SetOptions{Scopes: []string{"a"}},
			"cursor", func(context.Context, *Config) error { return nil }, secretsNeverUsed(t))

		require.Error(t, err)
		assert.NotEmpty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	})
}

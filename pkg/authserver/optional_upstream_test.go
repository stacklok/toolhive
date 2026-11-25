// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// toggleableIssuer is an OIDC issuer whose discovery endpoint answers 503
// until up is set.
type toggleableIssuer struct {
	*httptest.Server
	up atomic.Bool
}

func newToggleableIssuer(t *testing.T, up bool) *toggleableIssuer {
	t.Helper()
	ti := &toggleableIssuer{}
	ti.up.Store(up)
	ti.Server = httptest.NewUnstartedServer(nil)
	discovery := oidcDiscoveryHandler(func() string { return ti.URL })
	ti.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ti.up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		discovery.ServeHTTP(w, r)
	})
	ti.Start()
	t.Cleanup(ti.Close)
	return ti
}

func optionalOIDCUpstreamConfig(name, issuerURL string, required *bool) UpstreamConfig {
	return UpstreamConfig{
		Name:     name,
		Type:     UpstreamProviderTypeOIDC,
		Required: required,
		OIDCConfig: &upstream.OIDCConfig{
			CommonOAuthConfig: upstream.CommonOAuthConfig{
				ClientID:    "test-client",
				RedirectURI: "https://example.com/callback",
				Scopes:      []string{"openid"},
			},
			Issuer:            issuerURL,
			InsecureAllowHTTP: true,
			AllowPrivateIPs:   true,
		},
	}
}

// primaryOAuth2UpstreamConfig is a first upstream that needs no network at
// construction, so only the optional upstream's issuer decides the outcome.
func primaryOAuth2UpstreamConfig() UpstreamConfig {
	return UpstreamConfig{
		Name: "primary",
		Type: UpstreamProviderTypeOAuth2,
		OAuth2Config: &upstream.OAuth2Config{
			CommonOAuthConfig: upstream.CommonOAuthConfig{
				ClientID:    "primary-client",
				RedirectURI: "https://example.com/callback",
			},
			AuthorizationEndpoint: "https://idp.example.com/authorize",
			TokenEndpoint:         "https://idp.example.com/token",
		},
	}
}

func boolPtr(b bool) *bool { return &b }

func TestNewServer_OptionalUpstreamAtStartup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		required      *bool
		issuerUp      bool
		wantErr       string
		wantAvailable bool
	}{
		{
			name:     "required upstream with failed discovery stops startup",
			required: nil,
			issuerUp: false,
			wantErr:  `failed to create upstream provider "optional"`,
		},
		{
			name:     "explicitly required upstream with failed discovery stops startup",
			required: boolPtr(true),
			issuerUp: false,
			wantErr:  `failed to create upstream provider "optional"`,
		},
		{
			name:          "optional upstream with failed discovery starts unavailable",
			required:      boolPtr(false),
			issuerUp:      false,
			wantAvailable: false,
		},
		{
			name:          "optional upstream with working discovery starts available",
			required:      boolPtr(false),
			issuerUp:      true,
			wantAvailable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			issuer := newToggleableIssuer(t, tt.issuerUp)
			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })

			srv, err := newServer(t.Context(), Config{
				Issuer:      "https://example.com",
				KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
				HMACSecrets: &servercrypto.HMACSecrets{Current: validHMACSecret()},
				Upstreams: []UpstreamConfig{
					primaryOAuth2UpstreamConfig(),
					optionalOIDCUpstreamConfig("optional", issuer.URL, tt.required),
				},
				AllowedAudiences: []string{"https://mcp.example.com"},
			}, stor)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = srv.Close() })

			assert.Equal(t, []UpstreamAvailability{{Name: "optional", Available: tt.wantAvailable}},
				OptionalUpstreams(srv), "only the optional upstream is listed")

			_, err = srv.upstreams[1].Provider.AuthorizationURL("state", "challenge")
			if tt.wantAvailable {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, upstream.ErrUpstreamUnavailable)
			}

			// Close stops a pending retry instead of waiting out its backoff.
			closed := make(chan error, 1)
			go func() { closed <- srv.Close() }()
			select {
			case err := <-closed:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not return; the optional upstream retry was not stopped")
			}
		})
	}
}

func TestOptionalUpstream_RetryPublishesProviderOnceDiscoverySucceeds(t *testing.T) {
	t.Parallel()

	issuer := newToggleableIssuer(t, false)
	cfg := optionalOIDCUpstreamConfig("optional", issuer.URL, boolPtr(false))
	o := newOptionalUpstream(&cfg, DefaultUpstreamFactory, nil)
	o.initialInterval = 10 * time.Millisecond
	o.maxInterval = 20 * time.Millisecond

	retries := startOptionalRetries([]handlers.NamedUpstream{{Name: "optional", Provider: o}})
	require.NotNil(t, retries)
	t.Cleanup(retries.stop)

	// Still unavailable while discovery keeps failing.
	time.Sleep(50 * time.Millisecond)
	require.False(t, o.available())
	_, err := o.AuthorizationURL("state", "challenge")
	require.ErrorIs(t, err, upstream.ErrUpstreamUnavailable)

	issuer.up.Store(true)
	require.Eventually(t, o.available, 5*time.Second, 10*time.Millisecond,
		"the provider is published once discovery succeeds")

	authURL, err := o.AuthorizationURL("state", "challenge")
	require.NoError(t, err)
	assert.Contains(t, authURL, issuer.URL+"/auth")
	assert.Equal(t, upstream.ProviderTypeOIDC, o.Type())

	stopped := make(chan struct{})
	go func() { retries.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the retry goroutine did not exit after success")
	}
}

func TestStartOptionalRetries_NothingToRetryReturnsNil(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	cfg := optionalOIDCUpstreamConfig("optional", "https://unused.example.com", boolPtr(false))

	retries := startOptionalRetries([]handlers.NamedUpstream{
		{Name: "required", Provider: mocks.NewMockOAuth2Provider(ctrl)},
		{Name: "optional", Provider: newOptionalUpstream(&cfg, DefaultUpstreamFactory, mocks.NewMockOAuth2Provider(ctrl))},
	})

	assert.Nil(t, retries, "neither a required nor an available optional upstream is retried")
	retries.stop() // safe on nil
}

func TestOptionalUpstream_RetryDropsProviderBuiltAfterCancel(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ctx, cancel := context.WithCancel(context.Background())
	built := mocks.NewMockOAuth2Provider(ctrl)
	factory := func(context.Context, *UpstreamConfig) (upstream.OAuth2Provider, error) {
		cancel() // the server closes while this attempt is in flight
		return built, nil
	}
	cfg := optionalOIDCUpstreamConfig("optional", "https://unused.example.com", boolPtr(false))
	o := newOptionalUpstream(&cfg, factory, nil)
	o.initialInterval = time.Millisecond

	o.retry(ctx)

	assert.False(t, o.available(), "a provider built after cancellation must not be published")
}

func TestOptionalUpstream_Delegation(t *testing.T) {
	t.Parallel()

	cfg := optionalOIDCUpstreamConfig("optional", "https://unused.example.com", boolPtr(false))
	ctx := context.Background()
	wantIdentity := &upstream.Identity{Subject: "user"}
	wantTokens := &upstream.Tokens{AccessToken: "at"}

	t.Run("unavailable placeholder refuses every call", func(t *testing.T) {
		t.Parallel()
		o := newOptionalUpstream(&cfg, DefaultUpstreamFactory, nil)

		assert.Equal(t, upstream.ProviderTypeOIDC, o.Type())
		_, err := o.AuthorizationURL("state", "challenge")
		assert.ErrorIs(t, err, upstream.ErrUpstreamUnavailable)
		assert.ErrorContains(t, err, `"optional"`)
		_, err = o.ExchangeCodeForIdentity(ctx, "code", "verifier", "nonce")
		assert.ErrorIs(t, err, upstream.ErrUpstreamUnavailable)
		_, err = o.RefreshTokens(ctx, "rt", "subject")
		assert.ErrorIs(t, err, upstream.ErrUpstreamUnavailable)
		assert.ErrorIs(t, o.UpdateClientCredentials(upstream.ClientCredentials{ClientID: "c"}),
			upstream.ErrUpstreamUnavailable)
		o.CloseIdleConnections() // no provider, nothing to drain, must not panic
	})

	t.Run("resolved wrapper delegates every call", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		inner := mocks.NewMockOAuth2Provider(ctrl)
		inner.EXPECT().Type().Return(upstream.ProviderTypeOIDC)
		inner.EXPECT().AuthorizationURL("state", "challenge").Return("https://idp/auth", nil)
		inner.EXPECT().ExchangeCodeForIdentity(ctx, "code", "verifier", "nonce").Return(wantIdentity, nil)
		inner.EXPECT().RefreshTokens(ctx, "rt", "subject").Return(wantTokens, errors.New("refresh failed"))
		o := newOptionalUpstream(&cfg, DefaultUpstreamFactory, inner)

		assert.Equal(t, upstream.ProviderTypeOIDC, o.Type())
		authURL, err := o.AuthorizationURL("state", "challenge")
		require.NoError(t, err)
		assert.Equal(t, "https://idp/auth", authURL)
		identity, err := o.ExchangeCodeForIdentity(ctx, "code", "verifier", "nonce")
		require.NoError(t, err)
		assert.Same(t, wantIdentity, identity)
		tokens, err := o.RefreshTokens(ctx, "rt", "subject")
		assert.EqualError(t, err, "refresh failed", "errors pass through unchanged")
		assert.Same(t, wantTokens, tokens)
		assert.ErrorContains(t, o.UpdateClientCredentials(upstream.ClientCredentials{ClientID: "c"}),
			"cannot replace its client registration", "a provider without the capability refuses")
	})

	t.Run("resolved wrapper forwards client credentials to a capable provider", func(t *testing.T) {
		t.Parallel()
		issuer := newToggleableIssuer(t, true)
		realCfg := optionalOIDCUpstreamConfig("optional", issuer.URL, boolPtr(false))
		inner, err := DefaultUpstreamFactory(ctx, &realCfg)
		require.NoError(t, err)
		o := newOptionalUpstream(&realCfg, DefaultUpstreamFactory, inner)

		require.NoError(t, o.UpdateClientCredentials(upstream.ClientCredentials{ClientID: "replaced-client"}))
		authURL, err := o.AuthorizationURL("state", "challenge")
		require.NoError(t, err)
		assert.Contains(t, authURL, "client_id=replaced-client")
	})
}

func TestConfigValidate_RejectsOptionalFirstUpstream(t *testing.T) {
	t.Parallel()

	cfg := Config{Upstreams: []UpstreamConfig{
		optionalOIDCUpstreamConfig("first", "https://idp.example.com", boolPtr(false)),
		primaryOAuth2UpstreamConfig(),
	}}

	require.ErrorContains(t, cfg.validateUpstreams(),
		`upstream "first": required=false is not allowed on the first upstream`)
}

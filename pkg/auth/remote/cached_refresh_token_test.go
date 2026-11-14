// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/auth/oauth"
	secretmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestDecodeCachedRefreshToken_RejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		value      string
		wantLegacy bool
	}{
		{name: "legacy raw token", value: "legacy-refresh-token", wantLegacy: true},
		{name: "malformed envelope", value: `{"version":`},
		{name: "unsupported envelope version", value: `{"version":2,"token":"refresh","issuer":"https://issuer.example","token_url":"https://issuer.example/token"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envelope, err := decodeCachedRefreshToken(tt.value)
			require.Error(t, err)
			assert.Nil(t, envelope)
			assert.Equal(t, tt.wantLegacy, errors.Is(err, errLegacyCachedRefreshToken))
		})
	}
}

func TestHandler_TryRestoreFromCachedTokens(t *testing.T) {
	t.Parallel()

	t.Run("matching authorization server binding refreshes at token endpoint", func(t *testing.T) {
		t.Parallel()

		var receivedRefreshToken string
		tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, r.ParseForm())
			receivedRefreshToken = r.Form.Get("refresh_token")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer","expires_in":3600}`))
			require.NoError(t, err)
		}))
		t.Cleanup(tokenEndpoint.Close)

		cachedToken, err := encodeCachedRefreshToken("refresh-token", "https://issuer.example", tokenEndpoint.URL)
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		provider := secretmocks.NewMockProvider(ctrl)
		provider.EXPECT().GetSecret(gomock.Any(), "refresh-token-ref").Return(cachedToken, nil)

		handler := &Handler{
			config: &Config{
				ClientID:              "client-id",
				ClientSecret:          "client-secret",
				TokenURL:              tokenEndpoint.URL,
				CachedRefreshTokenRef: "refresh-token-ref",
			},
			secretProvider: provider,
		}

		tokenSource, err := handler.tryRestoreFromCachedTokens(t.Context(), "https://issuer.example", nil, nil)
		require.NoError(t, err)
		require.NotNil(t, tokenSource)
		assert.Equal(t, "refresh-token", receivedRefreshToken)
	})

	tests := []struct {
		name             string
		envelopeIssuer   string
		discoveredIssuer string
		envelopeEndpoint string
	}{
		{
			name:             "mismatched token endpoint",
			envelopeIssuer:   "https://issuer.example",
			discoveredIssuer: "https://issuer.example",
			envelopeEndpoint: "https://legitimate.example/token",
		},
		{
			name:             "cross authorization server issuer",
			envelopeIssuer:   "https://legitimate.example",
			discoveredIssuer: "https://attacker.example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var attackerRequests int
			attackerEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attackerRequests++
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(attackerEndpoint.Close)

			envelopeEndpoint := tt.envelopeEndpoint
			if envelopeEndpoint == "" {
				envelopeEndpoint = attackerEndpoint.URL
			}
			cachedToken, err := encodeCachedRefreshToken("refresh-token", tt.envelopeIssuer, envelopeEndpoint)
			require.NoError(t, err)

			ctrl := gomock.NewController(t)
			provider := secretmocks.NewMockProvider(ctrl)
			provider.EXPECT().GetSecret(gomock.Any(), "refresh-token-ref").Return(cachedToken, nil)

			handler := &Handler{
				config: &Config{
					TokenURL:              attackerEndpoint.URL,
					CachedRefreshTokenRef: "refresh-token-ref",
					CachedClientSecretRef: "client-secret-ref",
					CachedRegTokenRef:     "registration-token-ref",
					CachedRegClientURI:    attackerEndpoint.URL + "/register/client-id",
					CachedSecretExpiry:    time.Now().Add(time.Hour),
				},
				secretProvider: provider,
			}

			tokenSource, err := handler.tryRestoreFromCachedTokens(t.Context(), tt.discoveredIssuer, nil, nil)
			require.Error(t, err)
			assert.Nil(t, tokenSource)
			assert.Zero(t, attackerRequests)
		})
	}
}

func TestHandler_TokenPersisterFor_StoresAuthorizationServerBinding(t *testing.T) {
	t.Parallel()

	var persistedValue string
	handler := &Handler{
		tokenPersister: func(value string, _ time.Time) error {
			persistedValue = value
			return nil
		},
	}

	persister := handler.tokenPersisterFor("https://issuer.example", "https://issuer.example/token")
	require.NotNil(t, persister)
	require.NoError(t, persister("refresh-token", time.Now().Add(time.Hour)))

	envelope, err := decodeCachedRefreshToken(persistedValue)
	require.NoError(t, err)
	assert.Equal(t, "refresh-token", envelope.Token)
	assert.Equal(t, "https://issuer.example", envelope.Issuer)
	assert.Equal(t, "https://issuer.example/token", envelope.TokenURL)
}

func TestHandler_TryRestoreFromCachedTokens_RejectsUnsafeEnvelopeBeforeCredentialReads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "malformed envelope", value: `{"version":`},
		{name: "unsupported envelope version", value: `{"version":2,"token":"refresh","issuer":"https://issuer.example","token_url":"https://issuer.example/token"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			provider := secretmocks.NewMockProvider(ctrl)
			provider.EXPECT().GetSecret(gomock.Any(), "refresh-token-ref").Return(tt.value, nil)

			handler := &Handler{
				config: &Config{
					CachedRefreshTokenRef: "refresh-token-ref",
					CachedClientSecretRef: "client-secret-ref",
					CachedRegTokenRef:     "registration-token-ref",
					CachedRegClientURI:    "https://issuer.example/register/client-id",
					CachedSecretExpiry:    time.Now().Add(time.Hour),
				},
				secretProvider: provider,
			}

			tokenSource, err := handler.tryRestoreFromCachedTokens(t.Context(), "https://issuer.example", nil, &discovery.AuthServerInfo{
				TokenURL: "https://issuer.example/token",
			})
			require.Error(t, err)
			assert.Nil(t, tokenSource)
			assert.Contains(t, err.Error(), "cached refresh token")
		})
	}
}

func TestHandler_TryRestoreFromCachedTokens_MigratesLegacyToken(t *testing.T) {
	t.Parallel()

	t.Run("operator-locked config binds to configured issuer and token URL", func(t *testing.T) {
		t.Parallel()

		var receivedRefreshToken string
		tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, r.ParseForm())
			receivedRefreshToken = r.Form.Get("refresh_token")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer","expires_in":3600}`))
			require.NoError(t, err)
		}))
		t.Cleanup(tokenEndpoint.Close)

		ctrl := gomock.NewController(t)
		provider := secretmocks.NewMockProvider(ctrl)
		provider.EXPECT().GetSecret(gomock.Any(), "refresh-token-ref").Return("legacy-refresh-token", nil)

		var persistedValue string
		handler := &Handler{
			config: &Config{
				Issuer:                "https://issuer.example",
				ClientID:              "client-id",
				ClientSecret:          "client-secret",
				TokenURL:              tokenEndpoint.URL,
				CachedRefreshTokenRef: "refresh-token-ref",
			},
			secretProvider: provider,
			tokenPersister: func(value string, _ time.Time) error {
				persistedValue = value
				return nil
			},
		}

		tokenSource, err := handler.tryRestoreFromCachedTokens(t.Context(), "https://issuer.example", nil, nil)
		require.NoError(t, err)
		require.NotNil(t, tokenSource)
		assert.Equal(t, "legacy-refresh-token", receivedRefreshToken)

		require.NotEmpty(t, persistedValue)
		envelope, err := decodeCachedRefreshToken(persistedValue)
		require.NoError(t, err)
		assert.Equal(t, "https://issuer.example", envelope.Issuer)
		assert.Equal(t, tokenEndpoint.URL, envelope.TokenURL)
	})

	// A legacy bare token has no independently trusted issuer to bind to in
	// auto-discovery mode: migrating it would mean trusting whatever the
	// remote currently advertises, replaying the exact exploit the envelope
	// format exists to close. It must be rejected outright, forcing a fresh
	// OAuth flow.
	t.Run("auto-discovery config rejects legacy token instead of trusting current discovery", func(t *testing.T) {
		t.Parallel()

		authorizationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(authorizationServer.Close)

		ctrl := gomock.NewController(t)
		provider := secretmocks.NewMockProvider(ctrl)
		provider.EXPECT().GetSecret(gomock.Any(), "refresh-token-ref").Return("legacy-refresh-token", nil)

		handler := &Handler{
			config: &Config{
				CachedRefreshTokenRef: "refresh-token-ref",
			},
			secretProvider: provider,
		}

		authServerInfo := &discovery.AuthServerInfo{
			Issuer:   authorizationServer.URL,
			TokenURL: authorizationServer.URL + "/token",
		}

		_, _, _, migrated, err := handler.restoreCachedRefreshToken(
			t.Context(), authorizationServer.URL, authServerInfo)
		require.Error(t, err)
		assert.False(t, migrated)
		assert.Contains(t, err.Error(), "re-authentication required")
	})
}

func TestHandler_Authenticate_RejectsCrossAuthorizationServerCacheBeforeFallback(t *testing.T) {
	t.Parallel()

	registrationStarted := make(chan struct{}, 1)
	registrationFinished := make(chan struct{})
	var tokenRequests atomic.Int32
	var registrationRequest string
	const (
		cachedRefreshToken = "cached-refresh-token"
		cachedClientSecret = "cached-client-secret"
		cachedRegistration = "cached-registration-token"
	)

	var authorizationServer *httptest.Server
	authorizationServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"issuer":"` + authorizationServer.URL + `","authorization_endpoint":"` + authorizationServer.URL + `/authorize","token_endpoint":"` + authorizationServer.URL + `/token","registration_endpoint":"` + authorizationServer.URL + `/register","code_challenge_methods_supported":["S256"]}`))
			require.NoError(t, err)
		case "/register":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			registrationRequest = r.Header.Get("Authorization") + string(body)
			registrationStarted <- struct{}{}
			<-r.Context().Done()
			close(registrationFinished)
		case "/token":
			tokenRequests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(authorizationServer.Close)

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+authorizationServer.URL+`"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(remoteServer.Close)

	envelope, err := encodeCachedRefreshToken(cachedRefreshToken, "https://authorization-server-a.example", "https://authorization-server-a.example/token")
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	provider := secretmocks.NewMockProvider(ctrl)
	provider.EXPECT().GetSecret(gomock.Any(), "refresh-token-ref").Return(envelope, nil)

	handler := &Handler{
		config: &Config{
			CachedRefreshTokenRef: "refresh-token-ref",
			CachedClientID:        "cached-client-id",
			CachedClientSecretRef: "client-secret-ref",
			CachedRegTokenRef:     "registration-token-ref",
			CachedRegClientURI:    "https://authorization-server-a.example/register/cached-client-id",
			CachedSecretExpiry:    time.Now().Add(time.Hour),
		},
		secretProvider: provider,
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	authenticated := make(chan error, 1)
	go func() {
		_, authenticateErr := handler.Authenticate(ctx, remoteServer.URL)
		authenticated <- authenticateErr
	}()

	select {
	case <-registrationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for authorization server B dynamic registration")
	}
	cancel()

	select {
	case <-registrationFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dynamic registration cancellation")
	}
	select {
	case authenticateErr := <-authenticated:
		require.Error(t, authenticateErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Authenticate to return after cancellation")
	}

	assert.Zero(t, tokenRequests.Load())
	assert.NotContains(t, registrationRequest, cachedRefreshToken)
	assert.NotContains(t, registrationRequest, cachedClientSecret)
	assert.NotContains(t, registrationRequest, cachedRegistration)
}

func TestHandler_WrapWithPersistence_WritesClientIdentityBeforeRefreshToken(t *testing.T) {
	t.Parallel()

	flowResult := func(clientID string) *discovery.OAuthFlowResult {
		return &discovery.OAuthFlowResult{
			TokenSource:  oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "access-token"}),
			Config:       &oauth.Config{TokenURL: "https://issuer.example/token"},
			ClientID:     clientID,
			RefreshToken: "refresh-token",
		}
	}

	t.Run("client credentials are written first", func(t *testing.T) {
		t.Parallel()

		var order []string
		handler := &Handler{
			config: &Config{},
			clientCredentialsPersister: func(string, string, time.Time, string, string, string, int) error {
				order = append(order, "credentials")
				return nil
			},
			tokenPersister: func(string, time.Time) error {
				order = append(order, "refresh-token")
				return nil
			},
		}

		handler.wrapWithPersistence(flowResult("dcr-client-id"), "https://issuer.example")

		assert.Equal(t, []string{"credentials", "refresh-token"}, order)
	})

	t.Run("refresh token is not cached when credentials cannot be stored", func(t *testing.T) {
		t.Parallel()

		refreshPersisted := false
		handler := &Handler{
			config: &Config{},
			clientCredentialsPersister: func(string, string, time.Time, string, string, string, int) error {
				return errors.New("secret store unavailable")
			},
			tokenPersister: func(string, time.Time) error {
				refreshPersisted = true
				return nil
			},
		}

		source := handler.wrapWithPersistence(flowResult("dcr-client-id"), "https://issuer.example")

		assert.False(t, refreshPersisted, "refresh token cached without matching client credentials")
		// The current process still gets a usable token source, it is just not
		// wrapped for persistence.
		require.NotNil(t, source)
		_, wrapped := source.(*PersistingTokenSource)
		assert.False(t, wrapped, "refreshed tokens would still be persisted")
	})

	t.Run("CIMD client_id is recorded before the refresh token", func(t *testing.T) {
		t.Parallel()

		var cimdAtPersist string
		handler := &Handler{config: &Config{}}
		handler.tokenPersister = func(string, time.Time) error {
			cimdAtPersist = handler.config.CachedCIMDClientID
			return nil
		}

		handler.wrapWithPersistence(flowResult("https://client.example/metadata.json"), "https://issuer.example")

		assert.Equal(t, "https://client.example/metadata.json", cimdAtPersist)
	})
}

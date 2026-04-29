// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// minimalConfig returns a Config with the minimum fields for a configured gateway.
func minimalConfig() *Config {
	return &Config{
		GatewayURL: "https://llm.example.com",
		OIDC: OIDCConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
	}
}

// ── DeriveSecretKey ───────────────────────────────────────────────────────────

func TestDeriveSecretKey(t *testing.T) {
	t.Parallel()

	key1 := DeriveSecretKey("https://llm.example.com", "https://auth.example.com")
	key2 := DeriveSecretKey("https://llm.example.com", "https://auth.example.com")
	key3 := DeriveSecretKey("https://other.example.com", "https://auth.example.com")

	assert.Equal(t, key1, key2, "same inputs must produce the same key")
	assert.NotEqual(t, key1, key3, "different gateway URLs must produce different keys")
	assert.True(t, len(key1) > len("LLM_OAUTH_"), "key must be longer than the prefix")
	assert.Contains(t, key1, "LLM_OAUTH_", "key must carry the LLM-specific prefix")
}

// ── ErrTokenRequired is returned in non-interactive mode ─────────────────────

func TestTokenSource_NonInteractive_NoCache_ReturnsErrTokenRequired(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	mockSecrets.EXPECT().GetSecret(gomock.Any(), gomock.Any()).
		Return("", errors.New("not found")).AnyTimes()

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, ErrTokenRequired)
}

// Backend errors surface as the specific error, not the generic ErrTokenRequired.
func TestTokenSource_NonInteractive_BackendError_ReturnsLastErr(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	backendErr := errors.New("keyring is locked")
	mockSecrets.EXPECT().GetSecret(gomock.Any(), gomock.Any()).
		Return("", backendErr).AnyTimes()

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	_, err := ts.Token(context.Background())

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenRequired),
		"backend error must surface as lastErr, not the generic ErrTokenRequired")
	assert.ErrorContains(t, err, "keyring is locked")
}

// Nil secrets provider returns an actionable error, not ErrTokenRequired.
func TestTokenSource_NonInteractive_NilSecrets_ReturnsActionableError(t *testing.T) {
	t.Parallel()

	ts := NewTokenSource(minimalConfig(), nil, false, nil)
	_, err := ts.Token(context.Background())

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenRequired))
}

// ── KeyProvider uses CachedRefreshTokenRef when set ───────────────────────────

// When CachedRefreshTokenRef is set the token source must look up that exact key
// in the secrets provider — not a newly derived key.
func TestTokenSource_UsesCachedRefreshTokenRef(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	const persistedKey = "my-persisted-key"

	cfg := minimalConfig()
	cfg.OIDC.CachedRefreshTokenRef = persistedKey

	// AT cache key (_AT suffix) and the base key must both use persistedKey.
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), persistedKey+"_AT").
		Return("", errors.New("not found"))
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), persistedKey).
		Return("", errors.New("not found"))

	ts := NewTokenSource(cfg, mockSecrets, false, nil)
	_, _ = ts.Token(context.Background())
	// Expectations verify that persistedKey was used.
}

// When CachedRefreshTokenRef is empty the key is derived from GatewayURL+Issuer.
func TestTokenSource_DerivesKeyWhenNoCachedRef(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	cfg := minimalConfig()
	expectedBase := DeriveSecretKey(cfg.GatewayURL, cfg.OIDC.Issuer)

	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), expectedBase+"_AT").
		Return("", errors.New("not found"))
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), expectedBase).
		Return("", errors.New("not found"))

	ts := NewTokenSource(cfg, mockSecrets, false, nil)
	_, _ = ts.Token(context.Background())
}

// ── TokenRefUpdater wired as ConfigPersister ──────────────────────────────────

// TokenRefUpdater is invoked when the OIDC provider rotates the refresh token.
// This verifies that the LLM layer correctly wires the updater through to the
// shared tokensource.ConfigPersister so callers can persist the new token ref.
func TestTokenSource_TokenRefUpdater_WiredAsConfigPersister(t *testing.T) {
	t.Parallel()

	srv := newTokenServer(t, "new-access-token", "rotated-refresh-token")

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return "old-refresh-token", nil
		}).AnyTimes()
	mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	var updaterKey string
	updater := TokenRefUpdater(func(key string, _ time.Time) { updaterKey = key })

	cfg := minimalConfig()
	cfg.OIDC.Issuer = srv.URL
	ts := NewTokenSource(cfg, mock, false, updater)

	tok, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "new-access-token", tok)
	assert.NotEmpty(t, updaterKey, "TokenRefUpdater must be called when the refresh token is rotated")
}

// ── SanitizeTokenError ────────────────────────────────────────────────────────

func TestSanitizeTokenError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantSubs   []string
		wantAbsent []string
	}{
		{
			name:     "plain error",
			err:      errors.New("something went wrong"),
			wantSubs: []string{"something went wrong"},
		},
		{
			name:     "nil-like generic",
			err:      errors.New("any message"),
			wantSubs: []string{"any message"},
		},
		{
			name: "oauth2 RetrieveError with description",
			err: &oauth2.RetrieveError{
				ErrorCode:        "invalid_grant",
				ErrorDescription: "Token has been expired or revoked.",
				Body:             []byte("sensitive-body-content"),
			},
			wantSubs:   []string{"invalid_grant", "Token has been expired or revoked."},
			wantAbsent: []string{"sensitive-body-content"},
		},
		{
			name: "oauth2 RetrieveError without description",
			err: &oauth2.RetrieveError{
				ErrorCode: "invalid_client",
				Body:      []byte("sensitive-body-content"),
			},
			wantSubs:   []string{"invalid_client"},
			wantAbsent: []string{"sensitive-body-content"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeTokenError(tc.err)
			for _, sub := range tc.wantSubs {
				assert.Contains(t, got, sub)
			}
			for _, absent := range tc.wantAbsent {
				assert.NotContains(t, got, absent)
			}
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newTokenServer builds a minimal OIDC discovery + token endpoint that returns
// the given access token and refresh token on any token request.
func newTokenServer(t *testing.T, at, rt string) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	oidcHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q}`,
			srv.URL, srv.URL+"/authorize", srv.URL+"/token")
	}
	mux.HandleFunc("/.well-known/openid-configuration", oidcHandler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", oidcHandler)
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + at + `","refresh_token":"` + rt + `","token_type":"Bearer","expires_in":3600}`))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

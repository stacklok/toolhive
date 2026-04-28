// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	. "github.com/stacklok/toolhive/pkg/registry/auth"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// ── DeriveSecretKey ───────────────────────────────────────────────────────────

func TestDeriveSecretKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		registryURL string
		issuer      string
	}{
		{"typical", "https://registry.example.com", "https://auth.example.com"},
		{"empty strings", "", ""},
		{"empty issuer", "https://registry.example.com", ""},
		{"empty registry", "", "https://auth.example.com"},
		{"localhost", "http://localhost:5000", "http://localhost:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := DeriveSecretKey(tt.registryURL, tt.issuer)

			require.True(t, len(key) > len("REGISTRY_OAUTH_"))
			require.Equal(t, "REGISTRY_OAUTH_", key[:len("REGISTRY_OAUTH_")])

			suffix := key[len("REGISTRY_OAUTH_"):]
			require.Len(t, suffix, 8)

			for _, c := range suffix {
				require.True(t,
					(c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
					"suffix character %q is not a lowercase hex digit", c)
			}

			h := sha256.Sum256([]byte(tt.registryURL + "\x00" + tt.issuer))
			expected := "REGISTRY_OAUTH_" + hex.EncodeToString(h[:4])
			require.Equal(t, expected, key)
		})
	}
}

func TestDeriveSecretKey_Deterministic(t *testing.T) {
	t.Parallel()

	key1 := DeriveSecretKey("https://registry.example.com", "https://auth.example.com")
	key2 := DeriveSecretKey("https://registry.example.com", "https://auth.example.com")
	require.Equal(t, key1, key2)
}

func TestDeriveSecretKey_UniquePerInputCombination(t *testing.T) {
	t.Parallel()

	combinations := []struct{ url, issuer string }{
		{"https://registry-a.example.com", "https://auth.example.com"},
		{"https://registry-b.example.com", "https://auth.example.com"},
		{"https://registry-a.example.com", "https://auth-other.example.com"},
		{"https://registry-b.example.com", "https://auth-other.example.com"},
	}

	seen := make(map[string]struct{}, len(combinations))
	for _, c := range combinations {
		key := DeriveSecretKey(c.url, c.issuer)
		_, dup := seen[key]
		require.False(t, dup, "duplicate key for url=%q issuer=%q: %q", c.url, c.issuer, key)
		seen[key] = struct{}{}
	}
}

func TestDeriveSecretKey_NullByteIsolatesSegments(t *testing.T) {
	t.Parallel()
	key1 := DeriveSecretKey("ab", "c")
	key2 := DeriveSecretKey("a", "bc")
	require.NotEqual(t, key1, key2)
}

// ── NewTokenSource ────────────────────────────────────────────────────────────

func TestNewTokenSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.RegistryOAuthConfig
		wantNil bool
	}{
		{
			name:    "nil config returns nil source",
			cfg:     nil,
			wantNil: true,
		},
		{
			name:    "non-nil config returns non-nil source",
			cfg:     &config.RegistryOAuthConfig{Issuer: "https://auth.example.com", ClientID: "id"},
			wantNil: false,
		},
		{
			name: "config with scopes and audience returns non-nil source",
			cfg: &config.RegistryOAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "id",
				Scopes:   []string{"openid"},
				Audience: "api://my-api",
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src, err := NewTokenSource(tt.cfg, "https://registry.example.com", nil, false)
			require.NoError(t, err)
			if tt.wantNil {
				require.Nil(t, src)
			} else {
				require.NotNil(t, src)
			}
		})
	}
}

// ── Token – non-interactive, no cache ────────────────────────────────────────

// When no credentials are cached and the caller is non-interactive,
// Token() must return ErrRegistryAuthRequired.
func TestToken_NonInteractive_NoCache_ReturnsErrRegistryAuthRequired(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	// Return a not-found-style error so both AT and RT cache lookups miss cleanly.
	mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).
		Return("", errors.New("not found")).AnyTimes()

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{Issuer: "https://auth.example.com", ClientID: "c"},
		"https://registry.example.com", mock, false,
	)
	require.NoError(t, err)

	_, tokErr := src.Token(context.Background())
	require.ErrorIs(t, tokErr, ErrRegistryAuthRequired)
}

// When the secrets backend returns a non-not-found error (e.g. keyring locked),
// Token() must surface that specific error rather than the generic ErrRegistryAuthRequired.
func TestToken_NonInteractive_BackendError_SurfacesLastErr(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	backendErr := errors.New("keyring is locked")
	mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).
		Return("", backendErr).AnyTimes()

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{Issuer: "https://auth.example.com", ClientID: "c"},
		"https://registry.example.com", mock, false,
	)
	require.NoError(t, err)

	_, tokErr := src.Token(context.Background())
	require.Error(t, tokErr)
	assert.False(t, errors.Is(tokErr, ErrRegistryAuthRequired),
		"backend error must surface as lastErr, not the generic ErrRegistryAuthRequired")
	assert.ErrorContains(t, tokErr, "keyring is locked")
}

// Nil secrets provider returns an actionable error, not ErrRegistryAuthRequired.
func TestToken_NilSecretsProvider_ReturnsActionableError(t *testing.T) {
	t.Parallel()

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{Issuer: "https://auth.example.com", ClientID: "c"},
		"https://registry.example.com", nil, false,
	)
	require.NoError(t, err)

	_, tokErr := src.Token(context.Background())
	require.Error(t, tokErr)
	assert.False(t, errors.Is(tokErr, ErrRegistryAuthRequired))
}

// ── KeyProvider uses CachedRefreshTokenRef when set ───────────────────────────

// When CachedRefreshTokenRef is set, Token() must look up that exact key.
func TestToken_UsesCachedRefreshTokenRef(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	const persistedKey = "my-cached-ref-key"
	// Expect the AT cache key and the base key — both derived from persistedKey.
	mock.EXPECT().
		GetSecret(gomock.Any(), persistedKey+"_AT").
		Return("", errors.New("not found"))
	mock.EXPECT().
		GetSecret(gomock.Any(), persistedKey).
		Return("", errors.New("not found"))

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{
			Issuer:                "https://auth.example.com",
			ClientID:              "c",
			CachedRefreshTokenRef: persistedKey,
		},
		"https://registry.example.com", mock, false,
	)
	require.NoError(t, err)
	_, _ = src.Token(context.Background())
	// Mock expectations verify the correct key was used.
}

// When CachedRefreshTokenRef is empty, Token() derives the key from URL+issuer.
func TestToken_DerivesKeyWhenNoCachedRef(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	const registryURL = "https://registry.example.com"
	const issuer = "https://auth.example.com"
	derivedKey := DeriveSecretKey(registryURL, issuer)

	mock.EXPECT().
		GetSecret(gomock.Any(), derivedKey+"_AT").
		Return("", errors.New("not found"))
	mock.EXPECT().
		GetSecret(gomock.Any(), derivedKey).
		Return("", errors.New("not found"))

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{Issuer: issuer, ClientID: "c"},
		registryURL, mock, false,
	)
	require.NoError(t, err)
	_, _ = src.Token(context.Background())
}

// ── Token restores from cached refresh token ──────────────────────────────────

func TestToken_RefreshTokenCache_UsesPersistedToken(t *testing.T) {
	t.Parallel()

	srv := newTokenServer(t, "fresh-access-token", "rt-rotated")

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	// AT cache miss; refresh token present.
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return "stored-refresh-token", nil
		}).AnyTimes()
	mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{Issuer: srv.URL, ClientID: "c"},
		"https://registry.example.com", mock, false,
	)
	require.NoError(t, err)

	tok, tokErr := src.Token(context.Background())
	require.NoError(t, tokErr)
	assert.Equal(t, "fresh-access-token", tok)
}

// ── PersistingTokenSource applied in tryRestoreFromCache ─────────────────────

// This is a regression test for the bug where registry's tryRestoreFromCache did
// not wrap the inner source with PersistingTokenSource, meaning rotated refresh
// tokens were never re-persisted after a cache restore (the fix introduced in
// the LLM implementation is now shared with the registry path).
func TestToken_RefreshTokenCache_RotatedTokenPersisted(t *testing.T) {
	t.Parallel()

	// Build a fake OIDC+token endpoint that returns a rotated refresh token.
	var setSecretCalls []string
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
	mock.EXPECT().
		SetSecret(gomock.Any(), gomock.AssignableToTypeOf(""), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key, val string) error {
			setSecretCalls = append(setSecretCalls, key+"="+val)
			return nil
		}).AnyTimes()

	src, err := NewTokenSource(
		&config.RegistryOAuthConfig{Issuer: srv.URL, ClientID: "c"},
		"https://registry.example.com", mock, false,
	)
	require.NoError(t, err)

	tok, tokErr := src.Token(context.Background())
	require.NoError(t, tokErr)
	assert.Equal(t, "new-access-token", tok)

	// PersistingTokenSource must have written the rotated refresh token.
	var persistedRT bool
	for _, call := range setSecretCalls {
		if strings.Contains(call, "rotated-refresh-token") {
			persistedRT = true
		}
	}
	assert.True(t, persistedRT,
		"rotated refresh token must be re-persisted via PersistingTokenSource; SetSecret calls: %v", setSecretCalls)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newTokenServer builds a server that handles OIDC discovery AND a token
// endpoint that returns the given access token and refresh token.
func newTokenServer(t *testing.T, at, rt string) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	oidcHandler := func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
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

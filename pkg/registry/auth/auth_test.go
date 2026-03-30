// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestDeriveSecretKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serviceURL string
		issuer     string
	}{
		{
			name:       "typical registry and issuer",
			serviceURL: "https://registry.example.com",
			issuer:     "https://auth.example.com",
		},
		{
			name:       "empty strings",
			serviceURL: "",
			issuer:     "",
		},
		{
			name:       "empty issuer",
			serviceURL: "https://registry.example.com",
			issuer:     "",
		},
		{
			name:       "empty registry URL",
			serviceURL: "",
			issuer:     "https://auth.example.com",
		},
		{
			name:       "localhost registry",
			serviceURL: "http://localhost:5000",
			issuer:     "http://localhost:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := DeriveSecretKey(tt.serviceURL, tt.issuer)

			// Must start with the correct prefix
			require.True(t, len(key) > len("REGISTRY_OAUTH_"), "key too short")
			require.Equal(t, "REGISTRY_OAUTH_", key[:len("REGISTRY_OAUTH_")])

			// The suffix must be exactly 8 hex characters (4 bytes of sha256)
			suffix := key[len("REGISTRY_OAUTH_"):]
			require.Len(t, suffix, 8, "hex suffix must be exactly 8 characters")

			// Verify each character is a valid hex character
			for _, c := range suffix {
				require.True(t,
					(c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
					"suffix character %q is not a lowercase hex digit", c,
				)
			}

			// Verify the derivation formula: sha256(registryURL + "\x00" + issuer)[:4]
			h := sha256.Sum256([]byte(tt.serviceURL + "\x00" + tt.issuer))
			expected := "REGISTRY_OAUTH_" + hex.EncodeToString(h[:4])
			require.Equal(t, expected, key)
		})
	}
}

func TestDeriveSecretKey_Deterministic(t *testing.T) {
	t.Parallel()

	registryURL := "https://registry.example.com"
	issuer := "https://auth.example.com"

	key1 := DeriveSecretKey(registryURL, issuer)
	key2 := DeriveSecretKey(registryURL, issuer)

	require.Equal(t, key1, key2, "DeriveSecretKey must be deterministic")
}

func TestDeriveSecretKey_UniquePerInputCombination(t *testing.T) {
	t.Parallel()

	combinations := []struct {
		serviceURL string
		issuer     string
	}{
		{"https://registry-a.example.com", "https://auth.example.com"},
		{"https://registry-b.example.com", "https://auth.example.com"},
		{"https://registry-a.example.com", "https://auth-other.example.com"},
		{"https://registry-b.example.com", "https://auth-other.example.com"},
	}

	keys := make(map[string]struct{}, len(combinations))
	for _, combo := range combinations {
		key := DeriveSecretKey(combo.serviceURL, combo.issuer)
		_, alreadySeen := keys[key]
		require.False(t, alreadySeen,
			"DeriveSecretKey produced a duplicate key for registryURL=%q issuer=%q: %q",
			combo.serviceURL, combo.issuer, key,
		)
		keys[key] = struct{}{}
	}
}

func TestDeriveSecretKey_NullByteIsolatesSegments(t *testing.T) {
	t.Parallel()

	// Without the null-byte separator these two pairs would hash identically:
	// ("ab", "c") and ("a", "bc") both concatenate to "abc".
	// The separator prevents that collision.
	key1 := DeriveSecretKey("ab", "c")
	key2 := DeriveSecretKey("a", "bc")

	require.NotEqual(t, key1, key2,
		"keys must differ when registry URL and issuer are split differently")
}

func TestNewTokenSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        *config.OAuthConfig
		wantNil    bool
		wantErrNil bool
	}{
		{
			name:       "nil config returns nil source and nil error",
			cfg:        nil,
			wantNil:    true,
			wantErrNil: true,
		},
		{
			name: "non-nil config returns non-nil source",
			cfg: &config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client-id",
			},
			wantNil:    false,
			wantErrNil: true,
		},
		{
			name: "config with scopes and audience returns non-nil source",
			cfg: &config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client-id",
				Scopes:   []string{"openid", "profile"},
				Audience: "api://my-api",
			},
			wantNil:    false,
			wantErrNil: true,
		},
		{
			name: "config with Resource field returns non-nil source",
			cfg: &config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client-id",
				Resource: "https://api.example.com/resource",
			},
			wantNil:    false,
			wantErrNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src, err := NewTokenSource(tt.cfg, "https://registry.example.com", nil, false, nil)

			if tt.wantErrNil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			if tt.wantNil {
				require.Nil(t, src)
			} else {
				require.NotNil(t, src)
			}
		})
	}
}

func TestOAuthTokenSource_Token_NonInteractiveNoCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		buildProvider func(ctrl *gomock.Controller) *secretsmocks.MockProvider
	}{
		{
			name:          "non-interactive with no secrets provider returns ErrRegistryAuthRequired",
			buildProvider: nil, // nil secrets provider
		},
		{
			name: "non-interactive with secrets provider error returns ErrRegistryAuthRequired",
			buildProvider: func(ctrl *gomock.Controller) *secretsmocks.MockProvider {
				mock := secretsmocks.NewMockProvider(ctrl)
				mock.EXPECT().
					GetSecret(gomock.Any(), gomock.Any()).
					Return("", errors.New("connection refused"))
				return mock
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			var provider secrets.Provider
			if tt.buildProvider != nil {
				provider = tt.buildProvider(ctrl)
			}

			src := &oauthTokenSource{
				oauthCfg: &config.OAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "test-client",
				},
				serviceURL:      "https://registry.example.com",
				secretsProvider: provider,
				interactive:     false,
			}

			_, err := src.Token(context.Background())

			require.Error(t, err)
			require.True(t, errors.Is(err, ErrRegistryAuthRequired),
				"expected ErrRegistryAuthRequired, got: %v", err)
		})
	}
}

func TestOAuthTokenSource_RefreshTokenKey(t *testing.T) {
	t.Parallel()

	const registryURL = "https://registry.example.com"
	const issuer = "https://auth.example.com"

	tests := []struct {
		name                  string
		cachedRefreshTokenRef string
		wantKey               string
	}{
		{
			name:                  "returns CachedRefreshTokenRef when set",
			cachedRefreshTokenRef: "my-cached-ref-key",
			wantKey:               "my-cached-ref-key",
		},
		{
			name:                  "falls back to DeriveSecretKey when CachedRefreshTokenRef is empty",
			cachedRefreshTokenRef: "",
			wantKey:               DeriveSecretKey(registryURL, issuer),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := &oauthTokenSource{
				oauthCfg: &config.OAuthConfig{
					Issuer:                issuer,
					ClientID:              "test-client",
					CachedRefreshTokenRef: tt.cachedRefreshTokenRef,
				},
				serviceURL: registryURL,
			}

			got := src.refreshTokenKey()
			require.Equal(t, tt.wantKey, got)
		})
	}
}

// mockOAuth2TokenSource is a test double for oauth2.TokenSource (no-context variant).
type mockOAuth2TokenSource struct {
	token *oauth2.Token
	err   error
}

func (m *mockOAuth2TokenSource) Token() (*oauth2.Token, error) {
	return m.token, m.err
}

// newOIDCTestServer starts an httptest server that handles the two well-known
// OIDC discovery paths used by CreateOAuthConfigFromOIDC. It returns the server
// and shuts it down automatically when the test completes.
func newOIDCTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	handler := func(w http.ResponseWriter, _ *http.Request) {
		issuer := srv.URL
		doc := map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
		}
	}

	// CreateOAuthConfigFromOIDC tries both OIDC and OAuth well-known paths.
	mux.HandleFunc("/.well-known/openid-configuration", handler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", handler)

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestOAuthTokenSource_Token_InMemoryCacheHit verifies that when the in-memory
// token source holds a valid, non-expired token, Token() returns it immediately
// without consulting the secrets provider.
func TestOAuthTokenSource_Token_InMemoryCacheHit(t *testing.T) {
	t.Parallel()

	validToken := &oauth2.Token{
		AccessToken: "cached-access-token",
		Expiry:      time.Now().Add(time.Hour),
		TokenType:   "Bearer",
	}

	src := &oauthTokenSource{
		oauthCfg: &config.OAuthConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
		serviceURL:      "https://registry.example.com",
		secretsProvider: nil, // should never be called
		interactive:     false,
		tokenSource:     &mockOAuth2TokenSource{token: validToken},
	}

	got, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "cached-access-token", got)
}

// TestOAuthTokenSource_Token_InMemoryCacheExpiredFallsThrough verifies that when
// the in-memory token source returns an expired token (past Expiry), Token() clears
// the cache and falls through to return ErrRegistryAuthRequired in non-interactive mode
// without a secrets provider.
func TestOAuthTokenSource_Token_InMemoryCacheExpiredFallsThrough(t *testing.T) {
	t.Parallel()

	expiredToken := &oauth2.Token{
		AccessToken: "expired-token",
		Expiry:      time.Now().Add(-time.Hour), // already expired
		TokenType:   "Bearer",
	}

	src := &oauthTokenSource{
		oauthCfg: &config.OAuthConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
		serviceURL:      "https://registry.example.com",
		secretsProvider: nil,
		interactive:     false,
		tokenSource:     &mockOAuth2TokenSource{token: expiredToken},
	}

	_, err := src.Token(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRegistryAuthRequired),
		"expected ErrRegistryAuthRequired, got: %v", err)
	// In-memory cache should have been cleared.
	require.Nil(t, src.tokenSource)
}

// TestOAuthTokenSource_Token_InMemoryCacheErrorFallsThrough verifies that when
// the in-memory token source returns an error, Token() clears the cache and falls
// through to return ErrRegistryAuthRequired in non-interactive mode.
func TestOAuthTokenSource_Token_InMemoryCacheErrorFallsThrough(t *testing.T) {
	t.Parallel()

	src := &oauthTokenSource{
		oauthCfg: &config.OAuthConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
		serviceURL:      "https://registry.example.com",
		secretsProvider: nil,
		interactive:     false,
		tokenSource:     &mockOAuth2TokenSource{err: errors.New("token refresh failed")},
	}

	_, err := src.Token(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRegistryAuthRequired),
		"expected ErrRegistryAuthRequired, got: %v", err)
	// In-memory cache should have been cleared.
	require.Nil(t, src.tokenSource)
}

// TestOAuthTokenSource_TryRestoreFromCache_NilProvider verifies that
// tryRestoreFromCache returns an error immediately when no secrets provider is set.
func TestOAuthTokenSource_TryRestoreFromCache_NilProvider(t *testing.T) {
	t.Parallel()

	src := &oauthTokenSource{
		oauthCfg: &config.OAuthConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
		serviceURL:      "https://registry.example.com",
		secretsProvider: nil, // genuine nil interface — triggers the nil guard in tryRestoreFromCache
	}

	err := src.tryRestoreFromCache(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no secrets provider available")
}

// TestOAuthTokenSource_TryRestoreFromCache covers the error paths in tryRestoreFromCache
// that involve the secrets provider returning errors or empty values.
func TestOAuthTokenSource_TryRestoreFromCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		buildProvider   func(ctrl *gomock.Controller) *secretsmocks.MockProvider
		wantErrContains string
	}{
		{
			name: "GetSecret returns error",
			buildProvider: func(ctrl *gomock.Controller) *secretsmocks.MockProvider {
				mock := secretsmocks.NewMockProvider(ctrl)
				mock.EXPECT().
					GetSecret(gomock.Any(), gomock.Any()).
					Return("", errors.New("vault unavailable"))
				return mock
			},
			wantErrContains: "failed to retrieve cached refresh token",
		},
		{
			name: "GetSecret returns empty string",
			buildProvider: func(ctrl *gomock.Controller) *secretsmocks.MockProvider {
				mock := secretsmocks.NewMockProvider(ctrl)
				mock.EXPECT().
					GetSecret(gomock.Any(), gomock.Any()).
					Return("", nil)
				return mock
			},
			wantErrContains: "no cached refresh token found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			provider := tt.buildProvider(ctrl)

			src := &oauthTokenSource{
				oauthCfg: &config.OAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "test-client",
				},
				serviceURL:      "https://registry.example.com",
				secretsProvider: provider,
			}

			err := src.tryRestoreFromCache(context.Background())
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrContains)
		})
	}
}

// TestOAuthTokenSource_TryRestoreFromCache_WithOIDCServer verifies that
// tryRestoreFromCache succeeds when a valid refresh token is found in the secrets
// provider and an OIDC discovery document is available.
func TestOAuthTokenSource_TryRestoreFromCache_WithOIDCServer(t *testing.T) {
	t.Parallel()

	srv := newOIDCTestServer(t)

	ctrl := gomock.NewController(t)
	mockProvider := secretsmocks.NewMockProvider(ctrl)
	mockProvider.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("my-refresh-token", nil)

	src := &oauthTokenSource{
		oauthCfg: &config.OAuthConfig{
			Issuer:   srv.URL,
			ClientID: "test-client",
		},
		serviceURL:      "https://registry.example.com",
		secretsProvider: mockProvider,
	}

	err := src.tryRestoreFromCache(context.Background())
	require.NoError(t, err)
	// We only verify the tokenSource was set; actually exchanging the refresh
	// token requires a real /token endpoint and is covered by integration tests.
	require.NotNil(t, src.tokenSource,
		"tokenSource must be set after successful cache restoration")
}

// TestOAuthTokenSource_CreateTokenPersister covers the createTokenPersister helper.
func TestOAuthTokenSource_CreateTokenPersister(t *testing.T) {
	t.Parallel()

	const refreshTokenKey = "REGISTRY_OAUTH_testkey"
	const refreshTokenValue = "rt-abc123"

	tests := []struct {
		name          string
		setupMock     func(mock *secretsmocks.MockProvider)
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name: "SetSecret succeeds",
			setupMock: func(mock *secretsmocks.MockProvider) {
				mock.EXPECT().
					SetSecret(gomock.Any(), refreshTokenKey, refreshTokenValue).
					Return(nil)
			},
			wantErr: false,
		},
		{
			name: "SetSecret returns error",
			setupMock: func(mock *secretsmocks.MockProvider) {
				mock.EXPECT().
					SetSecret(gomock.Any(), refreshTokenKey, refreshTokenValue).
					Return(fmt.Errorf("storage full"))
			},
			wantErr:       true,
			wantErrSubstr: "failed to persist refresh token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := secretsmocks.NewMockProvider(ctrl)
			tt.setupMock(mockProvider)

			src := &oauthTokenSource{
				oauthCfg: &config.OAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "test-client",
				},
				serviceURL:      "https://registry.example.com",
				secretsProvider: mockProvider,
			}

			persister := src.createTokenPersister(refreshTokenKey)
			require.NotNil(t, persister)

			// Call the persister function — expiry value does not affect SetSecret behaviour.
			err := persister(refreshTokenValue, time.Now().Add(time.Hour))
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrSubstr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestBuildOAuthFlowConfig_ResourceVsAudience is the regression guard for the bug fix
// that separates RFC 8707 Resource from the provider-specific Audience parameter.
// Resource is passed directly to CreateOAuthConfigFromOIDC (and used at the token
// endpoint), whereas Audience is injected into OAuthParams (authorization URL only).
func TestBuildOAuthFlowConfig_ResourceVsAudience(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		audience          string
		resource          string
		wantAudienceParam string // expected value of OAuthParams["audience"], "" means absent
		wantResourceParam bool   // true if we expect "resource" key in OAuthParams (should always be false)
	}{
		{
			name:              "Audience set — appears in OAuthParams[audience]",
			audience:          "https://api.auth0.com/",
			resource:          "",
			wantAudienceParam: "https://api.auth0.com/",
			wantResourceParam: false,
		},
		{
			name:              "Resource set, Audience empty — OAuthParams not polluted",
			audience:          "",
			resource:          "https://api.example.com/",
			wantAudienceParam: "",
			wantResourceParam: false,
		},
		{
			name:              "Both Resource and Audience set — only Audience in OAuthParams",
			audience:          "my-audience",
			resource:          "https://api.example.com/",
			wantAudienceParam: "my-audience",
			wantResourceParam: false,
		},
		{
			name:              "Neither set — OAuthParams is nil",
			audience:          "",
			resource:          "",
			wantAudienceParam: "",
			wantResourceParam: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newOIDCTestServer(t)

			src := &oauthTokenSource{
				oauthCfg: &config.OAuthConfig{
					Issuer:   srv.URL,
					ClientID: "test-client",
					Audience: tt.audience,
					Resource: tt.resource,
				},
				serviceURL: "https://registry.example.com",
			}

			cfg, err := src.buildOAuthFlowConfig(context.Background())
			require.NoError(t, err)
			require.NotNil(t, cfg)

			if tt.wantAudienceParam != "" {
				require.NotNil(t, cfg.OAuthParams, "OAuthParams must be non-nil when audience is set")
				require.Equal(t, tt.wantAudienceParam, cfg.OAuthParams["audience"],
					"OAuthParams[audience] must match the configured Audience value")
			} else {
				// Either nil map or absent key — neither is acceptable as a spurious value.
				if cfg.OAuthParams != nil {
					_, hasAudience := cfg.OAuthParams["audience"]
					require.False(t, hasAudience,
						"OAuthParams must NOT contain 'audience' key when Audience is empty")
				}
			}

			// Resource must never appear in OAuthParams; it travels through a separate
			// channel (passed to CreateOAuthConfigFromOIDC and the token endpoint).
			if cfg.OAuthParams != nil {
				_, hasResource := cfg.OAuthParams["resource"]
				require.False(t, hasResource,
					"Resource must NOT be placed in OAuthParams; it is handled by CreateOAuthConfigFromOIDC")
			}
		})
	}
}

// TestOAuthTokenSource_ConfigUpdater covers the injectable configUpdater callback and
// the updateConfigTokenRef method that dispatches to it.
func TestOAuthTokenSource_ConfigUpdater(t *testing.T) {
	t.Parallel()

	expiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("updateConfigTokenRef calls configUpdater when set", func(t *testing.T) {
		t.Parallel()

		var gotRef string
		var gotExpiry time.Time

		src := &oauthTokenSource{
			oauthCfg: &config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "test-client",
			},
			serviceURL: "https://registry.example.com",
			configUpdater: func(tokenRef string, exp time.Time) {
				gotRef = tokenRef
				gotExpiry = exp
			},
		}

		src.updateConfigTokenRef("my-token-ref", expiry)

		require.Equal(t, "my-token-ref", gotRef)
		require.Equal(t, expiry, gotExpiry)
	})

	t.Run("updateConfigTokenRef is a no-op when configUpdater is nil", func(t *testing.T) {
		t.Parallel()

		src := &oauthTokenSource{
			oauthCfg: &config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "test-client",
			},
			serviceURL:    "https://registry.example.com",
			configUpdater: nil,
		}

		// Must not panic.
		require.NotPanics(t, func() {
			src.updateConfigTokenRef("my-token-ref", expiry)
		})
	})

	t.Run("NewTokenSource stores non-nil configUpdater", func(t *testing.T) {
		t.Parallel()

		updater := func(_ string, _ time.Time) {}

		ts, err := NewTokenSource(
			&config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "my-client-id",
			},
			"https://registry.example.com",
			nil,
			false,
			updater,
		)
		require.NoError(t, err)
		require.NotNil(t, ts)

		src, ok := ts.(*oauthTokenSource)
		require.True(t, ok, "returned TokenSource must be *oauthTokenSource")
		require.NotNil(t, src.configUpdater)
	})

	t.Run("createTokenPersister with non-nil configUpdater calls it", func(t *testing.T) {
		t.Parallel()

		var gotRef string
		var gotExpiry time.Time

		ctrl := gomock.NewController(t)
		mockProvider := secretsmocks.NewMockProvider(ctrl)
		mockProvider.EXPECT().
			SetSecret(gomock.Any(), "my-key", "refresh-token").
			Return(nil)

		src := &oauthTokenSource{
			oauthCfg: &config.OAuthConfig{
				Issuer:   "https://auth.example.com",
				ClientID: "test-client",
			},
			serviceURL:      "https://registry.example.com",
			secretsProvider: mockProvider,
			configUpdater: func(tokenRef string, exp time.Time) {
				gotRef = tokenRef
				gotExpiry = exp
			},
		}

		persister := src.createTokenPersister("my-key")
		require.NotNil(t, persister)

		err := persister("refresh-token", expiry)
		require.NoError(t, err)

		require.Equal(t, "my-key", gotRef)
		require.Equal(t, expiry, gotExpiry)
	})
}

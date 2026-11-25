// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	upstreamtokenmocks "github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	keysmocks "github.com/stacklok/toolhive/pkg/authserver/server/keys/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// userSeen is what a storage layer observes about the canonical user while a
// load runs.
type userSeen struct {
	direct   string
	directOK bool
	resolved string
	resolvOK bool
}

func observeUser(ctx context.Context) userSeen {
	var s userSeen
	s.direct, s.directOK = PlatformUserFromContext(ctx)
	s.resolved, s.resolvOK = CanonicalUserFromContext(ctx)
	return s
}

func TestLoadUpstreamTokens_UserKeying(t *testing.T) {
	t.Parallel()

	const (
		issuer = "https://as.example.com"
		sub    = "alice"
	)
	creds := map[string]upstreamtoken.UpstreamCredential{"github": {AccessToken: "gh"}}
	claimsWith := func(tsid any) jwt.MapClaims {
		return jwt.MapClaims{"iss": issuer, "sub": sub, claimnames.TokenSessionIDKey: tsid}
	}
	blank := userSeen{"", true, "", true}
	user := userSeen{sub, true, sub, true}
	identityFallback := userSeen{"", false, "platform-" + sub, true}

	tests := []struct {
		name           string
		validatorIss   string
		embeddedIss    string
		route          AdmissionRoute
		tsid           any
		ctxUser        string
		readerErr      error
		wantRead       bool
		wantSeen       userSeen
		wantCreds      bool
		wantErr        bool
		wantErrIsNoUsr bool
	}{
		{
			name: "bound interactive sets the canonical user", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteInteractive, tsid: "t", wantRead: true, wantSeen: user, wantCreds: true,
		},
		{
			name: "bound interactive overrides a user already on the context", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteInteractive, tsid: "t", ctxUser: "bob", wantRead: true, wantSeen: user, wantCreds: true,
		},
		{
			name: "bound delegated is shadowed", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteDelegated, tsid: "t", wantRead: true, wantSeen: blank,
			readerErr: upstreamtoken.ErrNoCanonicalUser,
		},
		{
			name: "bound unspecified with a reader that loads is shadowed", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: blank, wantCreds: true,
		},
		{
			name: "bound unspecified is shadowed", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: blank,
			readerErr: fmt.Errorf("storage: %w", upstreamtoken.ErrNoCanonicalUser),
		},
		{
			name: "bound non-interactive shadows a user already on the context", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteUnspecified, tsid: "t", ctxUser: "bob", wantRead: true, wantSeen: blank,
			readerErr: upstreamtoken.ErrNoCanonicalUser,
		},
		{
			name: "bound non-interactive: other reader error is an error", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteDelegated, tsid: "t", wantRead: true, wantSeen: blank,
			readerErr: errors.New("redis down"), wantErr: true,
		},
		{
			name: "interactive: bare ErrNoCanonicalUser is an error", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteInteractive, tsid: "t", wantRead: true, wantSeen: user,
			readerErr: upstreamtoken.ErrNoCanonicalUser, wantErr: true, wantErrIsNoUsr: true,
		},
		{
			name: "interactive: wrapped ErrNoCanonicalUser is an error", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteInteractive, tsid: "t", wantRead: true, wantSeen: user,
			readerErr: fmt.Errorf("wrap: %w", upstreamtoken.ErrNoCanonicalUser), wantErr: true, wantErrIsNoUsr: true,
		},
		{
			name: "empty tsid reads nothing", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteInteractive, tsid: "",
		},
		{
			name: "non-string tsid reads nothing", validatorIss: issuer, embeddedIss: issuer,
			route: AdmissionRouteInteractive, tsid: 7,
		},
		{
			name: "unbound (no embedded issuer): key untouched", validatorIss: issuer, embeddedIss: "",
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: identityFallback, wantCreds: true,
		},
		{
			name: "unbound (embedded issuer differs): key untouched", validatorIss: issuer,
			embeddedIss: "https://elsewhere.example.com",
			route:       AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: identityFallback, wantCreds: true,
		},
		{
			name: "unbound (no validator issuer): key untouched", validatorIss: "", embeddedIss: issuer,
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: identityFallback, wantCreds: true,
		},
		{
			name: "unbound: bare ErrNoCanonicalUser is an error", validatorIss: issuer, embeddedIss: "",
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: identityFallback,
			readerErr: upstreamtoken.ErrNoCanonicalUser, wantErr: true, wantErrIsNoUsr: true,
		},
		{
			name: "unbound: wrapped ErrNoCanonicalUser is an error", validatorIss: issuer, embeddedIss: "",
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: identityFallback,
			readerErr: fmt.Errorf("storage: %w", upstreamtoken.ErrNoCanonicalUser), wantErr: true, wantErrIsNoUsr: true,
		},
		{
			name: "unbound: other reader error is an error", validatorIss: issuer, embeddedIss: "",
			route: AdmissionRouteUnspecified, tsid: "t", wantRead: true, wantSeen: identityFallback,
			readerErr: errors.New("redis down"), wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			reader := upstreamtokenmocks.NewMockTokenReader(ctrl)
			var seen userSeen
			if tt.wantRead {
				reader.EXPECT().GetAllUpstreamCredentials(gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ string) (map[string]upstreamtoken.UpstreamCredential, map[string]error, error) {
						seen = observeUser(ctx)
						if tt.readerErr != nil {
							return nil, nil, tt.readerErr
						}
						return creds, nil, nil
					})
			}

			v := &TokenValidator{
				issuer: tt.validatorIss, embeddedAuthServerIssuer: tt.embeddedIss, upstreamTokenReader: reader,
			}
			ctx := WithIdentity(context.Background(), &Identity{PrincipalInfo: PrincipalInfo{
				Subject: sub, PlatformUserID: "platform-" + sub,
			}})
			if tt.ctxUser != "" {
				ctx = WithPlatformUser(ctx, tt.ctxUser)
			}

			got, failed, err := v.loadUpstreamTokens(ctx, claimsWith(tt.tsid), tt.route)
			if tt.wantRead {
				require.Equal(t, tt.wantSeen, seen)
			}
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, tt.wantErrIsNoUsr, errors.Is(err, upstreamtoken.ErrNoCanonicalUser))
				return
			}
			require.NoError(t, err)
			require.Nil(t, failed)
			if tt.wantCreds {
				require.Equal(t, creds, got)
			} else {
				require.Nil(t, got)
			}
		})
	}
}

func TestIsSessionlessGrant_TokenSessionID(t *testing.T) {
	t.Parallel()

	const issuer = "https://as.example.com"
	tests := []struct {
		name      string
		tsid      any
		hasReader bool
		want      bool
	}{
		{name: "proof without tsid", want: true, hasReader: true},
		{name: "proof with empty tsid", tsid: "", hasReader: true, want: true},
		{name: "proof with non-string tsid", tsid: 7, hasReader: true, want: true},
		{name: "proof with tsid and a reader", tsid: "t", hasReader: true, want: false},
		{name: "proof with tsid and no reader keeps today's result", tsid: "t", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := &TokenValidator{issuer: issuer, embeddedAuthServerIssuer: issuer}
			if tt.hasReader {
				v.upstreamTokenReader = upstreamtokenmocks.NewMockTokenReader(gomock.NewController(t))
			}
			claims := jwt.MapClaims{"iss": issuer, claimnames.NoUpstreamSessionKey: true}
			if tt.tsid != nil {
				claims[claimnames.TokenSessionIDKey] = tt.tsid
			}

			got := v.isSessionlessGrant(&Identity{}, claims, provenanceSignedEmbeddedKey)

			require.Equal(t, tt.want, got)
		})
	}
}

// TestMiddleware_UserKeying drives the real middleware, so the route the
// validator assigns decides what the storage double sees in the load context.
func TestMiddleware_UserKeying(t *testing.T) {
	t.Parallel()

	const (
		issuer   = "test-issuer"
		audience = "test-audience"
	)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key, err := jwk.Import[jwk.Key](&privateKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, key.Set(jwk.KeyIDKey, testKeyID))
	require.NoError(t, key.Set(jwk.AlgorithmKey, "RS256"))
	require.NoError(t, key.Set(jwk.KeyUsageKey, "sig"))
	keySet := jwk.NewSet()
	require.NoError(t, keySet.AddKey(key))
	jwksServer, caCertPath := createTestJWKSServer(t, keySet)
	t.Cleanup(jwksServer.Close)

	type setup struct {
		embedded    bool
		keyProvider bool
		audience    string
		ctxUser     string
	}
	bound := setup{embedded: true, keyProvider: true, audience: audience}

	run := func(t *testing.T, s setup, extra jwt.MapClaims, reader upstreamtoken.TokenReader) (*httptest.ResponseRecorder, *Identity) {
		t.Helper()
		opts := []TokenValidatorOption{WithUpstreamTokenReader(reader)}
		if s.embedded {
			opts = append(opts, WithEmbeddedAuthServerIssuer(issuer))
		}
		if s.keyProvider {
			provider := keysmocks.NewMockPublicKeyProvider(gomock.NewController(t))
			provider.EXPECT().PublicKeys(gomock.Any()).
				Return([]*keys.PublicKeyData{{KeyID: testKeyID, Algorithm: "RS256", PublicKey: &privateKey.PublicKey}}, nil).
				AnyTimes()
			opts = append(opts, WithKeyProvider(provider))
		}
		v, vErr := NewTokenValidator(context.Background(), TokenValidatorConfig{
			Issuer: issuer, Audience: s.audience, JWKSURL: jwksServer.URL,
			ClientID: "c", CACertPath: caCertPath, AllowPrivateIP: true,
		}, opts...)
		require.NoError(t, vErr)
		require.NoError(t, v.ensureJWKSRegistered(context.Background()))
		_, lErr := v.jwksClient.Lookup(context.Background(), jwksServer.URL)
		require.NoError(t, lErr)

		claims := jwt.MapClaims{
			"iss": issuer, "sub": "alice",
			"exp": time.Now().Add(time.Hour).Unix(), claimnames.TokenSessionIDKey: "tsid-1",
		}
		if s.audience != "" {
			claims["aud"] = s.audience
		}
		for k, val := range extra {
			if val == nil {
				delete(claims, k)
				continue
			}
			claims[k] = val
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = testKeyID
		signed, sErr := tok.SignedString(privateKey)
		require.NoError(t, sErr)

		var captured *Identity
		h := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			captured, _ = IdentityFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+signed)
		if s.ctxUser != "" {
			req = req.WithContext(WithPlatformUser(req.Context(), s.ctxUser))
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr, captured
	}

	readerSeeing := func(t *testing.T, seen *userSeen, err error) upstreamtoken.TokenReader {
		t.Helper()
		ctrl := gomock.NewController(t)
		r := upstreamtokenmocks.NewMockTokenReader(ctrl)
		r.EXPECT().GetAllUpstreamCredentials(gomock.Any(), "tsid-1").
			DoAndReturn(func(ctx context.Context, _ string) (map[string]upstreamtoken.UpstreamCredential, map[string]error, error) {
				*seen = observeUser(ctx)
				if err != nil {
					return nil, nil, err
				}
				return map[string]upstreamtoken.UpstreamCredential{"github": {AccessToken: "gh"}}, nil, nil
			})
		return r
	}

	blank := userSeen{"", true, "", true}

	t.Run("unbound validator: gateway-style reader sees the Identity's PlatformUserID", func(t *testing.T) {
		t.Parallel()
		var seen userSeen
		rr, id := run(t, setup{audience: audience}, nil, readerSeeing(t, &seen, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, AdmissionRouteUnspecified, id.AdmissionRoute)
		require.Equal(t, userSeen{"", false, "alice", true}, seen)
		require.Equal(t, map[string]string{"github": "gh"}, id.UpstreamTokens)
	})

	t.Run("unbound validator: ErrNoCanonicalUser is a 503", func(t *testing.T) {
		t.Parallel()
		var seen userSeen
		rr, id := run(t, setup{audience: audience}, nil, readerSeeing(t, &seen, upstreamtoken.ErrNoCanonicalUser))
		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		require.Nil(t, id)
	})

	t.Run("bound validator: interactive token carries the user", func(t *testing.T) {
		t.Parallel()
		var seen userSeen
		rr, id := run(t, bound, nil, readerSeeing(t, &seen, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, AdmissionRouteInteractive, id.AdmissionRoute)
		require.Equal(t, userSeen{"alice", true, "alice", true}, seen)
		require.Equal(t, map[string]string{"github": "gh"}, id.UpstreamTokens)
	})

	t.Run("bound validator: interactive token overrides a user already on the request", func(t *testing.T) {
		t.Parallel()
		var seen userSeen
		s := bound
		s.ctxUser = "bob"
		rr, _ := run(t, s, nil, readerSeeing(t, &seen, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, userSeen{"alice", true, "alice", true}, seen)
	})

	// Each case is a token on a bound validator that does not classify as
	// interactive; the load context must carry the blank user.
	shadowed := []struct {
		name  string
		setup setup
		extra jwt.MapClaims
		route AdmissionRoute
	}{
		{"no_upstream_session", bound,
			jwt.MapClaims{claimnames.NoUpstreamSessionKey: true}, AdmissionRouteUnspecified},
		{"no key provider", setup{embedded: true, audience: audience}, nil, AdmissionRouteUnspecified},
		{"no audience", setup{embedded: true, keyProvider: true}, nil, AdmissionRouteUnspecified},
	}
	for _, tt := range shadowed {
		t.Run("bound validator: "+tt.name+" is shadowed", func(t *testing.T) {
			t.Parallel()
			var seen userSeen
			rr, id := run(t, tt.setup, tt.extra, readerSeeing(t, &seen, upstreamtoken.ErrNoCanonicalUser))
			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, tt.route, id.AdmissionRoute)
			require.Equal(t, blank, seen)
			require.Nil(t, id.UpstreamTokens)
		})
	}

	t.Run("bound validator: delegated marker with tsid is rejected before the load", func(t *testing.T) {
		t.Parallel()
		reader := upstreamtokenmocks.NewMockTokenReader(gomock.NewController(t))
		rr, id := run(t, bound, jwt.MapClaims{claimnames.DelegatedAccessKey: true}, reader)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Nil(t, id)
	})

	t.Run("bound validator: delegated token without tsid reads nothing", func(t *testing.T) {
		t.Parallel()
		reader := upstreamtokenmocks.NewMockTokenReader(gomock.NewController(t))
		rr, id := run(t, bound, jwt.MapClaims{claimnames.DelegatedAccessKey: true, claimnames.TokenSessionIDKey: nil}, reader)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, AdmissionRouteDelegated, id.AdmissionRoute)
		require.Nil(t, id.UpstreamTokens)
	})

	t.Run("bound validator: user already on the request is shadowed for a non-interactive token", func(t *testing.T) {
		t.Parallel()
		var seen userSeen
		s := bound
		s.ctxUser = "bob"
		rr, _ := run(t, s, jwt.MapClaims{claimnames.NoUpstreamSessionKey: true},
			readerSeeing(t, &seen, upstreamtoken.ErrNoCanonicalUser))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, blank, seen)
	})

	t.Run("bound validator over the real user-keyed wrapper", func(t *testing.T) {
		t.Parallel()
		base := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = base.Close() })
		wrapper, wErr := storage.NewUserKeyedUpstreamStorage(base)
		require.NoError(t, wErr)
		for _, owner := range []string{"alice", "bob"} {
			require.NoError(t, wrapper.StoreUpstreamTokens(
				WithPlatformUser(context.Background(), owner), "ignored", "github", &storage.UpstreamTokens{
					ProviderID: "github", AccessToken: "gh-" + owner, UserID: owner,
					ExpiresAt: time.Now().Add(time.Hour),
				}))
		}
		reader := upstreamtoken.NewInProcessService(wrapper, nil)

		rr, id := run(t, bound, nil, reader)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, AdmissionRouteInteractive, id.AdmissionRoute)
		require.Equal(t, map[string]string{"github": "gh-alice"}, id.UpstreamTokens)

		rr, id = run(t, bound, jwt.MapClaims{claimnames.NoUpstreamSessionKey: true}, reader)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Nil(t, id.UpstreamTokens)
		require.False(t, id.SessionlessGrant,
			"a token with a tsid is never sessionless, even with a proof, when the load was attempted")
	})

	t.Run("bound validator without an audience over session-keyed storage still loads", func(t *testing.T) {
		t.Parallel()
		base := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = base.Close() })
		require.NoError(t, base.StoreUpstreamTokens(context.Background(), "tsid-1", "github", &storage.UpstreamTokens{
			ProviderID: "github", AccessToken: "gh-alice", UserID: "alice", ExpiresAt: time.Now().Add(time.Hour),
		}))

		rr, id := run(t, setup{embedded: true, keyProvider: true}, nil, upstreamtoken.NewInProcessService(base, nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, AdmissionRouteUnspecified, id.AdmissionRoute)
		require.Equal(t, map[string]string{"github": "gh-alice"}, id.UpstreamTokens,
			"session-keyed storage ignores the blank canonical user")
	})

	t.Run("bound validator: interactive ErrNoCanonicalUser is a 503", func(t *testing.T) {
		t.Parallel()
		var seen userSeen
		rr, _ := run(t, bound, nil, readerSeeing(t, &seen, upstreamtoken.ErrNoCanonicalUser))
		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})
}

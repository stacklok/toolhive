// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	keysmocks "github.com/stacklok/toolhive/pkg/authserver/server/keys/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// TestUserKeyedUpstreamLoadEndToEnd runs a token through the real middleware over
// user-keyed storage (memory and miniredis). A validator bound to the embedded
// issuer, with its in-process key provider and an audience, classifies the token
// as interactive and loads the user's row; a validator for the same token without the
// embedded issuer answers 503, because the unbound path never writes the
// canonical-user key and the user-keyed storage reads only that key, so the
// misconfiguration is not silently turned into "no upstream tokens".
func TestUserKeyedUpstreamLoadEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		embeddedIssuer = "https://auth.toolhive.example.com"
		audience       = "https://mcp.example.com/mcp"
		keyID          = "e2e-user-keyed-key"
		providerName   = "google"
		sessionID      = "tsid-shared"
		userID         = "alice"
	)

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubKey, err := jwk.Import[jwk.Key](&privateKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, pubKey.Set(jwk.KeyIDKey, keyID))
	require.NoError(t, pubKey.Set(jwk.AlgorithmKey, "RS256"))
	require.NoError(t, pubKey.Set(jwk.KeyUsageKey, "sig"))
	keySet := jwk.NewSet()
	require.NoError(t, keySet.AddKey(pubKey))
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		buf, mErr := json.Marshal(keySet)
		if mErr != nil {
			http.Error(w, mErr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf)
	}))
	t.Cleanup(jwksServer.Close)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": embeddedIssuer, "aud": audience, "sub": userID,
		"exp":                        time.Now().Add(time.Hour).Unix(),
		claimnames.TokenSessionIDKey: sessionID,
	})
	tok.Header["kid"] = keyID
	token, err := tok.SignedString(privateKey)
	require.NoError(t, err)

	backends := map[string]func(t *testing.T) storage.Storage{
		"memory": func(t *testing.T) storage.Storage {
			t.Helper()
			s := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
		"redis": func(t *testing.T) storage.Storage {
			t.Helper()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			return storage.NewRedisStorageWithClient(client, "test:auth:")
		},
	}

	for name, newBackend := range backends {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stor, err := storage.NewUserKeyedUpstreamStorage(newBackend(t))
			require.NoError(t, err)
			require.NoError(t, stor.StoreUpstreamTokens(
				auth.WithPlatformUser(t.Context(), userID), "ignored", providerName, &storage.UpstreamTokens{
					ProviderID:  providerName,
					AccessToken: "ya29.opaque-access-token",
					UserID:      userID,
					ExpiresAt:   time.Now().Add(time.Hour),
				}))

			authenticate := func(t *testing.T, bindEmbedded bool, validatorAudience string) (*httptest.ResponseRecorder, *auth.Identity) {
				t.Helper()
				opts := []auth.TokenValidatorOption{
					auth.WithUpstreamTokenReader(upstreamtoken.NewInProcessService(stor, nil)),
				}
				if bindEmbedded {
					provider := keysmocks.NewMockPublicKeyProvider(gomock.NewController(t))
					provider.EXPECT().PublicKeys(gomock.Any()).
						Return([]*keys.PublicKeyData{{KeyID: keyID, Algorithm: "RS256", PublicKey: &privateKey.PublicKey}}, nil).
						AnyTimes()
					opts = append(opts, auth.WithEmbeddedAuthServerIssuer(embeddedIssuer), auth.WithKeyProvider(provider))
				}
				validator, vErr := auth.NewTokenValidator(t.Context(), auth.TokenValidatorConfig{
					Issuer: embeddedIssuer, Audience: validatorAudience, JWKSURL: jwksServer.URL,
					ClientID: "vmcp", AllowPrivateIP: true, InsecureAllowHTTP: true,
				}, opts...)
				require.NoError(t, vErr)

				var captured *auth.Identity
				handler := validator.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					captured, _ = auth.IdentityFromContext(r.Context())
				}))
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				return rr, captured
			}

			rr, identity := authenticate(t, true, audience)
			require.Equal(t, http.StatusOK, rr.Code)
			require.NotNil(t, identity)
			require.Equal(t, map[string]string{providerName: "ya29.opaque-access-token"}, identity.UpstreamTokens)

			rr, identity = authenticate(t, false, audience)
			require.Equal(t, http.StatusServiceUnavailable, rr.Code)
			require.Nil(t, identity)

			// A bound validator with no audience gives every token the unspecified
			// route, so even an interactive token loads nothing.
			rr, identity = authenticate(t, true, "")
			require.Equal(t, http.StatusOK, rr.Code)
			require.NotNil(t, identity)
			require.Nil(t, identity.UpstreamTokens)
		})
	}
}

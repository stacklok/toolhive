// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	upstreamtokenmocks "github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
)

// TestMiddlewareExpiredTokenAdmission covers WithExpiredTokenAdmission: an
// expired JWT reaches the admitter only when every check but exp holds and its
// exp is younger than the maximum age, and a refusal is today's 401 to the byte.
func TestMiddlewareExpiredTokenAdmission(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
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

	const maxAge = 10 * time.Minute

	makeValidator := func(t *testing.T, opts ...TokenValidatorOption) *TokenValidator {
		t.Helper()
		v, vErr := NewTokenValidator(context.Background(), TokenValidatorConfig{
			Issuer: "test-issuer", Audience: "test-audience",
			JWKSURL: jwksServer.URL, ClientID: "test-client",
			CACertPath: caCertPath, AllowPrivateIP: true,
			RequiredSubjectPrefix: "agent:",
		}, opts...)
		require.NoError(t, vErr)
		require.NoError(t, v.ensureJWKSRegistered(context.Background()))
		_, lErr := v.jwksClient.Lookup(context.Background(), jwksServer.URL)
		require.NoError(t, lErr)
		return v
	}
	sign := func(t *testing.T, signer *rsa.PrivateKey, claims jwt.MapClaims) string {
		t.Helper()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = testKeyID
		s, sErr := tok.SignedString(signer)
		require.NoError(t, sErr)
		return s
	}
	expiredAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	claims := func(overrides jwt.MapClaims) jwt.MapClaims {
		c := jwt.MapClaims{
			"iss": "test-issuer", "aud": "test-audience", "sub": "agent:one",
			"exp": expiredAt.Unix(), "jti": "j-1",
		}
		for k, v := range overrides {
			if v == nil {
				delete(c, k)
				continue
			}
			c[k] = v
		}
		return c
	}

	type result struct {
		identity *Identity
		rr       *httptest.ResponseRecorder
	}
	serve := func(t *testing.T, v *TokenValidator, token string) result {
		t.Helper()
		var captured *Identity
		handler := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			captured, _ = IdentityFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return result{identity: captured, rr: rr}
	}
	// countingAdmitter answers admit and counts its calls.
	countingAdmitter := func(admit bool) (ExpiredTokenAdmitter, *atomic.Int32) {
		var calls atomic.Int32
		return func(_ *http.Request, _ ExpiredToken) bool {
			calls.Add(1)
			return admit
		}, &calls
	}

	expiredToken := sign(t, privateKey, claims(nil))
	strict := serve(t, makeValidator(t), expiredToken)
	require.Nil(t, strict.identity)
	require.Equal(t, http.StatusUnauthorized, strict.rr.Code)
	require.Contains(t, strict.rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
	require.Contains(t, strict.rr.Body.String(), "token is expired")

	// requireStrict asserts the response is exactly the strict validator's.
	requireStrict := func(t *testing.T, got result) {
		t.Helper()
		require.Nil(t, got.identity, "the handler must not run")
		require.Equal(t, strict.rr.Code, got.rr.Code)
		require.Equal(t, strict.rr.Header().Get("WWW-Authenticate"), got.rr.Header().Get("WWW-Authenticate"))
		require.Equal(t, strict.rr.Body.String(), got.rr.Body.String())
	}

	t.Run("an admitter refusal is today's 401", func(t *testing.T) {
		t.Parallel()
		admit, calls := countingAdmitter(false)
		requireStrict(t, serve(t, makeValidator(t, WithExpiredTokenAdmission(maxAge, admit)), expiredToken))
		require.EqualValues(t, 1, calls.Load())
	})

	t.Run("an admitted token marks the identity and loads no upstream tokens", func(t *testing.T) {
		t.Parallel()
		// No expectation: loading upstream tokens fails the test.
		reader := upstreamtokenmocks.NewMockTokenReader(gomock.NewController(t))
		var seen ExpiredToken
		admit := func(r *http.Request, tok ExpiredToken) bool {
			require.Equal(t, "/mcp", r.URL.Path)
			seen = tok
			return true
		}
		v := makeValidator(t, WithExpiredTokenAdmission(maxAge, admit), WithUpstreamTokenReader(reader))
		token := sign(t, privateKey, claims(jwt.MapClaims{claimnames.TokenSessionIDKey: "session-1"}))
		got := serve(t, v, token)
		require.Equal(t, http.StatusOK, got.rr.Code)
		require.NotNil(t, got.identity)
		require.True(t, got.identity.AdmittedAfterExpiry.Equal(expiredAt))
		require.Nil(t, got.identity.UpstreamTokens)
		require.Equal(t, "agent:one", got.identity.Subject)
		require.True(t, seen.ExpiredAt.Equal(expiredAt))
		require.Equal(t, "j-1", seen.Claims["jti"])
	})

	refusedBeforeAdmitter := map[string]string{
		"bad signature": sign(t, otherKey, claims(nil)),
		"wrong issuer":  sign(t, privateKey, claims(jwt.MapClaims{"iss": "other-issuer"})),
		"wrong audience": sign(t, privateKey,
			claims(jwt.MapClaims{"aud": "other-audience"})),
		"future nbf": sign(t, privateKey,
			claims(jwt.MapClaims{"nbf": time.Now().Add(time.Hour).Unix()})),
		"subject restriction": sign(t, privateKey, claims(jwt.MapClaims{"sub": "user:one"})),
		"exp older than the maximum age": sign(t, privateKey,
			claims(jwt.MapClaims{"exp": time.Now().Add(-maxAge - time.Second).Unix()})),
		"missing exp":  sign(t, privateKey, claims(jwt.MapClaims{"exp": nil})),
		"opaque token": "not-a-jwt",
	}
	for name, token := range refusedBeforeAdmitter {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			admit, calls := countingAdmitter(true)
			withOption := serve(t, makeValidator(t, WithExpiredTokenAdmission(maxAge, admit)), token)
			without := serve(t, makeValidator(t), token)
			require.Nil(t, withOption.identity)
			require.Equal(t, http.StatusUnauthorized, withOption.rr.Code)
			require.Equal(t, without.rr.Header().Get("WWW-Authenticate"), withOption.rr.Header().Get("WWW-Authenticate"))
			require.Equal(t, without.rr.Body.String(), withOption.rr.Body.String())
			require.Zero(t, calls.Load(), "the admitter must not be asked")
		})
	}

	t.Run("a valid token never asks the admitter", func(t *testing.T) {
		t.Parallel()
		admit, calls := countingAdmitter(true)
		token := sign(t, privateKey, claims(jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()}))
		got := serve(t, makeValidator(t, WithExpiredTokenAdmission(maxAge, admit)), token)
		require.Equal(t, http.StatusOK, got.rr.Code)
		require.True(t, got.identity.AdmittedAfterExpiry.IsZero())
		require.Zero(t, calls.Load())
	})
}

func TestNewTokenValidatorRefusesInvalidExpiredTokenAdmission(t *testing.T) {
	t.Parallel()
	admit := func(*http.Request, ExpiredToken) bool { return true }
	for name, opt := range map[string]TokenValidatorOption{
		"zero maximum age":     WithExpiredTokenAdmission(0, admit),
		"negative maximum age": WithExpiredTokenAdmission(-time.Second, admit),
		"nil admitter":         WithExpiredTokenAdmission(time.Minute, nil),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewTokenValidator(context.Background(), TokenValidatorConfig{
				Issuer: "test-issuer", JWKSURL: "https://jwks.example.com/keys",
			}, opt)
			require.ErrorContains(t, err, "expired token admission")
		})
	}
}

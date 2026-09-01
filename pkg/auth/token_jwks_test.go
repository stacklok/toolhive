// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newECKey generates a fresh ECDSA P-256 key and imports its public half as a
// jwk.Key carrying the given key ID.
func newECKey(t *testing.T, kid string) (jwk.Key, *ecdsa.PrivateKey) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	key, err := jwk.Import(&priv.PublicKey)
	require.NoError(t, err)
	require.NoError(t, key.Set(jwk.KeyIDKey, kid))
	require.NoError(t, key.Set(jwk.AlgorithmKey, "ES256"))
	require.NoError(t, key.Set(jwk.KeyUsageKey, "sig"))
	return key, priv
}

// jwksDoc serialises the given public keys into a JWKS JSON document.
func jwksDoc(t *testing.T, keys ...jwk.Key) []byte {
	t.Helper()

	set := jwk.NewSet()
	for _, key := range keys {
		require.NoError(t, set.AddKey(key))
	}
	raw, err := json.Marshal(set)
	require.NoError(t, err)
	return raw
}

// mutableJWKSServer is a JWKS endpoint whose payload and status can be changed
// mid-test, and which counts how many times it was fetched.
type mutableJWKSServer struct {
	*httptest.Server

	hits   atomic.Int32
	mu     sync.Mutex
	body   []byte
	status int
}

func newMutableJWKSServer(t *testing.T, initial []byte) *mutableJWKSServer {
	t.Helper()

	s := &mutableJWKSServer{body: initial, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mutableJWKSServer) set(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
	s.status = http.StatusOK
}

func (s *mutableJWKSServer) setStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// signJWSToken signs claims with the given key under the given kid.
func signJWSToken(t *testing.T, priv *ecdsa.PrivateKey, kid, issuer string) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": issuer,
		"aud": "test-audience",
		"exp": time.Now().Add(time.Hour).Unix(),
		"sub": "test-user",
	})
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

const jwksTestIssuer = "test-issuer"

// newJWSTestValidator builds a TokenValidator pointed at the given JWKS URL.
func newJWSTestValidator(t *testing.T, jwksURL string) *TokenValidator {
	t.Helper()

	v, err := NewTokenValidator(context.Background(), TokenValidatorConfig{
		Issuer:            jwksTestIssuer,
		Audience:          "test-audience",
		JWKSURL:           jwksURL,
		AllowPrivateIP:    true,
		InsecureAllowHTTP: true, // loopback httptest server over plain HTTP
	})
	require.NoError(t, err)
	return v
}

// TestTokenValidator_SlowFirstFetchStillSucceeds replaces the old
// registration-state test for ErrNotReady tolerance with a black-box check: a
// JWKS endpoint whose first fetch is slow (but completes within the
// registration budget) must still yield a working validator on first use.
func TestTokenValidator_SlowFirstFetchStillSucceeds(t *testing.T) {
	t.Parallel()

	key, priv := newECKey(t, testKeyID)

	hit := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		hit <- struct{}{}
		// Slow, not broken: the first fetch completes within the 5s
		// registration budget.
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksDoc(t, key))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	validator := newJWSTestValidator(t, srv.URL+"/jwks")

	claims, err := validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, jwksTestIssuer))
	require.NoError(t, err, "a slow-but-successful first fetch must not fail validation")
	require.Equal(t, "test-user", claims["sub"])
}

// TestTokenValidator_OIDCRediscoveryToNewJWKSURLReregisters replaces the old
// registration-state test for ErrResourceAlreadyExists with a black-box check:
// after OIDC re-discovery resolves a NEW jwks URL, the fetcher registers the
// new URL cleanly (the old URL staying registered in the cache must not get
// in the way) and validation works end to end.
func TestTokenValidator_OIDCRediscoveryToNewJWKSURLReregisters(t *testing.T) {
	t.Parallel()

	key, priv := newECKey(t, testKeyID)
	keySetDoc := jwksDoc(t, key)

	jwksA := newMutableJWKSServer(t, keySetDoc)
	jwksB := newMutableJWKSServer(t, keySetDoc)

	// Discovery document advertising jwks A, switchable to jwks B.
	var advertise atomic.Pointer[string]
	advertise.Store(&jwksA.URL)
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   jwksTestIssuer,
			"jwks_uri": *advertise.Load() + "/jwks",
		})
	}))
	t.Cleanup(discovery.Close)

	validator, err := NewTokenValidator(context.Background(), TokenValidatorConfig{
		Issuer:            discovery.URL,
		Audience:          "test-audience",
		AllowPrivateIP:    true,
		InsecureAllowHTTP: true,
	})
	require.NoError(t, err)

	// First validation: lazy discovery resolves jwks A and registers it.
	_, err = validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, discovery.URL))
	require.NoError(t, err)

	// Re-discovery resolves jwks B this time: clear the discovery state as a
	// fresh discovery would, keeping the fetcher's cache as it is (URL A
	// registered).
	advertise.Store(&jwksB.URL)
	validator.oidcDiscoveryMu.Lock()
	validator.oidcDiscovered = false
	validator.oidcDiscoveryMu.Unlock()
	validator.jwksURL = ""

	_, err = validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, discovery.URL))
	require.NoError(t, err, "re-discovery to a new jwks URL must re-register cleanly")
}

// TestTokenValidator_JWKSBodyCap proves the newly-extended inbound hardening:
// a JWKS response larger than the default 1 MiB body cap can never be fully
// read, so the fetch never succeeds and validation fails (jwx surfaces every
// fetch failure as its own ready-wait timeout — see
// TestMultiIssuerTokenValidator_FetchJWKS in the tokenexchange package for the
// same behavior; the cap's error itself is pinned by TestLimitedBodyTransport
// in pkg/auth/jwks).
func TestTokenValidator_JWKSBodyCap(t *testing.T) {
	t.Parallel()

	key, priv := newECKey(t, testKeyID)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(jwksDoc(t, key), &doc))
	doc["padding"] = make([]byte, 1<<20+1024)
	oversized, err := json.Marshal(doc)
	require.NoError(t, err)

	srv := newMutableJWKSServer(t, oversized)

	validator := newJWSTestValidator(t, srv.URL+"/jwks")

	_, err = validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, jwksTestIssuer))
	require.Error(t, err, "a JWKS response exceeding the body cap must fail validation")
	require.Equal(t, int32(1), srv.hits.Load())

	// Within the fetch-failure backoff window, the stored error is replayed
	// without hitting the endpoint again.
	_, err = validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, jwksTestIssuer))
	require.Error(t, err)
	require.Equal(t, int32(1), srv.hits.Load(),
		"a never-successfully-fetched endpoint must not be re-fetched within the backoff window")
}

// TestTokenValidator_KeyRotationRefreshesOnUnknownKid proves rate-limited
// refresh-on-unknown-kid on the inbound path: a token signed with a rotated
// key validates on the very first attempt after rotation (one forced refresh),
// but a SECOND rotation immediately afterwards does not force another fetch —
// the 30s default gate must hold.
func TestTokenValidator_KeyRotationRefreshesOnUnknownKid(t *testing.T) {
	t.Parallel()

	key1, priv1 := newECKey(t, "v1")
	key2, priv2 := newECKey(t, "v2")
	key3, priv3 := newECKey(t, "v3")

	srv := newMutableJWKSServer(t, jwksDoc(t, key1))
	validator := newJWSTestValidator(t, srv.URL+"/jwks")

	// Prime the cache with v1.
	_, err := validator.ValidateToken(context.Background(),
		signJWSToken(t, priv1, "v1", jwksTestIssuer))
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load())

	// Rotate to v2: the unknown kid forces exactly one refresh and the new
	// key must validate on this first attempt.
	srv.set(jwksDoc(t, key2))
	_, err = validator.ValidateToken(context.Background(),
		signJWSToken(t, priv2, "v2", jwksTestIssuer))
	require.NoError(t, err, "a token signed with a rotated key must validate after the forced refresh")
	require.Equal(t, int32(2), srv.hits.Load())

	// Rotate again and immediately present a v3 token: the unknown-kid
	// refresh gate (30s default) must be closed, so no fetch happens and
	// validation fails.
	srv.set(jwksDoc(t, key3))
	_, err = validator.ValidateToken(context.Background(),
		signJWSToken(t, priv3, "v3", jwksTestIssuer))
	require.Error(t, err, "a second rotation within the kid-refresh gate must not resolve")
	require.Equal(t, int32(2), srv.hits.Load(),
		"the default kid-refresh gate must prevent a second forced fetch within 30s")
}

// TestTokenValidator_StaleOnFetchError proves stale-on-error on the inbound
// path: once a JWKS has been fetched successfully, a later failing refresh
// must not evict it — previously fetched keys keep validating.
func TestTokenValidator_StaleOnFetchError(t *testing.T) {
	t.Parallel()

	key, priv := newECKey(t, testKeyID)
	srv := newMutableJWKSServer(t, jwksDoc(t, key))
	validator := newJWSTestValidator(t, srv.URL+"/jwks")

	_, err := validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, jwksTestIssuer))
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load())

	// The endpoint breaks: the previously fetched key set must still validate
	// (stale-on-error), and no background refresh may have re-fetched.
	srv.setStatus(http.StatusInternalServerError)

	claims, err := validator.ValidateToken(context.Background(),
		signJWSToken(t, priv, testKeyID, jwksTestIssuer))
	require.NoError(t, err, "a failing refresh must not evict the previously fetched key set")
	assert.Equal(t, "test-user", claims["sub"])
	require.Equal(t, int32(1), srv.hits.Load())
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	josev3 "github.com/go-jose/go-jose/v3"
	josev4 "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	fositestorage "github.com/ory/fosite/storage"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const (
	e2eIssuer   = "https://auth.example.com"
	e2eTokenURL = "https://auth.example.com/oauth/token"
	e2eClientID = "e2e-assertion-client"
	e2eKeyID    = "e2e-client-key"
)

// registeringStore is the fosite storage plus the client registration the
// memory backend offers.
type registeringStore interface {
	fosite.Storage
	RegisterClient(context.Context, fosite.Client) error
}

// newAssertionProvider builds the provider through NewAuthorizationServer over
// a real backend holding a private_key_jwt client, and returns the key that
// signs for it.
func newAssertionProvider(t *testing.T, store registeringStore) (*fosite.Fosite, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	require.NoError(t, store.RegisterClient(context.Background(), &fosite.DefaultOpenIDConnectClient{
		DefaultClient: &fosite.DefaultClient{ID: e2eClientID},
		JSONWebKeys: &josev3.JSONWebKeySet{Keys: []josev3.JSONWebKey{{
			Key: &key.PublicKey, KeyID: e2eKeyID, Use: "sig", Algorithm: "ES256",
		}}},
		TokenEndpointAuthMethod:           "private_key_jwt",
		TokenEndpointAuthSigningAlgorithm: "ES256",
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	config, err := server.NewAuthorizationServerConfig(&server.AuthorizationServerParams{
		Issuer:               e2eIssuer,
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     10 * time.Minute,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	})
	require.NoError(t, err)
	provider, err := server.NewAuthorizationServer(config, store, nil)
	require.NoError(t, err)
	fositeProvider, ok := provider.(*fosite.Fosite)
	require.True(t, ok)
	return fositeProvider, key
}

func signE2EAssertion(t *testing.T, key *ecdsa.PrivateKey, jti string, exp any) string {
	t.Helper()
	signer, err := josev4.NewSigner(
		josev4.SigningKey{Algorithm: josev4.ES256, Key: key},
		(&josev4.SignerOptions{}).WithType("JWT").WithHeader(josev4.HeaderKey("kid"), e2eKeyID))
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": e2eClientID, "sub": e2eClientID, "aud": e2eTokenURL, "jti": jti, "exp": exp,
	}).Serialize()
	require.NoError(t, err)
	return token
}

func authenticate(provider *fosite.Fosite, assertion string) error {
	form := url.Values{
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	req := httptest.NewRequest(http.MethodPost, e2eTokenURL, strings.NewReader(form.Encode()))
	_, err := provider.AuthenticateClient(context.Background(), req, form)
	return err
}

// requireAuthenticatesOnce proves the assertion authenticates the first time
// and that its replay is refused because the jti is known.
func requireAuthenticatesOnce(t *testing.T, provider *fosite.Fosite, assertion string) {
	t.Helper()
	require.NoError(t, authenticate(provider, assertion), "first use")

	err := authenticate(provider, assertion)
	var rfcErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfcErr, "replay")
	assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
	assert.Contains(t, rfcErr.HintField, "MUST only be used once")
}

// TestClientAssertionExpiry_ExpiringWithinTheCurrentSecond covers the window
// where fosite's whole-second check still accepts an assertion whose exp is at
// or just before now. Its replay marker is kept until exp plus the skew and the
// grace, so the assertion authenticates once and cannot be replayed. Whether
// the second rolls over during the test does not matter: fosite then reports a
// time error and the clock skew fallback accepts the exp that is 1s ago.
func TestClientAssertionExpiry_ExpiringWithinTheCurrentSecond(t *testing.T) {
	t.Parallel()
	provider, key := newAssertionProvider(t, storage.NewMemoryStorage())

	requireAuthenticatesOnce(t, provider, signE2EAssertion(t, key, "jti-last-second", time.Now().Unix()))
}

// TestClientAssertionExpiry_ExpiredWithinTheSkew sends an assertion whose exp
// is 30s ago. fosite refuses it with a time error and the clock skew fallback
// takes over, through the real provider chain and a real backend.
func TestClientAssertionExpiry_ExpiredWithinTheSkew(t *testing.T) {
	t.Parallel()
	provider, key := newAssertionProvider(t, storage.NewMemoryStorage())

	requireAuthenticatesOnce(t, provider, signE2EAssertion(t, key, "jti-skewed", time.Now().Add(-30*time.Second).Unix()))
}

// TestClientAssertionExpiry_ExpiredWithinTheSkewOnRedis is the same check on
// the Redis backend, so the TTL of the marker is exercised too.
func TestClientAssertionExpiry_ExpiredWithinTheSkewOnRedis(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := storage.NewRedisStorageWithClient(client, "e2e:auth:")
	t.Cleanup(func() {
		_ = store.Close()
		mr.Close()
	})
	provider, key := newAssertionProvider(t, store)

	requireAuthenticatesOnce(t, provider, signE2EAssertion(t, key, "jti-skewed-redis", time.Now().Add(-30*time.Second).Unix()))
}

// fositeMemoryStore adds client registration to fosite's own MemoryStore, which
// keeps a jti only until the deadline SetClientAssertionJWT is given.
type fositeMemoryStore struct {
	*fositestorage.MemoryStore
}

func (s fositeMemoryStore) RegisterClient(_ context.Context, c fosite.Client) error {
	s.Clients[c.GetID()] = c
	return nil
}

// TestClientAssertionExpiry_ReplayOnStoreKeepingUntilTheGivenDeadline covers a
// third-party store that, like fosite's MemoryStore, keeps the jti only until
// the deadline it is given. The server must give it a deadline that outlives
// the clock skew, otherwise an assertion accepted in the skew window could be
// replayed until exp plus the skew.
func TestClientAssertionExpiry_ReplayOnStoreKeepingUntilTheGivenDeadline(t *testing.T) {
	t.Parallel()

	t.Run("exp already past, accepted through the skew", func(t *testing.T) {
		t.Parallel()
		provider, key := newAssertionProvider(t, fositeMemoryStore{fositestorage.NewMemoryStore()})
		assertion := signE2EAssertion(t, key, "jti-skewed-fosite-store", time.Now().Add(-30*time.Second).Unix())

		require.NoError(t, authenticate(provider, assertion), "first use")
		for i := range 3 {
			requireReplayRefused(t, authenticate(provider, assertion), i)
		}
	})

	t.Run("used before exp, replayed after exp", func(t *testing.T) {
		t.Parallel()
		provider, key := newAssertionProvider(t, fositeMemoryStore{fositestorage.NewMemoryStore()})
		exp := time.Now().Add(time.Second)
		assertion := signE2EAssertion(t, key, "jti-expiring-fosite-store", exp.Unix())

		require.NoError(t, authenticate(provider, assertion), "first use")
		time.Sleep(time.Until(time.Unix(exp.Unix(), 0)) + 1500*time.Millisecond)
		require.Greater(t, time.Now().Unix(), exp.Unix(), "exp must be past")
		for i := range 3 {
			requireReplayRefused(t, authenticate(provider, assertion), i)
		}
	})
}

func requireReplayRefused(t *testing.T, err error, attempt int) {
	t.Helper()
	var rfcErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfcErr, "replay %d", attempt)
	assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField, "replay %d", attempt)
	assert.Contains(t, rfcErr.HintField, "MUST only be used once", "replay %d", attempt)
}

// countingAssertionStore records SetClientAssertionJWT calls instead of
// storing them, so it cannot refuse anything itself.
type countingAssertionStore struct {
	*storage.MemoryStorage
	setCalls atomic.Int32
}

func (s *countingAssertionStore) SetClientAssertionJWT(context.Context, string, time.Time) error {
	s.setCalls.Add(1)
	return nil
}

// TestClientAssertionExpiry_ProviderWrapsStore proves that
// NewAuthorizationServer hands fosite a wrapper that refuses an exp whose
// retention has passed before the backend sees it, and that an exp-0 assertion
// cannot be replayed.
func TestClientAssertionExpiry_ProviderWrapsStore(t *testing.T) {
	t.Parallel()

	backend := &countingAssertionStore{MemoryStorage: storage.NewMemoryStorage()}
	provider, key := newAssertionProvider(t, backend)

	assertion := signE2EAssertion(t, key, "jti-wired", int64(0))
	err := authenticate(provider, assertion)

	var rfcErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfcErr)
	assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
	assert.Contains(t, rfcErr.HintField, "has expired or has no 'exp'")
	assert.Zero(t, backend.setCalls.Load(), "the backend must not see a refused marker")

	// The reservation ClientAssertionJWTValid took makes the jti known.
	err = authenticate(provider, assertion)
	require.ErrorAs(t, err, &rfcErr)
	assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
	assert.Zero(t, backend.setCalls.Load(), "the replay must not reach the backend")

	require.NoError(t, authenticate(provider, signE2EAssertion(t, key, "jti-ok", time.Now().Add(time.Minute).Unix())))
	assert.EqualValues(t, 1, backend.setCalls.Load(), "a valid marker must reach the backend")
}

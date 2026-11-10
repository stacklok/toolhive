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

	josev3 "github.com/go-jose/go-jose/v3"
	josev4 "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
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

// TestClientAssertionExpiry_LastWholeSecond covers the one window where fosite
// and the wrapper disagree: fosite accepts an assertion while now.Unix() <= exp,
// but the wrapper requires exp.After(now), so in [exp, exp+1s) fosite passes
// and the wrapper refuses.
func TestClientAssertionExpiry_LastWholeSecond(t *testing.T) {
	t.Parallel()
	provider, key := newAssertionProvider(t, storage.NewMemoryStorage())

	now := time.Now()
	if now.Nanosecond() > int(900*time.Millisecond) {
		t.Skip("too close to a second boundary: fosite could see the next second")
	}
	assertion := signE2EAssertion(t, key, "jti-last-second", now.Unix())

	for attempt := 1; attempt <= 2; attempt++ {
		err := authenticate(provider, assertion)

		var rfcErr *fosite.RFC6749Error
		require.ErrorAs(t, err, &rfcErr, "attempt %d", attempt)
		assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField, "attempt %d", attempt)
		if time.Now().Unix() != now.Unix() {
			t.Skip("clock crossed a second boundary during the test")
		}
		if attempt == 1 {
			assert.Contains(t, rfcErr.HintField, "'exp' in the future")
		}
	}
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
// NewAuthorizationServer hands fosite a wrapper that refuses a non-future exp
// before the backend sees it, and that an exp-0 assertion cannot be replayed.
func TestClientAssertionExpiry_ProviderWrapsStore(t *testing.T) {
	t.Parallel()

	backend := &countingAssertionStore{MemoryStorage: storage.NewMemoryStorage()}
	provider, key := newAssertionProvider(t, backend)

	assertion := signE2EAssertion(t, key, "jti-wired", int64(0))
	err := authenticate(provider, assertion)

	var rfcErr *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfcErr)
	assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
	assert.Contains(t, rfcErr.HintField, "'exp' in the future")
	assert.Zero(t, backend.setCalls.Load(), "the backend must not see a refused marker")

	// The reservation ClientAssertionJWTValid took makes the jti known.
	err = authenticate(provider, assertion)
	require.ErrorAs(t, err, &rfcErr)
	assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
	assert.Zero(t, backend.setCalls.Load(), "the replay must not reach the backend")

	require.NoError(t, authenticate(provider, signE2EAssertion(t, key, "jti-ok", time.Now().Add(time.Minute).Unix())))
	assert.EqualValues(t, 1, backend.setCalls.Load(), "a valid marker must reach the backend")
}

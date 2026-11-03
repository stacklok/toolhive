// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	assertionTestIssuer   = "https://auth.example.com"
	assertionTestTokenURL = "https://auth.example.com/oauth/token"
	assertionTestClientID = "assertion-client"
	assertionTestKeyID    = "client-key-1"
)

// assertionStore is the slice of fosite.Storage that client authentication
// with a private_key_jwt assertion touches.
type assertionStore struct {
	fosite.Storage // unused methods panic if reached
	client         fosite.Client
	seen           map[string]struct{}
	setErr         error
}

func (s *assertionStore) GetClient(context.Context, string) (fosite.Client, error) {
	return s.client, nil
}

func (s *assertionStore) ClientAssertionJWTValid(_ context.Context, jti string) error {
	if _, ok := s.seen[jti]; ok {
		return fosite.ErrJTIKnown
	}
	return nil
}

func (s *assertionStore) SetClientAssertionJWT(_ context.Context, jti string, _ time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.seen[jti] = struct{}{}
	return nil
}

func TestClientAssertionErrorStrategy(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	now := time.Now()
	valid := func(c *jwt.Claims) {}
	storageDown := errors.New("redis: connection refused")

	tests := []struct {
		name       string
		mutate     func(*jwt.Claims)
		kid        string
		signingKey *ecdsa.PrivateKey
		reuse      bool
		setErr     error
		wantErr    string // empty means authentication succeeds
		wantStatus int
	}{
		{name: "valid assertion authenticates", mutate: valid},
		{
			name:    "expired assertion",
			mutate:  func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(now.Add(-time.Hour)) },
			wantErr: "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:    "assertion issued in the future",
			mutate:  func(c *jwt.Claims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Hour)) },
			wantErr: "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:    "assertion not valid yet",
			mutate:  func(c *jwt.Claims) { c.NotBefore = jwt.NewNumericDate(now.Add(time.Hour)) },
			wantErr: "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:    "reused jti",
			mutate:  valid,
			reuse:   true,
			wantErr: "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:    "unknown key id",
			mutate:  valid,
			kid:     "no-such-key",
			wantErr: "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "signed with another key",
			mutate:     valid,
			signingKey: otherKey,
			wantErr:    "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:    "wrong audience",
			mutate:  func(c *jwt.Claims) { c.Audience = jwt.Audience{"https://elsewhere.example.com"} },
			wantErr: "invalid_client", wantStatus: http.StatusUnauthorized,
		},
		{
			name:    "storage failure is not an authentication verdict",
			mutate:  valid,
			setErr:  storageDown,
			wantErr: storageDown.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &assertionStore{
				seen:   map[string]struct{}{},
				setErr: tt.setErr,
				client: &fosite.DefaultOpenIDConnectClient{
					DefaultClient: &fosite.DefaultClient{ID: assertionTestClientID},
					JSONWebKeys: &josev3.JSONWebKeySet{Keys: []josev3.JSONWebKey{{
						Key: &key.PublicKey, KeyID: assertionTestKeyID, Use: "sig", Algorithm: "ES256",
					}}},
					TokenEndpointAuthMethod:           "private_key_jwt",
					TokenEndpointAuthSigningAlgorithm: "ES256",
				},
			}
			cfg := &fosite.Config{AccessTokenIssuer: assertionTestIssuer, TokenURL: assertionTestTokenURL}
			provider := fosite.NewOAuth2Provider(store, newClientAssertionAudienceConfig(cfg))
			strategy := newClientAssertionErrorStrategy(provider.DefaultClientAuthenticationStrategy)

			claims := jwt.Claims{
				Issuer:   assertionTestClientID,
				Subject:  assertionTestClientID,
				Audience: jwt.Audience{assertionTestTokenURL},
				ID:       "jti-" + tt.name,
				IssuedAt: jwt.NewNumericDate(now),
				Expiry:   jwt.NewNumericDate(now.Add(time.Minute)),
			}
			tt.mutate(&claims)
			if tt.reuse {
				store.seen[claims.ID] = struct{}{}
			}
			kid := assertionTestKeyID
			if tt.kid != "" {
				kid = tt.kid
			}
			signer := key
			if tt.signingKey != nil {
				signer = tt.signingKey
			}
			assertion := signAssertion(t, signer, kid, claims)

			form := url.Values{
				"client_assertion_type": {jwtBearerClientAssertionType},
				"client_assertion":      {assertion},
			}
			req := httptest.NewRequest(http.MethodPost, assertionTestTokenURL, strings.NewReader(form.Encode()))

			client, err := strategy(context.Background(), req, form)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, assertionTestClientID, client.GetID())
				return
			}
			require.Error(t, err)
			var rfcErr *fosite.RFC6749Error
			if tt.wantStatus == 0 {
				assert.False(t, errors.As(err, &rfcErr), "storage errors must not be rewritten: %v", err)
				assert.ErrorIs(t, err, tt.setErr)
				return
			}
			require.ErrorAs(t, err, &rfcErr)
			assert.Equal(t, tt.wantErr, rfcErr.ErrorField)
			assert.Equal(t, tt.wantStatus, rfcErr.CodeField)
		})
	}
}

func TestClientAssertionErrorStrategy_IgnoresOtherAuthentication(t *testing.T) {
	t.Parallel()

	want := fosite.ErrInvalidRequest.WithHint("untouched")
	strategy := newClientAssertionErrorStrategy(
		func(context.Context, *http.Request, url.Values) (fosite.Client, error) { return nil, want })

	form := url.Values{"client_id": {"c"}}
	_, err := strategy(context.Background(), httptest.NewRequest(http.MethodPost, "/", nil), form)
	assert.Same(t, want, err)
}

func signAssertion(t *testing.T, key *ecdsa.PrivateKey, kid string, claims jwt.Claims) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid))
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

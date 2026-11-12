// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	josev4 "github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/token/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func newTestJWTSigner(t *testing.T) *jwt.DefaultSigner {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	jwk := &josev3.JSONWebKey{
		Key:       key,
		KeyID:     "test-kid",
		Algorithm: string(josev3.RS256),
		Use:       "sig",
	}
	return &jwt.DefaultSigner{
		GetPrivateKey: func(_ context.Context) (interface{}, error) { return jwk, nil },
	}
}

func decodeJOSEHeader(t *testing.T, token string) map[string]any {
	t.Helper()

	segments := strings.Split(token, ".")
	require.Len(t, segments, 3, "compact JWS must have three segments")

	raw, err := base64.RawURLEncoding.DecodeString(segments[0])
	require.NoError(t, err)

	var header map[string]any
	require.NoError(t, json.Unmarshal(raw, &header))
	return header
}

func TestATJWTAccessTokenSigner_Generate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		header    jwt.Mapper
		wantTyp   string
		wantExtra map[string]any
	}{
		{
			name:    "empty headers",
			header:  jwt.NewHeaders(),
			wantTyp: AccessTokenTypeATJWT,
		},
		{
			name:      "caller header entries are kept",
			header:    &jwt.Headers{Extra: map[string]any{"x-test": "v"}},
			wantTyp:   AccessTokenTypeATJWT,
			wantExtra: map[string]any{"x-test": "v"},
		},
		{
			name:    "non-access JWT keeps its typ",
			header:  &jwt.Headers{Extra: map[string]any{"typ": oauthproto.JWTTypeIDJAG}},
			wantTyp: oauthproto.JWTTypeIDJAG,
		},
		{
			name:    "untyped nil mapper",
			header:  nil,
			wantTyp: AccessTokenTypeATJWT,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var snapshot map[string]any
			if h, ok := tt.header.(*jwt.Headers); ok {
				snapshot = maps.Clone(h.Extra)
			}

			signer := NewATJWTAccessTokenSigner(newTestJWTSigner(t))
			claims := jwt.MapClaims{
				"sub": "user-1",
				"exp": time.Now().Add(time.Hour).Unix(),
			}

			token, _, err := signer.Generate(context.Background(), claims, tt.header)
			require.NoError(t, err)

			decoded := decodeJOSEHeader(t, token)
			assert.Equal(t, tt.wantTyp, decoded["typ"])
			assert.Equal(t, "test-kid", decoded["kid"])
			for k, v := range tt.wantExtra {
				assert.Equal(t, v, decoded[k], "header entry %q", k)
			}

			if h, ok := tt.header.(*jwt.Headers); ok {
				assert.Equal(t, snapshot, h.Extra, "caller's header must not change")
			}

			_, err = signer.Validate(context.Background(), token)
			assert.NoError(t, err)
		})
	}
}

func newTestSigningKey(t *testing.T) *josev4.JSONWebKey {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return &josev4.JSONWebKey{Key: privateKey, KeyID: "signer-test-key", Algorithm: string(josev4.ES256), Use: "sig"}
}

func newTestSignerConfig(t *testing.T) *AuthorizationServerConfig {
	t.Helper()

	jwk := newTestSigningKey(t)
	return &AuthorizationServerConfig{
		Config:      &fosite.Config{},
		SigningKey:  jwk,
		SigningJWKS: &josev4.JSONWebKeySet{Keys: []josev4.JSONWebKey{*jwk}},
	}
}

func TestSigner_SignIDJAG(t *testing.T) {
	t.Parallel()

	cfg := newTestSignerConfig(t)
	signer := NewSigner(cfg.SigningKey)

	token, err := signer.SignIDJAG(t.Context(), map[string]any{"sub": "user"})
	require.NoError(t, err)
	header := decodeJOSEHeader(t, token)

	t.Run("the given typ is kept", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, oauthproto.JWTTypeIDJAG, header["typ"])
	})

	t.Run("kid is a key in the served JWKS and the signature verifies", func(t *testing.T) {
		t.Parallel()
		kid, ok := header["kid"].(string)
		require.True(t, ok, "header must carry a kid, got %v", header)
		keys := cfg.PublicJWKS().Key(kid)
		require.Len(t, keys, 1, "kid must be in the served JWKS")

		parsed, err := josev4.ParseSignedCompact(token, []josev4.SignatureAlgorithm{josev4.ES256})
		require.NoError(t, err)
		_, err = parsed.Verify(keys[0].Key)
		require.NoError(t, err)
	})

}

func TestSigner_signRefusesTyps(t *testing.T) {
	t.Parallel()

	signer := NewSigner(newTestSigningKey(t))
	typs := []string{
		"", AccessTokenTypeATJWT, "AT+JWT", "At+JWT", "application/at+jwt", "application/AT+JWT",
		"JWT", "jwt", "application/JWT", "application/jwt",
	}
	for _, typ := range typs {
		t.Run(fmt.Sprintf("typ %q is an error", typ), func(t *testing.T) {
			t.Parallel()
			_, err := signer.sign(t.Context(), map[string]any{"sub": "user"}, typ)
			require.Error(t, err)
		})
	}

	t.Run("a non-access-token typ is signed", func(t *testing.T) {
		t.Parallel()
		token, err := signer.sign(t.Context(), map[string]any{"sub": "user"}, "example+jwt")
		require.NoError(t, err)
		assert.Equal(t, "example+jwt", decodeJOSEHeader(t, token)["typ"])
	})
}

func TestSigner_AccessTokenStrategy(t *testing.T) {
	t.Parallel()

	signer := NewSigner(newTestSigningKey(t))

	tests := []struct {
		name    string
		atJWT   bool
		wantTyp string
	}{
		{name: "at+jwt wrapper sets typ", atJWT: true, wantTyp: AccessTokenTypeATJWT},
		{name: "raw signer uses fosite's JWT typ", atJWT: false, wantTyp: "JWT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := &fosite.Config{AccessTokenIssuer: "https://issuer.example.com", AccessTokenLifespan: time.Hour}
			strategy, err := signer.AccessTokenStrategy(config, tt.atJWT)
			require.NoError(t, err)

			session := &oauth2.JWTSession{
				JWTClaims: &jwt.JWTClaims{Subject: "user"},
				JWTHeader: &jwt.Headers{},
				ExpiresAt: map[fosite.TokenType]time.Time{fosite.AccessToken: time.Now().Add(time.Hour)},
			}
			requester := fosite.NewAccessRequest(session)
			requester.Client = &fosite.DefaultClient{ID: "client"}

			token, _, err := strategy.GenerateAccessToken(t.Context(), requester)
			require.NoError(t, err)
			header := decodeJOSEHeader(t, token)
			assert.Equal(t, tt.wantTyp, header["typ"])
			assert.Equal(t, "signer-test-key", header["kid"])
		})
	}
}

func TestSigner_UnusableSigner(t *testing.T) {
	t.Parallel()

	for name, signer := range map[string]*Signer{"nil": nil, "zero value": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := signer.SignIDJAG(t.Context(), map[string]any{"sub": "user"})
			require.ErrorIs(t, err, errNoSigner)

			strategy, err := signer.AccessTokenStrategy(&fosite.Config{}, true)
			require.Error(t, err)
			assert.Nil(t, strategy)
		})
	}
}

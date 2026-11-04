// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite/token/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			header:  &jwt.Headers{Extra: map[string]any{"typ": "oauth-id-jag+jwt"}},
			wantTyp: "oauth-id-jag+jwt",
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

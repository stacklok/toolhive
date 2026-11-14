// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compactJWS builds a compact JWS whose protected header is the given JSON.
// The payload and signature are placeholders: the guard never verifies them.
func compactJWS(header string) string {
	return compactJWSWithPayload(header, `{}`)
}

// compactJWSWithPayload builds a compact JWS with the given header and payload
// JSON and a placeholder signature.
func compactJWSWithPayload(header, payload string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString([]byte(payload)) + ".c2ln"
}

// deeplyNestedClaims nests one level past encoding/json's limit of 10000, and
// far below the depth that overflows go-jose v3, so a regression fails the
// test instead of crashing the test binary.
func deeplyNestedClaims() string {
	return `{"sub":"c1","x":` + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + `}`
}

func TestUnsafeJWSGuardClientAuthenticationStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		form         url.Values
		wantDelegate bool
	}{
		{
			name:         "no client assertion",
			form:         url.Values{"client_id": {"c1"}},
			wantDelegate: true,
		},
		{
			name: "safe client assertion",
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				"client_assertion":      {compactJWS(`{"alg":"RS256","kid":"k1"}`)},
			},
			wantDelegate: true,
		},
		{
			name: "client assertion with embedded jwk",
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				"client_assertion":      {compactJWS(`{"alg":"RS256","jwk":{"kty":"RSA"}}`)},
			},
		},
		{
			name: "client assertion with deeply nested claims",
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				"client_assertion":      {compactJWSWithPayload(`{"alg":"RS256","kid":"k1"}`, deeplyNestedClaims())},
			},
		},
		{
			name: "embedded jwk in a repeated client assertion",
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				"client_assertion": {
					compactJWS(`{"alg":"RS256","kid":"k1"}`),
					compactJWS(`{"alg":"RS256","jwk":{"kty":"RSA"}}`),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			delegated := false
			strategy := newUnsafeJWSGuardClientAuthenticationStrategy(
				func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
					delegated = true
					return &fosite.DefaultClient{ID: "c1"}, nil
				},
			)

			client, err := strategy(context.Background(), httptest.NewRequest(http.MethodPost, "/oauth/token", nil), tt.form)

			assert.Equal(t, tt.wantDelegate, delegated)
			if tt.wantDelegate {
				require.NoError(t, err)
				assert.Equal(t, "c1", client.GetID())
				return
			}
			require.ErrorIs(t, err, fosite.ErrInvalidClient)
			assert.Nil(t, client)
		})
	}
}

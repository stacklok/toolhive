// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compactJWS builds a compact JWS whose protected header is the given JSON.
// The payload and signature are placeholders: the guard never verifies them.
func compactJWS(header string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString([]byte(`{}`)) + ".c2ln"
}

func TestIsUnsafeFositeJWS(t *testing.T) {
	t.Parallel()

	paddedHeader := base64.URLEncoding.EncodeToString([]byte(`{"alg":"RS256","jwk":{"kty":"RSA"}}`))

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "plain compact JWS", token: compactJWS(`{"alg":"RS256","kid":"k1"}`), want: false},
		{name: "embedded jwk", token: compactJWS(`{"alg":"RS256","jwk":{"kty":"RSA","n":"AQ","e":"AQAB"}}`), want: true},
		{name: "embedded jwk with differently cased key", token: compactJWS(`{"alg":"RS256","JWK":{"kty":"RSA"}}`), want: true},
		{name: "embedded jwk hidden by whitespace", token: " " + compactJWS(`{"alg":"RS256","jwk":{}}`)[:4] + "\n" + compactJWS(`{"alg":"RS256","jwk":{}}`)[4:], want: true},
		{name: "embedded jwk behind base64 padding", token: paddedHeader + ".e30.c2ln", want: true},
		{name: "JSON serialization", token: `{"payload":"e30","signatures":[]}`, want: true},
		{name: "JSON serialization after whitespace", token: "\t{\"payload\":\"e30\"}", want: true},
		{name: "too few segments", token: "e30.e30", want: true},
		{name: "too many segments", token: "e30.e30.e30.e30.e30", want: true},
		{name: "header not base64", token: "!!!.e30.c2ln", want: true},
		{name: "header not a JSON object", token: compactJWS(`["jwk"]`), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsUnsafeFositeJWS(tt.token))
		})
	}
}

func TestEmbeddedJWKGuardClientAuthenticationStrategy(t *testing.T) {
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
			strategy := newEmbeddedJWKGuardClientAuthenticationStrategy(
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

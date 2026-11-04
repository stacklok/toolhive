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

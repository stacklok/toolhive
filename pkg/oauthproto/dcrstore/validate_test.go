// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcrstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
)

func validCredentials() *Credentials {
	return &Credentials{
		Key: dcrkey.Key{
			Issuer:      "issuer",
			UpstreamID:  "upstream",
			RedirectURI: "redirect",
			ScopesHash:  dcrkey.ScopesHash(nil),
		},
		ClientID:              "client",
		AuthorizationEndpoint: "auth",
		TokenEndpoint:         "token",
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Credentials)
		hint   string // empty means valid
	}{
		{name: "valid without secret", mutate: func(*Credentials) {}},
		{name: "issuer", mutate: func(c *Credentials) { c.Key.Issuer = "" }, hint: "dcr credentials key issuer cannot be empty"},
		{name: "upstream", mutate: func(c *Credentials) { c.Key.UpstreamID = "" }, hint: "dcr credentials key upstream_id cannot be empty"},
		{name: "redirect", mutate: func(c *Credentials) { c.Key.RedirectURI = "" }, hint: "dcr credentials key redirect_uri cannot be empty"},
		{name: "scopes hash", mutate: func(c *Credentials) { c.Key.ScopesHash = "" }, hint: "dcr credentials key scopes_hash cannot be empty"},
		{name: "client id", mutate: func(c *Credentials) { c.ClientID = "" }, hint: "dcr credentials client_id cannot be empty"},
		{name: "auth endpoint", mutate: func(c *Credentials) { c.AuthorizationEndpoint = "" }, hint: "dcr credentials authorization_endpoint cannot be empty"},
		{name: "token endpoint", mutate: func(c *Credentials) { c.TokenEndpoint = "" }, hint: "dcr credentials token_endpoint cannot be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creds := validCredentials()
			tt.mutate(creds)
			err := Validate(creds)
			if tt.hint == "" {
				require.NoError(t, err)
				return
			}
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tt.hint, ve.Hint)
		})
	}

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		var ve *ValidationError
		require.ErrorAs(t, Validate(nil), &ve)
		assert.Equal(t, "dcr credentials cannot be nil", ve.Hint)
	})
}

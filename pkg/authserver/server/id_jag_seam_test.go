// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/url"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestOwnsResourceParameter(t *testing.T) {
	t.Parallel()

	exchange := fosite.Arguments{oauthproto.GrantTypeTokenExchange}
	requestedIDJAG := url.Values{"requested_token_type": {oauthproto.TokenTypeIDJAG}}

	tests := []struct {
		name   string
		grants fosite.Arguments
		form   url.Values
		want   bool
	}{
		{name: "token exchange asking for an ID-JAG", grants: exchange, form: requestedIDJAG, want: true},
		{
			name:   "grant_type with two values",
			grants: fosite.Arguments{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeRefreshToken},
			form:   requestedIDJAG,
		},
		{name: "other grant type", grants: fosite.Arguments{oauthproto.GrantTypeAuthorizationCode}, form: requestedIDJAG},
		{name: "requested_token_type absent", grants: exchange, form: url.Values{}},
		{
			name:   "requested_token_type access token",
			grants: exchange,
			form:   url.Values{"requested_token_type": {oauthproto.TokenTypeAccessToken}},
		},
		{
			// url.Values.Get returns the first value, so a repeated parameter is read as its
			// first value. A misread is safe: the token endpoint refuses any response that
			// is not an ID-JAG or that carries a granted audience.
			name:   "repeated requested_token_type, ID-JAG first",
			grants: exchange,
			form:   url.Values{"requested_token_type": {oauthproto.TokenTypeIDJAG, oauthproto.TokenTypeAccessToken}},
			want:   true,
		},
		{
			name:   "repeated requested_token_type, ID-JAG second",
			grants: exchange,
			form:   url.Values{"requested_token_type": {oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeIDJAG}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			requester := &fosite.AccessRequest{GrantTypes: tt.grants, Request: fosite.Request{Form: tt.form}}
			assert.Equal(t, tt.want, OwnsResourceParameter(requester))
		})
	}
}

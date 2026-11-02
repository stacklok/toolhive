// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// ReasonDPoPNotSupported is the stable error_description the token endpoint
// sends when a request for an ID-JAG carries a DPoP header. It is the whole
// description, with no hint.
const ReasonDPoPNotSupported = "dpop_not_supported"

// OwnsResourceParameter reports whether a token-exchange request asks for an
// ID-JAG, in which case the ID-JAG path owns the "resource" parameter and the
// token endpoint must not validate it against AllowedAudiences or grant it as
// the access token audience: the resource names an endpoint behind another
// authorization server.
//
// It keys only on the grant type and requested_token_type, never on
// subject_token_type, so every subject source (access token, ID token, SAML)
// shares the answer. A repeated requested_token_type is read as its first
// value, as fosite and the handlers' CanHandleTokenEndpointRequest read it.
//
// It lives in this package, not in tokenexchange, because package handlers
// calls it and must not import tokenexchange and its dependencies.
func OwnsResourceParameter(requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeTokenExchange) &&
		requester.GetRequestForm().Get("requested_token_type") == oauthproto.TokenTypeIDJAG
}

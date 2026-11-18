// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// reasonRepeatedParameter goes with invalid_request when the
// authorization_details parameter appears more than once.
const reasonRepeatedParameter = "repeated_parameter"

func detailsRefusal(reason string) error {
	return errorsx.WithStack(exactReason(string(authorizationdetails.CodeInvalidAuthorizationDetails), reason))
}

// refuseSignedDetails refuses an assertion that carries authorization_details.
// The ID-JAG draft (section 4.4.1) asks a redeeming server to process the claim,
// but this server cannot carry a signed restriction into the token, so it
// refuses instead of dropping or replacing it with the request parameter.
func refuseSignedDetails(claims *ValidatedClaims) error {
	_, inExtra := claims.Extra[authorizationDetailsParam]
	_, inClaims := claims.Claims[authorizationDetailsParam]
	if inExtra || inClaims {
		return invalidAssertion("The assertion must not carry an 'authorization_details' claim.")
	}
	return nil
}

// redemptionDetailsRequest builds the hook request from the validated
// assertion and the granted resource. clientID is the authenticated client,
// empty for the plain grant, which authenticates none.
func redemptionDetailsRequest(claims *ValidatedClaims, clientID, resource string) authorizationdetails.Request {
	return authorizationdetails.Request{
		ClientID:        clientID,
		GrantedAudience: []string{resource},
		Subject: authorizationdetails.Subject{
			Issuer:   claims.Issuer,
			Subject:  claims.Subject,
			ClientID: claims.ClientID,
			JWTID:    claims.JWTID,
			Audience: slices.Clone(claims.Audience),
			IssuedAt: claims.IssuedAt,
			Expiry:   claims.Expiry,
			External: true,
			Claims:   deepCopyClaims(claims.Claims),
		},
	}
}

// redemptionDetailsClaim validates the authorization_details parameter of a
// JWT-bearer or ID-JAG redemption against the injected registry, for the
// JWT-bearer grant type. It returns nil when the parameter is absent or a
// single empty value. With no registry, or no type registered for the grant,
// any entry is refused as an unknown type (RFC 9396). Otherwise it returns the
// granted JSON as the hook produced it, for the access token claim and the
// response member.
func (h *JWTBearerHandler) redemptionDetailsClaim(
	ctx context.Context, form url.Values, claims *ValidatedClaims, clientID, resource string,
) (json.RawMessage, error) {
	values := form[authorizationDetailsParam]
	if len(values) == 0 || (len(values) == 1 && values[0] == "") {
		return nil, nil
	}
	if len(values) > 1 {
		warnAuthorizationDetailsRejected(clientID, rejectRepeated)
		return nil, errorsx.WithStack(exactReason(string(authorizationdetails.CodeInvalidRequest), reasonRepeatedParameter))
	}
	if h.detailsRegistry == nil {
		warnAuthorizationDetailsRejected(clientID, rejectMalformed)
		return nil, detailsRefusal(reasonMalformedEntry)
	}
	pending, err := h.detailsRegistry.Parse(values[0], oauthproto.GrantTypeJWTBearer)
	if err != nil {
		warnAuthorizationDetailsRejected(clientID, rejectMalformed)
		return nil, detailsRefusal(reasonMalformedEntry)
	}
	granted, err := pending.Grant(ctx, redemptionDetailsRequest(claims, clientID, resource))
	if err != nil {
		return nil, errorsx.WithStack(authorizationDetailsGrantError(clientID, err))
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(granted, &entries); err != nil || len(entries) == 0 {
		// "[]" never reaches a hook, so an empty grant is refused here.
		return nil, detailsRefusal(reasonMalformedEntry)
	}
	return granted, nil
}

// setRedemptionDetailsClaim copies the granted entries into the access token.
func setRedemptionDetailsClaim(issued *session.Session, entries json.RawMessage) {
	if entries != nil {
		issued.JWTClaims.Extra[authorizationDetailsParam] = entries
	}
}

// echoRedemptionDetailsClaim returns the entries stored in the session in the
// token response, so the response carries exactly what the token does.
func echoRedemptionDetailsClaim(requester fosite.AccessRequester, responder fosite.AccessResponder) {
	if issued, ok := requester.GetSession().(*session.Session); ok {
		if entries, present := issued.JWTClaims.Extra[authorizationDetailsParam]; present {
			responder.SetExtra(authorizationDetailsParam, entries)
		}
	}
}

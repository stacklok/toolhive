// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/ory/fosite"
)

// jwtBearerClientAssertionType is the client_assertion_type for private_key_jwt
// client authentication (RFC 7523 section 2.2).
const jwtBearerClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// clientAssertionClaimErrors are the messages of the claim validation errors
// that fosite returns unwrapped, as plain errors, when it parses a client
// assertion (see fosite/token/jwt MapClaims.Valid).
var clientAssertionClaimErrors = map[string]struct{}{
	"Token is expired":         {},
	"Token used before issued": {},
	"Token is not valid yet":   {},
}

// newClientAssertionErrorStrategy makes every rejected private_key_jwt client
// assertion surface as invalid_client, as RFC 6749 section 5.2 requires for a
// failed client authentication.
//
// fosite already does this for most failures, but a few escape with another
// error: an expired, not-yet-valid or future-issued assertion comes back as a
// plain error (HTTP 500 server_error), and an unknown signing key or a reused
// jti comes back as invalid_request / jti_known (HTTP 400). Errors that are not
// a verdict on the assertion, such as a storage failure or a server
// misconfiguration, are returned unchanged.
func newClientAssertionErrorStrategy(next fosite.ClientAuthenticationStrategy) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		client, err := next(ctx, r, form)
		if err == nil || form.Get("client_assertion_type") != jwtBearerClientAssertionType {
			return client, err
		}
		return client, mapClientAssertionError(err)
	}
}

func mapClientAssertionError(err error) error {
	var rfcErr *fosite.RFC6749Error
	if errors.As(err, &rfcErr) {
		switch rfcErr.ErrorField {
		case fosite.ErrInvalidRequest.ErrorField, fosite.ErrJTIKnown.ErrorField:
			return fosite.ErrInvalidClient.WithHint(rfcErr.HintField).WithWrap(err)
		}
		return err
	}
	if _, ok := clientAssertionClaimErrors[err.Error()]; ok {
		return fosite.ErrInvalidClient.
			WithHint("The 'client_assertion' claims could not be validated, check the iat, nbf and exp values.").
			WithWrap(err)
	}
	return err
}

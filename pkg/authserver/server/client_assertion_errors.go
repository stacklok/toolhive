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

// privateKeyJWTAssertionType is the client_assertion_type for private_key_jwt
// client authentication (RFC 7523 section 2.2).
const privateKeyJWTAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// clientAssertionClaimErrors are the messages of the claim validation errors
// that fosite returns unwrapped, as plain errors, when it parses a client
// assertion (see fosite/token/jwt MapClaims.Valid). fosite returns the bare
// errors.New value and drops the ValidationError flags, so there is nothing
// typed to match. TestClientAssertionErrorStrategy runs real fosite, so a
// changed message fails that test on upgrade.
var clientAssertionClaimErrors = map[string]struct{}{
	"Token is expired":         {},
	"Token used before issued": {},
	"Token is not valid yet":   {},
}

type clientAssertionStoreFailureKey struct{}

type clientAssertionStoreFailure struct{ err error }

// RecordClientAssertionStoreFailure lets a storage backend report that it could
// not check or record a client assertion's jti for a reason other than a replay.
// fosite discards the error ClientAssertionJWTValid returns, so the client
// authentication strategy reads the recorded error from the context instead. For
// both calls, the recorded error tells the strategy that the failure is a storage
// failure rather than a verdict on the assertion, so it is returned as
// server_error. Storage decorators must pass ctx through to the backend unchanged.
func RecordClientAssertionStoreFailure(ctx context.Context, err error) {
	if f, ok := ctx.Value(clientAssertionStoreFailureKey{}).(*clientAssertionStoreFailure); ok {
		f.err = err
	}
}

// NewClientAssertionStoreFailureContext returns a context on which
// RecordClientAssertionStoreFailure records, and a function that returns what
// was recorded (nil if nothing was). The client authentication strategy uses it
// around fosite; storage tests use it to check what a backend reports.
func NewClientAssertionStoreFailureContext(ctx context.Context) (context.Context, func() error) {
	failure := &clientAssertionStoreFailure{}
	return context.WithValue(ctx, clientAssertionStoreFailureKey{}, failure), func() error { return failure.err }
}

// newClientAssertionErrorStrategy makes every rejected private_key_jwt client
// assertion surface as invalid_client, as RFC 6749 section 5.2 requires for a
// failed client authentication.
//
// fosite already does this for most failures, but a few escape with another
// error: an expired, not-yet-valid or future-issued assertion comes back as a
// plain error (HTTP 500 server_error), and an unknown signing key or a reused
// jti comes back as invalid_request / jti_known (HTTP 400). A request that sets
// client_assertion_type but omits client_assertion stays invalid_request (HTTP
// 400), as RFC 6749 section 5.2 says for a missing parameter. Errors that are
// not a verdict on the assertion are not rewritten into invalid_client: a
// misconfiguration is returned unchanged, and a storage failure while checking
// or recording the jti is reported by the storage backend through
// RecordClientAssertionStoreFailure and returned as server_error (HTTP 500).
func newClientAssertionErrorStrategy(next fosite.ClientAuthenticationStrategy) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		if form.Get("client_assertion_type") != privateKeyJWTAssertionType || form.Get("client_assertion") == "" {
			return next(ctx, r, form)
		}
		ctx, storeFailure := NewClientAssertionStoreFailureContext(ctx)
		client, err := next(ctx, r, form)
		if err == nil {
			return client, nil
		}
		if failure := storeFailure(); failure != nil {
			return nil, fosite.ErrServerError.WithWrap(failure).WithDebug(failure.Error())
		}
		return client, mapClientAssertionError(err)
	}
}

func mapClientAssertionError(err error) error {
	var rfcErr *fosite.RFC6749Error
	if errors.As(err, &rfcErr) {
		switch rfcErr.ErrorField {
		// invalid_request: fosite's findPublicKey reports an unknown kid or no
		// matching key this way. It also covers "server configuration does not
		// support OpenID Connect specific authentication methods", which our
		// client types never hit. jti_known: a replayed jti.
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

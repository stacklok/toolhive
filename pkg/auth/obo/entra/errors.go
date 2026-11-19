// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra

import (
	"errors"
	"fmt"
)

// Sentinel errors for the OBO exchange failure classes. Callers compare with
// errors.Is; the concrete *ExchangeError returned by the Exchanger unwraps to
// the sentinel matching its Kind. They never carry token or secret material.
//
// This layer deliberately does no logging (it would risk leaking secrets and the
// consumer has the request context anyway). The audit record for a
// fail-fast conditional-access / claims challenge is the runtime middleware's
// and the vMCP strategy's responsibility: on errors.Is(err, ErrInteractionRequired) they
// emit the audit event using the safe-to-log fields the *ExchangeError already
// carries — Kind, OAuthError, Description, Claims, HTTPStatus — none of which
// contain credential material.
var (
	// ErrInteractionRequired indicates Entra refused the silent exchange and
	// demands interactive user action — a conditional-access / MFA / claims
	// challenge (for example AADSTS50076 or AADSTS50079). v1 fails fast on this
	// case rather than attempting an interactive passthrough; the originating
	// *ExchangeError carries the raw claims challenge for a future enhancement.
	ErrInteractionRequired = errors.New("entra obo: interaction required (conditional access / claims challenge)")

	// ErrInvalidGrant indicates the subject (user) token was rejected — expired,
	// malformed, or not consented for this resource. It is permanent for that
	// token: re-running the exchange with the same assertion will fail again.
	ErrInvalidGrant = errors.New("entra obo: subject token rejected")

	// ErrInvalidClient indicates the confidential client failed to authenticate
	// (wrong client_id / client_secret, or the secret has expired). It is a
	// permanent configuration error until the operator fixes the credentials.
	ErrInvalidClient = errors.New("entra obo: client authentication failed")

	// ErrTransient indicates a retryable failure — a 5xx, 429 or 408 from the
	// token endpoint, an OAuth "temporarily_unavailable" error, or a
	// transport-level error reaching it. Callers may retry with backoff.
	ErrTransient = errors.New("entra obo: transient token endpoint failure")
)

// ErrorKind classifies an OBO exchange failure so callers can decide between
// fail-fast (interaction required), permanent-error (bad grant / client), and
// retry (transient) without string-matching provider diagnostics.
type ErrorKind int

const (
	// KindUnknown is an unclassified failure: a non-success response that did
	// not match any recognised OAuth error code or status class. It is treated
	// as permanent (it does not unwrap to ErrTransient) so callers do not retry
	// indefinitely on an unrecognised, likely permanent, error.
	KindUnknown ErrorKind = iota

	// KindInteractionRequired is a conditional-access / MFA / claims challenge.
	KindInteractionRequired

	// KindInvalidGrant is a rejected subject token.
	KindInvalidGrant

	// KindInvalidClient is a confidential-client authentication failure.
	KindInvalidClient

	// KindTransient is a retryable server-side or transport failure.
	KindTransient
)

// ExchangeError is the typed error returned when an Entra OBO exchange fails. It
// classifies the failure (Kind) and unwraps to the matching sentinel so callers
// can use either errors.As (to read Kind / Claims / OAuthError) or errors.Is
// (against the sentinels above).
//
// It deliberately carries only non-sensitive fields: the RFC 6749 error code,
// the provider's diagnostic description, the HTTP status, and — for an
// interaction-required challenge — the raw claims request. It never contains the
// user's assertion, the client secret, or any issued access token, so it is safe
// to log and to surface in API responses.
type ExchangeError struct {
	// Kind is the classified failure class.
	Kind ErrorKind

	// OAuthError is the RFC 6749 "error" code from the response body (for
	// example "invalid_grant"). Empty when the failure had no parseable body
	// (transport error, 5xx with a non-JSON body).
	OAuthError string

	// Description is the provider's "error_description" diagnostic. For Entra
	// this is the human-readable "AADSTSnnnnn: ..." message. It contains no
	// credential material and is safe to log.
	Description string

	// Claims is the raw claims challenge from an interaction-required response
	// (the "claims" field Entra returns for conditional access). It is empty for
	// every other Kind. It is a request for additional claims, not a credential.
	Claims string

	// HTTPStatus is the token endpoint's HTTP status code, or 0 when the request
	// never produced a response (transport-level failure).
	HTTPStatus int
}

// Error implements error. It composes only the non-sensitive fields; in
// particular it never includes the subject token, client secret, or issued
// access token.
func (e *ExchangeError) Error() string {
	detail := e.OAuthError
	if e.Description != "" {
		if detail != "" {
			detail += ": "
		}
		detail += e.Description
	}
	if detail == "" && e.HTTPStatus != 0 {
		detail = fmt.Sprintf("token endpoint returned HTTP %d", e.HTTPStatus)
	}
	if detail == "" {
		detail = "no response from token endpoint"
	}
	return fmt.Sprintf("%s (%s)", e.kindString(), detail)
}

// Unwrap maps the Kind onto its sentinel so errors.Is works against the
// exported sentinels. KindUnknown intentionally unwraps to nil — an unknown
// failure matches none of the sentinels and is therefore treated as permanent.
func (e *ExchangeError) Unwrap() error {
	switch e.Kind {
	case KindInteractionRequired:
		return ErrInteractionRequired
	case KindInvalidGrant:
		return ErrInvalidGrant
	case KindInvalidClient:
		return ErrInvalidClient
	case KindTransient:
		return ErrTransient
	case KindUnknown:
		return nil
	}
	return nil
}

// kindString returns a short human label for the failure class, used only to
// build Error() output.
func (e *ExchangeError) kindString() string {
	switch e.Kind {
	case KindInteractionRequired:
		return "interaction required"
	case KindInvalidGrant:
		return "invalid grant"
	case KindInvalidClient:
		return "invalid client"
	case KindTransient:
		return "transient failure"
	case KindUnknown:
		return "exchange failed"
	}
	return "exchange failed"
}

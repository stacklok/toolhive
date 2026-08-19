// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oautherr classifies OAuth token-endpoint failures as transient or
// permanent.
//
// The distinction matters wherever a cached credential is exchanged without a
// human present: a transient failure (the IdP is down, rate-limiting, or a WAF
// answered instead of it) is worth retrying, while a permanent one means the
// OAuth server itself rendered a verdict on the credential and no amount of
// retrying will help — the only way forward is a fresh interactive login.
//
// It is a leaf package so both the token-source construction path and the
// workload auth monitor can share one implementation instead of each carrying
// its own copy of the rules.
package oautherr

import (
	"errors"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

// IsTransientRetrieveError reports whether an *oauth2.RetrieveError should be
// treated as transient. The classification rules are:
//
//   - nil Response: non-transient. There is no signal to act on, so callers
//     fall through to the unauthenticated path rather than retry blindly.
//   - 5xx status: transient (server-side issue, likely to resolve).
//   - 429 Too Many Requests: transient regardless of body (HTTP standard).
//   - 4xx with an empty ErrorCode: transient. The oauth2 library populates
//     ErrorCode from the RFC 6749 'error' field in a JSON response body. An
//     empty ErrorCode means the response was not a parseable OAuth error —
//     typically an HTML page from a WAF, CDN, or reverse proxy that
//     intercepted the request before it reached the OAuth server. These
//     infrastructure errors (Cloudflare blocks, residential-IP allowlist
//     misses, transient bad-config deploys) commonly resolve on their own.
//   - 4xx with a populated ErrorCode: permanent. The OAuth server returned
//     a structured error code (invalid_grant, invalid_client, etc.) telling
//     us specifically what's wrong; retrying won't help.
func IsTransientRetrieveError(retrieveErr *oauth2.RetrieveError) bool {
	if retrieveErr == nil || retrieveErr.Response == nil {
		return false
	}
	statusCode := retrieveErr.Response.StatusCode

	if statusCode >= 500 {
		return true
	}
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if retrieveErr.ErrorCode == "" {
		return true
	}
	return false
}

// IsPermanentCredentialError reports whether err is an *oauth2.RetrieveError
// whose response carries a structured RFC 6749 'error' code, implying the
// OAuth server itself rendered a verdict on the cached credentials
// (invalid_grant, invalid_client, etc.).
//
// This is the strict inverse of IsTransientRetrieveError on the
// *oauth2.RetrieveError branch: a response is "permanent" iff the classifier
// would NOT call it transient. Concretely, it is true only when ErrorCode is
// populated. 4xx responses without an OAuth error code (HTML pages from a WAF,
// CDN, or reverse proxy) — like 5xx, 429, 408, and nil-Response shapes — are
// treated as non-permanent because there is no OAuth-protocol verdict to act
// on. Telling a user their stored credential is dead based on a
// non-spec-compliant response would frequently mislead operators whose real
// problem is upstream of the OAuth server.
func IsPermanentCredentialError(err error) bool {
	retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
	if !ok || retrieveErr.Response == nil {
		return false
	}
	return !IsTransientRetrieveError(retrieveErr)
}

// RetrieveErrorCode returns the RFC 6749 'error' code from err when err wraps
// an *oauth2.RetrieveError, and "" otherwise.
//
// The code and description are the only two fields of a RetrieveError safe to
// put in a message: its Error() embeds the raw response body, which for a token
// endpoint can echo back bearer material. Callers building a user-facing string
// should reach for this rather than the error's own text.
func RetrieveErrorCode(err error) string {
	retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
	if !ok {
		return ""
	}
	return retrieveErr.ErrorCode
}

// IsParseError detects errors from the oauth2 library that indicate the token
// endpoint returned an unparsable response body on a 2xx status. This
// typically happens when a load balancer, CDN, or reverse proxy intercepts the
// request and returns its own HTML page instead of the expected JSON token
// response. The oauth2 library uses fmt.Errorf with %v (not %w) for these
// errors, so string matching is the only reliable detection method.
func IsParseError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "oauth2: cannot parse json") ||
		strings.Contains(msg, "oauth2: cannot parse response")
}

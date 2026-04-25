// Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver includes helpers for handling MCP backend responses that
// indicate the embedded auth server's cached upstream token is no longer
// usable. See MCPExternalAuthConfig.EmbeddedAuthServerConfig.OnUpstreamAuthFailure.
package authserver

import (
	"net/http"
	"strings"
)

// UpstreamAuthFailurePolicyValue is the string form of the configured policy.
type UpstreamAuthFailurePolicyValue string

const (
	// PolicySilentRefresh preserves pre-existing behavior: pass the backend's
	// 401 through to the client unchanged and rely on the next scheduled
	// upstream refresh to recover. This is the default when unset.
	PolicySilentRefresh UpstreamAuthFailurePolicyValue = "silent-refresh"

	// PolicyInvalidateSession revokes the user's Toolhive-issued access +
	// refresh tokens and evicts the cached upstream token set. The client
	// response is rewritten to HTTP 401 + `WWW-Authenticate: Bearer
	// error="invalid_token"` per RFC 6750, which MCP-spec-compliant clients
	// interpret as "full re-auth required".
	PolicyInvalidateSession UpstreamAuthFailurePolicyValue = "invalidate-session"

	// DefaultMatcherStatus is the status code the default matcher triggers on
	// when OnUpstreamAuthFailure.TriggerOn is not specified.
	DefaultMatcherStatus = http.StatusUnauthorized

	// DefaultMatcherError is the WWW-Authenticate `error` parameter the
	// default matcher looks for when OnUpstreamAuthFailure.TriggerOn is
	// empty. Per RFC 6750 §3.1, `invalid_token` means the access token
	// provided is invalid, expired, revoked, malformed, or otherwise
	// unacceptable — exactly the class of failure this feature addresses.
	DefaultMatcherError = "invalid_token"
)

// AuthFailureMatcher mirrors v1beta1.UpstreamAuthFailureMatcher in primitive
// form. Defined here to avoid an import cycle with the operator API package.
// Callers convert the CRD types to this shape before invoking matching.
type AuthFailureMatcher struct {
	// Status is the HTTP status codes to match. Empty means [401].
	Status []int
	// WWWAuthenticateError optionally matches the `error` parameter of the
	// `WWW-Authenticate: Bearer` response header. Empty means any value matches.
	WWWAuthenticateError string
}

// AuthFailurePolicy is the resolved policy passed to ResponseMatchesAuthFailure.
type AuthFailurePolicy struct {
	// Policy is one of PolicySilentRefresh / PolicyInvalidateSession.
	// Empty string is treated as PolicySilentRefresh (the default).
	Policy UpstreamAuthFailurePolicyValue
	// TriggerOn is the list of matcher patterns. If empty, the default
	// matcher (HTTP 401 + WWW-Authenticate: Bearer error="invalid_token") is used.
	TriggerOn []AuthFailureMatcher
}

// ResponseMatchesAuthFailure reports whether the backend response matches any
// of the configured matchers and the policy is set to invalidate-session.
//
// This function inspects only the status code and the WWW-Authenticate
// response header. It does not consume or buffer the response body.
func ResponseMatchesAuthFailure(
	policy *AuthFailurePolicy,
	statusCode int,
	wwwAuthHeader string,
) bool {
	// No policy or explicit silent-refresh: never treat as a reauth trigger.
	if policy == nil || policy.Policy == "" || policy.Policy == PolicySilentRefresh {
		return false
	}

	matchers := policy.TriggerOn
	if len(matchers) == 0 {
		return statusCode == DefaultMatcherStatus &&
			extractBearerErrorParam(wwwAuthHeader) == DefaultMatcherError
	}

	headerErr := extractBearerErrorParam(wwwAuthHeader)
	for i := range matchers {
		if matcherMatches(&matchers[i], statusCode, headerErr) {
			return true
		}
	}
	return false
}

func matcherMatches(m *AuthFailureMatcher, status int, bearerErr string) bool {
	statusOK := false
	if len(m.Status) == 0 {
		statusOK = status == DefaultMatcherStatus
	} else {
		for _, s := range m.Status {
			if s == status {
				statusOK = true
				break
			}
		}
	}
	if !statusOK {
		return false
	}
	if m.WWWAuthenticateError == "" {
		return true
	}
	return bearerErr == m.WWWAuthenticateError
}

// extractBearerErrorParam parses a `WWW-Authenticate` header and returns the
// value of the `error` parameter from a `Bearer` challenge, or "" if not
// present. Handles quoted and unquoted values per RFC 7235 §2.1 /
// RFC 6750 §3.1.
func extractBearerErrorParam(header string) string {
	if header == "" {
		return ""
	}
	lower := strings.ToLower(header)
	idx := strings.Index(lower, "bearer")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimLeft(header[idx+len("bearer"):], " \t")

	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(strings.ToLower(part), "error") {
			continue
		}
		eq := strings.Index(part, "=")
		if eq < 0 {
			continue
		}
		keyName := strings.TrimSpace(strings.ToLower(part[:eq]))
		if keyName != "error" {
			continue
		}
		val := strings.TrimSpace(part[eq+1:])
		val = strings.Trim(val, `"`)
		return val
	}
	return ""
}

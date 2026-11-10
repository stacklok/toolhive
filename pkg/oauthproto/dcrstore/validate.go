// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcrstore

// ValidationError reports a Credentials record that a store must refuse to
// persist. Hint is a short, stable description of the first problem found.
// Stores that answer through another error vocabulary (for example an OAuth
// authorization server) convert it with errors.As.
type ValidationError struct {
	Hint string
}

// Error implements error.
func (e *ValidationError) Error() string { return "dcr: " + e.Hint }

func invalid(hint string) error { return &ValidationError{Hint: hint} }

// Validate enforces the rejection contract every Store implementation must
// apply before persisting. Sharing one function keeps the rules from drifting
// between backends: a record that fails against one cannot silently persist
// against another.
//
// Rejected inputs: nil creds, an unpopulated Key (empty Issuer, UpstreamID,
// RedirectURI, or ScopesHash), and missing RFC 7591 mandatory response fields
// (ClientID, AuthorizationEndpoint, TokenEndpoint). An empty ScopesHash is
// rejected because the canonical digest of any scope set, including the empty
// one, is non-empty, so an empty string can only be a caller bug. An empty
// UpstreamID is rejected because the resolver always derives it from the
// request (issue #5823); an empty value would re-open the cross-upstream
// collision it was added to close. ClientSecret is left permissive because
// RFC 7591 §2 public clients (auth method "none") register without a secret.
//
// The returned error is a *ValidationError.
func Validate(creds *Credentials) error {
	if creds == nil {
		return invalid("dcr credentials cannot be nil")
	}
	for _, f := range []struct{ value, hint string }{
		{creds.Key.Issuer, "dcr credentials key issuer cannot be empty"},
		{creds.Key.UpstreamID, "dcr credentials key upstream_id cannot be empty"},
		{creds.Key.RedirectURI, "dcr credentials key redirect_uri cannot be empty"},
		{creds.Key.ScopesHash, "dcr credentials key scopes_hash cannot be empty"},
		{creds.ClientID, "dcr credentials client_id cannot be empty"},
		{creds.AuthorizationEndpoint, "dcr credentials authorization_endpoint cannot be empty"},
		{creds.TokenEndpoint, "dcr credentials token_endpoint cannot be empty"},
	} {
		if f.value == "" {
			return invalid(f.hint)
		}
	}
	return nil
}

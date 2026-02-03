// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"strings"
	"testing"
)

func TestValidateRedirectURI(t *testing.T) {
	t.Parallel()

	// Each test case specifies expected behavior for both policies.
	// Empty error string means the URI should be accepted.
	tests := []struct {
		name       string
		uri        string
		strictErr  string // empty = OK with Strict policy
		privateErr string // empty = OK with AllowPrivateSchemes policy
	}{
		// HTTPS URIs - valid for both policies
		{name: "https", uri: "https://example.com/callback"},
		{name: "https with port", uri: "https://example.com:8443/callback"},
		{name: "https with query", uri: "https://example.com/callback?state=abc"},

		// HTTP loopback - valid for both policies (RFC 8252)
		{name: "http localhost", uri: "http://localhost/callback"},
		{name: "http localhost with port", uri: "http://localhost:8080/callback"},
		{name: "http 127.0.0.1", uri: "http://127.0.0.1/callback"},
		{name: "http 127.0.0.1 with port", uri: "http://127.0.0.1:9090/callback"},

		// Private-use schemes (RFC 8252 §7.1) - only with AllowPrivateSchemes
		{
			name:      "cursor scheme",
			uri:       "cursor://callback",
			strictErr: "http (for loopback) or https",
		},
		{
			name:      "vscode scheme",
			uri:       "vscode://callback/auth",
			strictErr: "http (for loopback) or https",
		},
		{
			name:      "custom app scheme",
			uri:       "myapp://oauth/redirect",
			strictErr: "http (for loopback) or https",
		},

		// Fragment - rejected by both policies (RFC 6749 §3.1.2)
		{
			name:       "fragment in https",
			uri:        "https://example.com/callback#section",
			strictErr:  "absolute URI without a fragment",
			privateErr: "absolute URI without a fragment",
		},
		{
			name:       "fragment in custom scheme",
			uri:        "cursor://callback#section",
			strictErr:  "absolute URI without a fragment", // fragment check happens before scheme check
			privateErr: "absolute URI without a fragment",
		},

		// HTTP non-loopback - rejected by both policies
		{
			name:       "http non-loopback",
			uri:        "http://example.com/callback",
			strictErr:  "http (for loopback) or https",
			privateErr: "secure scheme",
		},

		// Length limit
		{
			name:       "URI too long",
			uri:        "https://example.com/" + strings.Repeat("a", MaxRedirectURILength),
			strictErr:  "too long",
			privateErr: "too long",
		},

		// Malformed URIs - rejected by both
		{
			name:       "relative URI",
			uri:        "/callback",
			strictErr:  "absolute URI without a fragment",
			privateErr: "absolute URI without a fragment",
		},
		{
			name:       "empty URI",
			uri:        "",
			strictErr:  "absolute URI without a fragment",
			privateErr: "absolute URI without a fragment",
		},

		// Edge case: scheme-only URI passes fosite's absolute URI check
		{name: "scheme-only https", uri: "https://"},
	}

	for _, tt := range tests {
		// Test with Strict policy
		t.Run(tt.name+"/strict", func(t *testing.T) {
			t.Parallel()
			assertValidation(t, tt.uri, RedirectURIPolicyStrict, tt.strictErr)
		})

		// Test with AllowPrivateSchemes policy
		t.Run(tt.name+"/private", func(t *testing.T) {
			t.Parallel()
			assertValidation(t, tt.uri, RedirectURIPolicyAllowPrivateSchemes, tt.privateErr)
		})
	}
}

func assertValidation(t *testing.T, uri string, policy RedirectURIPolicy, wantErrContains string) {
	t.Helper()
	err := ValidateRedirectURI(uri, policy)
	if wantErrContains != "" {
		if err == nil {
			t.Errorf("expected error containing %q, got nil", wantErrContains)
		} else if !strings.Contains(err.Error(), wantErrContains) {
			t.Errorf("expected error containing %q, got %q", wantErrContains, err.Error())
		}
	} else if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

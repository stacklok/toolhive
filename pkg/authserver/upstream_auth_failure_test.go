// Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"net/http"
	"testing"
)

func TestExtractBearerErrorParam(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"no-bearer", `Basic realm="x"`, ""},
		{"basic-error", `Bearer error="invalid_token"`, "invalid_token"},
		{"with-realm-and-error", `Bearer realm="api", error="insufficient_scope"`, "insufficient_scope"},
		{"unquoted-error", `Bearer error=invalid_token`, "invalid_token"},
		{"case-insensitive-scheme", `BEARER error="invalid_token"`, "invalid_token"},
		{"with-other-params", `Bearer realm="api", error="invalid_token", error_description="role X filtered"`, "invalid_token"},
		{"error-not-confused-with-error_uri", `Bearer error_uri="https://example.com/e"`, ""},
		{"whitespace-around-equals", `Bearer error = "invalid_token"`, "invalid_token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractBearerErrorParam(tc.header)
			if got != tc.want {
				t.Errorf("extractBearerErrorParam(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestResponseMatchesAuthFailure_NilPolicy(t *testing.T) {
	t.Parallel()
	if ResponseMatchesAuthFailure(nil, 401, `Bearer error="invalid_token"`) {
		t.Error("nil policy must never match (default = silent-refresh)")
	}
}

func TestResponseMatchesAuthFailure_SilentRefresh(t *testing.T) {
	t.Parallel()
	p := &AuthFailurePolicy{Policy: PolicySilentRefresh}
	if ResponseMatchesAuthFailure(p, 401, `Bearer error="invalid_token"`) {
		t.Error("silent-refresh policy must never match")
	}
}

func TestResponseMatchesAuthFailure_DefaultMatcher(t *testing.T) {
	t.Parallel()
	p := &AuthFailurePolicy{
		Policy: PolicyInvalidateSession,
		// TriggerOn omitted → use default matcher
	}
	cases := []struct {
		name   string
		status int
		header string
		want   bool
	}{
		{"matches-default", 401, `Bearer error="invalid_token"`, true},
		{"unquoted", 401, `Bearer error=invalid_token`, true},
		{"wrong-status", 403, `Bearer error="invalid_token"`, false},
		{"insufficient-scope-not-default", 401, `Bearer error="insufficient_scope"`, false},
		{"no-www-auth", 401, "", false},
		{"non-auth-error", 500, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResponseMatchesAuthFailure(p, tc.status, tc.header)
			if got != tc.want {
				t.Errorf("default matcher status=%d header=%q: got %v want %v",
					tc.status, tc.header, got, tc.want)
			}
		})
	}
}

func TestResponseMatchesAuthFailure_CustomMatchers(t *testing.T) {
	t.Parallel()
	p := &AuthFailurePolicy{
		Policy: PolicyInvalidateSession,
		TriggerOn: []AuthFailureMatcher{
			{Status: []int{401}, WWWAuthenticateError: "invalid_token"},
			{Status: []int{403}, WWWAuthenticateError: "insufficient_scope"},
		},
	}
	cases := []struct {
		name   string
		status int
		header string
		want   bool
	}{
		{"first-matcher", 401, `Bearer error="invalid_token"`, true},
		{"second-matcher", 403, `Bearer error="insufficient_scope"`, true},
		{"403-without-error-doesnt-match", 403, "", false},
		{"401-with-different-error", 401, `Bearer error="insufficient_scope"`, false},
		{"401-without-www-auth-doesnt-match", 401, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResponseMatchesAuthFailure(p, tc.status, tc.header)
			if got != tc.want {
				t.Errorf("custom matchers status=%d header=%q: got %v want %v",
					tc.status, tc.header, got, tc.want)
			}
		})
	}
}

func TestResponseMatchesAuthFailure_StatusOnlyMatcher(t *testing.T) {
	t.Parallel()
	p := &AuthFailurePolicy{
		Policy: PolicyInvalidateSession,
		TriggerOn: []AuthFailureMatcher{
			{Status: []int{http.StatusUnauthorized}},
		},
	}
	if !ResponseMatchesAuthFailure(p, 401, "") {
		t.Error("status-only matcher should match 401 without WWW-Authenticate")
	}
	if ResponseMatchesAuthFailure(p, 200, "") {
		t.Error("status-only matcher should not match non-401")
	}
}

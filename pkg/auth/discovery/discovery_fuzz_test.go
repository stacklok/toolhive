// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"strings"
	"testing"
)

// FuzzParseWWWAuthenticate checks that WWW-Authenticate parsing never panics
// on attacker-controlled headers from a remote MCP server.
func FuzzParseWWWAuthenticate(f *testing.F) {
	for _, seed := range []string{
		`Bearer realm="https://auth.example.com"`,
		`Bearer realm="example", scope="openid profile", resource_metadata="https://example.com/.well-known/oauth-protected-resource"`,
		`Bearer error="invalid_token", error_description="The token \"expired\""`,
		`OAuth realm="https://oauth.example.com", resource_metadata=https://x`,
		`Basic realm="example"`,
		`Digest realm="example", nonce="abc"`,
		`Bearer realm="unterminated`,
		`Bearer realm=`,
		"Bearer " + strings.Repeat(`realm="\\"`, 64),
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(_ *testing.T, header string) {
		_, _ = ParseWWWAuthenticate(header)
	})
}

// FuzzExtractParameter checks that parameter extraction never panics and that
// a found value is always a substring-derived value no longer than the input.
func FuzzExtractParameter(f *testing.F) {
	for _, seed := range []struct{ params, name string }{
		{`realm="example", scope="openid"`, "realm"},
		{`realm=example, scope=openid`, "scope"},
		{`error_description="say \"hi\""`, "error_description"},
		{`realm="unterminated`, "realm"},
		{`realm=`, "realm"},
		{`resource_metadata="https://example.com/a,b"`, "resource_metadata"},
		{"", ""},
	} {
		f.Add(seed.params, seed.name)
	}

	f.Fuzz(func(t *testing.T, params, name string) {
		if got := ExtractParameter(params, name); len(got) > len(params) {
			t.Fatalf("value %q longer than input %q", got, params)
		}
	})
}

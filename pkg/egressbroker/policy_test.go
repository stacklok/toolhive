// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

const testPolicyYAML = `
providers:
- provider: github
  allowedHosts: ["api.github.com", "*.githubusercontent.com"]
  allowedMethods: ["GET", "POST"]
  allowedPathPrefixes: ["/repos/", "/user"]
- provider: slack
  allowedHosts: ["slack.com"]
  allowedPathPrefixes: ["/api/"]
`

func mustParse(t *testing.T, doc string) *egressbroker.EgressPolicy {
	t.Helper()
	p, err := egressbroker.ParsePolicy([]byte(doc))
	require.NoError(t, err)
	return p
}

func TestParsePolicyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"empty document", ``, "no providers"},
		{"no providers", `providers: []`, "no providers"},
		{"invalid yaml", `{{{`, "failed to parse"},
		{"empty provider name", `providers: [{provider: "", allowedHosts: ["a.example.com"]}]`, "empty name"},
		{"no allowed hosts", `providers: [{provider: "github"}]`, "no allowedHosts"},
		{"host with scheme", `providers: [{provider: "g", allowedHosts: ["https://api.example.com"]}]`, "bare hostname"},
		{"host with port", `providers: [{provider: "g", allowedHosts: ["api.example.com:443"]}]`, "bare hostname"},
		{"host with path", `providers: [{provider: "g", allowedHosts: ["api.example.com/v1"]}]`, "bare hostname"},
		{"wildcard mid-label", `providers: [{provider: "g", allowedHosts: ["api.*.example.com"]}]`, "wildcards only allowed"},
		{"bare wildcard suffix", `providers: [{provider: "g", allowedHosts: ["*.com"]}]`, "not a valid hostname"},
		{"single-label host", `providers: [{provider: "g", allowedHosts: ["localhost"]}]`, "not a valid hostname"},
		{"unknown method", `providers: [{provider: "g", allowedHosts: ["a.example.com"], allowedMethods: ["YEET"]}]`, "unknown method"},
		{"path prefix not absolute",
			`providers: [{provider: "g", allowedHosts: ["a.example.com"], allowedPathPrefixes: ["repos"]}]`, "not starting with '/'"},
		{"duplicate provider",
			"providers:\n- {provider: g, allowedHosts: [a.example.com]}\n- {provider: g, allowedHosts: [b.example.com]}",
			"duplicate provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := egressbroker.ParsePolicy([]byte(tt.doc))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestProviderFor(t *testing.T) {
	t.Parallel()
	p := mustParse(t, testPolicyYAML)

	tests := []struct {
		name     string
		host     string
		provider string
		ok       bool
	}{
		{"exact host", "api.github.com", "github", true},
		{"exact host case-insensitive", "API.GitHub.COM", "github", true},
		{"exact host trailing dot", "api.github.com.", "github", true},
		{"wildcard one label", "raw.githubusercontent.com", "github", true},
		{"wildcard does not match bare suffix", "githubusercontent.com", "", false},
		{"wildcard matches deep subdomain", "a.b.githubusercontent.com", "github", true},
		{"unrelated host", "evil.example.com", "", false},
		{"superdomain of exact host", "github.com", "", false},
		{"exact host in other provider", "slack.com", "slack", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, ok := p.ProviderFor(tt.host)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.provider, provider)
		})
	}
}

func TestAllows(t *testing.T) {
	t.Parallel()
	p := mustParse(t, testPolicyYAML)

	tests := []struct {
		name     string
		provider string
		method   string
		path     string
		want     bool
	}{
		{"allowed method + prefix", "github", "GET", "/repos/foo/bar", true},
		{"allowed method case-insensitive", "github", "get", "/repos/foo", true},
		{"POST declared", "github", "POST", "/repos/foo", true},
		{"DELETE not declared", "github", "DELETE", "/repos/foo", false},
		{"path outside prefixes", "github", "GET", "/admin/users", false},
		{"prefix boundary: /user matches /user", "github", "GET", "/user", true},
		{"prefix match is a string prefix", "github", "GET", "/user/repos", true},
		{"empty path treated as /", "github", "GET", "", false},
		{"unknown provider", "nonexistent", "GET", "/anything", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, p.Allows(tt.provider, tt.method, tt.path))
		})
	}
}

func TestAllowsMethodDefault(t *testing.T) {
	t.Parallel()
	// slack entry declares no methods → safe read-only default.
	p := mustParse(t, testPolicyYAML)

	assert.True(t, p.Allows("slack", "GET", "/api/chat"))
	assert.True(t, p.Allows("slack", "HEAD", "/api/chat"))
	assert.True(t, p.Allows("slack", "OPTIONS", "/api/chat"))
	assert.False(t, p.Allows("slack", "POST", "/api/chat"),
		"empty allowedMethods must default to read-only")
}

func TestAllowsPathDefault(t *testing.T) {
	t.Parallel()
	p := mustParse(t, `
providers:
- provider: google
  allowedHosts: ["www.googleapis.com"]
`)
	assert.True(t, p.Allows("google", "GET", "/"),
		"empty allowedPathPrefixes must allow all paths on allowed hosts")
	assert.True(t, p.Allows("google", "GET", "/anything/deep"))
	assert.False(t, p.Allows("google", "POST", "/"),
		"method default still applies")
}

func TestHostAllowlist(t *testing.T) {
	t.Parallel()
	p := mustParse(t, testPolicyYAML)
	assert.Equal(t,
		[]string{"*.githubusercontent.com", "api.github.com", "slack.com"},
		p.HostAllowlist())
}

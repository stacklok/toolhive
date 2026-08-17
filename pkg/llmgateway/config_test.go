// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llmgateway_test

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// TestQuoteForPOSIXShell pins the escaping shape for the characters that matter.
func TestQuoteForPOSIXShell(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain path", "/usr/local/bin/thv", `'/usr/local/bin/thv'`},
		{"space", "/App Support/thv", `'/App Support/thv'`},
		{"single quote", "/it's/thv", `'/it'\''s/thv'`},
		{"double quote", `/say "hi"/thv`, `'/say "hi"/thv'`},
		{"dollar and backtick", "/$HOME/`id`/thv", "'/$HOME/`id`/thv'"},
		{"semicolon", "/a;rm -rf/thv", `'/a;rm -rf/thv'`},
		{"newline", "/a\nb/thv", "'/a\nb/thv'"},
		{"empty", "", `''`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, llmgateway.QuoteForPOSIXShell(tc.in))
		})
	}
}

// TestQuoteForPOSIXShell_SurvivesRealShell is the evidence behind the claim that
// single-quoting is a total transform, which is what lets the token-helper
// writers drop metacharacter validation entirely. Each string is round-tripped
// through /bin/sh and must come back byte-identical.
func TestQuoteForPOSIXShell_SurvivesRealShell(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting; /bin/sh unavailable")
	}

	inputs := []string{
		"/usr/local/bin/thv",
		"/App Support/thv",
		"/it's/thv",
		`/say "hi"/thv`,
		"/$HOME/`id`/thv",
		"/a;rm -rf/thv",
		"/a&&b/thv",
		"/a|b/thv",
		"/a#b/thv",
		"/a$(id)b/thv",
		"/a\nb/thv",
		"/a\\b/thv",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			// printf %s re-emits the argument verbatim, so any shell
			// interpretation of the quoted form shows up as a mismatch.
			script := "printf %s " + llmgateway.QuoteForPOSIXShell(in)
			out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput() // #nosec G204 -- test-controlled input
			require.NoError(t, err, "sh failed: %s", out)
			assert.Equal(t, in, string(out), "quoted string must survive /bin/sh verbatim")
		})
	}

	// Control: the same hostile strings unquoted do NOT survive, proving the
	// test would catch a broken escaper rather than passing trivially.
	out, err := exec.Command("/bin/sh", "-c", "printf %s /a$(id)b").CombinedOutput()
	require.NoError(t, err, "sh failed: %s", out)
	assert.NotEqual(t, "/a$(id)b", string(out))
	assert.True(t, strings.HasPrefix(string(out), "/a"))
}

// TestRefreshWindowExceedsHelperTTL guards the belt-and-suspenders invariant
// between the two values ToolHive writes for Claude Code: the token source's
// preemptive refresh window MUST exceed the apiKeyHelper TTL, so that every
// helper invocation in the final window forces a proactive refresh and Claude
// Code never receives an about-to-expire token. If either constant changes and
// the invariant breaks, this test fails before the regression ships.
func TestRefreshWindowExceedsHelperTTL(t *testing.T) {
	t.Parallel()

	assert.Greater(t, llmgateway.LLMTokenRefreshWindow, llmgateway.ClaudeCodeHelperTTL,
		"refresh window must exceed the helper TTL so a helper call always lands inside it")
	assert.Equal(t, 2*llmgateway.ClaudeCodeHelperTTL, llmgateway.LLMTokenRefreshWindow,
		"refresh window is derived as 2x the helper TTL")
	assert.Equal(t, int64(300000), llmgateway.ClaudeCodeHelperTTL.Milliseconds(),
		"helper TTL is written to settings.json in milliseconds")
	assert.Equal(t, 10*time.Minute, llmgateway.LLMTokenRefreshWindow)

	// Codex writes refresh_interval_ms and relies on the same invariant.
	assert.Greater(t, llmgateway.LLMTokenRefreshWindow, llmgateway.CodexHelperTTL,
		"refresh window must exceed Codex's helper TTL for the same reason it exceeds Claude Code's")
}

func TestProxyOriginOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		// Normal cases — path, query, and fragment stripped.
		{name: "strips_path", input: "http://localhost:14000/v1", want: "http://localhost:14000"},
		{name: "strips_long_path", input: "http://localhost:9000/v1beta/openai", want: "http://localhost:9000"},
		{name: "strips_query_and_fragment", input: "http://host:8080/path?q=1#frag", want: "http://host:8080"},
		{name: "strips_fragment_only", input: "http://host:8080#frag", want: "http://host:8080"},
		// ForceQuery: trailing "?" must not persist into the origin.
		{name: "strips_force_query", input: "http://host:8080/path?", want: "http://host:8080"},
		// Userinfo must not be persisted into the settings file.
		{name: "strips_userinfo", input: "http://user:pass@host:8080/path", want: "http://host:8080"},
		// IPv6 host.
		{name: "ipv6_host", input: "http://[::1]:14000/v1", want: "http://[::1]:14000"},
		// Empty input — url.Parse succeeds but Host is ""; fall back to rawURL.
		{name: "empty_input", input: "", want: ""},
		// Scheme-less "host:port/path" — url.Parse treats this as Scheme=host,
		// Opaque=port/path with no Host; fall back to rawURL.
		{name: "scheme_less_host_port", input: "localhost:14000/v1", want: "localhost:14000/v1"},
		// Opaque URI — fall back to rawURL.
		{name: "opaque_uri", input: "mailto:user@example.com", want: "mailto:user@example.com"},
		// Invalid URL — fall back to rawURL.
		{name: "invalid_url", input: "::invalid", want: "::invalid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, llmgateway.ProxyOriginOf(tc.input))
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llmgateway_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

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

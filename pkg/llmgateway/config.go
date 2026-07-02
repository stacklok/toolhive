// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package llmgateway contains shared types for the LLM gateway feature,
// imported by both pkg/llm and pkg/client to avoid an import cycle.
package llmgateway

import (
	"net/url"
	"time"
)

// ClaudeCodeHelperTTL is written to settings.json as
// CLAUDE_CODE_API_KEY_HELPER_TTL_MS: how often Claude Code re-invokes the
// apiKeyHelper ("thv llm token"). Pinned to Claude Code's documented default so
// the cadence is deterministic rather than relying on the implicit default.
const ClaudeCodeHelperTTL = 5 * time.Minute

// LLMTokenRefreshWindow is how far before expiry the LLM gateway token source
// proactively refreshes. It is derived as 2x ClaudeCodeHelperTTL so that every
// helper invocation in the final window forces a refresh, meaning Claude Code
// never receives an about-to-expire token. The invariant
// LLMTokenRefreshWindow > ClaudeCodeHelperTTL is what makes this belt-and-
// suspenders pairing work; keep the two values in sync (guarded by a test).
const LLMTokenRefreshWindow = 2 * ClaudeCodeHelperTTL

// ClaudeDesktopHelperTTL is written to the Claude Desktop config as
// inferenceCredentialHelperTtlSec: how often Claude Desktop re-invokes the
// credential helper (a shim that runs "thv llm token"). Kept equal to
// ClaudeCodeHelperTTL so the same LLMTokenRefreshWindow invariant holds — every
// invocation in the final window forces a refresh, so Claude Desktop never
// receives an about-to-expire token.
const ClaudeDesktopHelperTTL = ClaudeCodeHelperTTL

// ClaudeDesktopHelperTimeout is written as inferenceCredentialHelperTimeoutSec:
// how long Claude Desktop waits for the credential helper to print a token
// before killing it. Sized to allow a silent OIDC refresh (cached refresh token
// → new access token) but not a full interactive re-auth, which "thv llm setup"
// performs up front so the steady-state helper call stays fast.
const ClaudeDesktopHelperTimeout = 30 * time.Second

// ProxyOriginOf returns rawURL with its path, query, fragment, and userinfo
// stripped so only the scheme and host remain (the "origin"). Tools like
// Gemini CLI that append their own API path (e.g. /v1beta/...) need the
// origin rather than the full proxy base URL to avoid doubled path segments.
//
// Falls back to rawURL when:
//   - parsing fails, or
//   - the parsed URL has no Host (e.g. scheme-less "localhost:14000/v1" is
//     parsed by url.Parse as Scheme=localhost, Opaque=14000/v1 with no Host),
//     or
//   - the parsed URL has a non-empty Opaque field (opaque URI reference).
func ProxyOriginOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || u.Host == "" || u.Opaque != "" {
		return rawURL
	}
	origin := url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
	}
	return origin.String()
}

// ApplyConfig holds the values needed to configure a single tool's LLM
// gateway settings. Using a struct prevents positional-argument mistakes when
// the caller has multiple similar string values in scope.
type ApplyConfig struct {
	GatewayURL         string // direct-mode: URL of the upstream LLM gateway
	AnthropicBaseURL   string // direct-mode: effective ANTHROPIC_BASE_URL (gateway + optional path prefix)
	ProxyBaseURL       string // proxy-mode: URL of the localhost reverse proxy
	TokenHelperCommand string // direct-mode: shell command that prints a fresh token
	TLSSkipVerify      bool   // when true, instruct the tool to skip TLS verification
	// Models is the optional explicit model list written to clients that support
	// a model override (e.g. Claude Desktop's inferenceModels). Empty means the
	// client falls back to gateway-side model auto-discovery.
	Models []string
}

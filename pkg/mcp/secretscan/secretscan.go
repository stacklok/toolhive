// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package secretscan provides a best-effort content inspector for MCP
// tool-call results relayed through a ToolHive proxy.
//
// Background: the proxy sits between the calling client (e.g. an LLM agent)
// and an MCP backend server. The backend is not fully trusted -- it may be
// misconfigured, compromised, or malicious -- yet its tool-call output is
// otherwise forwarded to the client byte-for-byte on both the streamable-HTTP
// and legacy SSE transports (see pkg/transport/proxy/streamable and
// pkg/transport/proxy/transparent). A backend that returns credential-shaped
// text in a tool result would have that text delivered straight through,
// with no containment boundary. This package closes that specific gap: it
// recognizes common credential shapes in `tools/call` response text content
// and redacts them in place before the response reaches the client.
//
// This is defense-in-depth, not a content firewall: it only pattern-matches
// well-known credential formats in TextContent. It does not decode or scan
// binary/base64 payloads (ImageContent, AudioContent, embedded resource
// blobs), and it cannot catch secrets that don't match a known shape. Callers
// should treat a scan failure (malformed result JSON) as non-fatal and
// forward the original content unchanged -- this package must never be the
// reason a legitimate tool call breaks.
package secretscan

import (
	"encoding/json"
	"fmt"
	"regexp"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// redactionPlaceholder replaces a matched secret. It intentionally carries no
// information about the matched value (not even its length or pattern name)
// so the redaction itself cannot leak anything about the secret it replaced.
const redactionPlaceholder = "[REDACTED-BY-TOOLHIVE]"

// patterns lists the credential shapes scanned for. Each is a high-confidence,
// low-false-positive match on a known secret format; deliberately narrow
// rather than a generic "assignment to a sensitive-looking key name" heuristic,
// which would false-positive constantly on ordinary tool output.
var patterns = []*regexp.Regexp{
	// AWS access key ID.
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// GitHub personal access / app / OAuth / refresh tokens.
	regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[0-9A-Za-z_]{22,}\b`),
	// Slack tokens (bot/user/app/legacy).
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),
	// Google API key.
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	// Stripe live/test secret keys.
	regexp.MustCompile(`\bsk_(?:live|test)_[0-9A-Za-z]{16,}\b`),
	// Generic JWT (three dot-separated base64url segments).
	regexp.MustCompile(`\bey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	// PEM-encoded private key blocks (RSA/EC/PKCS8/OpenSSH/generic).
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?s)-----BEGIN OPENSSH PRIVATE KEY-----.*?-----END OPENSSH PRIVATE KEY-----`),
	// Generic "Authorization: Bearer <token>" shape. Unlike the patterns
	// above, this doesn't identify a specific issuer -- it catches any
	// opaque bearer credential by the way it is carried, which is the most
	// common shape for exfiltrated API/session tokens that don't match a
	// named provider's format.
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/=]{8,}\b`),
}

// Result reports what ScanAndRedactToolCallResult did.
type Result struct {
	// Redacted is the (possibly modified) result payload, always safe to
	// forward to the client.
	Redacted json.RawMessage
	// Matched is true if one or more patterns matched and were redacted.
	Matched bool
}

// ScanAndRedactToolCallResult inspects the `result` payload of a
// `tools/call` JSON-RPC response and redacts any TextContent entries that
// match a known credential shape.
//
// It is best-effort and fails open: if raw cannot be decoded as an MCP
// CallToolResult, it is returned unchanged with Matched=false and a non-nil
// error describing why decoding failed, so the caller can log it without
// treating it as a reason to block the response.
func ScanAndRedactToolCallResult(raw json.RawMessage) (Result, error) {
	if len(raw) == 0 {
		return Result{Redacted: raw}, nil
	}

	var result sdkmcp.CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{Redacted: raw}, fmt.Errorf("decoding tool call result: %w", err)
	}

	matched := false
	for i, c := range result.Content {
		text, ok := c.(sdkmcp.TextContent)
		if !ok {
			continue
		}
		redactedText, hit := redactText(text.Text)
		if !hit {
			continue
		}
		matched = true
		text.Text = redactedText
		result.Content[i] = text
	}

	if !matched {
		return Result{Redacted: raw}, nil
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		// Should not happen -- result round-tripped through the same type's
		// (Un)MarshalJSON -- but fail open rather than block the response.
		return Result{Redacted: raw}, fmt.Errorf("re-encoding redacted tool call result: %w", err)
	}
	return Result{Redacted: encoded, Matched: true}, nil
}

// redactText replaces every pattern match in s with redactionPlaceholder.
// Returns the (possibly unmodified) string and whether anything matched.
func redactText(s string) (string, bool) {
	matched := false
	for _, p := range patterns {
		if p.MatchString(s) {
			matched = true
			s = p.ReplaceAllString(s, redactionPlaceholder)
		}
	}
	return s, matched
}

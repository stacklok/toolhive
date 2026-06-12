// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tidwall/gjson"
)

// tokenResponseRewriter is an http.RoundTripper that normalizes non-standard
// OAuth token responses before the golang.org/x/oauth2 library parses them,
// and optionally extracts user identity claims from the raw response body.
//
// Some providers (e.g., GovSlack) nest token fields under non-standard paths
// like "authed_user.access_token" instead of the top-level "access_token".
// This RoundTripper intercepts the response, extracts fields using gjson
// dot-notation paths, and rewrites the response body with standard top-level
// field names so the oauth2 library can parse them normally.
//
// When identityCfg is set, identity extraction runs on the RAW pre-rewrite
// body before any field relocation occurs. The extracted identity is stored
// in extractedIdentity and consumed by the caller after Exchange returns.
// If extraction fails, extractionErr holds the cause so callers can surface
// operator-actionable diagnostics (path names, type descriptions) without
// re-reading the response body.
//
// Single-use contract: each instance handles exactly one token exchange and
// must not be shared across goroutines or reused for multiple exchanges. The
// caller reads extractedIdentity and extractionErr after Exchange returns —
// that return is the synchronization edge, so no mutex is needed for this
// one-shot usage pattern.
type tokenResponseRewriter struct {
	base              http.RoundTripper
	mapping           *TokenResponseMapping
	identityCfg       *IdentityFromTokenConfig
	tokenURL          string
	extractedIdentity *partialIdentity
	extractionErr     error
}

// RoundTrip intercepts HTTP responses from the token endpoint and rewrites
// the JSON body to place mapped fields at the top level. Non-token-endpoint
// requests (e.g., userInfo) pass through unchanged.
//
// When identityCfg is set, identity is extracted from the raw response body
// BEFORE the rewrite step so that identity paths are resolved against the
// original provider response, not the normalized form.
func (t *tokenResponseRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// Only rewrite responses from the token endpoint
	if req.URL.String() != t.tokenURL {
		return resp, nil
	}

	// Only rewrite successful responses (errors should pass through for proper error handling)
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}

	// Extract from the raw body before rewriteTokenResponse runs. The rewrite
	// never touches identity-shaped fields today, but extracting first makes
	// the ordering invariant independent of that assumption.
	if t.identityCfg != nil {
		result, extractErr := extractIdentityFromTokenResponse(body, t.identityCfg)
		if extractErr != nil {
			// WARN so an operator misconfiguration (e.g., wrong subjectPath) is
			// visible without enabling DEBUG. The error is safe to log: it
			// contains operator-supplied paths and type descriptions, never
			// any portion of the response body.
			slog.Warn("identity extraction from token response failed", "error", extractErr)
			t.extractionErr = extractErr
		} else {
			t.extractedIdentity = &result
		}
	}

	// Only run the field-rewrite step when a mapping is configured.
	// When mapping is nil (e.g. only identityCfg is set), pass the body through unchanged.
	var rewritten []byte
	if t.mapping != nil {
		rewritten = rewriteTokenResponse(body, t.mapping)
	} else {
		rewritten = body
	}

	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// rewriteTokenResponse extracts fields from the raw JSON using gjson paths
// and produces a new JSON object with standard OAuth 2.0 top-level field names.
// Fields that already exist at the top level and aren't overridden by the
// mapping are preserved.
func rewriteTokenResponse(body []byte, mapping *TokenResponseMapping) []byte {
	// Start with the original response to preserve any extra fields
	var original map[string]any
	if err := json.Unmarshal(body, &original); err != nil {
		// If we can't parse, return as-is and let oauth2 library handle the error
		return body
	}

	// Extract and set mapped fields at the top level
	if v := gjson.GetBytes(body, mapping.AccessTokenPath); v.Exists() {
		original["access_token"] = v.String()
	}

	// Always set token_type to "Bearer" for the oauth2 library.
	// Non-standard providers may use different values (e.g., GovSlack uses "user")
	// that the oauth2 library rejects. Since we're already using a custom mapping,
	// the original token_type value is not meaningful for standard validation.
	original["token_type"] = "Bearer"

	if path := pathOrDefault(mapping.RefreshTokenPath, "refresh_token"); path != "" {
		if v := gjson.GetBytes(body, path); v.Exists() {
			original["refresh_token"] = v.String()
		}
	}

	if path := pathOrDefault(mapping.ExpiresInPath, "expires_in"); path != "" {
		if v := gjson.GetBytes(body, path); v.Exists() && v.Int() > 0 {
			original["expires_in"] = v.Int()
		}
	}

	if path := pathOrDefault(mapping.ScopePath, "scope"); path != "" {
		if v := gjson.GetBytes(body, path); v.Exists() {
			original["scope"] = v.String()
		}
	}

	rewritten, err := json.Marshal(original)
	if err != nil {
		return body
	}
	return rewritten
}

// wrapHTTPClientForTokenExchange wraps an HTTP client's transport with a
// tokenResponseRewriter when either a TokenResponseMapping or an
// IdentityFromTokenConfig is configured (or both). Returns the original client
// and nil rewriter when both are nil, so the standard oauth2 library path is
// used. The returned rewriter (when non-nil) can be read after Exchange returns
// to retrieve any identity extracted during the token round-trip.
func wrapHTTPClientForTokenExchange(
	client *http.Client,
	mapping *TokenResponseMapping,
	identityCfg *IdentityFromTokenConfig,
	tokenURL string,
) (*http.Client, *tokenResponseRewriter) {
	if mapping == nil && identityCfg == nil {
		return client, nil
	}

	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	rewriter := &tokenResponseRewriter{
		base:        base,
		mapping:     mapping,
		identityCfg: identityCfg,
		tokenURL:    tokenURL,
	}

	// Create a shallow copy to avoid mutating the original client
	wrapped := *client
	wrapped.Transport = rewriter
	return &wrapped, rewriter
}

// pathOrDefault returns the path if non-empty, otherwise returns the default.
func pathOrDefault(path, defaultPath string) string {
	if path != "" {
		return path
	}
	return defaultPath
}

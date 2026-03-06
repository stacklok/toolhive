// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/tidwall/gjson"
)

// tokenResponseRewriter is an http.RoundTripper that normalizes non-standard
// OAuth token responses before the golang.org/x/oauth2 library parses them.
//
// Some providers (e.g., GovSlack) nest token fields under non-standard paths
// like "authed_user.access_token" instead of the top-level "access_token".
// This RoundTripper intercepts the response, extracts fields using gjson
// dot-notation paths, and rewrites the response body with standard top-level
// field names so the oauth2 library can parse them normally.
type tokenResponseRewriter struct {
	base     http.RoundTripper
	mapping  *TokenResponseMapping
	tokenURL string
}

// RoundTrip intercepts HTTP responses from the token endpoint and rewrites
// the JSON body to place mapped fields at the top level. Non-token-endpoint
// requests (e.g., userInfo) pass through unchanged.
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

	rewritten := rewriteTokenResponse(body, t.mapping)

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

// wrapHTTPClientWithMapping wraps an HTTP client's transport with a
// tokenResponseRewriter when a TokenResponseMapping is configured.
// Returns the original client unchanged if mapping is nil.
func wrapHTTPClientWithMapping(client *http.Client, mapping *TokenResponseMapping, tokenURL string) *http.Client {
	if mapping == nil {
		return client
	}

	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	// Create a shallow copy to avoid mutating the original client
	wrapped := *client
	wrapped.Transport = &tokenResponseRewriter{
		base:     base,
		mapping:  mapping,
		tokenURL: tokenURL,
	}
	return &wrapped
}

// pathOrDefault returns the path if non-empty, otherwise returns the default.
func pathOrDefault(path, defaultPath string) string {
	if path != "" {
		return path
	}
	return defaultPath
}

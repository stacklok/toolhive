// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"net/http"
	"time"
)

// httpClientTimeout matches the timeout already used by NonCachingRefresher's
// http.Client and is appropriate for OAuth token endpoints, which respond
// quickly under healthy conditions.
const httpClientTimeout = 30 * time.Second

// UserAgentTransport is an http.RoundTripper that adds the ToolHive User-Agent
// header to outbound requests when no User-Agent is already set.
//
// It is intended for HTTP clients passed to golang.org/x/oauth2 via the
// oauth2.HTTPClient context value. Without it, the library falls back to
// http.DefaultClient and Go's default Go-http-client/2.0 User-Agent, which
// upstream servers and WAFs cannot attribute to ToolHive.
type UserAgentTransport struct {
	// Base is the underlying RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper. It clones the request before
// mutating headers, per the RoundTripper contract, and sets User-Agent only
// when the request has no User-Agent set so that callers layering another
// transport can override the value.
func (t *UserAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	clonedReq := req.Clone(req.Context())
	if clonedReq.Header == nil {
		clonedReq.Header = make(http.Header)
	}
	if clonedReq.Header.Get("User-Agent") == "" {
		clonedReq.Header.Set("User-Agent", UserAgent)
	}
	return base.RoundTrip(clonedReq)
}

// NewHTTPClient returns an *http.Client whose transport sets the ToolHive
// User-Agent on every outbound request and that has a sensible timeout for
// OAuth token endpoint calls. Use it for HTTP clients passed to
// golang.org/x/oauth2 via the oauth2.HTTPClient context value.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &UserAgentTransport{},
		Timeout:   httpClientTimeout,
	}
}

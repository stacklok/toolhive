// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// serverExpiresInSeconds is the expires_in value baked into success
// responses. One second is short enough that the issued token's Expiry
// triggers a refresh attempt on every monitor tick once the caller has
// passed the short-retry leeway (which is what tests exercising
// transition timing want). Cannot use 0 here: golang.org/x/oauth2 leaves
// the issued Token.Expiry zero when expires_in is zero, which downstream
// monitors typically interpret as "no further checking needed" and exit.
const serverExpiresInSeconds = 1

// FailureMode controls how a ControllableServer responds to a token
// endpoint request. ModeSuccess returns a valid JSON token response; the
// other modes produce error shapes commonly observed in production.
type FailureMode int

const (
	// ModeSuccess returns 200 with a valid JSON token response carrying
	// access_token, token_type=Bearer, expires_in, and refresh_token.
	ModeSuccess FailureMode = iota
	// ModeWAFBlock returns 403 with an HTML body and no RFC 6749 error code —
	// the shape commonly returned by WAFs, CDNs, or reverse proxies that
	// block a request before it reaches the OAuth server. Classified as
	// transient (4xx without an OAuth error code) per RFC 6749 §5.2; see
	// pkg/auth/monitored_token_source.go isTransientRetrieveError.
	ModeWAFBlock
	// ModeServerError returns 500. Treated as transient.
	ModeServerError
	// ModeInvalidGrant returns 400 with {"error":"invalid_grant"}. Treated as
	// permanent (RFC 6749 §5.2).
	ModeInvalidGrant
)

// ControllableServer is an httptest.NewServer with a programmable token
// endpoint. Tests flip the mode at runtime to drive the token source under
// test through specific error shapes. Used to write end-to-end tests that
// exercise the real golang.org/x/oauth2 library against actual HTTP
// responses rather than synthetic Go values.
//
// Construct with NewControllableServer, swap behavior with SetMode, and
// close via the embedded *httptest.Server's Close().
type ControllableServer struct {
	*httptest.Server
	mu           sync.Mutex
	mode         FailureMode
	refreshCount int
}

// NewControllableServer returns a server in ModeSuccess. Success responses
// use a fixed 1-second expires_in (see serverExpiresInSeconds for rationale).
func NewControllableServer() *ControllableServer {
	s := &ControllableServer{mode: ModeSuccess}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// SetMode swaps the response behavior. Concurrent-safe.
func (s *ControllableServer) SetMode(m FailureMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m
}

// RequestCount returns the number of token-endpoint requests observed so
// far. Useful for tests that need to assert refresh activity occurred.
func (s *ControllableServer) RequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshCount
}

func (s *ControllableServer) handle(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.refreshCount++
	mode := s.mode
	s.mu.Unlock()

	switch mode {
	case ModeSuccess:
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"access_token":  "test-access-token",
			"token_type":    "Bearer",
			"expires_in":    serverExpiresInSeconds,
			"refresh_token": "test-refresh-token",
		}
		_ = json.NewEncoder(w).Encode(resp)
	case ModeWAFBlock:
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Request blocked by web application firewall</body></html>`))
	case ModeServerError:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	case ModeInvalidGrant:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}
}

// NewRealTokenSource builds a real golang.org/x/oauth2 token source
// pointed at the given token endpoint URL (typically a ControllableServer's
// URL). The returned source is hardcoded with a fixed initial refresh
// token ("test-refresh-token") and an expiry one hour in the past, so the
// first Token() call always triggers an HTTP refresh against the endpoint.
//
// This is intentionally non-configurable. Tests that need to exercise
// "valid current token, will not refresh yet" or "refresh token rotation"
// scenarios should construct their own oauth2.TokenSource directly rather
// than extending this helper.
func NewRealTokenSource(tokenEndpointURL string) oauth2.TokenSource {
	cfg := &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: tokenEndpointURL},
	}
	initial := &oauth2.Token{
		RefreshToken: "test-refresh-token",
		Expiry:       time.Now().Add(-time.Hour),
	}
	return cfg.TokenSource(context.Background(), initial)
}

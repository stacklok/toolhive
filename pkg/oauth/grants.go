// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// The tokenJSON struct shape and expirationTime.UnmarshalJSON below are
// adapted from golang.org/x/oauth2/internal/token.go (BSD-3-Clause,
// compatible with Apache-2.0). The upstream file is the authoritative
// reference for handling the PayPal-style JSON-string expires_in field and
// for the RFC 6749 Section 5.2 "2xx body carries an error" rule. The shape
// here is extended with the RFC 8693 issued_token_type field and the scope
// field so a single entry point serves both the authorization-code and
// token-exchange grants.

// Redact returns "<empty>" for an empty input and "[REDACTED]" otherwise.
// Grant-subpackage Config.String() methods use it to keep secrets (client
// secrets, JWT assertions, refresh tokens) out of logs and error output
// without each Config reimplementing the empty-vs-nonempty branch.
func Redact(value string) string {
	if value == "" {
		return "<empty>"
	}
	return "[REDACTED]"
}

// TokenResponse is the public result of a successful token endpoint exchange.
// It composes *oauth2.Token rather than embedding it, so the Token's
// Valid() / Extra() / Type() helpers are not promoted onto this type and
// future oauth2.Token additions cannot leak into this package's API.
//
// For the fields that RFC 8693 Section 2.2.1 requires beyond the standard
// oauth2 token (issued_token_type and scope), callers read them off the
// response directly.
type TokenResponse struct {
	// Token carries AccessToken, TokenType, RefreshToken, and Expiry populated
	// from the response body. The Raw and other unexported fields on
	// oauth2.Token are not set — use the sibling fields on TokenResponse.
	Token *oauth2.Token

	// IssuedTokenType is the RFC 8693 issued_token_type URN when the server
	// performed a token exchange. Empty for plain RFC 6749 responses.
	IssuedTokenType string

	// Scope is the raw (space-separated) scope string returned by the server.
	// Callers that need the list form can strings.Fields(scope).
	Scope string
}

// ParseTokenResponse is the single entry point for decoding a token endpoint
// response. It enforces RFC 6749 Sections 5.1 and 5.2 in a single place:
//
//   - The body is decoded as JSON first, independent of the HTTP status.
//   - If the status is non-2xx, OR the body carries an "error" field (RFC
//     6749 §5.2 — some providers return HTTP 200 with an error payload),
//     an *oauth2.RetrieveError is returned and *TokenResponse is nil.
//   - On success, access_token must be non-empty (RFC 6749 §5.1). token_type
//     is intentionally NOT required here: the x/oauth2 library treats it as
//     optional (Token.Type() defaults to "Bearer") and Google historically
//     omits it. Per-grant callers are responsible for any stricter validation
//     their specification demands.
//
// The caller is responsible for grant-specific validation (for example,
// RFC 8693 Section 2.2.1 requires the issued_token_type field to be present;
// that check belongs in the token-exchange grant's call site, not here).
//
// Malformed JSON on a failure status still yields an *oauth2.RetrieveError
// with the raw body preserved, so callers can surface the server's reply
// verbatim. Malformed JSON on a 2xx status is returned as a wrapped
// json.SyntaxError / json.UnmarshalTypeError via fmt.Errorf("%w", ...).
func ParseTokenResponse(resp *http.Response, body []byte) (*TokenResponse, error) {
	failureStatus := resp.StatusCode < 200 || resp.StatusCode > 299

	var tj tokenJSON
	if err := json.Unmarshal(body, &tj); err != nil {
		if failureStatus {
			return nil, parseRetrieveError(resp, body)
		}
		return nil, fmt.Errorf("oauth: cannot parse token response: %w", err)
	}

	if failureStatus || tj.ErrorCode != "" {
		return nil, parseRetrieveError(resp, body)
	}

	if tj.AccessToken == "" {
		return nil, fmt.Errorf("oauth: token response missing access_token (RFC 6749 Section 5.1)")
	}

	token := &oauth2.Token{
		AccessToken:  tj.AccessToken,
		TokenType:    tj.TokenType,
		RefreshToken: tj.RefreshToken,
	}
	if tj.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(tj.ExpiresIn) * time.Second)
	}

	return &TokenResponse{
		Token:           token,
		IssuedTokenType: tj.IssuedTokenType,
		Scope:           tj.Scope,
	}, nil
}

// NewFormRequest builds an HTTP POST request for an OAuth 2.0 token endpoint
// call. The body is the URL-encoded form in data, with Content-Type and
// Content-Length set. When both clientID and clientSecret are non-empty,
// HTTP Basic authentication is attached per RFC 6749 Section 2.3.1; the
// credentials are URL-encoded before being passed to SetBasicAuth, which is
// what Go's SetBasicAuth and the OAuth 2.0 spec both require.
//
// Callers own the request's Context — pass the deadline or cancellation
// signal they want DoTokenRequest to honour.
func NewFormRequest(
	ctx context.Context,
	endpoint string,
	data url.Values,
	clientID, clientSecret string,
) (*http.Request, error) {
	encoded := data.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("oauth: build token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", strconv.Itoa(len(encoded)))

	// RFC 6749 Section 2.3.1 requires URL-encoding of client credentials when
	// sent via Basic auth, and Go's SetBasicAuth docs mirror that requirement
	// for OAuth2 compatibility.
	if clientID != "" && clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}

	return req, nil
}

// DoTokenRequest executes a prepared token endpoint request and returns the
// parsed response. It is the high-level counterpart to NewFormRequest: most
// grants call NewFormRequest followed by DoTokenRequest.
//
// Passing a nil client is explicitly supported and selects the package-level
// shared default (see DefaultHTTPClient). Callers that need custom timeouts,
// a custom transport, or a test double MUST supply their own *http.Client —
// the nil shortcut does not take any caller-visible options.
//
// Behavior:
//
//   - If client is nil, DefaultHTTPClient is used so callers automatically
//     get the shared transport (connection reuse, consistent timeouts).
//   - The response body is read with io.LimitReader capped at
//     maxResponseBodySize (1 MiB, matching x/oauth2) before any parsing, so
//     a pathological server cannot exhaust memory.
//   - On every exit path — success, JSON decode failure, and RetrieveError
//     — the body is closed. The body is deliberately NOT drained:
//     io.Copy(io.Discard, resp.Body) would be unbounded on oversized or
//     never-terminating bodies and would defeat the 1 MiB cap above. When
//     the body exceeds the cap, net/http cannot reuse the connection; that
//     is the intended tradeoff and matches x/oauth2/internal/token.go.
//   - RFC 6749 Section 5.2 routing (a 2xx body with an "error" field) is
//     handled inside ParseTokenResponse; DoTokenRequest surfaces the
//     resulting *oauth2.RetrieveError unchanged.
//
// The request's own Context is authoritative: NewFormRequest builds the
// request with the caller's context attached, and client.Do observes
// req.Context() for cancellation and deadlines. A cancelled context fails
// fast without reaching the server.
func DoTokenRequest(client *http.Client, req *http.Request) (*TokenResponse, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request failed: %w", err)
	}
	defer func() {
		// Close without draining. Matching x/oauth2/internal/token.go — the
		// LimitReader below caps how much we read, and draining the remainder
		// via io.Copy(io.Discard, resp.Body) would be unbounded on oversized
		// or never-terminating bodies, which defeats the 1 MiB memory cap.
		// The tradeoff: when the body exceeds maxResponseBodySize, net/http
		// cannot reuse the underlying connection. That is acceptable — the
		// response is already pathological and connection reuse is not worth
		// unbounded reads.
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("oauth: close token response body", "error", closeErr)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response body: %w", err)
	}

	return ParseTokenResponse(resp, body)
}

// DefaultHTTPClient returns the process-wide *http.Client used by OAuth
// grant helpers when no explicit client is injected. Callers that build
// their own requests and call client.Do directly (without going through
// DoTokenRequest) use this to pick up the shared transport and inherit
// the same connection-reuse and timeout behavior as the helper path.
//
// The returned client is a process-wide singleton — callers MUST NOT mutate
// its Timeout or Transport fields. Code wanting custom timeouts must
// construct its own *http.Client and pass it to DoTokenRequest.
//
// http.Client is documented as safe for concurrent use by multiple goroutines
// (see https://pkg.go.dev/net/http#Client), so the returned value can be
// shared across goroutines without additional synchronization.
//
// TODO: consider a future opt-in SSRF-protected variant backed by
// pkg/networking.NewHttpClientBuilder. The builder blocks loopback and RFC
// 1918 ranges, which would break localhost IdPs (dex, Keycloak-in-Docker)
// and the httptest.NewServer-based tests that bind to 127.0.0.1. Not a
// default today for behavior-compatibility with pkg/auth/tokenexchange.
func DefaultHTTPClient() *http.Client {
	return sharedHTTPClient
}

// sharedHTTPClient is the process-wide default client used by OAuth grant
// helpers (see DoTokenRequest, DefaultHTTPClient). Initialized once in init
// so callers share the underlying transport and benefit from connection
// reuse across grants.
var sharedHTTPClient *http.Client

func init() {
	// Base on http.DefaultTransport.Clone() so we inherit stdlib defaults:
	// DialContext, Proxy: ProxyFromEnvironment (honors HTTP_PROXY/NO_PROXY
	// for corporate deployments), MaxIdleConns=100, IdleConnTimeout=90s,
	// ForceAttemptHTTP2=true. Overriding only the handshake timeout keeps
	// pool tuning and HTTP/2 auto-upgrade consistent with the rest of the
	// ecosystem.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Tighter than stdlib's 10s default is not needed here, but an explicit
	// cap prevents a hanging TLS handshake from silently consuming the
	// outer 30s request budget.
	transport.TLSHandshakeTimeout = 10 * time.Second
	// Deliberately NOT setting ResponseHeaderTimeout: some corporate IdP
	// chains (Entra OBO, federated Okta) legitimately take >10s to produce
	// the first byte. The outer Client.Timeout (defaultHTTPTimeout) is the
	// authoritative budget for the whole exchange.
	sharedHTTPClient = &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: transport,
	}
}

// tokenJSON is the wire shape decoded from a token endpoint response body.
// It intentionally covers both success and failure paths: RFC 6749 §5.2
// allows an "error" code to appear in a body returned with a 2xx status, so
// ParseTokenResponse decodes into this struct unconditionally before
// deciding which branch to return.
type tokenJSON struct {
	AccessToken     string         `json:"access_token"`
	TokenType       string         `json:"token_type"`
	RefreshToken    string         `json:"refresh_token"`
	ExpiresIn       expirationTime `json:"expires_in"` // number OR decimal string (PayPal, etc.)
	IssuedTokenType string         `json:"issued_token_type"`
	Scope           string         `json:"scope"`

	// RFC 6749 Section 5.2 error fields; may legitimately appear in 2xx bodies.
	ErrorCode        string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
}

// expirationTime accepts either a JSON number or a JSON string containing a
// decimal integer. At least PayPal returns expires_in as a string; a naive
// int field would fail to decode there. Negative values are treated as zero
// (no expiry), values larger than math.MaxInt32 are clamped. Copied with
// attribution from golang.org/x/oauth2/internal/token.go.
type expirationTime int32

// UnmarshalJSON implements json.Unmarshaler.
func (e *expirationTime) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	i, err := n.Int64()
	if err != nil {
		return err
	}
	if i < 0 {
		i = 0
	}
	if i > math.MaxInt32 {
		i = math.MaxInt32
	}
	*e = expirationTime(i)
	return nil
}

// retrieveErrorBody mirrors the RFC 6749 Section 5.2 fields that may appear
// in a token endpoint error response (both 2xx and non-2xx bodies — some
// providers return 200 with "error" set).
type retrieveErrorBody struct {
	ErrorCode        string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
}

// parseRetrieveError builds an *oauth2.RetrieveError from a token endpoint
// response. The resulting error always has Response and Body populated; the
// three OAuth fields are best-effort — a malformed or non-JSON body yields
// empty strings rather than an error. This helper is the single funnel
// ParseTokenResponse uses to report both non-2xx responses and 2xx bodies
// that carry an "error" field (RFC 6749 §5.2).
//
// Unexported: callers route through ParseTokenResponse.
func parseRetrieveError(resp *http.Response, body []byte) *oauth2.RetrieveError {
	retrieveErr := &oauth2.RetrieveError{
		Response: resp,
		Body:     body,
	}

	// Best-effort decode; malformed JSON leaves the OAuth fields empty.
	var parsed retrieveErrorBody
	if err := json.Unmarshal(body, &parsed); err == nil {
		retrieveErr.ErrorCode = parsed.ErrorCode
		retrieveErr.ErrorDescription = parsed.ErrorDescription
		retrieveErr.ErrorURI = parsed.ErrorURI
	}

	return retrieveErr
}

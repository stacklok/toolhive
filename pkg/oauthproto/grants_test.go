// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

func TestRedact(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "<empty>", Redact(""))
	assert.Equal(t, "[REDACTED]", Redact("secret"))
}

func TestDefaultHTTPClient_TimeoutsAndTransport(t *testing.T) {
	t.Parallel()

	client := DefaultHTTPClient()
	require.NotNil(t, client)

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "total timeout is 30s",
			check: func(t *testing.T) {
				t.Helper()
				assert.Equal(t, 30*time.Second, client.Timeout)
			},
		},
		{
			name: "transport is *http.Transport",
			check: func(t *testing.T) {
				t.Helper()
				_, ok := client.Transport.(*http.Transport)
				assert.True(t, ok, "Transport must be *http.Transport for tuning, got %T", client.Transport)
			},
		},
		{
			name: "TLS handshake timeout is 10s",
			check: func(t *testing.T) {
				t.Helper()
				transport, ok := client.Transport.(*http.Transport)
				require.True(t, ok)
				assert.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
			},
		},
		{
			// ResponseHeaderTimeout is intentionally NOT set: some corporate
			// IdP chains (Entra OBO, federated Okta) legitimately take >10s
			// to respond with the first byte. The outer Client.Timeout is
			// the authoritative budget for the whole exchange.
			name: "response header timeout is unset",
			check: func(t *testing.T) {
				t.Helper()
				transport, ok := client.Transport.(*http.Transport)
				require.True(t, ok)
				assert.Zero(t, transport.ResponseHeaderTimeout,
					"ResponseHeaderTimeout must remain unset so slow IdP chains can respond within the outer Timeout")
			},
		},
		{
			// Base-transport is http.DefaultTransport.Clone(), so Proxy is
			// ProxyFromEnvironment — honors HTTP_PROXY/NO_PROXY for corporate
			// deployments. A nil Proxy here would mean we lost the stdlib
			// default and are shipping a client that cannot traverse a
			// corporate egress proxy.
			name: "proxy inherits from environment",
			check: func(t *testing.T) {
				t.Helper()
				transport, ok := client.Transport.(*http.Transport)
				require.True(t, ok)
				assert.NotNil(t, transport.Proxy,
					"Proxy must be set (expected ProxyFromEnvironment inherited from http.DefaultTransport.Clone())")
			},
		},
		{
			name: "returns same singleton on repeated calls",
			check: func(t *testing.T) {
				t.Helper()
				// Connection reuse depends on callers receiving the same client.
				assert.Same(t, client, DefaultHTTPClient())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.check(t)
		})
	}
}

// TestDefaultHTTPClient_ConcurrentUse exercises the documented goroutine
// safety of *http.Client. A race here would signal a regression in either
// the client configuration or the test itself; run with `task test` which
// includes `-race`.
func TestDefaultHTTPClient_ConcurrentUse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	const workers = 100
	var g errgroup.Group
	for i := 0; i < workers; i++ {
		g.Go(func() error {
			resp, err := DefaultHTTPClient().Get(server.URL)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.Body.Close()
		})
	}
	// errgroup.Wait fails fast on any goroutine error (including panics
	// surfaced via recover at the caller level if they were to be added),
	// so a regression surfaces here instead of hanging until `go test`
	// hits its global timeout.
	require.NoError(t, g.Wait())
}

func TestExpirationTime_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    expirationTime
		wantErr bool
	}{
		{name: "empty input", input: ``, want: 0},
		{name: "null literal", input: `null`, want: 0},
		{name: "number", input: `3600`, want: 3600},
		{name: "decimal string", input: `"3600"`, want: 3600},
		{name: "negative number clamped to zero", input: `-1`, want: 0},
		{name: "overflow clamped to MaxInt32", input: `99999999999`, want: 2147483647},
		{name: "non-numeric string", input: `"abc"`, wantErr: true},
		{name: "bool", input: `true`, wantErr: true},
		{name: "decimal (float) rejected", input: `3.14`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got expirationTime
			err := got.UnmarshalJSON([]byte(tc.input))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseTokenResponse_Success(t *testing.T) {
	t.Parallel()

	type want struct {
		accessToken     string
		tokenType       string
		refreshToken    string
		issuedTokenType string
		scope           string
		expectExpiry    bool // true: Expiry must be ~now+seconds; false: Expiry.IsZero()
		expirySeconds   int
	}

	tests := []struct {
		name string
		body string
		want want
	}{
		{
			name: "happy path all 6 fields populated",
			body: `{
				"access_token":"at-1",
				"token_type":"Bearer",
				"refresh_token":"rt-1",
				"expires_in":3600,
				"issued_token_type":"urn:ietf:params:oauth:token-type:access_token",
				"scope":"read write"
			}`,
			want: want{
				accessToken:     "at-1",
				tokenType:       "Bearer",
				refreshToken:    "rt-1",
				issuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
				scope:           "read write",
				expectExpiry:    true,
				expirySeconds:   3600,
			},
		},
		{
			name: "expires_in as JSON number",
			body: `{"access_token":"at-1","token_type":"Bearer","expires_in":7200}`,
			want: want{
				accessToken:   "at-1",
				tokenType:     "Bearer",
				expectExpiry:  true,
				expirySeconds: 7200,
			},
		},
		{
			// This is the critical PayPal-style case: some IdPs return
			// expires_in as a JSON string. A naive `int` field would fail
			// to decode and ship a latent regression.
			name: "expires_in as JSON string (PayPal-style)",
			body: `{"access_token":"at-1","token_type":"Bearer","expires_in":"3600"}`,
			want: want{
				accessToken:   "at-1",
				tokenType:     "Bearer",
				expectExpiry:  true,
				expirySeconds: 3600,
			},
		},
		{
			name: "expires_in missing",
			body: `{"access_token":"at-1","token_type":"Bearer"}`,
			want: want{
				accessToken:  "at-1",
				tokenType:    "Bearer",
				expectExpiry: false,
			},
		},
		{
			name: "expires_in is zero",
			body: `{"access_token":"at-1","token_type":"Bearer","expires_in":0}`,
			want: want{
				accessToken:  "at-1",
				tokenType:    "Bearer",
				expectExpiry: false,
			},
		},
		{
			name: "expires_in is negative (clamped to zero)",
			body: `{"access_token":"at-1","token_type":"Bearer","expires_in":-1}`,
			want: want{
				accessToken:  "at-1",
				tokenType:    "Bearer",
				expectExpiry: false,
			},
		},
		{
			// Google historically omits token_type. The library accepts it
			// (Token.Type() defaults to "Bearer"); rejecting it would be
			// stricter than x/oauth2. Per-grant validation lives at the call
			// site.
			name: "missing token_type is accepted",
			body: `{"access_token":"at-1"}`,
			want: want{
				accessToken: "at-1",
			},
		},
		{
			name: "unknown extra fields are ignored",
			body: `{"access_token":"at-1","token_type":"Bearer","unknown_field":"x","nested":{"a":1}}`,
			want: want{
				accessToken: "at-1",
				tokenType:   "Bearer",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{StatusCode: http.StatusOK}
			before := time.Now()
			tokenResp, err := ParseTokenResponse(resp, []byte(tc.body))
			after := time.Now()

			require.NoError(t, err)
			require.NotNil(t, tokenResp)
			require.NotNil(t, tokenResp.Token, "TokenResponse must compose a non-nil *oauth2.Token")

			assert.Equal(t, tc.want.accessToken, tokenResp.Token.AccessToken)
			assert.Equal(t, tc.want.tokenType, tokenResp.Token.TokenType)
			assert.Equal(t, tc.want.refreshToken, tokenResp.Token.RefreshToken)
			assert.Equal(t, tc.want.issuedTokenType, tokenResp.IssuedTokenType)
			assert.Equal(t, tc.want.scope, tokenResp.Scope)

			if tc.want.expectExpiry {
				minExpiry := before.Add(time.Duration(tc.want.expirySeconds) * time.Second)
				maxExpiry := after.Add(time.Duration(tc.want.expirySeconds) * time.Second)
				assert.False(t, tokenResp.Token.Expiry.IsZero(), "Expiry should be set")
				assert.True(t,
					!tokenResp.Token.Expiry.Before(minExpiry) && !tokenResp.Token.Expiry.After(maxExpiry),
					"Expiry %v not in [%v, %v]", tokenResp.Token.Expiry, minExpiry, maxExpiry,
				)
			} else {
				assert.True(t, tokenResp.Token.Expiry.IsZero(), "Expiry should be zero")
			}
		})
	}
}

func TestParseTokenResponse_Failures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantErrMsg string // substring of err.Error()
	}{
		{
			name:       "missing access_token",
			body:       `{"token_type":"Bearer"}`,
			wantErrMsg: "missing access_token",
		},
		{
			name:       "malformed JSON on 2xx is a decode error",
			body:       `{not valid json`,
			wantErrMsg: "cannot parse token response",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{StatusCode: http.StatusOK}
			tokenResp, err := ParseTokenResponse(resp, []byte(tc.body))

			require.Error(t, err)
			assert.Nil(t, tokenResp)
			assert.Contains(t, err.Error(), tc.wantErrMsg)
		})
	}
}

func TestParseTokenResponse_RetrieveErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		statusCode       int
		body             string
		wantErrorCode    string
		wantErrorDesc    string
		wantErrorURI     string
		wantBodyContains string
	}{
		{
			// RFC 6749 §5.2: the "error" field can appear in a 2xx body.
			// x/oauth2 handles this; a naive "status<400?parseOK:parseError"
			// split would silently ship a bug. ParseTokenResponse routes
			// this case to *oauth2.RetrieveError.
			name:          "2xx body with error field",
			statusCode:    http.StatusOK,
			body:          `{"error":"invalid_grant","error_description":"token expired"}`,
			wantErrorCode: "invalid_grant",
			wantErrorDesc: "token expired",
		},
		{
			name:          "4xx body with all three fields",
			statusCode:    http.StatusBadRequest,
			body:          `{"error":"invalid_grant","error_description":"token expired","error_uri":"https://idp.example/errors/invalid_grant"}`,
			wantErrorCode: "invalid_grant",
			wantErrorDesc: "token expired",
			wantErrorURI:  "https://idp.example/errors/invalid_grant",
		},
		{
			name:             "4xx non-JSON body",
			statusCode:       http.StatusInternalServerError,
			body:             `<html>upstream down</html>`,
			wantBodyContains: "upstream down",
		},
		{
			name:          "4xx with only error code",
			statusCode:    http.StatusBadRequest,
			body:          `{"error":"invalid_client"}`,
			wantErrorCode: "invalid_client",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{StatusCode: tc.statusCode}
			tokenResp, err := ParseTokenResponse(resp, []byte(tc.body))

			require.Error(t, err)
			assert.Nil(t, tokenResp)

			var retrieveErr *oauth2.RetrieveError
			require.True(t, errors.As(err, &retrieveErr),
				"expected *oauth2.RetrieveError, got %T: %v", err, err)

			assert.Same(t, resp, retrieveErr.Response)
			assert.Equal(t, []byte(tc.body), retrieveErr.Body)
			assert.Equal(t, tc.wantErrorCode, retrieveErr.ErrorCode)
			assert.Equal(t, tc.wantErrorDesc, retrieveErr.ErrorDescription)
			assert.Equal(t, tc.wantErrorURI, retrieveErr.ErrorURI)
			if tc.wantBodyContains != "" {
				assert.Contains(t, string(retrieveErr.Body), tc.wantBodyContains)
			}
		})
	}
}

func TestNewFormRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		endpoint      string
		data          url.Values
		clientID      string
		clientSecret  string
		wantBasicAuth bool
		wantUser      string // URL-encoded form expected on the wire
		wantPass      string
	}{
		{
			name:          "form body with basic auth",
			endpoint:      "https://idp.example/token",
			data:          url.Values{"grant_type": {"authorization_code"}, "code": {"abc"}},
			clientID:      "client-1",
			clientSecret:  "secret-1",
			wantBasicAuth: true,
			wantUser:      "client-1",
			wantPass:      "secret-1",
		},
		{
			name:          "no basic auth when clientID is empty",
			endpoint:      "https://idp.example/token",
			data:          url.Values{"grant_type": {"authorization_code"}},
			clientID:      "",
			clientSecret:  "secret-1",
			wantBasicAuth: false,
		},
		{
			name:          "no basic auth when clientSecret is empty",
			endpoint:      "https://idp.example/token",
			data:          url.Values{"grant_type": {"authorization_code"}},
			clientID:      "client-1",
			clientSecret:  "",
			wantBasicAuth: false,
		},
		{
			name:          "credentials with special characters are URL-encoded",
			endpoint:      "https://idp.example/token",
			data:          url.Values{"grant_type": {"authorization_code"}},
			clientID:      "client:with@special",
			clientSecret:  "pa ss/wd+?",
			wantBasicAuth: true,
			wantUser:      "client%3Awith%40special",
			wantPass:      "pa+ss%2Fwd%2B%3F",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := NewFormRequest(context.Background(), tc.endpoint, tc.data, tc.clientID, tc.clientSecret)
			require.NoError(t, err)

			// Method + URL.
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t, tc.endpoint, req.URL.String())

			// Content-Type.
			assert.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"))

			// Body bytes match the URL-encoded form.
			bodyBytes, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.data.Encode(), string(bodyBytes))

			// Basic auth expectations via req.BasicAuth (Go's built-in decoder).
			user, pass, ok := req.BasicAuth()
			if tc.wantBasicAuth {
				require.True(t, ok, "expected Basic auth, Authorization=%q", req.Header.Get("Authorization"))
				assert.Equal(t, tc.wantUser, user)
				assert.Equal(t, tc.wantPass, pass)
			} else {
				assert.False(t, ok, "expected no Basic auth")
				assert.Empty(t, req.Header.Get("Authorization"))
			}
		})
	}
}

func TestNewFormRequest_InvalidEndpoint(t *testing.T) {
	t.Parallel()

	// http.NewRequestWithContext rejects control characters in the URL.
	_, err := NewFormRequest(context.Background(), "http://\x00invalid", url.Values{}, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oauth: build token request")
}

func TestDoTokenRequest_NilClientUsesDefault(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"at-1","token_type":"Bearer"}`))
	}))
	t.Cleanup(server.Close)

	req, err := NewFormRequest(context.Background(), server.URL, url.Values{"grant_type": {"x"}}, "", "")
	require.NoError(t, err)

	tokenResp, err := DoTokenRequest(nil, req)
	require.NoError(t, err)
	require.NotNil(t, tokenResp)
	assert.Equal(t, "at-1", tokenResp.Token.AccessToken)
}

func TestDoTokenRequest_TwoXXWithErrorRoutesToRetrieveError(t *testing.T) {
	t.Parallel()

	// RFC 6749 §5.2 gotcha: 200 OK with an "error" field in the body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token expired"}`))
	}))
	t.Cleanup(server.Close)

	req, err := NewFormRequest(context.Background(), server.URL, url.Values{}, "", "")
	require.NoError(t, err)

	tokenResp, err := DoTokenRequest(server.Client(), req)
	assert.Nil(t, tokenResp)
	require.Error(t, err)

	var retrieveErr *oauth2.RetrieveError
	require.True(t, errors.As(err, &retrieveErr))
	assert.Equal(t, "invalid_grant", retrieveErr.ErrorCode)
	assert.Equal(t, "token expired", retrieveErr.ErrorDescription)
}

// TestDoTokenRequest_ContextCancellation verifies a pre-cancelled context
// is rejected before the server is contacted.
func TestDoTokenRequest_ContextCancellation(t *testing.T) {
	t.Parallel()

	var hit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req, err := NewFormRequest(ctx, server.URL, url.Values{}, "", "")
	require.NoError(t, err)

	tokenResp, err := DoTokenRequest(server.Client(), req)
	require.Error(t, err)
	assert.Nil(t, tokenResp)
	assert.Zero(t, hit.Load(), "server should not have been contacted with cancelled context")
}

// trackingBody wraps an io.Reader and records whether the body was read
// and closed, plus the total number of bytes Read was allowed to consume.
// DoTokenRequest reads through a LimitReader capped at maxResponseBodySize
// and then closes without draining; bytesRead lets tests assert the cap
// is honored even when the underlying body is much larger.
type trackingBody struct {
	reader    io.Reader
	readHit   atomic.Bool
	closed    atomic.Bool
	bytesRead atomic.Int64
}

func (b *trackingBody) Read(p []byte) (int, error) {
	b.readHit.Store(true)
	n, err := b.reader.Read(p)
	b.bytesRead.Add(int64(n))
	return n, err
}

func (b *trackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

// trackingTransport returns a response with a trackingBody so the test can
// inspect whether DoTokenRequest drained and closed the body.
type trackingTransport struct {
	status     int
	bodyBytes  []byte
	lastBody   *trackingBody
	contentTyp string
}

func (t *trackingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	t.lastBody = &trackingBody{reader: strings.NewReader(string(t.bodyBytes))}
	header := http.Header{}
	if t.contentTyp != "" {
		header.Set("Content-Type", t.contentTyp)
	}
	return &http.Response{
		StatusCode: t.status,
		Header:     header,
		Body:       t.lastBody,
	}, nil
}

// TestDoTokenRequest_ClosesBody verifies that both the success and error
// paths close the response body. The body is intentionally NOT drained past
// the LimitReader cap (see TestDoTokenRequest_DoesNotDrainOversizedBody for
// the regression test on that property).
func TestDoTokenRequest_ClosesBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       []byte
		assertResp func(t *testing.T, tokenResp *TokenResponse, err error)
	}{
		{
			name:   "success (200 with valid token JSON)",
			status: http.StatusOK,
			body:   []byte(`{"access_token":"at-1","token_type":"Bearer"}`),
			assertResp: func(t *testing.T, tokenResp *TokenResponse, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, tokenResp)
				require.NotNil(t, tokenResp.Token)
				assert.Equal(t, "at-1", tokenResp.Token.AccessToken)
			},
		},
		{
			name:   "error (400 with RFC 6749 §5.2 error JSON plus trailing bytes)",
			status: http.StatusBadRequest,
			body:   []byte(`{"error":"invalid_grant"}` + strings.Repeat(" ", 1024)),
			assertResp: func(t *testing.T, tokenResp *TokenResponse, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Nil(t, tokenResp)
				var retrieveErr *oauth2.RetrieveError
				require.True(t, errors.As(err, &retrieveErr))
				assert.Equal(t, "invalid_grant", retrieveErr.ErrorCode)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := &trackingTransport{
				status:     tc.status,
				bodyBytes:  tc.body,
				contentTyp: "application/json",
			}
			client := &http.Client{Transport: tr}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example/token", strings.NewReader(""))
			require.NoError(t, err)

			tokenResp, err := DoTokenRequest(client, req)
			tc.assertResp(t, tokenResp, err)

			require.NotNil(t, tr.lastBody)
			assert.True(t, tr.lastBody.readHit.Load(), "body must be read")
			assert.True(t, tr.lastBody.closed.Load(), "body must be closed")
		})
	}
}

// TestDoTokenRequest_DoesNotDrainOversizedBody pins the behavior that the
// response body is closed without an unbounded drain. A malicious or
// misbehaving IdP could return an arbitrarily large body; the earlier
// io.Copy(io.Discard, resp.Body) drain in the defer would read all of it,
// defeating the maxResponseBodySize cap and (with a caller-supplied
// no-timeout client) blocking the goroutine indefinitely.
//
// This test wires a response body ten times larger than the cap and asserts
// the number of bytes read from the underlying body stays within one Read
// buffer of maxResponseBodySize — i.e., only the LimitReader's quota is
// consumed, not the full body.
func TestDoTokenRequest_DoesNotDrainOversizedBody(t *testing.T) {
	t.Parallel()

	// 10 MiB body — well over the 1 MiB cap.
	oversized := make([]byte, 10*maxResponseBodySize)
	for i := range oversized {
		oversized[i] = 'A'
	}

	tr := &trackingTransport{
		status:     http.StatusOK,
		bodyBytes:  oversized,
		contentTyp: "application/json",
	}
	client := &http.Client{Transport: tr}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example/token", strings.NewReader(""))
	require.NoError(t, err)

	// Expected: ParseTokenResponse fails to unmarshal 'AAAA…' as JSON on a
	// 2xx status, returning a wrapped parse error. The key property under
	// test is bytesRead, not the error surface.
	_, _ = DoTokenRequest(client, req)

	require.NotNil(t, tr.lastBody)
	assert.True(t, tr.lastBody.closed.Load(), "body must be closed")

	// io.LimitReader stops exactly at N bytes; the underlying Read is not
	// called again after the limit is hit. Allow a small slop (one typical
	// Read buffer, 32 KiB) for implementations that may over-fill on the
	// final Read.
	const slop = 32 << 10
	bytesRead := tr.lastBody.bytesRead.Load()
	assert.LessOrEqual(t, bytesRead, int64(maxResponseBodySize)+int64(slop),
		"DoTokenRequest must not drain the response body past the LimitReader cap")
}

// TestDoTokenRequest_ClientDoError surfaces transport-level errors via %w.
func TestDoTokenRequest_ClientDoError(t *testing.T) {
	t.Parallel()

	errTransport := errors.New("simulated dial failure")
	client := &http.Client{
		Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errTransport
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example/token", strings.NewReader(""))
	require.NoError(t, err)

	tokenResp, err := DoTokenRequest(client, req)
	require.Error(t, err)
	assert.Nil(t, tokenResp)
	assert.ErrorIs(t, err, errTransport)
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// failingBody returns errFailingBodyRead from Read and errFailingBodyClose
// from Close so the test can drive DoTokenRequest's read-and-drain error
// paths simultaneously.
type failingBody struct{}

var (
	errFailingBodyRead  = errors.New("simulated body read failure")
	errFailingBodyClose = errors.New("simulated body close failure")
)

func (failingBody) Read(_ []byte) (int, error) { return 0, errFailingBodyRead }
func (failingBody) Close() error               { return errFailingBodyClose }

// TestDoTokenRequest_BodyReadError surfaces transport-level body read
// failures via %w and still closes the body on the way out.
func TestDoTokenRequest_BodyReadError(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       failingBody{},
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example/token", strings.NewReader(""))
	require.NoError(t, err)

	tokenResp, err := DoTokenRequest(client, req)
	require.Error(t, err)
	assert.Nil(t, tokenResp)
	assert.ErrorIs(t, err, errFailingBodyRead)
}

// TestDoTokenRequest_RespectsContextTimeout proves req.Context() carries the
// caller's deadline through to transport cancellation.
func TestDoTokenRequest_RespectsContextTimeout(t *testing.T) {
	t.Parallel()

	// Server delays longer than the client's deadline.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)

	req, err := NewFormRequest(ctx, server.URL, url.Values{}, "", "")
	require.NoError(t, err)

	start := time.Now()
	tokenResp, err := DoTokenRequest(server.Client(), req)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Nil(t, tokenResp)
	assert.Less(t, elapsed, 500*time.Millisecond, "should have been cancelled before server replied")
}

func TestParseRetrieveError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		body                []byte
		wantErrorCode       string
		wantErrorDesc       string
		wantErrorURI        string
		wantBodyPreservedAs []byte // if non-nil, asserted explicitly (otherwise equals body)
	}{
		{
			name: "empty body",
			body: []byte{},
		},
		{
			name: "nil body",
			body: nil,
		},
		{
			name:          "only error field",
			body:          []byte(`{"error":"invalid_grant"}`),
			wantErrorCode: "invalid_grant",
		},
		{
			name:          "error and description",
			body:          []byte(`{"error":"invalid_grant","error_description":"token expired"}`),
			wantErrorCode: "invalid_grant",
			wantErrorDesc: "token expired",
		},
		{
			name:          "all three fields",
			body:          []byte(`{"error":"invalid_grant","error_description":"token expired","error_uri":"https://idp.example/docs/invalid_grant"}`),
			wantErrorCode: "invalid_grant",
			wantErrorDesc: "token expired",
			wantErrorURI:  "https://idp.example/docs/invalid_grant",
		},
		{
			name: "non-JSON body",
			body: []byte("<html>upstream is down</html>"),
		},
		{
			name: "JSON but not an object",
			body: []byte(`"just a string"`),
		},
		{
			name:          "unicode body",
			body:          []byte(`{"error":"invalid_grant","error_description":"トークンの有効期限が切れています"}`),
			wantErrorCode: "invalid_grant",
			wantErrorDesc: "トークンの有効期限が切れています",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{StatusCode: http.StatusBadRequest}
			retrieveErr := parseRetrieveError(resp, tc.body)

			require.NotNil(t, retrieveErr)
			assert.Same(t, resp, retrieveErr.Response, "Response must be populated")
			// Body is preserved verbatim regardless of whether decode succeeds.
			if tc.wantBodyPreservedAs != nil {
				assert.Equal(t, tc.wantBodyPreservedAs, retrieveErr.Body)
			} else {
				assert.Equal(t, tc.body, retrieveErr.Body)
			}
			assert.Equal(t, tc.wantErrorCode, retrieveErr.ErrorCode)
			assert.Equal(t, tc.wantErrorDesc, retrieveErr.ErrorDescription)
			assert.Equal(t, tc.wantErrorURI, retrieveErr.ErrorURI)
		})
	}
}

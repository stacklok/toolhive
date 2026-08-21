// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sentinelToken = "DAST-SENTINEL-TOKEN-DO-NOT-FORWARD" //nolint:gosec // test fixture, not a real credential

func toolCallResultJSON() string {
	return `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Authorization: Bearer ` + sentinelToken + `"}]}}`
}

// toolCallResultJSONSplit returns the same JSON-RPC message as
// toolCallResultJSON, but split into two fragments at a whitespace-
// insignificant JSON boundary (right after a top-level comma) -- valid to
// reassemble with a "\n" join, exactly as an SSE client concatenating two
// consecutive "data:" lines would. Used to simulate a hostile backend
// splitting a message across "data:" lines specifically to evade a
// per-line-only scanner.
func toolCallResultJSONSplit() (first, second string) {
	return `{"jsonrpc":"2.0",`,
		`"id":1,"result":{"content":[{"type":"text","text":"Authorization: Bearer ` + sentinelToken + `"}]}}`
}

// buildRedactingProxy wires up a *httputil.ReverseProxy fronting target using
// a TransparentProxy configured with WithSecretRedaction(true) for the given
// transport type -- mirroring the createBasicProxy/modifyResponse harness
// used by the existing tests in this package (see TestStreamingSessionIDDetection).
func buildRedactingProxy(t *testing.T, transportType string, targetURL *url.URL) *httputil.ReverseProxy {
	t.Helper()
	p := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, targetURL.String(), nil, nil, nil,
		false, false, transportType, nil, nil, "", false, nil,
		WithSecretRedaction(true),
	)
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()
		},
		FlushInterval:  -1,
		Transport:      newTracingTransport(http.DefaultTransport, p),
		ModifyResponse: p.modifyResponse,
	}
}

func TestSecretRedaction_StreamableHTTP_JSONResponse(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(toolCallResultJSON()))
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)

	proxy := buildRedactingProxy(t, "streamable-http", targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target.URL, nil)
	proxy.ServeHTTP(rec, req)

	assert.NotContains(t, rec.Body.String(), sentinelToken,
		"sentinel token must not reach the client")
	assert.Contains(t, rec.Body.String(), "REDACTED-BY-TOOLHIVE")
}

func TestSecretRedaction_StreamableHTTP_SSEResponse(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: " + toolCallResultJSON() + "\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)

	proxy := buildRedactingProxy(t, "streamable-http", targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target.URL, nil)
	proxy.ServeHTTP(rec, req)

	assert.NotContains(t, rec.Body.String(), sentinelToken,
		"sentinel token must not reach the client")
	assert.Contains(t, rec.Body.String(), "REDACTED-BY-TOOLHIVE")
}

func TestSecretRedaction_LegacySSETransport_DataLine(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: " + toolCallResultJSON() + "\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)

	proxy := buildRedactingProxy(t, "sse", targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target.URL, nil)
	proxy.ServeHTTP(rec, req)

	sc := bufio.NewScanner(rec.Body)
	var found bool
	for sc.Scan() {
		line := sc.Text()
		assert.NotContains(t, line, sentinelToken, "sentinel token must not reach the client")
		if strings.Contains(line, "REDACTED-BY-TOOLHIVE") {
			found = true
		}
	}
	assert.True(t, found, "expected a redacted data line")
}

func TestSecretRedaction_Disabled_ByDefault(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(toolCallResultJSON()))
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)

	// No WithSecretRedaction option -- default false preserves prior behavior.
	p := NewTransparentProxy("127.0.0.1", 0, targetURL.String(), nil, nil, nil,
		false, false, "streamable-http", nil, nil, "", false)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()
		},
		FlushInterval:  -1,
		Transport:      newTracingTransport(http.DefaultTransport, p),
		ModifyResponse: p.modifyResponse,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target.URL, nil)
	proxy.ServeHTTP(rec, req)

	assert.Contains(t, rec.Body.String(), sentinelToken,
		"disabled by default: response must pass through unmodified")
}

// TestSecretRedaction_StreamableHTTP_SplitAcrossDataLines is a regression
// test for a bypass: a hostile backend that splits a single JSON-RPC message
// across two "data:" lines (valid per the SSE spec -- a compliant client
// reassembles them) must not evade the scanner, which used to inspect each
// "data:" line in isolation.
func TestSecretRedaction_StreamableHTTP_SplitAcrossDataLines(t *testing.T) {
	t.Parallel()

	first, second := toolCallResultJSONSplit()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: " + first + "\ndata: " + second + "\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)

	proxy := buildRedactingProxy(t, "streamable-http", targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target.URL, nil)
	proxy.ServeHTTP(rec, req)

	assert.NotContains(t, rec.Body.String(), sentinelToken,
		"sentinel token split across data: lines must not reach the client")
	assert.Contains(t, rec.Body.String(), "REDACTED-BY-TOOLHIVE")
}

// TestSecretRedaction_LegacySSETransport_SplitAcrossDataLines is the same
// regression as above, for the legacy sse transport type's response
// processor.
func TestSecretRedaction_LegacySSETransport_SplitAcrossDataLines(t *testing.T) {
	t.Parallel()

	first, second := toolCallResultJSONSplit()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: " + first + "\ndata: " + second + "\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)

	proxy := buildRedactingProxy(t, "sse", targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target.URL, nil)
	proxy.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.NotContains(t, body, sentinelToken,
		"sentinel token split across data: lines must not reach the client")
	assert.Contains(t, body, "REDACTED-BY-TOOLHIVE")
}

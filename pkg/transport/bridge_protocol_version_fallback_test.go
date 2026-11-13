// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

// Regression coverage for toolhive#7119: `thv proxy stdio` against a remote
// MCP server that rejects protocol version 2026-07-28 (Semgrep's remote MCP
// server answers such requests with HTTP 400 "Unsupported protocol version:
// 2026-07-28").
//
// The failure was reported against CLI v0.53.0, whose client stack offered
// 2026-07-28 upstream with no fallback. On current main the underlying
// go-sdk client (via toolhive-core v0.0.59) first probes with
// server/discover at 2026-07-28 and, when the server rejects it, falls back
// to a legacy initialize at 2025-11-25 — a version Semgrep supports — so the
// bridge now completes initialize. These tests lock in that negotiation:
// the first two fail if the fallback regresses (or if the transparent proxy
// upgrades the legacy initialize back to Modern), and the third documents
// the fallback's boundary (a single retry at 2025-11-25).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

type recordedReq struct {
	method  string // JSON-RPC method, or "GET" for the SSE stream
	header  string // MCP-Protocol-Version header
	version string // protocolVersion from body params/_meta
	raw     string // first 300 bytes of body
	status  int
	at      time.Time
}

// semgrepLikeServer mimics the Semgrep remote MCP server from #7119: it
// rejects with HTTP 400 any request naming a protocol version newer than
// maxVersion, and answers initialize with the requested (accepted) version.
// The default profile (maxVersion "") mirrors Semgrep's supported list,
// which tops out at 2025-11-25.
type semgrepLikeServer struct {
	mu       sync.Mutex
	requests []recordedReq
	done     chan struct{}
	// maxVersion is the newest protocol version the server accepts.
	// Empty means 2025-11-25.
	maxVersion string
}

func (s *semgrepLikeServer) maxAccepted() string {
	if s.maxVersion == "" {
		return "2025-11-25"
	}
	return s.maxVersion
}

func unsupportedBody(rejected string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Bad Request: Unsupported protocol version: %s. Supported versions: 2024-11-05, 2025-03-26, 2025-06-18, 2025-11-25"}}`, rejected))
}

func (s *semgrepLikeServer) record(method, header, version, raw string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(raw) > 300 {
		raw = raw[:300]
	}
	s.requests = append(s.requests, recordedReq{method: method, header: header, version: version, raw: raw, status: status, at: time.Now()})
}

func (s *semgrepLikeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Standalone SSE stream (WithContinuousListening). Hold open.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.record("GET(SSE)", r.Header.Get("Mcp-Protocol-Version"), "", "", http.StatusOK)
		<-s.done
		return
	}
	body, _ := io.ReadAll(r.Body)
	rawBody := string(body)
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     json.RawMessage `json:"id"`
	}
	// The SDK may batch; unwrap a single-element array.
	trimmed := strings.TrimSpace(rawBody)
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
			body = arr[0]
		}
	}
	_ = json.Unmarshal(body, &msg)
	version := ""
	if len(msg.Params) > 0 {
		var p map[string]any
		if err := json.Unmarshal(msg.Params, &p); err == nil {
			if v, ok := p["protocolVersion"].(string); ok {
				version = v
			}
			if meta, ok := p["_meta"].(map[string]any); ok {
				if v, ok := meta["io.modelcontextprotocol/protocolVersion"].(string); ok {
					version = v
				}
			}
		}
	}
	header := r.Header.Get("Mcp-Protocol-Version")
	// Reject any request naming a version newer than this server accepts.
	tooNew := (header != "" && header > s.maxAccepted()) || (version != "" && version > s.maxAccepted())
	rejected := version
	if rejected == "" {
		rejected = header
	}

	switch msg.Method {
	case "server/discover":
		if tooNew {
			s.record(msg.Method, header, version, rawBody, http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(unsupportedBody(rejected))
			return
		}
		s.record(msg.Method, header, version, rawBody, http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"semgrep","version":"1.0"},"supportedVersions":["2024-11-05","2025-03-26","2025-06-18","2025-11-25"]}}`, string(msg.ID))
	case "initialize":
		if tooNew {
			s.record(msg.Method, header, version, rawBody, http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(unsupportedBody(rejected))
			return
		}
		s.record(msg.Method, header, version, rawBody, http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{},"serverInfo":{"name":"semgrep","version":"1.0"}}}`, string(msg.ID), version)
	case "notifications/initialized":
		s.record(msg.Method, header, version, rawBody, http.StatusAccepted)
		w.WriteHeader(http.StatusAccepted)
		return
	default:
		if strings.HasPrefix(msg.Method, "notifications/") {
			s.record(msg.Method, header, version, rawBody, http.StatusAccepted)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.record(msg.Method, header, version, rawBody, http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, string(msg.ID))
	}
}

// logRequestsSeen dumps what the fake backend observed, so a failure shows
// exactly which protocol version was offered on each hop.
func logRequestsSeen(t *testing.T, srv *semgrepLikeServer, via string) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	var sb strings.Builder
	for i, r := range srv.requests {
		fmt.Fprintf(&sb, "\n  [%d] %s method=%s header=%q bodyVersion=%q -> %d", i, r.at.Format("15:04:05.000"), r.method, r.header, r.version, r.status)
	}
	t.Logf("requests seen by Semgrep-like server%s:%s", via, sb.String())
}

// TestBridge_InitializeFallsBackToLegacyVersion is the #7119 regression
// test: the bridge's upstream client offers 2026-07-28 first (server/discover),
// the Semgrep-like server 400s it, and initialize must still complete via
// the legacy initialize at 2025-11-25 instead of failing with "upstream
// initialize failed ... likely a legacy SSE server".
func TestBridge_InitializeFallsBackToLegacyVersion(t *testing.T) {
	t.Parallel()
	srv := &semgrepLikeServer{done: make(chan struct{})}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { close(srv.done); ts.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	bridge, err := NewStdioBridge("semgrep", ts.URL, types.TransportTypeStreamableHTTP)
	require.NoError(t, err)
	up, err := bridge.connectUpstream(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = up.Close() })
	bridge.up = up // run() assigns this before initializeUpstream
	require.NoError(t, bridge.initializeUpstream(ctx), "bridge must fall back to a legacy initialize the server accepts")
	logRequestsSeen(t, srv, "")
}

// TestBridge_InitializeFallsBackViaTransparentProxy replays the same
// Semgrep-like backend through ToolHive's transparent proxy — the actual
// production path for `thv proxy stdio <remote-workload>` (bridge -> local
// proxy URL -> remote MCP server). The proxy must forward the discover
// rejection and the legacy initialize verbatim, not upgrade the initialize
// back to Modern (2026-07-28) or reject the discover probe itself.
func TestBridge_InitializeFallsBackViaTransparentProxy(t *testing.T) {
	t.Parallel()
	srv := &semgrepLikeServer{done: make(chan struct{})}
	backend := httptest.NewServer(srv)
	t.Cleanup(func() { close(srv.done); backend.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	proxy := transparent.NewTransparentProxy("127.0.0.1", 0, backend.URL,
		nil, nil, nil, false, true, "streamable-http", nil, nil, "", false)
	t.Cleanup(func() { _ = proxy.CloseListener() })
	require.NoError(t, proxy.Start(ctx))
	proxyURL := "http://" + proxy.ListenerAddr() + "/mcp"

	bridge, err := NewStdioBridge("semgrep", proxyURL, types.TransportTypeStreamableHTTP)
	require.NoError(t, err)
	up, err := bridge.connectUpstream(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = up.Close() })
	bridge.up = up
	require.NoError(t, bridge.initializeUpstream(ctx), "bridge must complete initialize through the transparent proxy")
	logRequestsSeen(t, srv, " (via proxy)")
}

// TestBridge_InitializeFailsWhenServerCapsBelowFallbackVersion documents
// the fallback's boundary: the SDK retries only once, with 2025-11-25, and
// never walks down to an older version the server lists, so a server that
// caps out below 2025-11-25 (here 2025-06-18) still fails initialize. The
// bridge cannot drive a lower version through toolhive-core's Initialize
// (the requested ProtocolVersion is ignored by the client shim), so walking
// further down belongs in toolhive-core/go-sdk, not in this repo. This test
// also proves the two regression tests above are sensitive: they fail when
// the fallback version itself is rejected, i.e. they genuinely exercise the
// fallback path rather than passing vacuously.
func TestBridge_InitializeFailsWhenServerCapsBelowFallbackVersion(t *testing.T) {
	t.Parallel()
	srv := &semgrepLikeServer{done: make(chan struct{}), maxVersion: "2025-06-18"}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { close(srv.done); ts.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	bridge, err := NewStdioBridge("semgrep", ts.URL, types.TransportTypeStreamableHTTP)
	require.NoError(t, err)
	up, err := bridge.connectUpstream(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = up.Close() })
	bridge.up = up
	require.Error(t, bridge.initializeUpstream(ctx),
		"SDK retries only with 2025-11-25, so a server capping at 2025-06-18 still fails initialize")
	logRequestsSeen(t, srv, "")
}

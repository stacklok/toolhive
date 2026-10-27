// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// maxLoggedValueBytes caps the attacker-controlled Host and path values
// written to the log.
const maxLoggedValueBytes = 256

// truncateForLog cuts s to at most maxLoggedValueBytes bytes.
func truncateForLog(s string) string {
	if len(s) > maxLoggedValueBytes {
		return s[:maxLoggedValueBytes]
	}
	return s
}

// loopbackHostGuard is vMCP's DNS-rebinding protection. It rejects with 403 a
// request that arrived on a loopback local address but whose Host header names
// a non-loopback host: that is the shape of a browser page whose own hostname
// was rebound to 127.0.0.1.
//
// It reproduces the localhost protection go-sdk's StreamableHTTPHandler
// applies (same trigger, same predicate, same response). go-sdk's check covers
// Legacy requests that reach StreamableHTTPHandler. Modern (2026-07-28)
// requests (dispatchModern) and cross-replica Legacy sessions rehydrated by
// toolhive-core's mcpcompat (serveRehydrated) did not pass through it; this
// guard covers all of them. mcpcompat's ServeHTTP also runs session cleanup
// (deleteRehydrated, forgetSession, sessionIDMgr.Terminate) for a Legacy DELETE
// around go-sdk's Host check, so without this guard a DELETE with a forged Host
// and a known session ID could end that session even though go-sdk answered
// 403; the guard runs first and stops it. Server.Handler installs this guard on
// the MCP route only (ahead of classification, so both revisions get the same
// rule). /health, /ping, /readyz, /status, /api/backends/health, /metrics,
// /.well-known/ and the embedded auth server routes are deliberately left
// unguarded: they are unauthenticated by design and expose only operational
// metadata (names, health, metric labels), and local probers and
// Host-preserving proxies may send any Host.
//
// The trigger is the connection's local address, not the configured bind
// address, exactly as in go-sdk: a server bound to 0.0.0.0 is protected for
// connections that arrive over loopback, and requests on a non-loopback local
// address (e.g. traffic to a pod IP) are not affected. A loopback hop that
// preserves a non-loopback Host (a sidecar or tunnel forwarding to 127.0.0.1
// without rewriting Host) is rejected and must rewrite the Host. A request with
// no local address in its context (a handler invoked directly, not through
// net/http's server) is passed through, as go-sdk does. The check applies only
// to connections that arrive over loopback: a vMCP bound to 0.0.0.0 with
// anonymous auth and reached through a LAN IP is not protected by it (go-sdk
// behaves the same), so do not combine a non-loopback bind with anonymous auth.
//
// The WARN on each rejection is deliberate: it is how an operator whose tunnel
// or proxy keeps the client's Host finds out why MCP requests fail. The
// plain-text 403 body is deliberate: it matches go-sdk's response.
// The MCP spec defines no Host check; for the related Origin rejection it
// requires 403 and makes a JSON-RPC body optional.
func loopbackHostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if ok && localAddr != nil && isLoopbackHost(localAddr.String()) && !isLoopbackHost(r.Host) {
			slog.Warn("rejected request with a non-loopback Host header on a loopback listener (DNS-rebinding protection)",
				"host", truncateForLog(r.Host), "method", r.Method, "path", truncateForLog(r.URL.Path))
			http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q", r.Host), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether addr (a host or host:port, IPv6 optionally
// bracketed) names the loopback interface. It deliberately mirrors go-sdk's
// internal util.IsLoopback rather than reusing networking.IsLoopbackHost, so
// the guard accepts exactly the Host values go-sdk does ("localhost",
// case-sensitive as go-sdk matches it, and any loopback IP literal); Legacy
// requests that also reach go-sdk get the same decision twice.
func isLoopbackHost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port: the whole value is the host.
		host = strings.Trim(addr, "[]")
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

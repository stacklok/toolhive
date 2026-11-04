// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
)

// TestLoopbackHostGuard pins the guard's decision table: which (local address,
// Host) pairs are rejected. Each case is also run through a bare
// mcpcompat StreamableHTTPServer, which applies go-sdk's localhost protection,
// and the two decisions must agree. A go-sdk bump that changes its loopback rule therefore fails here instead of
// silently letting Legacy and Modern requests diverge.
func TestLoopbackHostGuard(t *testing.T) {
	t.Parallel()

	ipv4Loopback := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4483}
	ipv6Loopback := &net.TCPAddr{IP: net.ParseIP("::1"), Port: 4483}
	podIP := &net.TCPAddr{IP: net.ParseIP("10.244.0.7"), Port: 4483}

	tests := []struct {
		name      string
		localAddr net.Addr // nil: no local address in the request context
		host      string
		wantPass  bool
	}{
		{name: "loopback listener, 127.0.0.1 Host", localAddr: ipv4Loopback, host: "127.0.0.1:4483", wantPass: true},
		{name: "loopback listener, other 127/8 Host", localAddr: ipv4Loopback, host: "127.0.0.2:4483", wantPass: true},
		{name: "loopback listener, localhost Host", localAddr: ipv4Loopback, host: "localhost:4483", wantPass: true},
		{name: "loopback listener, localhost Host without port", localAddr: ipv4Loopback, host: "localhost", wantPass: true},
		{name: "loopback listener, IPv6 loopback Host", localAddr: ipv4Loopback, host: "[::1]:4483", wantPass: true},
		{name: "loopback listener, bracketed IPv6 Host without port", localAddr: ipv4Loopback, host: "[::1]", wantPass: true},
		{name: "loopback listener, bare IPv6 Host", localAddr: ipv4Loopback, host: "::1", wantPass: true},
		{name: "loopback listener, IPv6 loopback Host with zone", localAddr: ipv4Loopback, host: "[::1%lo0]:4483", wantPass: true},
		{name: "loopback listener, IPv4-mapped loopback Host", localAddr: ipv4Loopback, host: "[::ffff:127.0.0.1]:4483", wantPass: true},
		{name: "IPv6 loopback listener, localhost Host", localAddr: ipv6Loopback, host: "localhost:4483", wantPass: true},
		{name: "loopback listener, foreign Host", localAddr: ipv4Loopback, host: "attacker.example:4483", wantPass: false},
		{name: "IPv6 loopback listener, foreign Host", localAddr: ipv6Loopback, host: "attacker.example:4483", wantPass: false},
		{name: "loopback listener, alias of 127.0.0.1", localAddr: ipv4Loopback, host: "my-alias.local:4483", wantPass: false},
		{name: "loopback listener, non-loopback IP Host", localAddr: ipv4Loopback, host: "192.168.1.10:4483", wantPass: false},
		{name: "loopback listener, localhost subdomain", localAddr: ipv4Loopback, host: "localhost.attacker.example:4483", wantPass: false},
		// go-sdk matches the literal "localhost" only: no trailing dot, case-sensitive.
		{name: "loopback listener, fully-qualified localhost.", localAddr: ipv4Loopback, host: "localhost.:4483", wantPass: false},
		{name: "loopback listener, upper-case LOCALHOST", localAddr: ipv4Loopback, host: "LOCALHOST:4483", wantPass: false},
		{name: "loopback listener, 127.0.0.1 with trailing dot", localAddr: ipv4Loopback, host: "127.0.0.1.:4483", wantPass: false},
		{name: "loopback listener, unspecified 0.0.0.0 Host", localAddr: ipv4Loopback, host: "0.0.0.0:4483", wantPass: false},
		{name: "loopback listener, empty Host", localAddr: ipv4Loopback, host: "", wantPass: false},
		{name: "non-loopback listener, foreign Host", localAddr: podIP, host: "vmcp.example.com", wantPass: true},
		{name: "no local address in context", localAddr: nil, host: "attacker.example", wantPass: true},
	}

	// The reference: the same transport vMCP serves Legacy requests through,
	// with no options, so go-sdk's default localhost protection is active.
	reference := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("parity", "0.0.0"))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			newRequest := func() *http.Request {
				ctx := context.Background()
				if tt.localAddr != nil {
					ctx = context.WithValue(ctx, http.LocalAddrContextKey, tt.localAddr)
				}
				req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/mcp", nil)
				req.Header.Set("Accept", "text/event-stream")
				req.Host = tt.host
				return req
			}

			var reached bool
			h := loopbackHostGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newRequest())

			assert.Equal(t, tt.wantPass, reached, "Host=%q localAddr=%v", tt.host, tt.localAddr)
			if tt.wantPass {
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code)
			}

			// go-sdk answers a rejected Host with 403 "invalid Host header";
			// anything else (here a 4xx for the session-less GET) means its
			// localhost protection let the request through.
			refRec := httptest.NewRecorder()
			reference.ServeHTTP(refRec, newRequest())
			sdkRejected := refRec.Code == http.StatusForbidden &&
				strings.Contains(refRec.Body.String(), "invalid Host header")
			assert.Equal(t, !tt.wantPass, sdkRejected,
				"go-sdk must make the same decision for Host=%q localAddr=%v (status %d, body %q)",
				tt.host, tt.localAddr, refRec.Code, refRec.Body.String())
		})
	}
}

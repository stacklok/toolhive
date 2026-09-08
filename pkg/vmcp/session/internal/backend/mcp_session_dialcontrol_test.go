// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// denyAllDial is a net.Dialer.Control hook that refuses every dial, standing in
// for an SSRF/private-address guard an embedder would install.
func denyAllDial(_, _ string, _ syscall.RawConn) error {
	return errors.New("dial blocked by test policy")
}

// TestHTTPConnector_DialControlGuardsSessionInit proves that a Control hook
// supplied via WithDialControl fires on the session-init dial for every backend
// transport: a deny-all hook makes the connect fail before any request reaches
// the backend. The companion unguarded connector against the same server shows
// the target IS dialed without the hook, so it is the guard — not an
// unreachable server — that blocks the request.
func TestHTTPConnector_DialControlGuardsSessionInit(t *testing.T) {
	t.Parallel()

	for _, transport := range []string{"streamable-http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()

			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(srv.Close)

			target := &vmcp.BackendTarget{
				WorkloadID:    "guarded-backend",
				WorkloadName:  "guarded-backend",
				BaseURL:       srv.URL,
				TransportType: transport,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)

			// Guarded: the deny-all hook must fire and block the dial before any
			// request. Recording invocation (rather than only asserting error +
			// zero hits) ensures an unrelated earlier failure — transport
			// construction, auth, URL parsing — cannot pass this test for the
			// wrong reason.
			var fired atomic.Bool
			control := func(_, _ string, _ syscall.RawConn) error {
				fired.Store(true)
				return errors.New("dial blocked by test policy")
			}
			guarded := NewHTTPConnector(newTestRegistry(t), WithDialControl(control))
			sess, _, err := guarded(ctx, target, nil, "", nil)
			if sess != nil {
				_ = sess.Close()
			}
			require.Error(t, err, "deny-all dial control must fail session init")
			assert.True(t, fired.Load(), "the dial-control hook must have been invoked")
			assert.Zero(t, hits.Load(), "guarded session-init dial must not reach the backend")

			// Unguarded: the same target IS dialed, proving the server is
			// reachable and the guard above is what stopped the dial.
			unguarded := NewHTTPConnector(newTestRegistry(t))
			sess2, _, _ := unguarded(ctx, target, nil, "", nil)
			if sess2 != nil {
				_ = sess2.Close()
			}
			assert.Positive(t, hits.Load(), "unguarded session-init dial must reach the backend")
		})
	}
}

// TestBackendBaseTransport_NilHookReturnsDefault pins the no-op guarantee: with
// no dial control the base transport is http.DefaultTransport itself (not a
// clone), so the dial path is byte-for-byte identical to before the hook
// existed.
func TestBackendBaseTransport_NilHookReturnsDefault(t *testing.T) {
	t.Parallel()

	assert.Same(t, http.DefaultTransport, backendBaseTransport(nil),
		"a nil dial control must leave http.DefaultTransport untouched")
	assert.NotSame(t, http.DefaultTransport, backendBaseTransport(denyAllDial),
		"a non-nil dial control must yield a distinct transport carrying the hook")
}

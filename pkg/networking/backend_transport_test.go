// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripperFunc adapts a plain function to http.RoundTripper so a test can
// replace http.DefaultTransport with a value that is NOT a *http.Transport.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestProtectedDialerControl verifies the exported dial-control hook refuses
// private/loopback/link-local peers while allowing a public one. It is the
// policy wired into the vMCP backend dial paths by pkg/vmcp/cli.
func TestProtectedDialerControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "loopback IPv4 blocked", address: "127.0.0.1:8080", wantErr: true},
		{name: "loopback IPv6 blocked", address: "[::1]:8080", wantErr: true},
		{name: "RFC 1918 blocked", address: "10.0.0.5:443", wantErr: true},
		{name: "link-local blocked", address: "169.254.169.254:80", wantErr: true},
		{name: "public IPv4 allowed", address: "93.184.216.34:443", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ProtectedDialerControl("tcp", tt.address, nil)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestCloneDefaultTransportWithDialControl verifies the shared backend
// transport construction: a nil control clones DefaultTransport (a distinct
// value that still reaches the server), and a non-nil control's hook fires on
// the dial so a deny-all hook blocks the request before it leaves the client.
func TestCloneDefaultTransportWithDialControl(t *testing.T) {
	t.Parallel()

	denyAll := func(_, _ string, _ syscall.RawConn) error {
		return errors.New("dial blocked by test policy")
	}

	t.Run("nil control clones a reachable transport", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		transport := CloneDefaultTransportWithDialControl(nil)
		require.NotNil(t, transport)
		assert.NotSame(t, http.DefaultTransport, transport, "must be a clone, not the shared DefaultTransport")

		resp, err := (&http.Client{Transport: transport}).Get(srv.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, int32(1), hits.Load())
	})

	t.Run("non-nil control fires on the dial", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		transport := CloneDefaultTransportWithDialControl(denyAll)
		_, err := (&http.Client{Transport: transport}).Get(srv.URL)
		require.Error(t, err, "deny-all dial control must block the request")
		assert.Zero(t, hits.Load(), "a blocked dial must not reach the server")
	})
}

// TestCloneDefaultTransportWithDialControl_ReconstructsWhenDefaultReplaced covers
// the fallback branch taken when http.DefaultTransport is not a *http.Transport
// (e.g. replaced by a test or third-party library): the helper must reconstruct
// the Go standard-library defaults rather than silently drop them. This test is
// intentionally NOT parallel — it mutates the process-global http.DefaultTransport
// and restores it via t.Cleanup, which runs before any parallel test starts.
//
//nolint:paralleltest // Mutates the process-global http.DefaultTransport, so neither this test nor its subtests may run in parallel.
func TestCloneDefaultTransportWithDialControl_ReconstructsWhenDefaultReplaced(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("replaced transport should not be used")
	})

	t.Run("nil control reconstructs the stdlib defaults", func(t *testing.T) {
		transport := CloneDefaultTransportWithDialControl(nil)
		require.NotNil(t, transport)
		assert.NotNil(t, transport.Proxy, "proxy must be preserved")
		assert.NotNil(t, transport.DialContext, "dial context must be set")
		assert.True(t, transport.ForceAttemptHTTP2, "HTTP/2 must be preserved")
		assert.Equal(t, maxIdleConns, transport.MaxIdleConns)
		assert.Equal(t, idleConnTimeout, transport.IdleConnTimeout)
		assert.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
		assert.Equal(t, 1*time.Second, transport.ExpectContinueTimeout)
	})

	t.Run("non-nil control reconstructs and installs the hook", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		transport := CloneDefaultTransportWithDialControl(func(_, _ string, _ syscall.RawConn) error {
			return errors.New("dial blocked by test policy")
		})
		_, err := (&http.Client{Transport: transport}).Get(srv.URL)
		require.Error(t, err, "the reconstructed transport must still honor the dial control")
		assert.Zero(t, hits.Load())
	})
}

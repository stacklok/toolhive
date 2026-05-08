// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeNameSeq disambiguates concurrent test pipes so parallel runs don't
// collide on the global pipe namespace.
var pipeNameSeq atomic.Uint64

func TestCheckHealth_NamedPipe_Success(t *testing.T) {
	t.Parallel()

	pipeName := fmt.Sprintf("thv-test-%d", pipeNameSeq.Add(1))
	pipePath := `\\.\pipe\` + pipeName

	listener, err := winio.ListenPipe(pipePath, &winio.PipeConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	expectedNonce := "pipe-nonce"
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, expectedNonce)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux} //nolint:gosec // test server, ReadHeaderTimeout not relevant
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	err = CheckHealth(context.Background(), "npipe://"+pipeName, expectedNonce)
	require.NoError(t, err)
}

func TestCheckHealth_NamedPipe_NotFound(t *testing.T) {
	t.Parallel()
	err := CheckHealth(context.Background(), "npipe://nonexistent-pipe-thv-test", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check failed")
}

// TestCheckHealth_NamedPipe_HungServerCancelsOnContext pins that a peer which
// accepts the connection but never responds does not wedge CheckHealth: when
// the caller's context expires the dial / read returns and CheckHealth surfaces
// a wrapped error. This is the discovery StateUnhealthy path; without this
// guarantee a hung peer would block the previous-instance probe forever.
func TestCheckHealth_NamedPipe_HungServerCancelsOnContext(t *testing.T) {
	t.Parallel()

	pipeName := fmt.Sprintf("thv-test-hung-%d", pipeNameSeq.Add(1))
	pipePath := `\\.\pipe\` + pipeName

	listener, err := winio.ListenPipe(pipePath, &winio.PipeConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	// Collect accepted conns under a mutex so the background goroutine never
	// touches t after the test body returns. t.Cleanup must be registered
	// from the test goroutine, not from the accept loop, otherwise a late
	// Accept could race with test teardown ("Log in goroutine after Test
	// has completed").
	var (
		connsMu sync.Mutex
		conns   []net.Conn
	)
	t.Cleanup(func() {
		connsMu.Lock()
		defer connsMu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})

	// Drain accepts so the dial succeeds, but never write anything back. The
	// goroutine exits when the listener is closed via t.Cleanup above.
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			// Hold the connection open without responding so CheckHealth's
			// HTTP read blocks until the context deadline fires.
			connsMu.Lock()
			conns = append(conns, conn)
			connsMu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = CheckHealth(ctx, "npipe://"+pipeName, "")
	elapsed := time.Since(start)

	require.Error(t, err)
	// The CheckHealth call must return promptly after the context expires
	// rather than blocking on the hung peer indefinitely. healthTimeout is
	// 5 s, so anything within ~2 s of the 250 ms ctx is the context path.
	assert.Less(t, elapsed, 2*time.Second, "CheckHealth wedged on a hung peer")
}

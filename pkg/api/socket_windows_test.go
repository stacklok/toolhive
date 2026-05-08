// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package api

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/server/discovery"
)

// pipeNameSeq disambiguates concurrent test pipes so parallel runs don't
// collide on the global Windows pipe namespace.
var pipeNameSeq atomic.Uint64

func uniqueTestPipe() string {
	return fmt.Sprintf(`\\.\pipe\thv-api-test-%d`, pipeNameSeq.Add(1))
}

func TestSocketURL_Windows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{"named pipe", `\\.\pipe\thv-api`, "npipe://thv-api"},
		// AF_UNIX Windows paths are now percent-encoded so the resulting URL
		// round-trips through net/url.Parse cleanly. The previous form
		// (unix://C:\path\thv.sock) was rejected by url.Parse with
		// "invalid port :\\path\\thv.sock".
		{"af_unix windows path", `C:\path\thv.sock`, `unix:///C:%5Cpath%5Cthv.sock`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, socketURL(tt.address))
		})
	}
}

// TestSocketURL_RoundTrip_NamedPipe pins that socketURL output is always
// parseable by ParseNamedPipeURL, closing the producer/consumer loop.
func TestSocketURL_RoundTrip_NamedPipe(t *testing.T) {
	t.Parallel()
	addr := `\\.\pipe\thv-api`
	got, err := discovery.ParseNamedPipeURL(socketURL(addr))
	require.NoError(t, err)
	assert.Equal(t, addr, got)
}

// TestSocketURL_RoundTrip_AFUnix pins that socketURL output for an AF_UNIX
// drive-letter path is always parseable by ParseUnixSocketPath, closing the
// producer/consumer loop on the case where percent-encoding matters most.
// Without the synthetic leading slash in socketURL, url.URL.String() emits
// only two slashes and url.Parse mis-reads the drive letter as host:port.
func TestSocketURL_RoundTrip_AFUnix(t *testing.T) {
	t.Parallel()
	addr := `C:\path\thv.sock`
	got, err := discovery.ParseUnixSocketPath(socketURL(addr))
	require.NoError(t, err)
	assert.Equal(t, addr, got)
}

func TestSetupUnixSocket_NamedPipe(t *testing.T) {
	t.Parallel()
	pipePath := uniqueTestPipe()

	listener, err := setupUnixSocket(pipePath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	// The listener should accept a winio dial within a short timeout, proving
	// it is wired to the named-pipe namespace and not to AF_UNIX.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	connCh := make(chan error, 1)
	go func() {
		conn, dialErr := winio.DialPipeContext(ctx, pipePath)
		if conn != nil {
			_ = conn.Close()
		}
		connCh <- dialErr
	}()

	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			_ = conn.Close()
		}
	}()

	select {
	case err := <-connCh:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("dial against named-pipe listener timed out")
	}
}

func TestCleanupUnixSocket_NamedPipe_NoOp(t *testing.T) {
	t.Parallel()
	// Passing a pipe address to cleanup must not error or panic. There is no
	// file to remove; the assertion here is simply that the call returns
	// cleanly.
	cleanupUnixSocket(`\\.\pipe\thv-api-cleanup-noop`)
}

// TestSetupUnixSocket_NamedPipe_FirstInstanceWins pins winio's first-wins
// semantics: ListenPipe sets FILE_FLAG_FIRST_PIPE_INSTANCE, so a second
// listener targeting the same name must fail. This is the safety net the
// discovery layer relies on to detect a stale-PID conflict; if a future winio
// bump silently relaxed it, two thv processes could bind the same pipe and
// quietly race for traffic.
func TestSetupUnixSocket_NamedPipe_FirstInstanceWins(t *testing.T) {
	t.Parallel()
	pipePath := uniqueTestPipe()

	first, err := setupUnixSocket(pipePath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	// setupUnixSocket returns (nil, err) on the named-pipe failure path, so
	// no defensive Close is needed for the second listener.
	_, err = setupUnixSocket(pipePath)
	require.Error(t, err, "second ListenPipe on the same name must fail")
	assert.Contains(t, err.Error(), "failed to create named pipe listener")
}

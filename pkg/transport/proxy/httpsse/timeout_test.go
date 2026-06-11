// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// startProxy starts an HTTPSSEProxy on a random port and registers cleanup.
func startProxy(t *testing.T, opts ...Option) *HTTPSSEProxy {
	t.Helper()
	proxy := NewHTTPSSEProxy("localhost", 0, false, nil, nil, opts...)
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})
	return proxy
}

// TestHTTPSSEProxy_ServerTimeoutsConfigured verifies that the read/write timeout
// options are wired onto the proxy's http.Server, and that non-positive values
// preserve the package defaults.
func TestHTTPSSEProxy_ServerTimeoutsConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []Option
		wantRead  time.Duration
		wantWrite time.Duration
	}{
		{
			name:      "defaults",
			opts:      nil,
			wantRead:  defaultReadTimeout,
			wantWrite: defaultWriteTimeout,
		},
		{
			name:      "overrides applied",
			opts:      []Option{WithReadTimeout(5 * time.Second), WithWriteTimeout(7 * time.Second)},
			wantRead:  5 * time.Second,
			wantWrite: 7 * time.Second,
		},
		{
			name:      "non-positive ignored",
			opts:      []Option{WithReadTimeout(0), WithWriteTimeout(-time.Second)},
			wantRead:  defaultReadTimeout,
			wantWrite: defaultWriteTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proxy := startProxy(t, tt.opts...)
			require.NotNil(t, proxy.server)
			assert.Equal(t, tt.wantRead, proxy.server.ReadTimeout)
			assert.Equal(t, tt.wantWrite, proxy.server.WriteTimeout)
		})
	}
}

// TestHTTPSSEProxy_SSEStreamSurvivesWriteTimeout proves that the WriteTimeout
// middleware mounted on the SSE endpoint clears the connection write deadline, so
// a server->client write that happens after http.Server.WriteTimeout would have
// fired still reaches the client instead of severing the stream.
func TestHTTPSSEProxy_SSEStreamSurvivesWriteTimeout(t *testing.T) {
	t.Parallel()

	const writeTimeout = 200 * time.Millisecond

	proxy := startProxy(t, WithWriteTimeout(writeTimeout))

	// Open the SSE stream and read the initial endpoint event to confirm the
	// connection is established and obtain the session ID. The Accept header is
	// required for the WriteTimeout middleware to recognize this as an SSE
	// connection and clear the write deadline.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/sse", proxy.server.Addr), nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	sseResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sseResp.Body.Close() })

	sessionID, err := extractSessionID(sseResp.Body)
	require.NoError(t, err)
	require.NotEmpty(t, sessionID)

	// Wait well past the server WriteTimeout. Without the middleware clearing the
	// write deadline, any subsequent server->client write would fail here.
	time.Sleep(3 * writeTimeout)

	// Push a message to the live SSE client after the deadline window.
	msg, err := jsonrpc2.NewCall(
		jsonrpc2.StringID("after-deadline"),
		"test.method",
		map[string]any{"data": "still alive"},
	)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(t.Context(), msg))

	// The client must receive the post-deadline message rather than EOF.
	received := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := sseResp.Body.Read(buf)
		if err != nil {
			readErr <- err
			return
		}
		received <- string(buf[:n])
	}()

	select {
	case data := <-received:
		assert.Contains(t, data, "after-deadline",
			"SSE client should receive the message written after WriteTimeout")
	case err := <-readErr:
		t.Fatalf("SSE stream was severed (write deadline not cleared): %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for post-deadline SSE message")
	}
}

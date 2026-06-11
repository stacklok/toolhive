// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// slowBodyReader trickles bytes one at a time with a delay before each, so a full
// request body takes far longer than the server ReadTimeout to upload.
type slowBodyReader struct {
	total   int
	delay   time.Duration
	emitted int
}

func (r *slowBodyReader) Read(p []byte) (int, error) {
	if r.emitted >= r.total {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.emitted++
	if len(p) > 0 {
		p[0] = ' '
		return 1, nil
	}
	return 0, nil
}

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

// TestHTTPSSEProxy_SSEStreamSurvivesTimeout proves a long-lived SSE GET stream is
// not severed by the server's read/write deadlines:
//   - WriteTimeout: the middleware mounted on the SSE endpoint clears the
//     connection write deadline, so a server->client write that happens after
//     http.Server.WriteTimeout would have fired still reaches the client.
//   - ReadTimeout (the issue's no-regression criterion): ReadTimeout bounds the
//     request read only, so the stream lives on the response side past it (the GET
//     request itself is read instantly).
func TestHTTPSSEProxy_SSEStreamSurvivesTimeout(t *testing.T) {
	t.Parallel()

	const timeout = 200 * time.Millisecond

	tests := []struct {
		name    string
		opt     Option
		callID  string
		severed string
	}{
		{
			name:    "WriteTimeout",
			opt:     WithWriteTimeout(timeout),
			callID:  "after-write-timeout",
			severed: "SSE stream was severed (write deadline not cleared)",
		},
		{
			name:    "ReadTimeout",
			opt:     WithReadTimeout(timeout),
			callID:  "after-read-timeout",
			severed: "SSE stream was severed by ReadTimeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := startProxy(t, tt.opt)

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

			// Wait well past the deadline. Without the stream surviving it, any
			// subsequent server->client write would fail here.
			time.Sleep(3 * timeout)

			// Push a message to the live SSE client after the deadline window.
			msg, err := jsonrpc2.NewCall(
				jsonrpc2.StringID(tt.callID),
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
				assert.Contains(t, data, tt.callID,
					"SSE client should receive the message written after the deadline")
			case err := <-readErr:
				t.Fatalf("%s: %v", tt.severed, err)
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for post-deadline SSE message")
			}
		})
	}
}

// TestHTTPSSEProxy_SlowBodyUploadTerminatedByReadTimeout proves a slow trickle
// upload to the JSON-RPC POST endpoint is terminated near ReadTimeout.
func TestHTTPSSEProxy_SlowBodyUploadTerminatedByReadTimeout(t *testing.T) {
	t.Parallel()

	const readTimeout = 300 * time.Millisecond
	proxy := startProxy(t, WithReadTimeout(readTimeout))

	body := &slowBodyReader{total: 15, delay: 200 * time.Millisecond}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s/messages?session_id=x", proxy.server.Addr), body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.total)

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if resp != nil {
		_ = resp.Body.Close()
	}

	if err == nil {
		assert.NotEqual(t, http.StatusOK, resp.StatusCode,
			"slow upload should not be accepted as a successful request")
	}
	assert.Less(t, elapsed, 2*time.Second,
		"connection should be terminated near ReadTimeout, not held for the full upload")
}

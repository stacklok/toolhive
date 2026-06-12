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

// TestHTTPSSEProxy_ReadTimeoutConfigured verifies the read timeout option is wired
// onto the proxy's http.Server (non-positive values keep the default), and that
// WriteTimeout stays unset so long-lived SSE response streams are not severed.
func TestHTTPSSEProxy_ReadTimeoutConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     []Option
		wantRead time.Duration
	}{
		{"default", nil, defaultReadTimeout},
		{"override applied", []Option{WithReadTimeout(5 * time.Second)}, 5 * time.Second},
		{"non-positive ignored", []Option{WithReadTimeout(0)}, defaultReadTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proxy := startProxy(t, tt.opts...)
			require.NotNil(t, proxy.server)
			assert.Equal(t, tt.wantRead, proxy.server.ReadTimeout)
			assert.Zero(t, proxy.server.WriteTimeout)
		})
	}
}

// TestHTTPSSEProxy_SSEStreamSurvivesReadTimeout is the issue's no-regression
// criterion: ReadTimeout bounds the request read only, so a long-lived SSE GET
// stream must survive well past it (the GET request is read instantly; the stream
// lives on the response side).
func TestHTTPSSEProxy_SSEStreamSurvivesReadTimeout(t *testing.T) {
	t.Parallel()

	const readTimeout = 200 * time.Millisecond
	proxy := startProxy(t, WithReadTimeout(readTimeout))

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

	// Wait well past ReadTimeout, then confirm the stream still delivers.
	time.Sleep(3 * readTimeout)

	msg, err := jsonrpc2.NewCall(jsonrpc2.StringID("after-read-timeout"), "test.method",
		map[string]any{"data": "still alive"})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(t.Context(), msg))

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
		assert.Contains(t, data, "after-read-timeout",
			"SSE GET stream should survive past ReadTimeout")
	case err := <-readErr:
		t.Fatalf("SSE stream was severed by ReadTimeout: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE message after ReadTimeout")
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

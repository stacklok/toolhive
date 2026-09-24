// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/proxy/httpsse"
	"github.com/stacklok/toolhive/pkg/transport/proxy/streamable"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const testProxyReadTimeout = 300 * time.Millisecond

// plumbingSlowBody trickles a request body slowly enough that an applied test
// timeout expires well before the body is complete. Without the transport's
// WithReadTimeout plumbing, the request takes roughly three seconds instead.
type plumbingSlowBody struct {
	total   int
	emitted int
}

func (r *plumbingSlowBody) Read(p []byte) (int, error) {
	if r.emitted >= r.total {
		return 0, io.EOF
	}
	time.Sleep(200 * time.Millisecond)
	if len(p) == 0 {
		return 0, nil
	}
	r.emitted++
	p[0] = ' '
	return 1, nil
}

// The stdio proxy constructors require a concrete port, so these cases run
// sequentially to minimize the close-and-rebind window around ephemeral ports.
func TestFactoryReadTimeoutChangesLiveProxyBehavior(t *testing.T) { //nolint:paralleltest
	tests := []struct {
		name       string
		startProxy func(t *testing.T) string
	}{
		{
			name:       "stdio streamable HTTP proxy",
			startProxy: startStdioStreamableProxyWithFactoryTimeout,
		},
		{
			name:       "stdio SSE proxy",
			startProxy: startStdioSSEProxyWithFactoryTimeout,
		},
		{
			name:       "direct HTTP transparent proxy",
			startProxy: startTransparentProxyWithFactoryTimeout,
		},
	}

	// Keep the network cases sequential for the ephemeral-port handoff above.
	for _, tt := range tests { //nolint:paralleltest
		t.Run(tt.name, func(t *testing.T) {
			assertSlowUploadTimesOut(t, tt.startProxy(t))
		})
	}
}

func startStdioStreamableProxyWithFactoryTimeout(t *testing.T) string {
	t.Helper()

	transport, err := NewFactory().Create(types.Config{
		Type:        types.TransportTypeStdio,
		ProxyMode:   types.ProxyModeStreamableHTTP,
		ReadTimeout: testProxyReadTimeout,
	})
	require.NoError(t, err)
	stdioTransport, ok := transport.(*StdioTransport)
	require.True(t, ok, "factory should create a StdioTransport")

	port := reserveLoopbackPort(t)
	proxy := streamable.NewHTTPProxy(LocalhostIPv4, port, nil, nil, stdioTransport.streamableProxyOptions()...)
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	addr := fmt.Sprintf("%s:%d", LocalhostIPv4, port)
	waitForListener(t, addr)
	return "http://" + addr + streamable.StreamableHTTPEndpoint
}

func startStdioSSEProxyWithFactoryTimeout(t *testing.T) string {
	t.Helper()

	transport, err := NewFactory().Create(types.Config{
		Type:        types.TransportTypeStdio,
		ProxyMode:   types.ProxyModeSSE,
		ReadTimeout: testProxyReadTimeout,
	})
	require.NoError(t, err)
	stdioTransport, ok := transport.(*StdioTransport)
	require.True(t, ok, "factory should create a StdioTransport")

	port := reserveLoopbackPort(t)
	proxy := httpsse.NewHTTPSSEProxy(LocalhostIPv4, port, false, nil, nil, stdioTransport.sseProxyOptions()...)
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	addr := fmt.Sprintf("%s:%d", LocalhostIPv4, port)
	waitForListener(t, addr)
	setupCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(setupCtx, http.MethodGet, "http://"+addr+"/sse", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // loopback test server
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)

	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)
	const marker = "session_id="
	start := bytes.Index(buf[:n], []byte(marker))
	require.NotEqual(t, -1, start, "SSE endpoint event should contain a session ID")
	start += len(marker)
	end := bytes.IndexByte(buf[start:n], '\n')
	require.NotEqual(t, -1, end, "SSE endpoint event should terminate its data line")
	sessionID := string(bytes.TrimSpace(buf[start : start+end]))
	require.NotEmpty(t, sessionID)

	return fmt.Sprintf("http://%s/messages?session_id=%s", addr, sessionID)
}

func startTransparentProxyWithFactoryTimeout(t *testing.T) string {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	transport, err := NewFactory().Create(types.Config{
		Type:        types.TransportTypeStreamableHTTP,
		Host:        LocalhostIPv4,
		ReadTimeout: testProxyReadTimeout,
	})
	require.NoError(t, err)
	httpTransport, ok := transport.(*HTTPTransport)
	require.True(t, ok, "factory should create an HTTPTransport")
	httpTransport.SetRemoteURL(backend.URL + "/mcp")
	require.NoError(t, httpTransport.Start(t.Context()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpTransport.Stop(stopCtx)
	})

	proxy, ok := httpTransport.proxy.(*transparent.TransparentProxy)
	require.True(t, ok, "HTTPTransport should start a transparent proxy")
	addr := proxy.ListenerAddr()
	require.NotEmpty(t, addr)
	return "http://" + addr + "/mcp"
}

func assertSlowUploadTimesOut(t *testing.T, url string) {
	t.Helper()

	body := &plumbingSlowBody{total: 15}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.total)

	client := &http.Client{Timeout: 5 * time.Second}
	start := time.Now()
	resp, requestErr := client.Do(req)
	elapsed := time.Since(start)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}

	if requestErr == nil {
		assert.NotEqual(t, http.StatusOK, resp.StatusCode,
			"a slow upload should not reach a successful handler response")
	}
	assert.Less(t, elapsed, 2*time.Second,
		"configured timeout should terminate the upload before its three-second body completes")
}

func reserveLoopbackPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", LocalhostIPv4+":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond, "proxy should begin listening")
}

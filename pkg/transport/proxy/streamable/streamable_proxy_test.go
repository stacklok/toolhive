// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/transport/session"
)

// TestNewHTTPProxy tests the creation of a new HTTP proxy
//
//nolint:paralleltest // Test modifies shared proxy state
func TestNewHTTPProxy(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, nil, nil)

	assert.NotNil(t, proxy)
	assert.Equal(t, "localhost", proxy.host)
	assert.Equal(t, 8080, proxy.port)
	assert.NotNil(t, proxy.messageCh)
	assert.NotNil(t, proxy.responseCh)
}

// TestProxyChannelCommunication tests basic proxy channel communication
//
//nolint:paralleltest // Test modifies shared proxy state
func TestProxyChannelCommunication(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, nil, nil)
	ctx := t.Context()

	// Test that we can send a message to the destination
	request, err := jsonrpc2.NewCall(jsonrpc2.StringID("test"), "test.method", nil)
	require.NoError(t, err)

	err = proxy.SendMessageToDestination(request)
	assert.NoError(t, err)

	// Verify message was received
	select {
	case msg := <-proxy.GetMessageChannel():
		assert.Equal(t, request, msg)
	case <-time.After(1 * time.Second):
		t.Fatal("Message not received")
	}

	// Test that we can forward a response
	response, err := jsonrpc2.NewResponse(jsonrpc2.StringID("test"), "result", nil)
	require.NoError(t, err)

	err = proxy.ForwardResponseToClients(ctx, response)
	assert.NoError(t, err)

	// Verify response was forwarded
	select {
	case msg := <-proxy.GetResponseChannel():
		assert.Equal(t, response, msg)
	case <-time.After(1 * time.Second):
		t.Fatal("Response not forwarded")
	}
}

// TestSendMessageToDestination tests sending messages to the destination
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendMessageToDestination(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, nil, nil)

	// Create a test message
	msg, err := jsonrpc2.NewCall(jsonrpc2.StringID("test"), "test.method", nil)
	require.NoError(t, err)

	// Send the message
	err = proxy.SendMessageToDestination(msg)
	assert.NoError(t, err)

	// Verify the message was sent
	select {
	case receivedMsg := <-proxy.messageCh:
		assert.Equal(t, msg, receivedMsg)
	case <-time.After(1 * time.Second):
		t.Fatal("Message was not sent to channel")
	}
}

// TestSendMessageToDestination_ChannelFull tests sending when channel is full
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendMessageToDestination_ChannelFull(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, nil, nil)

	// Fill the channel
	for i := 0; i < 100; i++ {
		msg, _ := jsonrpc2.NewCall(jsonrpc2.StringID(fmt.Sprintf("test%d", i)), "test.method", nil)
		proxy.messageCh <- msg
	}

	// Try to send another message
	msg, _ := jsonrpc2.NewCall(jsonrpc2.StringID("overflow"), "test.method", nil)
	err := proxy.SendMessageToDestination(msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send message to destination")
}

// TestStartStop tests starting and stopping the proxy
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestStartStop(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 0, nil, nil) // Use port 0 for auto-assignment
	ctx := t.Context()

	// Start the proxy
	err := proxy.Start(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, proxy.server)

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Stop the proxy
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err = proxy.Stop(stopCtx)
	assert.NoError(t, err)

	// Verify shutdown channel is closed
	select {
	case <-proxy.shutdownCh:
		// Good, channel is closed
	default:
		t.Fatal("Shutdown channel was not closed")
	}
}

// TestResolveRequestTimeout tests the resolveRequestTimeout function.
func TestResolveRequestTimeout(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     time.Duration
	}{
		{
			name:     "no env var returns default",
			envValue: "",
			want:     defaultRequestTimeout,
		},
		{
			name:     "valid duration string",
			envValue: "10m",
			want:     10 * time.Minute,
		},
		{
			name:     "valid short duration",
			envValue: "45s",
			want:     45 * time.Second,
		},
		{
			name:     "invalid string returns default",
			envValue: "garbage",
			want:     defaultRequestTimeout,
		},
		{
			name:     "zero duration returns default",
			envValue: "0s",
			want:     defaultRequestTimeout,
		},
		{
			name:     "negative duration returns default",
			envValue: "-5m",
			want:     defaultRequestTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Always call t.Setenv to ensure the env var is in a known state,
			// even for the "unset" case (empty string is treated as unset).
			t.Setenv(proxyRequestTimeoutEnv, tt.envValue)
			got := resolveRequestTimeout()
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestNewHTTPProxyUsesResolvedTimeout verifies the constructor wires the
// resolved timeout into the proxy struct.
func TestNewHTTPProxyUsesResolvedTimeout(t *testing.T) {
	t.Setenv(proxyRequestTimeoutEnv, "7m")
	proxy := NewHTTPProxy("localhost", 0, nil, nil)
	assert.Equal(t, 7*time.Minute, proxy.requestTimeout)
}

// TestNewHTTPProxyDefaultTimeout verifies the default timeout when no env var
// is set. Not parallel because t.Setenv is needed to clear any ambient
// TOOLHIVE_PROXY_REQUEST_TIMEOUT from the test runner's environment.
func TestNewHTTPProxyDefaultTimeout(t *testing.T) { //nolint:paralleltest
	t.Setenv(proxyRequestTimeoutEnv, "")
	proxy := NewHTTPProxy("localhost", 0, nil, nil)
	assert.Equal(t, defaultRequestTimeout, proxy.requestTimeout)
}

func TestNewHTTPProxyWithSessionStorage(t *testing.T) {
	t.Parallel()
	storage := session.NewLocalStorage()
	proxy := NewHTTPProxy("localhost", 0, nil, nil, WithSessionStorage(storage))
	require.NotNil(t, proxy)
	require.NotNil(t, proxy.sessionManager)
}

// TestNewHTTPProxy_AuthInfoHandlerMounted verifies that a sentinel authInfoHandler
// registered via WithAuthInfoHandler is reachable at /.well-known/oauth-protected-resource
// after Start is called.
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestNewHTTPProxy_AuthInfoHandlerMounted(t *testing.T) {
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil, WithAuthInfoHandler(sentinel))
	ctx := t.Context()

	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	// Poll until server is ready (max 500 ms).
	url := fmt.Sprintf("http://localhost:%d/.well-known/oauth-protected-resource", port)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = http.Get(url) //nolint:gosec // test-only URL construction
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "server did not become ready")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestNewHTTPProxy_AuthInfoHandlerNil verifies that when no authInfoHandler is set,
// requests to /.well-known/oauth-protected-resource return a JSON 404 (not a Go default 404).
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestNewHTTPProxy_AuthInfoHandlerNil(t *testing.T) {
	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil) // no WithAuthInfoHandler
	ctx := t.Context()

	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	url := fmt.Sprintf("http://localhost:%d/.well-known/oauth-protected-resource", port)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = http.Get(url) //nolint:gosec // test-only URL construction
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "server did not become ready")
	defer resp.Body.Close()

	// WellKnownHandler returns a JSON 404, not 200.
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
}

// TestNewHTTPProxy_PrefixHandlerMounted verifies that a handler registered via
// WithPrefixHandlers is reachable at its declared path prefix.
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestNewHTTPProxy_PrefixHandlerMounted(t *testing.T) {
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil,
		WithPrefixHandlers(map[string]http.Handler{"/oauth/token": sentinel}),
	)
	ctx := t.Context()

	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	url := fmt.Sprintf("http://localhost:%d/oauth/token", port)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = http.Get(url) //nolint:gosec // test-only URL construction
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "server did not become ready")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestNewHTTPProxy_PrefixHandlerDoesNotShadowMCP verifies that registering prefix
// handlers does not prevent the /mcp endpoint from being reachable.
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestNewHTTPProxy_PrefixHandlerDoesNotShadowMCP(t *testing.T) {
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil,
		WithPrefixHandlers(map[string]http.Handler{"/oauth/token": sentinel}),
		WithStandaloneSSE(false), // keep GET's 405 response so this test terminates promptly
	)
	ctx := t.Context()

	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	// /mcp returns 405 for GET (only POST is accepted), which proves the
	// endpoint is registered — a missing route would be 404.
	url := fmt.Sprintf("http://localhost:%d%s", port, StreamableHTTPEndpoint)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = http.Get(url) //nolint:gosec // test-only URL construction
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "server did not become ready")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestNewHTTPProxy_ExactWellKnownBeatsSubtree verifies that Go's http.ServeMux
// specificity rule causes an exact /.well-known/openid-configuration prefix handler
// to win over the /.well-known/ subtree authInfoHandler (PRM endpoint), ensuring
// the two registrations coexist without collision.
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestNewHTTPProxy_ExactWellKnownBeatsSubtree(t *testing.T) {
	oidcHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // sentinel: exact match wins
	})
	prmHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil,
		WithPrefixHandlers(map[string]http.Handler{"/.well-known/openid-configuration": oidcHandler}),
		WithAuthInfoHandler(prmHandler),
	)
	ctx := t.Context()

	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	// Poll until server is ready.
	oidcURL := fmt.Sprintf("http://localhost:%d/.well-known/openid-configuration", port)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = http.Get(oidcURL) //nolint:gosec // test-only URL construction
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "server did not become ready")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTeapot, resp.StatusCode, "exact /.well-known/openid-configuration must win")

	prmURL := fmt.Sprintf("http://localhost:%d/.well-known/oauth-protected-resource", port)
	resp2, err := http.Get(prmURL) //nolint:gosec // test-only URL construction
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode, "/.well-known/ subtree must still reach authInfoHandler")
}

// nonFlushableResponseWriter implements http.ResponseWriter but deliberately
// does NOT implement http.Flusher, to exercise handleGet's capability check.
type nonFlushableResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

func newNonFlushableResponseWriter() *nonFlushableResponseWriter {
	return &nonFlushableResponseWriter{header: make(http.Header)}
}

func (w *nonFlushableResponseWriter) Header() http.Header { return w.header }

func (w *nonFlushableResponseWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

func (w *nonFlushableResponseWriter) WriteHeader(statusCode int) { w.statusCode = statusCode }

// TestHandleGet_StandaloneSSEDisabledReturns405 verifies handleGet returns 405
// when the proxy was constructed with WithStandaloneSSE(false), regardless of
// whether the underlying ResponseWriter supports flushing.
func TestHandleGet_StandaloneSSEDisabledReturns405(t *testing.T) {
	t.Parallel()

	proxy := NewHTTPProxy("localhost", 0, nil, nil, WithStandaloneSSE(false))
	req := httptest.NewRequest(http.MethodGet, StreamableHTTPEndpoint, nil)
	rec := httptest.NewRecorder()

	proxy.handleGet(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, 0, proxy.serverStreams.streamCount(), "no stream should be registered when SSE is disabled")
}

// TestHandleGet_NonFlushableWriterReturns500 verifies handleGet fails loudly
// with 500 when the ResponseWriter does not support streaming, rather than
// silently degrading (standaloneSSE defaults to true here).
func TestHandleGet_NonFlushableWriterReturns500(t *testing.T) {
	t.Parallel()

	proxy := NewHTTPProxy("localhost", 0, nil, nil)
	req := httptest.NewRequest(http.MethodGet, StreamableHTTPEndpoint, nil)
	rec := newNonFlushableResponseWriter()

	proxy.handleGet(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.statusCode)
	assert.Equal(t, 0, proxy.serverStreams.streamCount(), "stream must not remain registered after the capability check fails")
}

// TestHandleGet_DefaultEnabledRegistersAndDeregistersStream verifies that,
// with standaloneSSE at its default (enabled) and a known Mcp-Session-Id, a
// GET request registers a stream for the lifetime of the connection and
// deregisters it on client disconnect (request context cancellation).
func TestHandleGet_DefaultEnabledRegistersAndDeregistersStream(t *testing.T) {
	t.Parallel()

	proxy := NewHTTPProxy("localhost", 0, nil, nil)
	sessID := uuid.NewString()
	require.NoError(t, proxy.sessionManager.AddWithID(sessID))

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, StreamableHTTPEndpoint, nil).WithContext(ctx)
	req.Header.Set("Mcp-Session-Id", sessID)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.handleGet(rec, req)
	}()

	require.Eventually(t, func() bool {
		return proxy.serverStreams.streamCount() == 1
	}, time.Second, 5*time.Millisecond, "stream was not registered")

	cancel() // simulate client disconnect

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleGet did not return after client disconnect")
	}

	assert.Equal(t, 0, proxy.serverStreams.streamCount(), "stream must be deregistered after disconnect")
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
}

// TestHandleGet_SessionDecisionMatrix verifies handleGet's Mcp-Session-Id
// resolution matrix directly (without a full HTTP round-trip): no header is
// rejected with 400, an unknown session with 404, and a known session opens
// the stream with 200.
func TestHandleGet_SessionDecisionMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setHeader     bool
		useKnownSess  bool
		wantStatus    int
		wantStreamReg bool
	}{
		{name: "no header", setHeader: false, wantStatus: http.StatusBadRequest},
		{name: "unknown session", setHeader: true, useKnownSess: false, wantStatus: http.StatusNotFound},
		{name: "known session", setHeader: true, useKnownSess: true, wantStatus: http.StatusOK, wantStreamReg: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := NewHTTPProxy("localhost", 0, nil, nil)
			sessID := uuid.NewString()
			require.NoError(t, proxy.sessionManager.AddWithID(sessID))

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			req := httptest.NewRequest(http.MethodGet, StreamableHTTPEndpoint, nil).WithContext(ctx)
			if tt.setHeader {
				if tt.useKnownSess {
					req.Header.Set("Mcp-Session-Id", sessID)
				} else {
					req.Header.Set("Mcp-Session-Id", uuid.NewString())
				}
			}
			rec := httptest.NewRecorder()

			done := make(chan struct{})
			go func() {
				defer close(done)
				proxy.handleGet(rec, req)
			}()

			if tt.wantStreamReg {
				require.Eventually(t, func() bool {
					return proxy.serverStreams.streamCount() == 1
				}, time.Second, 5*time.Millisecond, "stream was not registered")
				cancel()
			}

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handleGet did not return")
			}

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

package transparent

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func init() {
	logger.Initialize() // ensure logging doesn't panic
}

func TestStreamingSessionIDDetection(t *testing.T) {
	t.Parallel()
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, true, false, "streamable-http", nil)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(200)

		// Simulate SSE lines
		w.Write([]byte("data: hello\n"))
		w.Write([]byte("data: sessionId=ABC123\n"))
		w.(http.Flusher).Flush()

		time.Sleep(10 * time.Millisecond)
		w.Write([]byte("data: more\n"))
	}))
	defer target.Close()

	// set up reverse proxy using ModifyResponse
	parsedURL, _ := http.NewRequest("GET", target.URL, nil)
	proxyURL := httputil.NewSingleHostReverseProxy(parsedURL.URL)
	proxyURL.FlushInterval = -1
	proxyURL.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}
	proxyURL.ModifyResponse = proxy.modifyForSessionID

	// hit the proxy
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	proxyURL.ServeHTTP(rec, req)

	// read all SSE lines
	sc := bufio.NewScanner(rec.Body)
	var bodyLines []string
	for sc.Scan() {
		bodyLines = append(bodyLines, sc.Text())
	}
	assert.Contains(t, bodyLines, "data: sessionId=ABC123")

	// side-effect: proxy should have seen session
	assert.True(t, proxy.serverInitialized(), "server should have been initialized")
	_, ok := proxy.sessionManager.Get("ABC123")
	assert.True(t, ok, "sessionManager should have stored ABC123")
}

func createBasicProxy(p *TransparentProxy, targetURL *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = targetURL.Scheme
		r.URL.Host = targetURL.Host
		r.Host = targetURL.Host
	}
	proxy.FlushInterval = -1
	proxy.Transport = &tracingTransport{base: http.DefaultTransport, p: p}
	proxy.ModifyResponse = p.modifyForSessionID
	return proxy
}

func TestNoSessionIDInNonSSE(t *testing.T) {
	t.Parallel()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "streamable-http", nil)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Set both content-type and also optionally MCP header to test behavior
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"hello": "world"}`))
	}))
	defer target.Close()

	targetURL, _ := url.Parse(target.URL)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)

	proxy.ServeHTTP(rec, req)

	assert.False(t, p.serverInitialized(), "server should not be initialized for application/json")
	_, ok := p.sessionManager.Get("XYZ789")
	assert.False(t, ok, "no session should be added")
}

func TestHeaderBasedSessionInitialization(t *testing.T) {
	t.Parallel()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "streamable-http", nil)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Set both content-type and also optionally MCP header to test behavior
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "XYZ789")
		w.WriteHeader(200)
		w.Write([]byte(`{"hello": "world"}`))
	}))
	defer target.Close()

	targetURL, _ := url.Parse(target.URL)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	proxy.ServeHTTP(rec, req)

	assert.True(t, p.serverInitialized(), "server should not be initialized for application/json")
	_, ok := p.sessionManager.Get("XYZ789")
	assert.True(t, ok, "no session should be added")
}

func TestTracePropagationHeaders(t *testing.T) {
	t.Parallel()

	// Initialize OTel for testing
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Mock downstream server that captures headers
	var capturedHeaders http.Header
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "success"}`))
	}))
	defer downstream.Close()

	// Create transparent proxy pointing to mock server
	proxy := NewTransparentProxy("localhost", 0, downstream.URL, nil, nil, false, false, "", nil)

	// Parse downstream URL
	targetURL, err := url.Parse(downstream.URL)
	assert.NoError(t, err)

	// Create reverse proxy with the same director logic as the main code
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1

	// Store the original director
	originalDirector := reverseProxy.Director

	// Override director to inject trace propagation headers (same as main code)
	reverseProxy.Director = func(req *http.Request) {
		// Apply original director logic first
		originalDirector(req)

		// Inject OpenTelemetry trace propagation headers for downstream tracing
		if req.Context() != nil {
			otel.GetTextMapPropagator().Inject(req.Context(), propagation.HeaderCarrier(req.Header))
		}
	}

	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	// Create request with trace context
	ctx, span := otel.Tracer("test").Start(context.Background(), "test-operation")
	defer span.End()

	req := httptest.NewRequest("POST", "/test", strings.NewReader(`{"method": "test"}`))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	// Get expected propagation headers
	expectedHeaders := make(http.Header)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(expectedHeaders))

	// Send request through proxy
	recorder := httptest.NewRecorder()
	reverseProxy.ServeHTTP(recorder, req)

	// Verify propagation headers were injected
	for headerName := range expectedHeaders {
		assert.NotEmpty(t, capturedHeaders.Get(headerName),
			"Expected trace propagation header %s to be present", headerName)
	}

	// Verify the request still works normally
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestWellKnownPathPrefixMatching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		requestPath        string
		expectedStatusCode int
		shouldCallHandler  bool
		description        string
	}{
		{
			name:               "base path without resource component",
			requestPath:        "/.well-known/oauth-protected-resource",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "RFC 9728 base path should route to authInfoHandler",
		},
		{
			name:               "path with single resource component",
			requestPath:        "/.well-known/oauth-protected-resource/mcp",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "Path with /mcp resource component should route to authInfoHandler",
		},
		{
			name:               "path with multiple resource components",
			requestPath:        "/.well-known/oauth-protected-resource/api/v1/service",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "Path with multiple resource components should route to authInfoHandler",
		},
		{
			name:               "path with different resource name",
			requestPath:        "/.well-known/oauth-protected-resource/resource1",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "Path with arbitrary resource component should route to authInfoHandler",
		},
		{
			name:               "non-matching well-known path",
			requestPath:        "/.well-known/other-endpoint",
			expectedStatusCode: http.StatusNotFound,
			shouldCallHandler:  false,
			description:        "Different well-known endpoint should return 404",
		},
		{
			name:               "path without leading dot",
			requestPath:        "/well-known/oauth-protected-resource",
			expectedStatusCode: http.StatusNotFound,
			shouldCallHandler:  false,
			description:        "Path without leading dot should return 404",
		},
		{
			name:               "similar but non-matching path with suffix",
			requestPath:        "/.well-known/oauth-protected-resource-other",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "Per RFC 9728, prefix matching means this should match",
		},
		{
			name:               "path with trailing slash",
			requestPath:        "/.well-known/oauth-protected-resource/",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "Path with trailing slash should route to authInfoHandler",
		},
		{
			name:               "path with query parameters",
			requestPath:        "/.well-known/oauth-protected-resource?param=value",
			expectedStatusCode: http.StatusOK,
			shouldCallHandler:  true,
			description:        "Path with query parameters should route to authInfoHandler",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Track whether the auth info handler was called
			handlerCalled := false
			authHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"authorized": true}`))
			})

			// Create the well-known handler directly (same logic as in transparent_proxy.go)
			wellKnownHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Per RFC 9728, match /.well-known/oauth-protected-resource and any subpaths
				// e.g., /.well-known/oauth-protected-resource/mcp
				if strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource") {
					authHandler.ServeHTTP(w, r)
				} else {
					http.NotFound(w, r)
				}
			})

			// Create a mux and register the well-known handler (same as in transparent_proxy.go)
			mux := http.NewServeMux()
			mux.Handle("/.well-known/", wellKnownHandler)

			// Create a test request
			req := httptest.NewRequest("GET", tt.requestPath, nil)
			recorder := httptest.NewRecorder()

			// Serve the request through the mux
			mux.ServeHTTP(recorder, req)

			// Verify status code
			assert.Equal(t, tt.expectedStatusCode, recorder.Code,
				"%s: expected status %d but got %d", tt.description, tt.expectedStatusCode, recorder.Code)

			// Verify whether handler was called
			assert.Equal(t, tt.shouldCallHandler, handlerCalled,
				"%s: handler call mismatch (expected=%v, actual=%v)", tt.description, tt.shouldCallHandler, handlerCalled)

			// For successful cases, verify response body
			if tt.shouldCallHandler && recorder.Code == http.StatusOK {
				assert.Contains(t, recorder.Body.String(), "authorized",
					"%s: expected response body to contain auth info", tt.description)
			}
		})
	}
}

func TestWellKnownPathWithoutAuthHandler(t *testing.T) {
	t.Parallel()

	// Test that when authInfoHandler is nil, the well-known route is not registered
	// Create a mux without registering the well-known handler (simulating authInfoHandler == nil case)
	mux := http.NewServeMux()

	// Only register a default handler that returns 404 for everything
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	// Create a test request to well-known path
	req := httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil)
	recorder := httptest.NewRecorder()

	// Serve the request
	mux.ServeHTTP(recorder, req)

	// When no auth handler is provided, the well-known route should not be registered
	// The request should fall through to the default handler which returns 404
	assert.Equal(t, http.StatusNotFound, recorder.Code,
		"Without auth handler, well-known path should return 404")
}

// TestTransparentProxy_IdempotentStop tests that Stop() can be called multiple times safely
func TestTransparentProxy_IdempotentStop(t *testing.T) {
	t.Parallel()

	// Create a proxy
	proxy := NewTransparentProxy("127.0.0.1", 0, "http://localhost:8080", nil, nil, false, false, "sse", nil)

	ctx := context.Background()

	// Start the proxy (this creates the shutdown channel)
	err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	// First stop should succeed
	err = proxy.Stop(ctx)
	assert.NoError(t, err, "First Stop() should succeed")

	// Second stop should also succeed (idempotent)
	err = proxy.Stop(ctx)
	assert.NoError(t, err, "Second Stop() should succeed (idempotent)")

	// Third stop should also succeed
	err = proxy.Stop(ctx)
	assert.NoError(t, err, "Third Stop() should succeed (idempotent)")
}

// TestTransparentProxy_StopWithoutStart tests that Stop() works even if never started
func TestTransparentProxy_StopWithoutStart(t *testing.T) {
	t.Parallel()

	// Create a proxy but don't start it
	proxy := NewTransparentProxy("127.0.0.1", 0, "http://localhost:8080", nil, nil, false, false, "sse", nil)

	ctx := context.Background()

	// Stop should handle being called without Start
	err := proxy.Stop(ctx)
	// This may return an error or succeed depending on implementation
	// The key is it shouldn't panic
	_ = err
}

// callbackTracker is a helper to track callback invocations in a thread-safe manner
type callbackTracker struct {
	invoked bool
	mu      sync.Mutex
}

func newCallbackTracker() (*callbackTracker, func()) {
	tracker := &callbackTracker{}
	callback := func() {
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		tracker.invoked = true
	}
	return tracker, callback
}

func (ct *callbackTracker) isInvoked() bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.invoked
}

// setupRemoteProxyTest creates a proxy with health check enabled for remote servers
func setupRemoteProxyTest(t *testing.T, serverURL string, callback types.HealthCheckFailedCallback) (*TransparentProxy, context.Context, context.CancelFunc) {
	t.Helper()

	proxy := NewTransparentProxy(
		"127.0.0.1",
		0,
		serverURL,
		nil,
		nil,
		true, // enableHealthCheck
		true, // isRemote
		"sse",
		callback,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)

	err := proxy.Start(ctx)
	require.NoError(t, err)

	return proxy, ctx, cancel
}

// TestTransparentProxy_RemoteServerFailure tests various remote server failure scenarios
func TestTransparentProxy_RemoteServerFailure(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		setupServer    func(t *testing.T) (string, func()) // returns serverURL and cleanup func
		waitDuration   time.Duration
		expectCallback bool
		expectRunning  bool
		description    string
	}{
		{
			name: "5xx status code triggers failure",
			setupServer: func(t *testing.T) (string, func()) {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				return server.URL, server.Close
			},
			expectCallback: true,
			expectRunning:  false,
			description:    "5xx status codes should trigger callback and stop proxy",
		},
		{
			name: "connection refused triggers failure",
			setupServer: func(t *testing.T) (string, func()) {
				t.Helper()
				// Create a server, get its URL, then close it immediately
				tempServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				serverURL := tempServer.URL
				tempServer.Close()          // Close immediately - connection will be refused
				return serverURL, func() {} // No cleanup needed
			},
			expectCallback: true,
			expectRunning:  false,
			description:    "Connection refused should trigger callback and stop proxy",
		},
		{
			name: "4xx status code does not trigger failure",
			setupServer: func(t *testing.T) (string, func()) {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				return server.URL, server.Close
			},
			expectCallback: false,
			expectRunning:  true,
			description:    "4xx status codes should not trigger callback (considered healthy)",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tracker, callback := newCallbackTracker()
			serverURL, cleanup := tc.setupServer(t)
			defer cleanup()

			proxy, ctx, cancel := setupRemoteProxyTest(t, serverURL, callback)
			defer cancel()
			defer func() { _ = proxy.Stop(ctx) }()

			proxy.setServerInitialized()

			// Health check runs every 10 seconds, wait for one cycle + buffer
			time.Sleep(11 * time.Second)

			assert.Equal(t, tc.expectCallback, tracker.isInvoked(), tc.description)

			running, _ := proxy.IsRunning(ctx)
			assert.Equal(t, tc.expectRunning, running, "Proxy running state should match expectation")
		})
	}
}

// TestTransparentProxy_RemoteServerFailure_Timeout tests that timeouts
// trigger health check failure
func TestTransparentProxy_RemoteServerFailure_Timeout(t *testing.T) {
	t.Parallel()

	tracker, callback := newCallbackTracker()

	// Create server that hangs (simulates timeout)
	// Note: The pinger has a 5-second timeout, so we need to hang longer
	// Use a channel to allow graceful shutdown
	serverDone := make(chan struct{})
	hangingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Sleep longer than the pinger timeout (5 seconds)
		// But use a select to allow cancellation
		select {
		case <-time.After(6 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-serverDone:
			return
		}
	}))
	defer func() {
		close(serverDone)
		hangingServer.Close()
	}()

	proxy, ctx, cancel := setupRemoteProxyTest(t, hangingServer.URL, callback)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	proxy.setServerInitialized()

	// Health check runs every 10s, timeout occurs after 5s, so wait for health check + timeout + buffer
	time.Sleep(16 * time.Second)

	assert.True(t, tracker.isInvoked(), "Callback should be invoked on timeout")

	running, _ := proxy.IsRunning(ctx)
	assert.False(t, running, "Proxy should stop after timeout")
}

// TestTransparentProxy_RemoteServerFailure_BecomesUnavailable tests that a server
// that starts healthy but becomes unavailable triggers the callback
func TestTransparentProxy_RemoteServerFailure_BecomesUnavailable(t *testing.T) {
	t.Parallel()

	tracker, callback := newCallbackTracker()

	// Create server that starts healthy
	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer healthyServer.Close()

	proxy, ctx, cancel := setupRemoteProxyTest(t, healthyServer.URL, callback)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	proxy.setServerInitialized()

	// Wait for first health check (should succeed)
	time.Sleep(11 * time.Second)
	assert.False(t, tracker.isInvoked(), "Callback should NOT be invoked while server is healthy")

	// Now close the server to simulate it becoming unavailable
	healthyServer.Close()

	// Wait for next health check cycle
	time.Sleep(12 * time.Second)
	assert.True(t, tracker.isInvoked(), "Callback should be invoked after server becomes unavailable")

	running, _ := proxy.IsRunning(ctx)
	assert.False(t, running, "Proxy should stop after server becomes unavailable")
}

// TestTransparentProxy_RemoteServerFailure_5xxCodes tests various 5xx status codes
func TestTransparentProxy_RemoteServerFailure_5xxCodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		statusCode int
	}{
		{"500 Internal Server Error", http.StatusInternalServerError},
		{"502 Bad Gateway", http.StatusBadGateway},
		{"503 Service Unavailable", http.StatusServiceUnavailable},
		{"504 Gateway Timeout", http.StatusGatewayTimeout},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tracker, callback := newCallbackTracker()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			proxy, ctx, cancel := setupRemoteProxyTest(t, server.URL, callback)
			defer cancel()
			defer func() { _ = proxy.Stop(ctx) }()

			proxy.setServerInitialized()

			// Health check runs every 10 seconds, wait for one cycle + buffer
			time.Sleep(11 * time.Second)

			assert.True(t, tracker.isInvoked(), "Callback should be invoked for status code %d", tc.statusCode)

			running, _ := proxy.IsRunning(ctx)
			assert.False(t, running, "Proxy should stop after health check failure")
		})
	}
}

// TestTransparentProxy_RemoteServerHealthy_4xxCodes tests that 4xx codes
// are considered healthy (per the pinger logic)
func TestTransparentProxy_RemoteServerHealthy_4xxCodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		statusCode int
	}{
		{"401 Unauthorized", http.StatusUnauthorized},
		{"403 Forbidden", http.StatusForbidden},
		{"404 Not Found", http.StatusNotFound},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tracker, callback := newCallbackTracker()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			proxy, ctx, cancel := setupRemoteProxyTest(t, server.URL, callback)
			defer cancel()
			defer func() { _ = proxy.Stop(ctx) }()

			proxy.setServerInitialized()

			// Health check runs every 10 seconds, wait for one cycle + buffer
			time.Sleep(11 * time.Second)

			// 4xx codes should NOT trigger callback (they're considered healthy)
			assert.False(t, tracker.isInvoked(), "Callback should NOT be invoked for status code %d (4xx is healthy)", tc.statusCode)

			running, _ := proxy.IsRunning(ctx)
			assert.True(t, running, "Proxy should continue running for 4xx status codes")
		})
	}
}

// TestTransparentProxy_HealthCheckNotRunBeforeInitialization tests that health checks
// are skipped until the server is initialized
func TestTransparentProxy_HealthCheckNotRunBeforeInitialization(t *testing.T) {
	t.Parallel()

	tracker, callback := newCallbackTracker()

	// Create a failing server
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingServer.Close()

	proxy, ctx, cancel := setupRemoteProxyTest(t, failingServer.URL, callback)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	// Do NOT mark server as initialized
	// proxy.setServerInitialized() // Intentionally commented out

	// Wait for health check cycle (should be skipped since server is not initialized)
	time.Sleep(11 * time.Second)
	assert.False(t, tracker.isInvoked(), "Callback should NOT be invoked before server initialization")

	// Proxy should still be running
	running, _ := proxy.IsRunning(ctx)
	assert.True(t, running, "Proxy should continue running when server is not initialized")
}

// TestTransparentProxy_HealthCheckFailureWithNilCallback tests that proxy stops
// gracefully even when callback is nil
func TestTransparentProxy_HealthCheckFailureWithNilCallback(t *testing.T) {
	t.Parallel()

	// Create a failing server
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingServer.Close()

	proxy, ctx, cancel := setupRemoteProxyTest(t, failingServer.URL, nil) // nil callback
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	proxy.setServerInitialized()

	// Health check runs every 10 seconds, wait for one cycle + buffer
	time.Sleep(11 * time.Second)

	// Proxy should stop even without callback
	running, _ := proxy.IsRunning(ctx)
	assert.False(t, running, "Proxy should stop after health check failure even with nil callback")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, true, false, "sse", nil, nil, "", false, "/sse")
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
	proxyURL.ModifyResponse = proxy.modifyResponse

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
	proxy.ModifyResponse = p.modifyResponse
	return proxy
}

func TestNoSessionIDInNonSSE(t *testing.T) {
	t.Parallel()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "streamable-http", nil, nil, "", false, "/mcp")

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

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "streamable-http", nil, nil, "", false, "/mcp")

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
	proxy := NewTransparentProxy("localhost", 0, downstream.URL, nil, nil, false, false, "", nil, nil, "", false, "/")

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
	proxy := NewTransparentProxy("127.0.0.1", 0, "http://localhost:8080", nil, nil, false, false, "sse", nil, nil, "", false, "/sse")

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
	proxy := NewTransparentProxy("127.0.0.1", 0, "http://localhost:8080", nil, nil, false, false, "sse", nil, nil, "", false, "/sse")

	ctx := context.Background()

	// Stop should handle being called without Start
	err := proxy.Stop(ctx)
	// This may return an error or succeed depending on implementation
	// The key is it shouldn't panic
	_ = err
}

// TestTransparentProxy_UnauthorizedResponseCallback tests that 401 responses trigger the callback
func TestTransparentProxy_UnauthorizedResponseCallback(t *testing.T) {
	t.Parallel()

	callbackCalled := false
	var mu sync.Mutex
	callback := func() {
		mu.Lock()
		defer mu.Unlock()
		callbackCalled = true
	}

	// Create a test server that returns 401
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Unauthorized"}`))
	}))
	defer target.Close()

	// Parse target URL
	targetURL, err := url.Parse(target.URL)
	assert.NoError(t, err)

	// Create a proxy with unauthorized response callback and set targetURI
	proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, true, false, "streamable-http", nil, callback, "", false, "/mcp")

	// Verify callback is set
	assert.NotNil(t, proxy.onUnauthorizedResponse, "Callback should be set on proxy")

	// Create reverse proxy with tracing transport
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	tracingTrans := &tracingTransport{base: http.DefaultTransport, p: proxy}
	reverseProxy.Transport = tracingTrans

	// Make a request through the proxy
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	reverseProxy.ServeHTTP(rec, req)

	// Verify 401 was returned
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Verify callback was called
	mu.Lock()
	actualCalled := callbackCalled
	mu.Unlock()
	assert.True(t, actualCalled, "Unauthorized response callback should have been called")
}

func TestTransparentProxy_UnauthorizedResponseCallback_Multiple401s(t *testing.T) {
	t.Parallel()

	callbackCallCount := 0
	var mu sync.Mutex
	callback := func() {
		mu.Lock()
		defer mu.Unlock()
		callbackCallCount++
	}

	// Create a test server that returns 401
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Unauthorized"}`))
	}))
	defer target.Close()

	// Parse target URL
	targetURL, err := url.Parse(target.URL)
	assert.NoError(t, err)

	// Create a proxy with unauthorized response callback and set targetURI
	proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, true, false, "streamable-http", nil, callback, "", false, "/mcp")

	// Create reverse proxy with tracing transport
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	// Make multiple requests through the proxy
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", target.URL, nil)
		reverseProxy.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	}

	// Verify callback was called for each 401 response
	mu.Lock()
	actualCount := callbackCallCount
	mu.Unlock()
	assert.Equal(t, 5, actualCount, "Callback should be called for each 401 response")
}

func TestTransparentProxy_NoUnauthorizedCallbackOnSuccess(t *testing.T) {
	t.Parallel()

	callbackCalled := false
	var mu sync.Mutex
	callback := func() {
		mu.Lock()
		defer mu.Unlock()
		callbackCalled = true
	}

	// Create a test server that returns 200 OK
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	}))
	defer target.Close()

	// Parse target URL
	targetURL, err := url.Parse(target.URL)
	assert.NoError(t, err)

	// Create a proxy with unauthorized response callback and set targetURI
	proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, true, false, "streamable-http", nil, callback, "", false, "/mcp")

	// Create reverse proxy with tracing transport
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	// Make a request through the proxy
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	reverseProxy.ServeHTTP(rec, req)

	// Verify 200 was returned
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify callback was NOT called
	mu.Lock()
	actualCalled := callbackCalled
	mu.Unlock()
	assert.False(t, actualCalled, "Unauthorized response callback should NOT have been called for 200 OK")
}

func TestTransparentProxy_NilUnauthorizedCallback(t *testing.T) {
	t.Parallel()

	// Create a proxy with nil unauthorized response callback
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "streamable-http", nil, nil, "", false, "/mcp")

	// Create a test server that returns 401
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Unauthorized"}`))
	}))
	defer target.Close()

	// Parse target URL
	targetURL, err := url.Parse(target.URL)
	assert.NoError(t, err)

	// Create reverse proxy with tracing transport
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	// Make a request through the proxy - should not panic
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	reverseProxy.ServeHTTP(rec, req)

	// Verify 401 was returned
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestHealthCheckRetryConstants verifies the retry configuration constants
func TestHealthCheckRetryConstants(t *testing.T) {
	t.Parallel()

	// Verify retry count is reasonable (not too low, not too high)
	assert.GreaterOrEqual(t, healthCheckRetryCount, 2, "Should retry at least twice before giving up")
	assert.LessOrEqual(t, healthCheckRetryCount, 10, "Should not retry too many times")

	// Verify retry delay is reasonable
	assert.GreaterOrEqual(t, DefaultHealthCheckRetryDelay, 1*time.Second, "Retry delay should be at least 1 second")
	assert.LessOrEqual(t, DefaultHealthCheckRetryDelay, 30*time.Second, "Retry delay should not be too long")
}

// TestRewriteEndpointURL tests the rewriteEndpointURL function
func TestRewriteEndpointURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		originalURL string
		config      sseRewriteConfig
		expected    string
		expectError bool
	}{
		{
			name:        "no rewrite config",
			originalURL: "/sse?sessionId=abc123",
			config:      sseRewriteConfig{},
			expected:    "/sse?sessionId=abc123",
		},
		{
			name:        "prefix only - relative URL",
			originalURL: "/sse?sessionId=abc123",
			config:      sseRewriteConfig{prefix: "/playwright"},
			expected:    "/playwright/sse?sessionId=abc123",
		},
		{
			name:        "prefix without leading slash",
			originalURL: "/sse?sessionId=abc123",
			config:      sseRewriteConfig{prefix: "playwright"},
			expected:    "/playwright/sse?sessionId=abc123",
		},
		{
			name:        "prefix with trailing slash",
			originalURL: "/sse?sessionId=abc123",
			config:      sseRewriteConfig{prefix: "/playwright/"},
			expected:    "/playwright/sse?sessionId=abc123",
		},
		{
			name:        "prefix only - absolute URL",
			originalURL: "http://backend:8080/sse?sessionId=abc123",
			config:      sseRewriteConfig{prefix: "/playwright"},
			expected:    "http://backend:8080/playwright/sse?sessionId=abc123",
		},
		{
			name:        "full rewrite - absolute URL",
			originalURL: "http://backend:8080/sse?sessionId=abc123",
			config: sseRewriteConfig{
				prefix: "/playwright",
				scheme: "https",
				host:   "public.example.com",
			},
			expected: "https://public.example.com/playwright/sse?sessionId=abc123",
		},
		{
			name:        "scheme and host only - absolute URL",
			originalURL: "http://backend:8080/sse?sessionId=abc123",
			config: sseRewriteConfig{
				scheme: "https",
				host:   "public.example.com",
			},
			expected: "https://public.example.com/sse?sessionId=abc123",
		},
		{
			name:        "scheme and host only - relative URL becomes absolute",
			originalURL: "/sse?sessionId=abc123",
			config: sseRewriteConfig{
				scheme: "https",
				host:   "public.example.com",
			},
			expected: "https://public.example.com/sse?sessionId=abc123",
		},
		{
			name:        "scheme and host only - path-only URL remains relative",
			originalURL: "/sse?sessionId=abc123",
			config: sseRewriteConfig{
				scheme: "http",
			},
			expected: "/sse?sessionId=abc123",
		},
		{
			name:        "scheme and host only - path and prefix URL remains relative",
			originalURL: "/sse?sessionId=abc123",
			config: sseRewriteConfig{
				prefix: "/playwright",
				scheme: "http",
			},
			expected: "/playwright/sse?sessionId=abc123",
		},
		{
			name:        "preserves complex query string",
			originalURL: "/sse?sessionId=abc123&foo=bar&baz=qux",
			config:      sseRewriteConfig{prefix: "/api/v1"},
			expected:    "/api/v1/sse?sessionId=abc123&foo=bar&baz=qux",
		},
		{
			name:        "handles URL with fragment",
			originalURL: "/sse?sessionId=abc123#section",
			config:      sseRewriteConfig{prefix: "/playwright"},
			expected:    "/playwright/sse?sessionId=abc123#section",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := rewriteEndpointURL(tt.originalURL, tt.config)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestGetSSERewriteConfig tests the getSSERewriteConfig method
func TestGetSSERewriteConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		endpointPrefix    string
		trustProxyHeaders bool
		headers           map[string]string
		expectedPrefix    string
		expectedScheme    string
		expectedHost      string
	}{
		{
			name:              "no config, no headers",
			endpointPrefix:    "",
			trustProxyHeaders: false,
			headers:           nil,
			expectedPrefix:    "",
			expectedScheme:    "",
			expectedHost:      "",
		},
		{
			name:              "explicit endpoint prefix takes priority",
			endpointPrefix:    "/explicit",
			trustProxyHeaders: true,
			headers: map[string]string{
				"X-Forwarded-Prefix": "/from-header",
			},
			expectedPrefix: "/explicit",
			expectedScheme: "",
			expectedHost:   "",
		},
		{
			name:              "X-Forwarded-Prefix used when trust enabled and no explicit prefix",
			endpointPrefix:    "",
			trustProxyHeaders: true,
			headers: map[string]string{
				"X-Forwarded-Prefix": "/from-header",
			},
			expectedPrefix: "/from-header",
			expectedScheme: "",
			expectedHost:   "",
		},
		{
			name:              "X-Forwarded-Prefix ignored when trust disabled",
			endpointPrefix:    "",
			trustProxyHeaders: false,
			headers: map[string]string{
				"X-Forwarded-Prefix": "/from-header",
			},
			expectedPrefix: "",
			expectedScheme: "",
			expectedHost:   "",
		},
		{
			name:              "all X-Forwarded headers used when trust enabled",
			endpointPrefix:    "",
			trustProxyHeaders: true,
			headers: map[string]string{
				"X-Forwarded-Prefix": "/playwright",
				"X-Forwarded-Proto":  "https",
				"X-Forwarded-Host":   "public.example.com",
			},
			expectedPrefix: "/playwright",
			expectedScheme: "https",
			expectedHost:   "public.example.com",
		},
		{
			name:              "X-Forwarded-Proto and Host without Prefix",
			endpointPrefix:    "",
			trustProxyHeaders: true,
			headers: map[string]string{
				"X-Forwarded-Proto": "https",
				"X-Forwarded-Host":  "public.example.com",
			},
			expectedPrefix: "",
			expectedScheme: "https",
			expectedHost:   "public.example.com",
		},
		{
			name:              "explicit prefix with X-Forwarded-Proto and Host",
			endpointPrefix:    "/explicit",
			trustProxyHeaders: true,
			headers: map[string]string{
				"X-Forwarded-Prefix": "/ignored",
				"X-Forwarded-Proto":  "https",
				"X-Forwarded-Host":   "public.example.com",
			},
			expectedPrefix: "/explicit",
			expectedScheme: "https",
			expectedHost:   "public.example.com",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := NewTransparentProxy(
				"127.0.0.1", 0, "", nil, nil, false, false, "sse", nil, nil,
				tt.endpointPrefix, tt.trustProxyHeaders, "/sse",
			)

			req := httptest.NewRequest("GET", "/sse", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			// Access the SSE response processor to test configuration
			sseProcessor, ok := proxy.responseProcessor.(*SSEResponseProcessor)
			assert.True(t, ok, "expected SSE response processor")
			config := sseProcessor.getSSERewriteConfig(req)

			assert.Equal(t, tt.expectedPrefix, config.prefix)
			assert.Equal(t, tt.expectedScheme, config.scheme)
			assert.Equal(t, tt.expectedHost, config.host)
		})
	}
}

// TestSSEEndpointRewriting tests end-to-end SSE endpoint URL rewriting
func TestSSEEndpointRewriting(t *testing.T) {
	t.Parallel()

	// Create a proxy with X-Forwarded-Prefix trust enabled
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "sse", nil, nil, "", true, "/sse")

	// Create a mock SSE server that returns an endpoint event
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(200)

		// Send an endpoint event (this is what MCP servers send)
		w.Write([]byte("event: endpoint\n"))
		w.Write([]byte("data: /sse?sessionId=ABC123\n"))
		w.Write([]byte("\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()

	// Set up reverse proxy
	parsedURL, _ := http.NewRequest("GET", target.URL, nil)
	proxyURL := httputil.NewSingleHostReverseProxy(parsedURL.URL)
	proxyURL.FlushInterval = -1
	proxyURL.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}
	proxyURL.ModifyResponse = proxy.modifyResponse

	// Create request with X-Forwarded-Prefix header
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	req.Header.Set("X-Forwarded-Prefix", "/playwright")
	proxyURL.ServeHTTP(rec, req)

	// Read all SSE lines
	sc := bufio.NewScanner(rec.Body)
	var bodyLines []string
	for sc.Scan() {
		bodyLines = append(bodyLines, sc.Text())
	}

	// Verify the endpoint URL was rewritten
	assert.Contains(t, bodyLines, "data: /playwright/sse?sessionId=ABC123",
		"Endpoint URL should be rewritten with prefix")
	assert.Contains(t, bodyLines, "event: endpoint")

	// Session should still be tracked
	_, ok := proxy.sessionManager.Get("ABC123")
	assert.True(t, ok, "sessionManager should have stored ABC123")
}

// TestStripEndpointPrefix tests the stripEndpointPrefix function
func TestStripEndpointPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		path              string
		endpointPrefix    string
		mcpServerBasePath string
		expected          string
	}{
		// Basic stripping cases
		{
			name:              "basic strip - prefix at start",
			path:              "/abc/sse",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/sse",
		},
		{
			name:              "exact match",
			path:              "/abc",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/",
		},
		{
			name:              "prefix with trailing slash",
			path:              "/abc/sse",
			endpointPrefix:    "/abc/",
			mcpServerBasePath: "/sse",
			expected:          "/sse",
		},
		{
			name:              "prefix without leading slash",
			path:              "/abc/sse",
			endpointPrefix:    "abc",
			mcpServerBasePath: "/sse",
			expected:          "/sse",
		},
		{
			name:              "path with trailing slash",
			path:              "/abc/sse/",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/sse/",
		},
		// Edge case: prefix appears later in path
		{
			name:              "prefix appears later in path - strip only first",
			path:              "/abc/abc/sse",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/abc/sse",
		},
		{
			name:              "multiple prefix segments",
			path:              "/abc/abc/abc/sse",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/abc/abc/sse",
		},
		// Prefix not at start - should not strip
		{
			name:              "prefix not at start - no change",
			path:              "/sse/abc/abc",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/sse/abc/abc",
		},
		{
			name:              "prefix in middle of path",
			path:              "/sse/abc/test",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "/sse/abc/test",
		},
		// Prefix matches MCP server base path - special handling
		{
			name:              "prefix matches MCP server path - do not strip",
			path:              "/abc/sse",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/abc",
			expected:          "/abc/sse",
		},
		{
			name:              "prefix matches MCP server path - exact match",
			path:              "/abc",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/abc",
			expected:          "/abc",
		},
		{
			name:              "prefix matches MCP server path - normalized comparison",
			path:              "/abc/sse",
			endpointPrefix:    "/abc/",
			mcpServerBasePath: "/abc",
			expected:          "/abc/sse",
		},
		{
			name:              "prefix matches MCP server path - strip double prefix",
			path:              "/sse/sse/endpoint",
			endpointPrefix:    "/sse",
			mcpServerBasePath: "/sse",
			expected:          "/sse/endpoint",
		},
		{
			name:              "prefix matches MCP server path - strip double prefix with trailing",
			path:              "/sse/sse/",
			endpointPrefix:    "/sse",
			mcpServerBasePath: "/sse",
			expected:          "/sse/",
		},
		// Empty/edge cases
		{
			name:              "empty prefix - no change",
			path:              "/abc/sse",
			endpointPrefix:    "",
			mcpServerBasePath: "/sse",
			expected:          "/abc/sse",
		},
		{
			name:              "empty path - no change",
			path:              "",
			endpointPrefix:    "/abc",
			mcpServerBasePath: "/sse",
			expected:          "",
		},
		{
			name:              "root prefix - no change",
			path:              "/sse",
			endpointPrefix:    "/",
			mcpServerBasePath: "/sse",
			expected:          "/sse",
		},
		// Real-world examples
		{
			name:              "playwright prefix example",
			path:              "/playwright/sse",
			endpointPrefix:    "/playwright",
			mcpServerBasePath: "/sse",
			expected:          "/sse",
		},
		{
			name:              "playwright prefix with nested path",
			path:              "/playwright/playwright/sse",
			endpointPrefix:    "/playwright",
			mcpServerBasePath: "/sse",
			expected:          "/playwright/sse",
		},
		{
			name:              "playwright prefix matches remote server path - don't strip",
			path:              "/playwright/sse",
			endpointPrefix:    "/playwright",
			mcpServerBasePath: "/playwright",
			expected:          "/playwright/sse",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := stripEndpointPrefix(tt.path, tt.endpointPrefix, tt.mcpServerBasePath)
			assert.Equal(t, tt.expected, result, "stripEndpointPrefix(%q, %q, %q) = %q, want %q",
				tt.path, tt.endpointPrefix, tt.mcpServerBasePath, result, tt.expected)
		})
	}
}

// TestSSEEndpointRewritingWithExplicitPrefix tests SSE endpoint rewriting with explicit prefix
func TestSSEEndpointRewritingWithExplicitPrefix(t *testing.T) {
	t.Parallel()

	// Create a proxy with explicit endpoint prefix
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "sse", nil, nil, "/api/mcp", false, "/sse")

	// Create a mock SSE server
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(200)

		w.Write([]byte("event: endpoint\n"))
		w.Write([]byte("data: /sse?sessionId=DEF456\n"))
		w.Write([]byte("\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()

	// Set up reverse proxy
	parsedURL, _ := http.NewRequest("GET", target.URL, nil)
	proxyURL := httputil.NewSingleHostReverseProxy(parsedURL.URL)
	proxyURL.FlushInterval = -1
	proxyURL.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}
	proxyURL.ModifyResponse = proxy.modifyResponse

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	proxyURL.ServeHTTP(rec, req)

	// Read all SSE lines
	sc := bufio.NewScanner(rec.Body)
	var bodyLines []string
	for sc.Scan() {
		bodyLines = append(bodyLines, sc.Text())
	}

	// Verify the endpoint URL was rewritten with the explicit prefix
	assert.Contains(t, bodyLines, "data: /api/mcp/sse?sessionId=DEF456",
		"Endpoint URL should be rewritten with explicit prefix")
}

// TestPrefixStrippingInDirector tests that prefix is stripped from request path in Director
func TestPrefixStrippingInDirector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		endpointPrefix    string
		trustProxyHeaders bool
		requestPath       string
		forwardedPrefix   string
		expectedPath      string
		shouldStrip       bool
	}{
		{
			name:              "strip prefix when present",
			endpointPrefix:    "/abc",
			trustProxyHeaders: false,
			requestPath:       "/abc/sse",
			forwardedPrefix:   "",
			expectedPath:      "/sse",
			shouldStrip:       true,
		},
		{
			name:              "don't strip when ingress already stripped",
			endpointPrefix:    "/abc",
			trustProxyHeaders: true,
			requestPath:       "/sse",
			forwardedPrefix:   "/abc",
			expectedPath:      "/sse",
			shouldStrip:       false,
		},
		{
			name:              "strip prefix with nested path",
			endpointPrefix:    "/abc",
			trustProxyHeaders: false,
			requestPath:       "/abc/abc/sse",
			forwardedPrefix:   "",
			expectedPath:      "/abc/sse",
			shouldStrip:       true,
		},
		{
			name:              "don't strip when prefix not at start",
			endpointPrefix:    "/abc",
			trustProxyHeaders: false,
			requestPath:       "/sse/abc/test",
			forwardedPrefix:   "",
			expectedPath:      "/sse/abc/test",
			shouldStrip:       false,
		},
		{
			name:              "no prefix configured - no change",
			endpointPrefix:    "",
			trustProxyHeaders: false,
			requestPath:       "/abc/sse",
			forwardedPrefix:   "",
			expectedPath:      "/abc/sse",
			shouldStrip:       false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a backend server that records the path it receives
			var receivedPath string
			var pathMutex sync.Mutex
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pathMutex.Lock()
				receivedPath = r.URL.Path
				pathMutex.Unlock()
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			}))
			defer target.Close()

			// Create proxy with endpointPrefix
			proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, false, false, "sse", nil, nil, tt.endpointPrefix, tt.trustProxyHeaders, "")

			// Start the proxy
			ctx := context.Background()
			err := proxy.Start(ctx)
			require.NoError(t, err)
			defer func() {
				_ = proxy.Stop(ctx)
			}()

			// Wait a bit for proxy to start
			time.Sleep(50 * time.Millisecond)

			// Get the proxy URL (it will be on a random port)
			proxyAddr := proxy.listener.Addr().String()

			// Create request with path
			reqURL := "http://" + proxyAddr + tt.requestPath
			req, err := http.NewRequest("GET", reqURL, nil)
			require.NoError(t, err)

			// Set forwarded prefix header if needed
			if tt.forwardedPrefix != "" {
				req.Header.Set("X-Forwarded-Prefix", tt.forwardedPrefix)
			}

			// Make request through proxy
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Verify backend received the expected path
			pathMutex.Lock()
			actualPath := receivedPath
			pathMutex.Unlock()

			assert.Equal(t, tt.expectedPath, actualPath,
				"Backend should receive path %q, got %q (prefix stripping: %v)",
				tt.expectedPath, actualPath, tt.shouldStrip)
		})
	}
}

// TestSSEMessageEventNotRewritten tests that message events are not rewritten
func TestSSEMessageEventNotRewritten(t *testing.T) {
	t.Parallel()

	// Create a proxy with prefix configuration
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, false, false, "sse", nil, nil, "/playwright", false, "/sse")

	// Create a mock SSE server that sends both endpoint and message events
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(200)

		// Endpoint event - should be rewritten
		w.Write([]byte("event: endpoint\n"))
		w.Write([]byte("data: /sse?sessionId=ABC123\n"))
		w.Write([]byte("\n"))

		// Message event - should NOT be rewritten
		w.Write([]byte("event: message\n"))
		w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"tools/list\"}\n"))
		w.Write([]byte("\n"))
		w.(http.Flusher).Flush()
	}))
	defer target.Close()

	// Set up reverse proxy
	parsedURL, _ := http.NewRequest("GET", target.URL, nil)
	proxyURL := httputil.NewSingleHostReverseProxy(parsedURL.URL)
	proxyURL.FlushInterval = -1
	proxyURL.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}
	proxyURL.ModifyResponse = proxy.modifyResponse

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target.URL, nil)
	proxyURL.ServeHTTP(rec, req)

	// Read all SSE lines
	sc := bufio.NewScanner(rec.Body)
	var bodyLines []string
	for sc.Scan() {
		bodyLines = append(bodyLines, sc.Text())
	}

	// Endpoint event should be rewritten
	assert.Contains(t, bodyLines, "data: /playwright/sse?sessionId=ABC123",
		"Endpoint URL should be rewritten")

	// Message event should NOT be rewritten
	assert.Contains(t, bodyLines, "data: {\"jsonrpc\":\"2.0\",\"method\":\"tools/list\"}",
		"Message data should not be rewritten")
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
// Uses a 100ms health check interval, 50ms retry delay, and 100ms ping timeout for faster test execution
// With retry mechanism (3 consecutive ticker failures), shutdown occurs after ~200ms for instant failures (connection refused, 5xx) or ~700ms for timeouts
func setupRemoteProxyTest(t *testing.T, serverURL string, callback types.HealthCheckFailedCallback) (*TransparentProxy, context.Context, context.CancelFunc) {
	t.Helper()
	return setupRemoteProxyTestWithTimeout(t, serverURL, callback, 1*time.Second)
}

// setupRemoteProxyTestWithTimeout creates a proxy with a custom context timeout
func setupRemoteProxyTestWithTimeout(t *testing.T, serverURL string, callback types.HealthCheckFailedCallback, timeout time.Duration) (*TransparentProxy, context.Context, context.CancelFunc) {
	t.Helper()

	proxy := newTransparentProxyWithOptions(
		"127.0.0.1",
		0,
		serverURL,
		nil,
		nil,
		true, // enableHealthCheck
		true, // isRemote
		"sse",
		callback,
		nil,
		"",
		false,
		"",
		nil, // middlewares
		withHealthCheckInterval(100*time.Millisecond),    // Use 100ms for faster tests
		withHealthCheckRetryDelay(50*time.Millisecond),   // Use 50ms retry delay for faster tests
		withHealthCheckPingTimeout(100*time.Millisecond), // Use 100ms ping timeout for faster tests
	)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	err := proxy.Start(ctx)
	require.NoError(t, err)

	return proxy, ctx, cancel
}

// TestTransparentProxy_RemoteServerFailure_ConnectionRefused tests that connection
// failures (network-level, not HTTP status codes) trigger health check failure
func TestTransparentProxy_RemoteServerFailure_ConnectionRefused(t *testing.T) {
	t.Parallel()

	tracker, callback := newCallbackTracker()

	// Create a server, get its URL, then close it immediately
	// This simulates a server that was running but then stopped
	tempServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serverURL := tempServer.URL
	tempServer.Close() // Close immediately - connection will be refused

	proxy, ctx, cancel := setupRemoteProxyTest(t, serverURL, callback)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	proxy.setServerInitialized()

	// With retry mechanism (100ms ticker, 50ms retry delay) and instant connection failures:
	// The retry blocks the ticker case, so the next ticker fires after the retry completes.
	// - First ticker: T=0ms → fails instantly → consecutiveFailures=1 → retry timer (50ms)
	// - Retry: T=50ms → fails instantly → continue (consecutiveFailures stays 1)
	// - Second ticker: T=100ms (next interval) → fails instantly → consecutiveFailures=2 → retry timer (50ms)
	// - Retry: T=150ms → fails instantly → continue (consecutiveFailures stays 2)
	// - Third ticker: T=200ms (next interval) → fails instantly → consecutiveFailures=3 → shutdown
	// Total time: ~200ms for 3 consecutive ticker failures with instant failures
	time.Sleep(400 * time.Millisecond)

	assert.True(t, tracker.isInvoked(), "Callback should be invoked when connection is refused after 3 consecutive failures")

	running, _ := proxy.IsRunning(ctx)
	assert.False(t, running, "Proxy should stop after connection failure")
}

// TestTransparentProxy_RemoteServerFailure_Timeout tests that timeouts
// trigger health check failure
func TestTransparentProxy_RemoteServerFailure_Timeout(t *testing.T) {
	t.Parallel()

	tracker, callback := newCallbackTracker()

	// Create server that hangs (simulates timeout)
	// Note: With 100ms ping timeout and retry mechanism, we need 3 consecutive ticker failures
	// Each health check will timeout after 100ms, and with retries the total time is ~700ms
	// Use a channel to allow graceful shutdown
	serverDone := make(chan struct{})
	hangingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Sleep longer than all health checks combined (each takes 100ms to timeout)
		// Need to hang for at least 700ms to allow all 3 failures to complete
		// But use a select to allow cancellation
		select {
		case <-time.After(800 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-serverDone:
			return
		}
	}))
	defer func() {
		close(serverDone)
		hangingServer.Close()
	}()

	// With retry mechanism (100ms ticker, 50ms retry delay) and 100ms health check timeout:
	// The retry blocks the ticker case, so the next ticker fires after the retry completes.
	// - First ticker: T=0ms → timeout at T=100ms → fail → consecutiveFailures=1 → retry timer (50ms)
	// - Retry: T=150ms → timeout at T=250ms → fail → continue (consecutiveFailures stays 1)
	// - Second ticker: T=300ms (next interval after retry) → timeout at T=400ms → fail → consecutiveFailures=2 → retry timer (50ms)
	// - Retry: T=450ms → timeout at T=550ms → fail → continue (consecutiveFailures stays 2)
	// - Third ticker: T=600ms (next interval after retry) → timeout at T=700ms → fail → consecutiveFailures=3 → shutdown
	// Total time: ~700ms for 3 consecutive ticker failures with timeouts
	// Use 1 second timeout to allow for retry mechanism to complete with buffer (~700ms + margin)
	proxy, ctx, cancel := setupRemoteProxyTestWithTimeout(t, hangingServer.URL, callback, 1*time.Second)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	proxy.setServerInitialized()

	// Wait for shutdown to complete, using a retry loop to handle timing variations
	callbackInvoked := false
	proxyStopped := false
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		if tracker.isInvoked() {
			callbackInvoked = true
		}
		running, _ := proxy.IsRunning(ctx)
		if !running {
			proxyStopped = true
		}
		if callbackInvoked && proxyStopped {
			break
		}
	}

	assert.True(t, callbackInvoked, "Callback should be invoked on timeout after 3 consecutive failures")
	assert.True(t, proxyStopped, "Proxy should stop after timeout")
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
	time.Sleep(150 * time.Millisecond)
	assert.False(t, tracker.isInvoked(), "Callback should NOT be invoked while server is healthy")

	// Now close the server to simulate it becoming unavailable
	healthyServer.Close()

	// With retry mechanism (100ms ticker, 50ms retry delay) and instant connection failures:
	// Total time: ~200ms for 3 consecutive ticker failures with instant failures
	time.Sleep(400 * time.Millisecond)
	assert.True(t, tracker.isInvoked(), "Callback should be invoked after server becomes unavailable (3 consecutive failures)")

	running, _ := proxy.IsRunning(ctx)
	assert.False(t, running, "Proxy should stop after server becomes unavailable")
}

// TestTransparentProxy_RemoteServerStatusCodes tests various HTTP status codes
// and verifies that 5xx codes trigger failures while 4xx codes are considered healthy
func TestTransparentProxy_RemoteServerStatusCodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		statusCode     int
		expectCallback bool
		expectRunning  bool
		description    string
	}{
		// 5xx codes should trigger callback and stop proxy
		{
			name:           "500 Internal Server Error",
			statusCode:     http.StatusInternalServerError,
			expectCallback: true,
			expectRunning:  false,
			description:    "5xx codes should trigger callback",
		},
		{
			name:           "502 Bad Gateway",
			statusCode:     http.StatusBadGateway,
			expectCallback: true,
			expectRunning:  false,
			description:    "5xx codes should trigger callback",
		},
		{
			name:           "503 Service Unavailable",
			statusCode:     http.StatusServiceUnavailable,
			expectCallback: true,
			expectRunning:  false,
			description:    "5xx codes should trigger callback",
		},
		{
			name:           "504 Gateway Timeout",
			statusCode:     http.StatusGatewayTimeout,
			expectCallback: true,
			expectRunning:  false,
			description:    "5xx codes should trigger callback",
		},
		// 4xx codes should NOT trigger callback (considered healthy)
		{
			name:           "401 Unauthorized",
			statusCode:     http.StatusUnauthorized,
			expectCallback: false,
			expectRunning:  true,
			description:    "4xx codes should not trigger callback",
		},
		{
			name:           "403 Forbidden",
			statusCode:     http.StatusForbidden,
			expectCallback: false,
			expectRunning:  true,
			description:    "4xx codes should not trigger callback",
		},
		{
			name:           "404 Not Found",
			statusCode:     http.StatusNotFound,
			expectCallback: false,
			expectRunning:  true,
			description:    "4xx codes should not trigger callback",
		},
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

			if tc.expectCallback {
				// With retry mechanism (100ms ticker, 50ms retry delay):
				// Total time: ~200ms for instant failures (5xx status codes), ~700ms for timeouts
				time.Sleep(400 * time.Millisecond)
			} else {
				// For 4xx codes that should not trigger callback, wait for one health check cycle
				time.Sleep(150 * time.Millisecond)
			}

			assert.Equal(t, tc.expectCallback, tracker.isInvoked(), "%s: %s", tc.name, tc.description)

			running, _ := proxy.IsRunning(ctx)
			assert.Equal(t, tc.expectRunning, running, "%s: Proxy running state should match expectation", tc.name)
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

	// Do NOT mark server as initialized - health checks should be skipped

	// Wait for health check cycle (should be skipped since server is not initialized)
	time.Sleep(150 * time.Millisecond)
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

	// With retry mechanism (100ms ticker, 50ms retry delay) and instant failures (5xx status):
	// Total time: ~200ms for 3 consecutive ticker failures with instant failures
	time.Sleep(400 * time.Millisecond)

	// Proxy should stop even without callback
	running, _ := proxy.IsRunning(ctx)
	assert.False(t, running, "Proxy should stop after health check failure even with nil callback")
}

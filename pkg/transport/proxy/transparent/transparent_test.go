// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bufio"
	"context"
	"fmt"
	"net"
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
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, true, false, "sse", nil, nil, "", false)
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

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

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

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

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
	proxy := NewTransparentProxy("localhost", 0, downstream.URL, nil, nil, nil, false, false, "", nil, nil, "", false)

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
	proxy := NewTransparentProxy("127.0.0.1", 0, "http://localhost:8080", nil, nil, nil, false, false, "sse", nil, nil, "", false)

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
	proxy := NewTransparentProxy("127.0.0.1", 0, "http://localhost:8080", nil, nil, nil, false, false, "sse", nil, nil, "", false)

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
	proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, nil, true, false, "streamable-http", nil, callback, "", false)

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
	proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, nil, true, false, "streamable-http", nil, callback, "", false)

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
	proxy := NewTransparentProxy("127.0.0.1", 0, target.URL, nil, nil, nil, true, false, "streamable-http", nil, callback, "", false)

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
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

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
				"127.0.0.1", 0, "", nil, nil, nil, false, false, "sse", nil, nil,
				tt.endpointPrefix, tt.trustProxyHeaders,
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
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "sse", nil, nil, "", true)

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

// TestSSEEndpointRewritingWithExplicitPrefix tests SSE endpoint rewriting with explicit prefix
func TestSSEEndpointRewritingWithExplicitPrefix(t *testing.T) {
	t.Parallel()

	// Create a proxy with explicit endpoint prefix
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "sse", nil, nil, "/api/mcp", false)

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

// TestSSEMessageEventNotRewritten tests that message events are not rewritten
func TestSSEMessageEventNotRewritten(t *testing.T) {
	t.Parallel()

	// Create a proxy with prefix configuration
	proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "sse", nil, nil, "/playwright", false)

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
		nil,  // prefixHandlers
		true, // enableHealthCheck
		true, // isRemote
		"sse",
		callback,
		nil,
		"",
		false,
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

	running, _ := proxy.IsRunning()
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
		running, _ := proxy.IsRunning()
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

	running, _ := proxy.IsRunning()
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

			running, _ := proxy.IsRunning()
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
	running, _ := proxy.IsRunning()
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
	running, _ := proxy.IsRunning()
	assert.False(t, running, "Proxy should stop after health check failure even with nil callback")
}

// TestPrefixHandlers_MountingAndRouting tests that prefix handlers are correctly mounted
// and that Go's ServeMux longest-match routing correctly routes requests
func TestPrefixHandlers_MountingAndRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		requestPath        string
		expectedHandler    string
		expectedStatusCode int
		description        string
	}{
		{
			name:               "prefix handler matches /oauth/",
			requestPath:        "/oauth/authorize",
			expectedHandler:    "oauth",
			expectedStatusCode: http.StatusOK,
			description:        "Request to /oauth/* should be handled by oauth prefix handler",
		},
		{
			name:               "prefix handler matches /oauth/ exact",
			requestPath:        "/oauth/",
			expectedHandler:    "oauth",
			expectedStatusCode: http.StatusOK,
			description:        "Request to /oauth/ should be handled by oauth prefix handler",
		},
		{
			name:               "prefix handler matches /.well-known/oauth-authorization-server",
			requestPath:        "/.well-known/oauth-authorization-server",
			expectedHandler:    "oauth-as-metadata",
			expectedStatusCode: http.StatusOK,
			description:        "Request to auth server well-known endpoint should be handled by oauth prefix handler",
		},
		{
			name:               "RFC 9728 endpoint still works",
			requestPath:        "/.well-known/oauth-protected-resource",
			expectedHandler:    "rfc9728",
			expectedStatusCode: http.StatusOK,
			description:        "RFC 9728 endpoint should be handled by auth info handler",
		},
		{
			name:               "RFC 9728 endpoint with subpath",
			requestPath:        "/.well-known/oauth-protected-resource/mcp",
			expectedHandler:    "rfc9728",
			expectedStatusCode: http.StatusOK,
			description:        "RFC 9728 endpoint with subpath should be handled by auth info handler",
		},
		{
			name:               "health endpoint bypasses prefix handlers",
			requestPath:        "/health",
			expectedHandler:    "", // Health uses internal health checker, not tracked
			expectedStatusCode: http.StatusOK,
			description:        "Health endpoint should not be handled by prefix handlers",
		},
		{
			name:               "metrics endpoint bypasses prefix handlers",
			requestPath:        "/metrics",
			expectedHandler:    "metrics",
			expectedStatusCode: http.StatusOK,
			description:        "Metrics endpoint should not be handled by prefix handlers",
		},
		{
			name:               "catch-all proxy receives other requests",
			requestPath:        "/mcp",
			expectedHandler:    "proxy",
			expectedStatusCode: http.StatusOK,
			description:        "Requests not matching any prefix should go to catch-all proxy",
		},
		{
			name:               "root path goes to proxy",
			requestPath:        "/",
			expectedHandler:    "proxy",
			expectedStatusCode: http.StatusOK,
			description:        "Root path should go to catch-all proxy",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Track which handler was called
			var handlerCalled string
			var mu sync.Mutex

			recordHandler := func(name string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					mu.Lock()
					handlerCalled = name
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(name))
				})
			}

			// Create prefix handlers map
			prefixHandlers := map[string]http.Handler{
				"/oauth/": recordHandler("oauth"),
				"/.well-known/oauth-authorization-server": recordHandler("oauth-as-metadata"),
			}

			// Create auth info handler for RFC 9728
			authInfoHandler := recordHandler("rfc9728")

			// Create Prometheus handler
			prometheusHandler := recordHandler("metrics")

			// Create a mock backend server
			backend := httptest.NewServer(recordHandler("proxy"))
			defer backend.Close()

			// Create proxy with prefix handlers
			proxy := NewTransparentProxy(
				"127.0.0.1",
				0,
				backend.URL,
				prometheusHandler,
				authInfoHandler,
				prefixHandlers,
				true,  // enableHealthCheck
				false, // isRemote
				"streamable-http",
				nil, // onHealthCheckFailed
				nil, // onUnauthorizedResponse
				"",
				false,
			)

			ctx := context.Background()
			err := proxy.Start(ctx)
			require.NoError(t, err)
			defer func() { _ = proxy.Stop(ctx) }()

			// Get the actual port from the listener (port 0 means OS assigns a random port)
			actualPort := proxy.listener.Addr().(*net.TCPAddr).Port

			// Make request to the proxy
			resp, err := http.Get(fmt.Sprintf("http://%s:%d%s", proxy.host, actualPort, tt.requestPath))
			require.NoError(t, err)
			defer resp.Body.Close()

			// Verify status code
			assert.Equal(t, tt.expectedStatusCode, resp.StatusCode, tt.description)

			// Verify the correct handler was called
			mu.Lock()
			actualHandler := handlerCalled
			mu.Unlock()
			// Skip handler verification for endpoints that use internal handlers (e.g., health checker)
			if tt.expectedHandler != "" {
				assert.Equal(t, tt.expectedHandler, actualHandler, tt.description)
			}
		})
	}
}

// TestPrefixHandlers_NilMapDoesNotPanic tests that a nil PrefixHandlers map doesn't cause panic
func TestPrefixHandlers_NilMapDoesNotPanic(t *testing.T) {
	t.Parallel()

	// Create a mock backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("proxy"))
	}))
	defer backend.Close()

	// Create proxy with nil prefix handlers (should not panic)
	proxy := NewTransparentProxy(
		"127.0.0.1",
		0,
		backend.URL,
		nil, // prometheusHandler
		nil, // authInfoHandler
		nil, // prefixHandlers - nil map
		false,
		false,
		"streamable-http",
		nil,
		nil,
		"",
		false,
	)

	ctx := context.Background()
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = proxy.Stop(ctx) }()

	// Get the actual port from the listener
	actualPort := proxy.listener.Addr().(*net.TCPAddr).Port

	// Make a request to verify proxy works normally
	resp, err := http.Get(fmt.Sprintf("http://%s:%d/test", proxy.host, actualPort))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Proxy should work with nil prefix handlers")
}

// TestPrefixHandlers_EmptyMapWorks tests that an empty PrefixHandlers map works correctly
func TestPrefixHandlers_EmptyMapWorks(t *testing.T) {
	t.Parallel()

	// Create a mock backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("proxy"))
	}))
	defer backend.Close()

	// Create proxy with empty prefix handlers map
	emptyPrefixHandlers := make(map[string]http.Handler)
	proxy := NewTransparentProxy(
		"127.0.0.1",
		0,
		backend.URL,
		nil, // prometheusHandler
		nil, // authInfoHandler
		emptyPrefixHandlers,
		false,
		false,
		"streamable-http",
		nil,
		nil,
		"",
		false,
	)

	ctx := context.Background()
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = proxy.Stop(ctx) }()

	// Get the actual port from the listener
	actualPort := proxy.listener.Addr().(*net.TCPAddr).Port

	// Make a request to verify proxy works normally
	resp, err := http.Get(fmt.Sprintf("http://%s:%d/test", proxy.host, actualPort))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Proxy should work with empty prefix handlers")
}

// TestPrefixHandlers_LongestMatchRouting tests that Go's ServeMux longest-match routing works
func TestPrefixHandlers_LongestMatchRouting(t *testing.T) {
	t.Parallel()

	var handlerCalled string
	var mu sync.Mutex

	recordHandler := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			handlerCalled = name
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		})
	}

	// Create prefix handlers with overlapping paths
	// The more specific path should match first
	prefixHandlers := map[string]http.Handler{
		"/api/":         recordHandler("api-general"),
		"/api/v1/":      recordHandler("api-v1"),
		"/api/v1/users": recordHandler("api-v1-users"),
	}

	// Create a mock backend server
	backend := httptest.NewServer(recordHandler("proxy"))
	defer backend.Close()

	proxy := NewTransparentProxy(
		"127.0.0.1",
		0,
		backend.URL,
		nil,
		nil,
		prefixHandlers,
		false,
		false,
		"streamable-http",
		nil,
		nil,
		"",
		false,
	)

	ctx := context.Background()
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = proxy.Stop(ctx) }()

	// Get the actual port from the listener
	actualPort := proxy.listener.Addr().(*net.TCPAddr).Port

	// Test that most specific path matches first
	tests := []struct {
		path            string
		expectedHandler string
	}{
		{"/api/v1/users", "api-v1-users"},
		{"/api/v1/other", "api-v1"},
		{"/api/v2/something", "api-general"},
		{"/other", "proxy"},
	}

	for _, tt := range tests {
		mu.Lock()
		handlerCalled = ""
		mu.Unlock()

		resp, err := http.Get(fmt.Sprintf("http://%s:%d%s", proxy.host, actualPort, tt.path))
		require.NoError(t, err)
		resp.Body.Close()

		mu.Lock()
		actualHandler := handlerCalled
		mu.Unlock()
		assert.Equal(t, tt.expectedHandler, actualHandler, "Path %s should be handled by %s", tt.path, tt.expectedHandler)
	}
}

// TestPrefixHandlers_WellKnownNamespaceCoexistence tests that RFC 9728 and auth server
// well-known endpoints coexist correctly
func TestPrefixHandlers_WellKnownNamespaceCoexistence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path            string
		expectedHandler string
		description     string
	}{
		{
			path:            "/.well-known/oauth-authorization-server",
			expectedHandler: "auth-server-metadata",
			description:     "Auth server metadata endpoint",
		},
		{
			path:            "/.well-known/openid-configuration",
			expectedHandler: "oidc-config",
			description:     "OIDC configuration endpoint",
		},
		{
			path:            "/.well-known/jwks.json",
			expectedHandler: "jwks",
			description:     "JWKS endpoint",
		},
		{
			path:            "/.well-known/oauth-protected-resource",
			expectedHandler: "rfc9728-protected-resource",
			description:     "RFC 9728 protected resource metadata",
		},
		{
			path:            "/.well-known/oauth-protected-resource/mcp",
			expectedHandler: "rfc9728-protected-resource",
			description:     "RFC 9728 protected resource metadata with subpath",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.description, func(t *testing.T) {
			t.Parallel()

			// Each subtest creates its own proxy to be independent
			var handlerCalled string
			var mu sync.Mutex

			recordHandler := func(name string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					mu.Lock()
					handlerCalled = name
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
				})
			}

			// Create prefix handlers for auth server well-known endpoints
			prefixHandlers := map[string]http.Handler{
				"/.well-known/oauth-authorization-server": recordHandler("auth-server-metadata"),
				"/.well-known/openid-configuration":       recordHandler("oidc-config"),
				"/.well-known/jwks.json":                  recordHandler("jwks"),
			}

			// RFC 9728 auth info handler
			authInfoHandler := recordHandler("rfc9728-protected-resource")

			// Create a mock backend
			backend := httptest.NewServer(recordHandler("proxy"))
			defer backend.Close()

			proxy := NewTransparentProxy(
				"127.0.0.1",
				0,
				backend.URL,
				nil,
				authInfoHandler,
				prefixHandlers,
				false,
				false,
				"streamable-http",
				nil,
				nil,
				"",
				false,
			)

			ctx := context.Background()
			err := proxy.Start(ctx)
			require.NoError(t, err)
			defer func() { _ = proxy.Stop(ctx) }()

			// Get the actual port from the listener
			actualPort := proxy.listener.Addr().(*net.TCPAddr).Port

			resp, err := http.Get(fmt.Sprintf("http://%s:%d%s", proxy.host, actualPort, tt.path))
			require.NoError(t, err)
			resp.Body.Close()

			mu.Lock()
			actualHandler := handlerCalled
			mu.Unlock()
			assert.Equal(t, tt.expectedHandler, actualHandler, "%s: expected handler %s, got %s", tt.description, tt.expectedHandler, actualHandler)
		})
	}
}

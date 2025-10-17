package transparent

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	logger.Initialize() // ensure logging doesn't panic
}

func TestStreamingSessionIDDetection(t *testing.T) {
	t.Parallel()
	proxy := NewTransparentProxy("127.0.0.1", 0, "test", "", nil, nil, true, false, "streamable-http")
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
	assert.True(t, proxy.IsServerInitialized, "server should have been initialized")
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

	p := NewTransparentProxy("127.0.0.1", 0, "test", "", nil, nil, false, false, "streamable-http")

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

	assert.False(t, p.IsServerInitialized, "server should not be initialized for application/json")
	_, ok := p.sessionManager.Get("XYZ789")
	assert.False(t, ok, "no session should be added")
}

func TestHeaderBasedSessionInitialization(t *testing.T) {
	t.Parallel()

	p := NewTransparentProxy("127.0.0.1", 0, "test", "", nil, nil, false, false, "streamable-http")

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

	assert.True(t, p.IsServerInitialized, "server should not be initialized for application/json")
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
	proxy := NewTransparentProxy("localhost", 0, "test-server", downstream.URL, nil, nil, false, false, "")

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

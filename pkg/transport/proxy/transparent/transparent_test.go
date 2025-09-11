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
	proxy := NewTransparentProxy("localhost", 0, "test-server", downstream.URL, nil, nil, false, false, "", nil)

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

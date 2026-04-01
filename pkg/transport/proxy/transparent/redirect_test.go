// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedirectFollowing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		redirectStatus int
		method         string
		body           string
		wantBody       string
		wantMethod     string
	}{
		{
			name:           "308 redirect preserves POST and body",
			redirectStatus: http.StatusPermanentRedirect,
			method:         http.MethodPost,
			body:           `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			wantBody:       `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			wantMethod:     http.MethodPost,
		},
		{
			name:           "307 redirect preserves POST and body",
			redirectStatus: http.StatusTemporaryRedirect,
			method:         http.MethodPost,
			body:           `{"jsonrpc":"2.0","method":"initialize","id":1}`,
			wantBody:       `{"jsonrpc":"2.0","method":"initialize","id":1}`,
			wantMethod:     http.MethodPost,
		},
		{
			name:           "301 redirect preserves POST method",
			redirectStatus: http.StatusMovedPermanently,
			method:         http.MethodPost,
			body:           `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			wantBody:       `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			wantMethod:     http.MethodPost,
		},
		{
			name:           "302 redirect preserves POST method",
			redirectStatus: http.StatusFound,
			method:         http.MethodPost,
			body:           `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			wantBody:       `{"jsonrpc":"2.0","method":"tools/list","id":1}`,
			wantMethod:     http.MethodPost,
		},
		{
			name:           "GET redirect preserves method",
			redirectStatus: http.StatusPermanentRedirect,
			method:         http.MethodGet,
			wantMethod:     http.MethodGet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var receivedMethod atomic.Value
			var receivedBody atomic.Value

			// Single server with a mux: /redirect returns the configured
			// status code, /final records the request and returns 200.
			// Both paths share the same host:port so the same-host check passes.
			mux := http.NewServeMux()
			mux.HandleFunc("/redirect", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/final")
				w.WriteHeader(tt.redirectStatus)
			})
			mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
				receivedMethod.Store(r.Method)
				b, _ := io.ReadAll(r.Body)
				receivedBody.Store(string(b))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{},"id":1}`))
			})

			server := httptest.NewServer(mux)
			defer server.Close()

			p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

			targetURL, err := url.Parse(server.URL)
			require.NoError(t, err)
			proxy := createBasicProxy(p, targetURL)

			var reqBody io.Reader
			if tt.body != "" {
				reqBody = strings.NewReader(tt.body)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, server.URL+"/redirect", reqBody)
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			proxy.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code, "expected 200 after following redirect")
			assert.Equal(t, tt.wantMethod, receivedMethod.Load(), "HTTP method was not preserved")
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, receivedBody.Load(), "request body was not preserved")
			}
		})
	}
}

func TestRedirectLoopStopsAtMax(t *testing.T) {
	t.Parallel()

	var hitCount atomic.Int32

	// Backend always redirects to itself (same host).
	looper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Location", r.URL.String())
		w.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer looper.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(looper.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, looper.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	// The initial request plus maxRedirects follow-up attempts.
	assert.Equal(t, int32(maxRedirects+1), hitCount.Load(),
		"expected exactly maxRedirects+1 requests to the looping backend")
	assert.Equal(t, http.StatusPermanentRedirect, rec.Code,
		"should return the last redirect response when limit is reached")
}

func TestRedirectChainMultipleHops(t *testing.T) {
	t.Parallel()

	// Single server: /hop-a → /hop-b → /final (all same host).
	mux := http.NewServeMux()
	mux.HandleFunc("/hop-a", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/hop-b")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	mux.HandleFunc("/hop-b", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"tools":[]},"id":1}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, server.URL+"/hop-a",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"tools"`)
}

func TestRedirectMissingLocationHeader(t *testing.T) {
	t.Parallel()

	// Backend returns 308 without a Location header.
	noLocation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer noLocation.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(noLocation.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, noLocation.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusPermanentRedirect, rec.Code,
		"should return the 3xx response as-is when Location header is absent")
}

func TestRedirectRelativeLocation(t *testing.T) {
	t.Parallel()

	var receivedPath atomic.Value

	// Mux-based server where /old redirects to /new and /new returns 200.
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/new")
		w.WriteHeader(http.StatusPermanentRedirect)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		receivedPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{},"id":1}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, server.URL+"/old", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/new", receivedPath.Load(), "relative Location should resolve correctly")
}

func TestRedirectCrossHostBlocked(t *testing.T) {
	t.Parallel()

	// A different-host server that should never receive a request.
	var crossHostHit atomic.Bool
	crossHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		crossHostHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer crossHost.Close()

	// Origin server redirects to the cross-host server.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", crossHost.URL+"/secret")
		w.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer origin.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(origin.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, origin.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusPermanentRedirect, rec.Code,
		"cross-host redirect should be returned as-is, not followed")
	assert.False(t, crossHostHit.Load(),
		"cross-host server should never receive a request")
}

func TestNonRedirectPassesThrough(t *testing.T) {
	t.Parallel()

	// Backend returns 200 directly.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{},"id":1}`))
	}))
	defer backend.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, backend.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	req.Header.Set("Content-Type", "application/json")
	proxy.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"result"`)
}

// TestFollowRedirectsDirect tests followRedirects with a mock forward function,
// without going through the full proxy pipeline.
func TestFollowRedirectsDirect(t *testing.T) {
	t.Parallel()

	t.Run("follows same-host redirect", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		mockForward := func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return &http.Response{
					StatusCode: http.StatusPermanentRedirect,
					Header:     http.Header{"Location": {"/new-path"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "http://example.com/old-path",
			strings.NewReader(`{"body":true}`))
		resp, err := followRedirects(mockForward, req, []byte(`{"body":true}`))

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, 2, callCount)
	})

	t.Run("blocks cross-host redirect", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		mockForward := func(req *http.Request) (*http.Response, error) {
			callCount++
			return &http.Response{
				StatusCode: http.StatusPermanentRedirect,
				Header:     http.Header{"Location": {"http://evil.com/steal"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp",
			strings.NewReader(`{}`))
		resp, err := followRedirects(mockForward, req, []byte(`{}`))

		require.NoError(t, err)
		assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode,
			"cross-host redirect should be returned as-is")
		assert.Equal(t, 1, callCount, "should not follow the redirect")
	})

	t.Run("blocks HTTPS to HTTP downgrade", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		mockForward := func(req *http.Request) (*http.Response, error) {
			callCount++
			return &http.Response{
				StatusCode: http.StatusPermanentRedirect,
				Header:     http.Header{"Location": {"http://example.com/mcp"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "https://example.com/mcp",
			strings.NewReader(`{}`))
		resp, err := followRedirects(mockForward, req, []byte(`{}`))

		require.NoError(t, err)
		assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode,
			"HTTPS-to-HTTP downgrade should be returned as-is")
		assert.Equal(t, 1, callCount, "should not follow the redirect")
	})

	t.Run("preserves body across redirect", func(t *testing.T) {
		t.Parallel()
		var secondBody string
		callCount := 0
		mockForward := func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return &http.Response{
					StatusCode: http.StatusTemporaryRedirect,
					Header:     http.Header{"Location": {"/target"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			}
			b, _ := io.ReadAll(req.Body)
			secondBody = string(b)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Request:    req,
			}, nil
		}

		body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
		req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp",
			strings.NewReader(body))
		resp, err := followRedirects(mockForward, req, []byte(body))

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, body, secondBody, "body should be replayed from buffered bytes")
	})

	t.Run("stops at max redirects", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		mockForward := func(req *http.Request) (*http.Response, error) {
			callCount++
			return &http.Response{
				StatusCode: http.StatusPermanentRedirect,
				Header:     http.Header{"Location": {"/loop"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp",
			strings.NewReader(`{}`))
		resp, err := followRedirects(mockForward, req, []byte(`{}`))

		require.NoError(t, err)
		assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
		assert.Equal(t, maxRedirects+1, callCount)
	})

	t.Run("passes through non-redirect response", func(t *testing.T) {
		t.Parallel()
		mockForward := func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp",
			strings.NewReader(`{}`))
		resp, err := followRedirects(mockForward, req, []byte(`{}`))

		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

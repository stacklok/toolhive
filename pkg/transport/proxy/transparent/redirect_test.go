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

			// Final backend returns a valid response and records what it received.
			final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedMethod.Store(r.Method)
				b, _ := io.ReadAll(r.Body)
				receivedBody.Store(string(b))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{},"id":1}`))
			}))
			defer final.Close()

			// Redirecting backend returns the configured status with a Location header.
			redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", final.URL)
				w.WriteHeader(tt.redirectStatus)
			}))
			defer redirector.Close()

			p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

			targetURL, err := url.Parse(redirector.URL)
			require.NoError(t, err)
			proxy := createBasicProxy(p, targetURL)

			var reqBody io.Reader
			if tt.body != "" {
				reqBody = strings.NewReader(tt.body)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, redirector.URL+"/mcp", reqBody)
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

	// Backend always redirects to itself.
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

	// Final backend returns 200.
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"tools":[]},"id":1}`))
	}))
	defer final.Close()

	// Hop B redirects to final.
	hopB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", final.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer hopB.Close()

	// Hop A redirects to hop B.
	hopA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", hopB.URL)
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer hopA.Close()

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	targetURL, err := url.Parse(hopA.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, hopA.URL+"/mcp",
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

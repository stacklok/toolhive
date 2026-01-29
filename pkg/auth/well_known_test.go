// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWellKnownHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		authInfoHandler http.Handler
		expectedNil     bool
		testRequests    []testRequest
	}{
		{
			name:            "nil authInfoHandler returns nil",
			authInfoHandler: nil,
			expectedNil:     true,
		},
		{
			name: "exact path /.well-known/oauth-protected-resource routes to authInfoHandler",
			authInfoHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("auth-info"))
			}),
			expectedNil: false,
			testRequests: []testRequest{
				{
					path:           "/.well-known/oauth-protected-resource",
					expectedStatus: http.StatusOK,
					expectedBody:   "auth-info",
				},
			},
		},
		{
			name: "subpath /.well-known/oauth-protected-resource/mcp routes to authInfoHandler",
			authInfoHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("auth-info-mcp"))
			}),
			expectedNil: false,
			testRequests: []testRequest{
				{
					path:           "/.well-known/oauth-protected-resource/mcp",
					expectedStatus: http.StatusOK,
					expectedBody:   "auth-info-mcp",
				},
			},
		},
		{
			name: "subpath /.well-known/oauth-protected-resource/v1/metadata routes to authInfoHandler",
			authInfoHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("auth-info-v1"))
			}),
			expectedNil: false,
			testRequests: []testRequest{
				{
					path:           "/.well-known/oauth-protected-resource/v1/metadata",
					expectedStatus: http.StatusOK,
					expectedBody:   "auth-info-v1",
				},
			},
		},
		{
			name: "other .well-known paths return 404",
			authInfoHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("should-not-reach"))
			}),
			expectedNil: false,
			testRequests: []testRequest{
				{
					path:           "/.well-known/openid-configuration",
					expectedStatus: http.StatusNotFound,
					expectedBody:   "404 page not found\n",
				},
				{
					path:           "/.well-known/other",
					expectedStatus: http.StatusNotFound,
					expectedBody:   "404 page not found\n",
				},
			},
		},
		{
			name: "RFC 9728 compliance - all oauth-protected-resource paths work",
			authInfoHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("discovered"))
			}),
			expectedNil: false,
			testRequests: []testRequest{
				{
					path:           "/.well-known/oauth-protected-resource",
					expectedStatus: http.StatusOK,
					expectedBody:   "discovered",
				},
				{
					path:           "/.well-known/oauth-protected-resource/",
					expectedStatus: http.StatusOK,
					expectedBody:   "discovered",
				},
				{
					path:           "/.well-known/oauth-protected-resource/any/deep/path",
					expectedStatus: http.StatusOK,
					expectedBody:   "discovered",
				},
			},
		},
		{
			name: "handler preserves request context and headers",
			authInfoHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request is passed through correctly
				if r.Header.Get("X-Test-Header") == "test-value" {
					w.Header().Set("X-Response-Header", "response-value")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("headers-ok"))
				} else {
					w.WriteHeader(http.StatusBadRequest)
				}
			}),
			expectedNil: false,
			testRequests: []testRequest{
				{
					path:            "/.well-known/oauth-protected-resource",
					headers:         map[string]string{"X-Test-Header": "test-value"},
					expectedStatus:  http.StatusOK,
					expectedBody:    "headers-ok",
					expectedHeaders: map[string]string{"X-Response-Header": "response-value"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewWellKnownHandler(tt.authInfoHandler)

			if tt.expectedNil {
				assert.Nil(t, handler, "expected nil handler")
				return
			}

			require.NotNil(t, handler, "expected non-nil handler")

			// Test each request scenario
			for _, req := range tt.testRequests {
				t.Run(req.path, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodGet, req.path, nil)

					// Add test headers
					for key, value := range req.headers {
						request.Header.Set(key, value)
					}

					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, request)

					assert.Equal(t, req.expectedStatus, recorder.Code, "status code mismatch")
					assert.Equal(t, req.expectedBody, recorder.Body.String(), "body mismatch")

					// Check expected response headers
					for key, value := range req.expectedHeaders {
						assert.Equal(t, value, recorder.Header().Get(key), "header %s mismatch", key)
					}
				})
			}
		})
	}
}

func TestWellKnownHandler_HTTPMethods(t *testing.T) {
	t.Parallel()

	authInfoHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Echo back the HTTP method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(req.Method))
	})

	handler := NewWellKnownHandler(authInfoHandler)
	require.NotNil(t, handler)

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
		http.MethodOptions,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(method, "/.well-known/oauth-protected-resource", nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, method, recorder.Body.String())
		})
	}
}

func TestWellKnownHandler_EdgeCases(t *testing.T) {
	t.Parallel()

	authInfoHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := NewWellKnownHandler(authInfoHandler)
	require.NotNil(t, handler)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "path with query parameters routes correctly",
			path:           "/.well-known/oauth-protected-resource?format=json",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "path with trailing slash routes correctly",
			path:           "/.well-known/oauth-protected-resource/",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "different .well-known path returns 404",
			path:           "/.well-known/jwks.json", // Different endpoint
			expectedStatus: http.StatusNotFound,
			expectedBody:   "404 page not found\n",
		},
		{
			name:           "path prefix match is not sufficient",
			path:           "/.well-known/oauth", // Prefix but not full path
			expectedStatus: http.StatusNotFound,
			expectedBody:   "404 page not found\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assert.Equal(t, tt.expectedStatus, recorder.Code)
			assert.Equal(t, tt.expectedBody, recorder.Body.String())
		})
	}
}

// testRequest defines a test request scenario
type testRequest struct {
	path            string
	headers         map[string]string
	expectedStatus  int
	expectedBody    string
	expectedHeaders map[string]string
}

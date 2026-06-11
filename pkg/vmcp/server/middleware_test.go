// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestApplyRateLimitingWrapsConfiguredMiddleware(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{
		RateLimitMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Rate-Limit-Test", "wrapped")
				next.ServeHTTP(w, r)
			})
		},
	}}
	handler := s.applyRateLimiting(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "wrapped", rec.Header().Get("X-Rate-Limit-Test"))
}

func TestApplyRateLimitingPassesThroughWhenDisabled(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{}}
	handler := s.applyRateLimiting(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
}

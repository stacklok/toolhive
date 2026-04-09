// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp"
)

// dummyLimiter is a test double for the Limiter interface.
type dummyLimiter struct {
	decision *Decision
	err      error
}

func (d *dummyLimiter) Allow(context.Context, string, string) (*Decision, error) {
	return d.decision, d.err
}

// withParsedMCPRequest adds a ParsedMCPRequest to the request context.
func withParsedMCPRequest(r *http.Request, method, resourceID string, id any) *http.Request {
	parsed := &mcp.ParsedMCPRequest{
		Method:     method,
		ResourceID: resourceID,
		ID:         id,
		IsRequest:  true,
	}
	ctx := context.WithValue(r.Context(), mcp.MCPRequestContextKey, parsed)
	return r.WithContext(ctx)
}

func TestRateLimitHandler_ToolCallAllowed(t *testing.T) {
	t.Parallel()

	limiter := &dummyLimiter{decision: &Decision{Allowed: true}}
	handler := rateLimitHandler(limiter)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", "echo", 1)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRateLimitHandler_ToolCallRejected(t *testing.T) {
	t.Parallel()

	limiter := &dummyLimiter{decision: &Decision{Allowed: false, RetryAfter: 5 * time.Second}}
	handler := rateLimitHandler(limiter)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler should not be called when rate limited")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", "echo", 42)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "5", w.Header().Get("Retry-After"))
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	body, err := io.ReadAll(w.Body)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, float64(-32029), errObj["code"])
	assert.Equal(t, "Rate limit exceeded", errObj["message"])
	data := errObj["data"].(map[string]any)
	assert.Equal(t, float64(5), data["retryAfterSeconds"])
	assert.Equal(t, float64(42), resp["id"])
}

func TestRateLimitHandler_RedisErrorFailOpen(t *testing.T) {
	t.Parallel()

	limiter := &dummyLimiter{err: errors.New("redis connection refused")}
	nextCalled := false
	handler := rateLimitHandler(limiter)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", "echo", 1)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, nextCalled, "should fail open and call next handler")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRateLimitHandler_NoParsedMCPRequest_PassesThrough(t *testing.T) {
	t.Parallel()

	limiter := &dummyLimiter{decision: &Decision{Allowed: false}}
	nextCalled := false
	handler := rateLimitHandler(limiter)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	// No MCP context — non-JSON-RPC request (health check, SSE stream).
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, nextCalled, "should pass through when no MCP context")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRateLimitHandler_NonToolCallPassesThrough(t *testing.T) {
	t.Parallel()

	limiter := &dummyLimiter{decision: &Decision{Allowed: false, RetryAfter: time.Second}}
	nextCalled := false
	handler := rateLimitHandler(limiter)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/list", "", 1)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, nextCalled, "non-tools/call should pass through regardless of limiter")
	assert.Equal(t, http.StatusOK, w.Code)
}

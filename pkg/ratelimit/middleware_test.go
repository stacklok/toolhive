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

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	transportmocks "github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// dummyLimiter is a test double for the Limiter interface.
type dummyLimiter struct {
	decision *Decision
	err      error
}

func (d *dummyLimiter) Allow(context.Context, string, string) (*Decision, error) {
	return d.decision, d.err
}

// recordingLimiter captures the arguments passed to Allow.
type recordingLimiter struct {
	toolName string
	userID   string
}

func (r *recordingLimiter) Allow(_ context.Context, toolName, userID string) (*Decision, error) {
	r.toolName = toolName
	r.userID = userID
	return &Decision{Allowed: true}, nil
}

// withIdentity adds an auth.Identity with the given subject to the request context.
func withIdentity(r *http.Request, subject string) *http.Request {
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: subject}}
	ctx := auth.WithIdentity(r.Context(), identity)
	return r.WithContext(ctx)
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
	assert.Equal(t, float64(429), errObj["code"])
	assert.Equal(t, "Rate limit exceeded", errObj["message"])
	data := errObj["data"].(map[string]any)
	assert.Equal(t, float64(5), data["retryAfterSeconds"])
	assert.Equal(t, float64(42), resp["id"])
}

// TestRateLimitHandler_NotificationRejected is the live regression case:
// rateLimitHandler has no notification guard (unlike dispatchModern), so a
// well-formed tools/call notification (no id) that gets rate-limited reaches
// writeRateLimited with a nil requestID. Before the fix this emitted
// "id":null at HTTP 429; it must now omit the key entirely.
func TestRateLimitHandler_NotificationRejected(t *testing.T) {
	t.Parallel()

	limiter := &dummyLimiter{decision: &Decision{Allowed: false, RetryAfter: 5 * time.Second}}
	handler := rateLimitHandler(limiter)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler should not be called when rate limited")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", "echo", nil) // no id: a notification
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	body, err := io.ReadAll(w.Body)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	_, hasID := resp["id"]
	assert.False(t, hasID, `"id" key must be absent for a notification, never "id":null`)
}

// TestRateLimitedBody covers the "id" key's presence/absence per
// session.HasJSONRPCID: MCP narrows base JSON-RPC 2.0 so an absent id is
// encoded by omitting the key entirely, never as "id":null (schema/2025-11-25
// types the error response id as optional string|number).
func TestRateLimitedBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requestID  any
		wantIDKey  bool
		wantIDEcho string // substring the body must contain, verbatim, when wantIDKey
	}{
		{name: "nil id is omitted", requestID: nil, wantIDKey: false},
		{name: "null RawMessage id is omitted", requestID: json.RawMessage("null"), wantIDKey: false},
		{name: "nil RawMessage id is omitted", requestID: json.RawMessage(nil), wantIDKey: false},
		// int64 matches the real wire type: parsed.ID comes from jsonrpc2.ID.Raw(),
		// which yields nil | int64 | string, never float64.
		{name: "zero id is still present", requestID: int64(0), wantIDKey: true, wantIDEcho: `"id":0`},
		{name: "present id is present", requestID: int64(42), wantIDKey: true, wantIDEcho: `"id":42`},
		{name: "string id is present", requestID: "req-1", wantIDKey: true, wantIDEcho: `"id":"req-1"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := rateLimitedBody(tt.requestID, 5*time.Second)

			// Decoding into map[string]any (rather than a tagged struct) is
			// the only way to tell an absent "id" key apart from a present
			// null: a struct field would read as the zero value either way.
			var asMap map[string]any
			require.NoError(t, json.Unmarshal(body, &asMap))
			_, hasID := asMap["id"]
			assert.Equal(t, tt.wantIDKey, hasID, `"id" key presence`)
			if tt.wantIDKey {
				// A map decode would coerce a numeric id to float64, losing
				// the verbatim-echo guarantee -- so check the raw wire bytes
				// instead.
				assert.Contains(t, string(body), tt.wantIDEcho)
			}
		})
	}
}

// TestRateLimitedBodyMarshalFallback verifies the defensive fallback: if the
// response fails to marshal (unreachable in production, where requestID is
// always a simple value), rateLimitedBody still returns a valid JSON-RPC
// error with no "id" key (never "id":null) and the retryAfterSeconds value
// correctly interpolated. A channel is not JSON-marshalable and, being
// non-nil, satisfies session.HasJSONRPCID -- forcing the map's marshal to
// fail after "id" was already added.
func TestRateLimitedBodyMarshalFallback(t *testing.T) {
	t.Parallel()

	const want = `{"jsonrpc":"2.0","error":{"code":429,"message":"Rate limit exceeded","data":{"retryAfterSeconds":5}}}`
	got := rateLimitedBody(make(chan int), 5*time.Second)
	assert.Equal(t, want, string(got))
	assert.NotContains(t, string(got), `"id"`)
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

func TestRateLimitHandler_PassesUserID(t *testing.T) {
	t.Parallel()

	recorder := &recordingLimiter{}
	handler := rateLimitHandler(recorder)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", "echo", 1)
	req = withIdentity(req, "alice@example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "echo", recorder.toolName)
	assert.Equal(t, "alice@example.com", recorder.userID)
}

func TestRateLimitHandler_NoIdentityPassesEmptyUserID(t *testing.T) {
	t.Parallel()

	recorder := &recordingLimiter{}
	handler := rateLimitHandler(recorder)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", "echo", 1)
	// No identity in context — unauthenticated request.
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "echo", recorder.toolName)
	assert.Empty(t, recorder.userID, "unauthenticated requests should pass empty userID")
}

func TestRateLimitMiddlewareHandlerReturnsConfiguredHandler(t *testing.T) {
	t.Parallel()

	expected := rateLimitHandler(&dummyLimiter{decision: &Decision{Allowed: true}})
	mw := &rateLimitMiddleware{handler: expected}

	assert.NotNil(t, mw.Handler())
}

func TestNewMiddlewareReturnsUsableMiddleware(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	middleware, err := NewMiddleware(MiddlewareParams{
		Namespace:  "default",
		ServerName: "server",
		RedisAddr:  mr.Addr(),
		Config: &v1beta1.RateLimitConfig{
			Shared: &v1beta1.RateLimitBucket{
				MaxTokens:    1,
				RefillPeriod: metav1.Duration{Duration: time.Minute},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, middleware)
	require.NotNil(t, middleware.Handler())
	require.NoError(t, middleware.Close())
}

func TestCreateMiddlewareRegistersUsableMiddleware(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	cfg, err := transporttypes.NewMiddlewareConfig(MiddlewareType, MiddlewareParams{
		Namespace:  "default",
		ServerName: "server",
		RedisAddr:  mr.Addr(),
		Config: &v1beta1.RateLimitConfig{
			Shared: &v1beta1.RateLimitBucket{
				MaxTokens:    1,
				RefillPeriod: metav1.Duration{Duration: time.Minute},
			},
		},
	})
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	runner := transportmocks.NewMockMiddlewareRunner(ctrl)
	var registered transporttypes.Middleware
	runner.EXPECT().
		AddMiddleware(MiddlewareType, gomock.AssignableToTypeOf(&rateLimitMiddleware{})).
		Do(func(_ string, middleware transporttypes.Middleware) {
			registered = middleware
		})

	require.NoError(t, CreateMiddleware(cfg, runner))
	require.NotNil(t, registered)
	require.NotNil(t, registered.Handler())
	require.NoError(t, registered.Close())
}

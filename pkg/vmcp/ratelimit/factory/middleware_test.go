// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestNewMiddlewareDisabledWithoutConfig(t *testing.T) {
	t.Parallel()

	middleware, cleanup, err := NewMiddleware(t.Context(), Config{
		Namespace:  "default",
		ServerName: "vmcp",
	})

	require.NoError(t, err)
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestNewMiddlewareRequiresRedisSessionStorage(t *testing.T) {
	t.Parallel()

	middleware, cleanup, err := NewMiddleware(t.Context(), Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(1),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires Redis session storage")
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestNewMiddlewareRequiresRedisAddress(t *testing.T) {
	t.Parallel()

	middleware, cleanup, err := NewMiddleware(t.Context(), Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(1),
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires Redis session storage address")
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestNewMiddlewareRedisPingFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	middleware, cleanup, err := NewMiddleware(ctx, Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(1),
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  "127.0.0.1:1",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to Redis")
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestNewMiddlewareInvalidRateLimitConfig(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	middleware, cleanup, err := NewMiddleware(t.Context(), Config{
		Namespace:  "default",
		ServerName: "vmcp",
		RateLimiting: &ratelimittypes.RateLimitConfig{
			Shared: &ratelimittypes.RateLimitBucket{
				MaxTokens:    0,
				RefillPeriod: metav1.Duration{Duration: time.Minute},
			},
		},
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  mr.Addr(),
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create rate limiter")
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestRateLimitMiddlewarePerUserSharedAcrossTools(t *testing.T) {
	t.Parallel()

	handler := newTestRateLimitHandler(t, &ratelimittypes.RateLimitConfig{
		PerUser: &ratelimittypes.RateLimitBucket{
			MaxTokens:    1,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	})

	first := serveToolCall(t, handler, "backend_a_echo", "alice")
	assert.Equal(t, http.StatusOK, first.Code)

	second := serveToolCall(t, handler, "backend_b_echo", "alice")
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assertRateLimitedBody(t, second)
}

func TestRateLimitMiddlewareUsesPostAggregationToolNames(t *testing.T) {
	t.Parallel()

	handler := newTestRateLimitHandler(t, &ratelimittypes.RateLimitConfig{
		Tools: []ratelimittypes.ToolRateLimitConfig{
			{
				Name: "backend_a_echo",
				Shared: &ratelimittypes.RateLimitBucket{
					MaxTokens:    1,
					RefillPeriod: metav1.Duration{Duration: time.Minute},
				},
			},
		},
	})

	first := serveToolCall(t, handler, "backend_a_echo", "")
	assert.Equal(t, http.StatusOK, first.Code)

	otherTool := serveToolCall(t, handler, "backend_b_echo", "")
	assert.Equal(t, http.StatusOK, otherTool.Code)

	secondMatchingTool := serveToolCall(t, handler, "backend_a_echo", "")
	assert.Equal(t, http.StatusTooManyRequests, secondMatchingTool.Code)
}

func newTestRateLimitHandler(t *testing.T, cfg *ratelimittypes.RateLimitConfig) http.Handler {
	t.Helper()

	mr := miniredis.RunT(t)
	middleware, cleanup, err := NewMiddleware(t.Context(), Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: cfg,
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  mr.Addr(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, middleware)
	require.NotNil(t, cleanup)
	t.Cleanup(func() {
		require.NoError(t, cleanup(context.Background()))
	})

	return middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func serveToolCall(t *testing.T, handler http.Handler, toolName, userID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withParsedMCPRequest(req, "tools/call", toolName, 1)
	if userID != "" {
		req = withIdentity(req, userID)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func withParsedMCPRequest(r *http.Request, method, resourceID string, id any) *http.Request {
	parsed := &mcpparser.ParsedMCPRequest{
		Method:     method,
		ResourceID: resourceID,
		ID:         id,
		IsRequest:  true,
	}
	ctx := context.WithValue(r.Context(), mcpparser.MCPRequestContextKey, parsed)
	return r.WithContext(ctx)
}

func withIdentity(r *http.Request, subject string) *http.Request {
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: subject}}
	ctx := auth.WithIdentity(r.Context(), identity)
	return r.WithContext(ctx)
}

func sharedRateLimitConfig(maxTokens int32) *ratelimittypes.RateLimitConfig {
	return &ratelimittypes.RateLimitConfig{
		Shared: &ratelimittypes.RateLimitBucket{
			MaxTokens:    maxTokens,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	}
}

func assertRateLimitedBody(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	var resp map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, float64(ratelimit.CodeRateLimited), errObj["code"])
	assert.Equal(t, ratelimit.MessageRateLimited, errObj["message"])
}

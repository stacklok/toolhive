// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
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
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

func TestBuildRateLimitMiddlewareDisabledWithoutConfig(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{Name: "vmcp", Namespace: "default"}}

	middleware, cleanup, err := s.buildRateLimitMiddleware(t.Context())

	require.NoError(t, err)
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestBuildRateLimitMiddlewareRequiresRedisSessionStorage(t *testing.T) {
	t.Parallel()

	s := &Server{
		config: &Config{
			Name:         "vmcp",
			Namespace:    "default",
			RateLimiting: sharedRateLimitConfig(1),
		},
	}

	middleware, cleanup, err := s.buildRateLimitMiddleware(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires Redis session storage")
	assert.Nil(t, middleware)
	assert.Nil(t, cleanup)
}

func TestRateLimitMiddlewarePerUserSharedAcrossTools(t *testing.T) {
	t.Parallel()

	handler := newTestRateLimitHandler(t, &Config{
		Name:      "vmcp",
		Namespace: "default",
		RateLimiting: &ratelimittypes.RateLimitConfig{
			PerUser: &ratelimittypes.RateLimitBucket{
				MaxTokens:    1,
				RefillPeriod: metav1.Duration{Duration: time.Minute},
			},
		},
	})

	first := serveToolCall(t, handler, "backend_a_echo", "alice", nil)
	assert.Equal(t, http.StatusOK, first.Code)

	second := serveToolCall(t, handler, "backend_b_echo", "alice", nil)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assertRateLimitedBody(t, second)
}

func TestRateLimitMiddlewareUsesPostAggregationToolNames(t *testing.T) {
	t.Parallel()

	handler := newTestRateLimitHandler(t, &Config{
		Name:      "vmcp",
		Namespace: "default",
		RateLimiting: &ratelimittypes.RateLimitConfig{
			Tools: []ratelimittypes.ToolRateLimitConfig{
				{
					Name: "backend_a_echo",
					Shared: &ratelimittypes.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
		},
	})

	first := serveToolCall(t, handler, "backend_a_echo", "", nil)
	assert.Equal(t, http.StatusOK, first.Code)

	otherTool := serveToolCall(t, handler, "backend_b_echo", "", nil)
	assert.Equal(t, http.StatusOK, otherTool.Code)

	secondMatchingTool := serveToolCall(t, handler, "backend_a_echo", "", nil)
	assert.Equal(t, http.StatusTooManyRequests, secondMatchingTool.Code)
}

func TestRateLimitMiddlewareOptimizerExtractsInnerToolName(t *testing.T) {
	t.Parallel()

	handler := newTestRateLimitHandler(t, &Config{
		Name:            "vmcp",
		Namespace:       "default",
		OptimizerConfig: &optimizer.Config{},
		RateLimiting: &ratelimittypes.RateLimitConfig{
			Tools: []ratelimittypes.ToolRateLimitConfig{
				{
					Name: "backend_fetch_fetch",
					Shared: &ratelimittypes.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
		},
	})

	args := map[string]any{
		"tool_name":  "backend_fetch_fetch",
		"parameters": map[string]any{"url": "https://example.com"},
	}
	first := serveToolCall(t, handler, "call_tool", "", args)
	assert.Equal(t, http.StatusOK, first.Code)

	second := serveToolCall(t, handler, "call_tool", "", args)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
}

func TestRateLimitToolNameFallsBackToCallTool(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{OptimizerConfig: &optimizer.Config{}}}
	parsed := &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ResourceID: "call_tool",
		Arguments:  map[string]any{},
	}

	assert.Equal(t, "call_tool", s.rateLimitToolName(parsed))
}

func newTestRateLimitHandler(t *testing.T, cfg *Config) http.Handler {
	t.Helper()

	mr := miniredis.RunT(t)
	cfg.SessionStorage = &vmcpconfig.SessionStorageConfig{
		Provider: "redis",
		Address:  mr.Addr(),
	}

	s := &Server{config: cfg}
	middleware, cleanup, err := s.buildRateLimitMiddleware(t.Context())
	require.NoError(t, err)
	require.NotNil(t, middleware)
	t.Cleanup(func() {
		require.NoError(t, cleanup(context.Background()))
	})

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return withIdentityMiddleware(mcpparser.ParsingMiddleware(middleware(next)))
}

func withIdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Test-User")
		if user != "" {
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: user}}
			r = r.WithContext(auth.WithIdentity(r.Context(), identity))
		}
		next.ServeHTTP(w, r)
	})
}

func serveToolCall(
	t *testing.T,
	handler http.Handler,
	toolName string,
	user string,
	arguments map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()

	if arguments == nil {
		arguments = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": arguments,
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func assertRateLimitedBody(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	var resp map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, float64(-32029), errObj["code"])
	assert.Equal(t, "Rate limit exceeded", errObj["message"])
}

func sharedRateLimitConfig(maxTokens int32) *ratelimittypes.RateLimitConfig {
	return &ratelimittypes.RateLimitConfig{
		Shared: &ratelimittypes.RateLimitBucket{
			MaxTokens:    maxTokens,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	}
}

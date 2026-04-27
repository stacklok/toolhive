// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
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
	rlconfig "github.com/stacklok/toolhive/pkg/ratelimit/config"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestBuildRateLimitMiddleware_EnforcesToolsCall(t *testing.T) {
	t.Parallel()

	redisServer := miniredis.RunT(t)
	srv := &Server{
		config: &Config{
			Name:               "vmcp-a",
			RateLimitNamespace: "default",
			SessionStorage: &vmcpconfig.SessionStorageConfig{
				Provider: "redis",
				Address:  redisServer.Addr(),
			},
			RateLimiting: &rlconfig.Config{
				PerUser: &rlconfig.Bucket{
					MaxTokens:    1,
					RefillPeriod: metav1.Duration{Duration: time.Minute},
				},
			},
		},
	}

	middleware, closeFn, err := srv.buildRateLimitMiddleware(context.Background())
	require.NoError(t, err)
	require.NotNil(t, middleware)
	t.Cleanup(func() {
		if closeFn != nil {
			require.NoError(t, closeFn(context.Background()))
		}
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: "alice"},
	}))
	req = req.WithContext(context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, &mcpparser.ParsedMCPRequest{
		Method:     "tools/call",
		ResourceID: "backend_a_echo",
		ID:         1,
		IsRequest:  true,
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, req)
	assert.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, req)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
}

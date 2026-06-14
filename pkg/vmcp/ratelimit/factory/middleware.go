// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package factory builds vMCP-specific rate-limit middleware.
package factory

import (
	"context"
	"fmt"
	"net/http"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// Config contains the vMCP rate-limit middleware inputs.
type Config struct {
	Namespace        string
	ServerName       string
	RateLimiting     *ratelimittypes.RateLimitConfig
	SessionStorage   *vmcpconfig.SessionStorageConfig
	PassThroughTools map[string]struct{}
}

// NewMiddleware creates Redis-backed rate-limit middleware for vMCP.
func NewMiddleware(
	_ context.Context,
	cfg Config,
) (func(http.Handler) http.Handler, func(context.Context) error, error) {
	if cfg.RateLimiting == nil {
		return nil, nil, nil
	}
	if cfg.SessionStorage == nil || cfg.SessionStorage.Provider != "redis" {
		return nil, nil, fmt.Errorf("rate limiting requires Redis session storage")
	}
	if cfg.SessionStorage.Address == "" {
		return nil, nil, fmt.Errorf("rate limiting requires Redis session storage address")
	}

	middleware, err := ratelimit.NewMiddleware(ratelimit.MiddlewareParams{
		Namespace:  cfg.Namespace,
		ServerName: cfg.ServerName,
		Config:     cfg.RateLimiting,
		RedisAddr:  cfg.SessionStorage.Address,
		RedisDB:    cfg.SessionStorage.DB,
	})
	if err != nil {
		return nil, nil, err
	}

	cleanup := func(context.Context) error {
		return middleware.Close()
	}
	return withOptimizerToolNameResolution(middleware.Handler(), cfg.PassThroughTools), cleanup, nil
}

func withOptimizerToolNameResolution(
	rateLimitMiddleware func(http.Handler) http.Handler,
	passThroughTools map[string]struct{},
) func(http.Handler) http.Handler {
	if len(passThroughTools) == 0 {
		return rateLimitMiddleware
	}

	return func(next http.Handler) http.Handler {
		normalHandler := rateLimitMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parsed := mcpparser.GetParsedMCPRequest(r.Context())
			resolved := optimizerRateLimitRequest(parsed, passThroughTools)
			if resolved == parsed {
				normalHandler.ServeHTTP(w, r)
				return
			}

			// Rate limiting needs the inner backend tool name for optimizer
			// call_tool requests, but downstream middleware must still see the
			// original call_tool request. Override the parsed request only while
			// invoking the shared rate-limit middleware, then restore the original
			// request before continuing the vMCP handler chain.
			ctx := context.WithValue(r.Context(), mcpparser.MCPRequestContextKey, resolved)
			rateLimitRequest := r.WithContext(ctx)
			restoreOriginalRequest := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				next.ServeHTTP(w, r)
			})
			rateLimitMiddleware(restoreOriginalRequest).ServeHTTP(w, rateLimitRequest)
		})
	}
}

func optimizerRateLimitRequest(
	parsed *mcpparser.ParsedMCPRequest,
	passThroughTools map[string]struct{},
) *mcpparser.ParsedMCPRequest {
	if parsed == nil || parsed.Method != "tools/call" {
		return parsed
	}
	if _, ok := passThroughTools[parsed.ResourceID]; !ok {
		return parsed
	}

	innerToolName, ok := parsed.Arguments[optimizerdec.CallToolArgToolName].(string)
	if !ok || innerToolName == "" {
		return parsed
	}

	resolved := *parsed
	resolved.ResourceID = innerToolName
	return &resolved
}

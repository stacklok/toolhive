// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package factory builds vMCP-specific rate-limit middleware.
package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

const redisPingTimeout = 5 * time.Second

// Config contains the vMCP rate-limit middleware inputs.
type Config struct {
	Namespace      string
	ServerName     string
	RateLimiting   *ratelimittypes.RateLimitConfig
	SessionStorage *vmcpconfig.SessionStorageConfig
}

// NewMiddleware creates Redis-backed rate-limit middleware for vMCP.
func NewMiddleware(
	ctx context.Context,
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

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.SessionStorage.Address,
		DB:       int(cfg.SessionStorage.DB),
		Password: os.Getenv(vmcpconfig.RedisPasswordEnvVar),
	})

	pingCtx, cancel := context.WithTimeout(ctx, redisPingTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("rate limit middleware: failed to connect to Redis at %s: %w",
			cfg.SessionStorage.Address, err)
	}

	limiter, err := ratelimit.NewLimiter(client, cfg.Namespace, cfg.ServerName, cfg.RateLimiting)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("failed to create rate limiter: %w", err)
	}

	cleanup := func(context.Context) error {
		return client.Close()
	}
	return rateLimitHandler(limiter), cleanup, nil
}

func rateLimitHandler(limiter ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parsed := mcpparser.GetParsedMCPRequest(r.Context())
			if parsed == nil || parsed.Method != "tools/call" {
				next.ServeHTTP(w, r)
				return
			}

			var userID string
			if identity, ok := auth.IdentityFromContext(r.Context()); ok {
				userID = identity.Subject
			}
			decision, err := limiter.Allow(r.Context(), parsed.ResourceID, userID)
			if err != nil {
				slog.Warn("rate limit check failed, allowing request", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if !decision.Allowed {
				writeRateLimited(w, parsed.ID, decision.RetryAfter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeRateLimited(w http.ResponseWriter, requestID any, retryAfter time.Duration) {
	retrySeconds := int(math.Ceil(retryAfter.Seconds()))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
	w.WriteHeader(http.StatusTooManyRequests)
	//nolint:gosec // G104: writing a static JSON error response to an HTTP client
	_, _ = w.Write(rateLimitedBody(requestID, retryAfter))
}

func rateLimitedBody(requestID any, retryAfter time.Duration) []byte {
	retrySeconds := math.Ceil(retryAfter.Seconds())
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    ratelimit.CodeRateLimited,
			"message": ratelimit.MessageRateLimited,
			"data": map[string]any{
				"retryAfterSeconds": retrySeconds,
			},
		},
		"id": requestID,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","error":{"code":-32029,"message":"Rate limit exceeded","data":{"retryAfterSeconds":%.0f}},"id":null}`,
			retrySeconds,
		))
	}
	return data
}

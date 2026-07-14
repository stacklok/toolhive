// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// MiddlewareType is the type constant for the rate limit middleware.
	MiddlewareType = "ratelimit"

	// redisPasswordEnvVar is the environment variable containing the Redis password.
	// Shared with session storage — the operator injects it from the same Secret.
	redisPasswordEnvVar = "THV_SESSION_REDIS_PASSWORD" //nolint:gosec // G101: env var name, not a credential
)

// MiddlewareParams holds the parameters for the rate limit middleware factory.
type MiddlewareParams struct {
	Namespace  string                   `json:"namespace"`
	ServerName string                   `json:"server_name"`
	Config     *v1beta1.RateLimitConfig `json:"config"`
	RedisAddr  string                   `json:"redis_addr,omitempty"`
	RedisDB    int32                    `json:"redis_db,omitempty"`
}

// rateLimitMiddleware wraps rate limiting functionality for the factory pattern.
type rateLimitMiddleware struct {
	handler types.MiddlewareFunction
	closer  io.Closer
}

// Handler returns the middleware function used by the proxy.
func (m *rateLimitMiddleware) Handler() types.MiddlewareFunction {
	return m.handler
}

// Close cleans up the Redis client.
func (m *rateLimitMiddleware) Close() error {
	if m.closer != nil {
		return m.closer.Close()
	}
	return nil
}

// NewRedisLimiter creates a Redis-backed rate limiter from typed params.
func NewRedisLimiter(params MiddlewareParams) (Limiter, io.Closer, error) {
	if params.RedisAddr == "" {
		return nil, nil, fmt.Errorf("rate limit middleware requires a Redis address")
	}

	// TODO: share a Redis client builder with session storage to get TLS,
	// dial/read/write timeouts, and username support. For now, a basic client
	// suffices since rate limiting and session storage target the same Redis.
	client := redis.NewClient(&redis.Options{
		Addr:     params.RedisAddr,
		DB:       int(params.RedisDB),
		Password: os.Getenv(redisPasswordEnvVar),
	})

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("rate limit middleware: failed to connect to Redis at %s: %w", params.RedisAddr, err)
	}

	limiter, err := NewLimiter(client, params.Namespace, params.ServerName, params.Config)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("failed to create rate limiter: %w", err)
	}

	return limiter, client, nil
}

// NewMiddleware creates a Redis-backed rate limit middleware from typed params.
func NewMiddleware(params MiddlewareParams) (types.Middleware, error) {
	limiter, closer, err := NewRedisLimiter(params)
	if err != nil {
		return nil, err
	}

	return &rateLimitMiddleware{
		handler: rateLimitHandler(limiter),
		closer:  closer,
	}, nil
}

// CreateMiddleware is the factory function for rate limit middleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal rate limit middleware parameters: %w", err)
	}

	mw, err := NewMiddleware(params)
	if err != nil {
		return err
	}
	runner.AddMiddleware(MiddlewareType, mw)
	return nil
}

// rateLimitHandler returns a middleware function that enforces rate limits
// on tools/call requests.
func rateLimitHandler(limiter Limiter) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Rate limits only apply to parsed tools/call requests.
			// Non-JSON-RPC requests (health checks, SSE streams) have no
			// parsed context and pass through unconditionally.
			parsed := mcp.GetParsedMCPRequest(r.Context())
			if parsed == nil || parsed.Method != "tools/call" {
				next.ServeHTTP(w, r)
				return
			}

			identity, _ := auth.IdentityFromContext(r.Context())
			err := Allow(r.Context(), limiter, identity, parsed.ResourceID)
			var limited *RateLimitedError
			if errors.As(err, &limited) {
				writeRateLimited(w, parsed.ID, limited.RetryAfter)
				return
			}
			if err != nil {
				slog.Warn("rate limit check failed, allowing request", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimited writes an HTTP 429 response with a JSON-RPC error body.
func writeRateLimited(w http.ResponseWriter, requestID any, retryAfter time.Duration) {
	retrySeconds := int(math.Ceil(retryAfter.Seconds()))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
	w.WriteHeader(http.StatusTooManyRequests)
	//nolint:gosec // G104: writing a static JSON error response to an HTTP client
	_, _ = w.Write(rateLimitedBody(requestID, retryAfter))
}

// rateLimitedBody returns the JSON-encoded body for a rate-limited JSON-RPC error.
func rateLimitedBody(requestID any, retryAfter time.Duration) []byte {
	retrySeconds := math.Ceil(retryAfter.Seconds())
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    CodeRateLimited,
			"message": MessageRateLimited,
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

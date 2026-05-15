// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

const rateLimitRedisPingTimeout = 5 * time.Second

func (s *Server) buildRateLimitMiddleware(
	ctx context.Context,
) (func(http.Handler) http.Handler, func(context.Context) error, error) {
	if s.config.RateLimiting == nil {
		return nil, nil, nil
	}
	if s.config.SessionStorage == nil || s.config.SessionStorage.Provider != "redis" {
		return nil, nil, fmt.Errorf("rate limiting requires Redis session storage")
	}
	if s.config.SessionStorage.Address == "" {
		return nil, nil, fmt.Errorf("rate limiting requires Redis session storage address")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     s.config.SessionStorage.Address,
		DB:       int(s.config.SessionStorage.DB),
		Password: os.Getenv(vmcpconfig.RedisPasswordEnvVar),
	})

	pingCtx, cancel := context.WithTimeout(ctx, rateLimitRedisPingTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("rate limit middleware: failed to connect to Redis at %s: %w",
			s.config.SessionStorage.Address, err)
	}

	limiter, err := ratelimit.NewLimiter(client, s.config.Namespace, s.config.Name, s.config.RateLimiting)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("failed to create rate limiter: %w", err)
	}

	cleanup := func(context.Context) error {
		return client.Close()
	}
	return ratelimit.NewMiddleware(limiter, s.rateLimitToolName), cleanup, nil
}

func (s *Server) rateLimitToolName(parsed *mcpparser.ParsedMCPRequest) string {
	if parsed == nil {
		return ""
	}
	toolName := parsed.ResourceID
	if !s.optimizerEnabled() || toolName != "call_tool" {
		return toolName
	}
	if parsed.Arguments == nil {
		return toolName
	}
	innerToolName, ok := parsed.Arguments["tool_name"].(string)
	if !ok || innerToolName == "" {
		return toolName
	}
	return innerToolName
}

func (s *Server) optimizerEnabled() bool {
	return s.config.OptimizerConfig != nil || s.config.OptimizerFactory != nil
}

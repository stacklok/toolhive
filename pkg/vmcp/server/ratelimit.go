// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/toolhive/pkg/ratelimit"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

type httpMiddleware = transporttypes.MiddlewareFunction

type shutdownFunc func(context.Context) error

func (s *Server) buildRateLimitMiddleware(ctx context.Context) (httpMiddleware, shutdownFunc, error) {
	if s.config.RateLimiting == nil {
		return nil, nil, nil
	}
	if s.config.SessionStorage == nil ||
		!strings.EqualFold(s.config.SessionStorage.Provider, "redis") ||
		s.config.SessionStorage.Address == "" {
		return nil, nil, fmt.Errorf("rate limiting requires sessionStorage with provider redis")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     s.config.SessionStorage.Address,
		DB:       int(s.config.SessionStorage.DB),
		Password: os.Getenv(vmcpconfig.RedisPasswordEnvVar),
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("rate limit middleware: failed to connect to Redis at %s: %w",
			s.config.SessionStorage.Address, err)
	}

	namespace := s.config.RateLimitNamespace
	if namespace == "" {
		namespace = os.Getenv("VMCP_NAMESPACE")
	}
	if namespace == "" {
		namespace = "default"
	}

	limiter, err := ratelimit.NewLimiter(client, namespace, s.config.Name, s.config.RateLimiting)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("failed to create rate limiter: %w", err)
	}

	slog.Info("rate limit middleware enabled for vMCP",
		"namespace", namespace,
		"name", s.config.Name,
		"redis_addr", s.config.SessionStorage.Address,
		"redis_db", s.config.SessionStorage.DB)

	return ratelimit.HTTPMiddleware(limiter), func(context.Context) error {
		return client.Close()
	}, nil
}

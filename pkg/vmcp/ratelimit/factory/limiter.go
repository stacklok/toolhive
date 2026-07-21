// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package factory builds vMCP-specific rate-limit dependencies.
package factory

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Config contains the vMCP rate-limit dependency inputs.
type Config struct {
	Namespace      string
	ServerName     string
	RateLimiting   *ratelimittypes.RateLimitConfig
	SessionStorage *vmcpconfig.SessionStorageConfig
}

// NewLimiter creates a Redis-backed limiter for vMCP.
func NewLimiter(
	_ context.Context,
	cfg Config,
) (ratelimit.Limiter, func(context.Context) error, error) {
	if cfg.RateLimiting == nil {
		return nil, nil, nil
	}
	if cfg.SessionStorage == nil || cfg.SessionStorage.Provider != "redis" {
		return nil, nil, fmt.Errorf("rate limiting requires Redis session storage")
	}
	if cfg.SessionStorage.Address == "" {
		return nil, nil, fmt.Errorf("rate limiting requires Redis session storage address")
	}

	limiter, closer, err := ratelimit.NewRedisLimiter(ratelimit.MiddlewareParams{
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
		return closer.Close()
	}
	return limiter, cleanup, nil
}

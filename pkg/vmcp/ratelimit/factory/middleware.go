// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package factory builds vMCP-specific rate-limit middleware.
package factory

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Config contains the vMCP rate-limit middleware inputs.
type Config struct {
	Namespace      string
	ServerName     string
	RateLimiting   *ratelimittypes.RateLimitConfig
	SessionStorage *vmcpconfig.SessionStorageConfig
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

	middlewareConfig, err := transporttypes.NewMiddlewareConfig(ratelimit.MiddlewareType, ratelimit.MiddlewareParams{
		Namespace:  cfg.Namespace,
		ServerName: cfg.ServerName,
		Config:     cfg.RateLimiting,
		RedisAddr:  cfg.SessionStorage.Address,
		RedisDB:    cfg.SessionStorage.DB,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create rate limit middleware config: %w", err)
	}

	runner := &captureRunner{}
	if err := ratelimit.CreateMiddleware(middlewareConfig, runner); err != nil {
		return nil, nil, err
	}
	if runner.middleware == nil {
		return nil, nil, fmt.Errorf("rate limit middleware factory did not register middleware")
	}

	cleanup := func(context.Context) error {
		return runner.middleware.Close()
	}
	return runner.middleware.Handler(), cleanup, nil
}

type captureRunner struct {
	middleware transporttypes.Middleware
}

func (r *captureRunner) AddMiddleware(_ string, middleware transporttypes.Middleware) {
	r.middleware = middleware
}

func (*captureRunner) SetAuthInfoHandler(http.Handler) {}

func (*captureRunner) SetPrometheusHandler(http.Handler) {}

func (*captureRunner) GetConfig() transporttypes.RunnerConfig {
	return nil
}

func (*captureRunner) GetUpstreamTokenReader() upstreamtoken.TokenReader {
	return nil
}

func (*captureRunner) GetKeyProvider() keys.PublicKeyProvider {
	return nil
}

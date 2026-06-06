// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package factory builds vMCP-specific rate-limit middleware.
package factory

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
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
	return middleware.Handler(), cleanup, nil
}

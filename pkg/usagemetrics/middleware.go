// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// MiddlewareType is the type identifier for usage metrics middleware
	MiddlewareType = "usagemetrics"

	// shutdownTimeout is the maximum time to wait for collector shutdown
	shutdownTimeout = 10 * time.Second
)

// MiddlewareParams represents the parameters for usage metrics middleware
type MiddlewareParams struct {
	// No parameters needed
}

// Middleware implements the types.Middleware interface
type Middleware struct {
	collector *Collector
}

// Handler returns the middleware function
func (m *Middleware) Handler() types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if this is a tool call by examining the parsed MCP request
			if parsed := mcp.GetParsedMCPRequest(r.Context()); parsed != nil {
				if parsed.Method == "tools/call" {
					// Increment the tool call counter
					if m.collector != nil {
						m.collector.IncrementToolCall()
					}
				}
			}

			// Pass to next handler
			next.ServeHTTP(w, r)
		})
	}
}

// Close cleans up any resources
func (m *Middleware) Close() error {
	if m.collector != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		m.collector.Shutdown(ctx)
	}
	return nil
}

// CreateMiddleware is the factory function for creating usage metrics middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	// Create a new collector instance for this middleware
	collector, err := NewCollector()
	if err != nil {
		logger.Warnf("Failed to initialize usage metrics: %v", err)
		// Continue - metrics are non-critical, register no-op middleware
		mw := &Middleware{}
		runner.AddMiddleware(config.Type, mw)
		return nil
	}

	// Start the collector's background flush loop
	collector.Start()

	mw := &Middleware{
		collector: collector,
	}
	runner.AddMiddleware(config.Type, mw)
	return nil
}

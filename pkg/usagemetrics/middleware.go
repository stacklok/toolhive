// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
	sessions  *sessionClients
}

// NewMiddleware returns usage metrics middleware that reports to the given
// collector. It must run after the MCP parsing middleware. A nil collector
// yields a pass-through middleware.
func NewMiddleware(collector *Collector) *Middleware {
	return &Middleware{collector: collector, sessions: newSessionClients()}
}

// Handler returns the middleware function
func (m *Middleware) Handler() types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parsed := mcp.GetParsedMCPRequest(r.Context())
			if m.collector == nil || parsed == nil {
				next.ServeHTTP(w, r)
				return
			}

			switch parsed.Method {
			case "initialize":
				// Legacy clients name themselves once, at initialize. The session
				// ID is in the request for SSE and in the response for
				// streamable HTTP, so look again after the handler has run.
				client := NormalizeClientName(initializeClientName(parsed.Params))
				sessionID := requestSessionID(r)
				next.ServeHTTP(w, r)
				if sessionID == "" {
					sessionID = w.Header().Get(sessionIDHeader)
				}
				m.sessions.remember(sessionID, client)
				return
			case "tools/call":
				// Modern requests carry clientInfo on every request; Legacy
				// requests are attributed through their session.
				client := modernClientName(parsed)
				if client == "" {
					client = m.sessions.lookup(requestSessionID(r))
				}
				m.collector.IncrementToolCallFrom(client)
			}

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
	// Describe the server when the runner configuration can. RunConfig does;
	// the profile is read on every flush, after the runner is fully set up.
	var opts []CollectorOption
	if provider, ok := runner.GetConfig().(ProfileProvider); ok {
		opts = append(opts, WithProfile(provider.UsageMetricsProfile))
	}

	// Create a new collector instance for this middleware
	collector, err := NewCollector(opts...)
	if err != nil {
		slog.Warn("failed to initialize usage metrics", "error", err)
		// Continue - metrics are non-critical, register no-op middleware
		runner.AddMiddleware(config.Type, NewMiddleware(nil))
		return nil
	}

	// Start the collector's background flush loop
	collector.Start()

	runner.AddMiddleware(config.Type, NewMiddleware(collector))
	return nil
}

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

	// codeModeToolName is the vMCP code mode virtual tool. It mirrors
	// script.ExecuteToolScriptName (a test pins the two together) so this
	// package, which every proxy links, does not pull in the script engine.
	codeModeToolName = "execute_tool_script"
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
				// Legacy clients name themselves once, at initialize. The
				// session is recorded when the response starts, before the
				// client can see it and send its first tool call.
				client := NormalizeClientName(initializeClientName(parsed.Params))
				ow := &onFirstWrite{ResponseWriter: w, fn: func() {
					m.sessions.remember(initializeSessionID(r, w), client)
				}}
				next.ServeHTTP(ow, r)
				ow.before() // a handler that wrote nothing still gets an implicit 200
				return
			case "tools/call":
				m.collector.IncrementToolCallFrom(m.toolCallClient(r, parsed))
				if parsed.ResourceID == codeModeToolName {
					m.collector.IncrementCodeModeCall()
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// toolCallClient returns the client a tool call is attributed to, in order of
// precedence:
//  1. clientInfo from _meta, which Modern requests carry on every request.
//  2. The client recorded at initialize for the request's session (Legacy).
//  3. A known client named by the User-Agent header. This covers sessionless
//     servers, which never assign a session ID, and sessions this middleware
//     does not remember (after a restart, eviction, or on another replica).
//
// Otherwise the call is unknown.
func (m *Middleware) toolCallClient(r *http.Request, parsed *mcp.ParsedMCPRequest) string {
	if client := modernClientName(parsed); client != "" {
		return client
	}
	if client := m.sessions.lookup(requestSessionID(r)); client != ClientUnknown {
		return client
	}
	return userAgentClient(r)
}

// initializeSessionID returns the session an initialize request opens, read
// once the response headers are set. For streamable HTTP the server assigns it
// in the response; a client must not send one on initialize, so a request that
// carries the header but gets no new ID is a rejected or repeated initialize,
// and the existing session keeps its client. SSE carries the session in the
// request's query string.
func initializeSessionID(r *http.Request, w http.ResponseWriter) string {
	if id := w.Header().Get(sessionIDHeader); id != "" {
		return id
	}
	if r.Header.Get(sessionIDHeader) != "" {
		return ""
	}
	return requestSessionID(r)
}

// onFirstWrite calls fn once, just before the first byte or status of the
// response is written. The caller calls before itself once the handler returns,
// in case the handler wrote nothing.
type onFirstWrite struct {
	http.ResponseWriter
	fn   func()
	done bool
}

func (o *onFirstWrite) before() {
	if !o.done {
		o.done = true
		o.fn()
	}
}

func (o *onFirstWrite) WriteHeader(status int) {
	o.before()
	o.ResponseWriter.WriteHeader(status)
}

func (o *onFirstWrite) Write(b []byte) (int, error) {
	o.before()
	return o.ResponseWriter.Write(b)
}

// Flush supports streamed (SSE) initialize responses.
func (o *onFirstWrite) Flush() {
	o.before()
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (o *onFirstWrite) Unwrap() http.ResponseWriter {
	return o.ResponseWriter
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

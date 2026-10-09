// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package bodylimit provides HTTP middleware that caps the size of request
// bodies, rejecting oversized requests with 413 Request Entity Too Large.
//
// It is used both directly as a net/http middleware (the management API and the
// vMCP server) and via the runner middleware registry (the MCP proxies), so
// every inbound listener can bound the memory a single request may consume
// before it is buffered by handlers that call io.ReadAll.
package bodylimit

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/bodylimit/httplimit"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// MiddlewareType is the type constant for the body limit middleware in the
	// runner middleware registry.
	MiddlewareType = "bodylimit"

	// DefaultMaxRequestBodySize is the generic request body fallback limit.
	DefaultMaxRequestBodySize = httplimit.DefaultMaxRequestBodySize
)

// MiddlewareParams holds the parameters for the body limit middleware factory.
type MiddlewareParams struct {
	// MaxBytes is the maximum request body size in bytes. Values <= 0 are
	// treated as DefaultMaxRequestBodySize (zero never means "unlimited").
	MaxBytes int64 `json:"max_bytes"`
}

// bodyLimitMiddleware adapts the body-limit handler to the types.Middleware
// interface expected by the runner middleware registry.
type bodyLimitMiddleware struct {
	handler types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *bodyLimitMiddleware) Handler() types.MiddlewareFunction {
	return m.handler
}

// Close releases resources held by the middleware. The body-limit middleware
// holds none, so this is a no-op.
func (*bodyLimitMiddleware) Close() error {
	return nil
}

// Middleware forwards to the standard-library-only body-limit implementation.
func Middleware(maxBytes int64) func(http.Handler) http.Handler {
	return httplimit.Middleware(maxBytes)
}

// IsRequestTooLarge forwards the overflow check to the generic implementation.
func IsRequestTooLarge(err error) bool { return httplimit.IsRequestTooLarge(err) }

// CreateMiddleware is the types.MiddlewareFactory registered in the runner's
// GetSupportedMiddlewareFactories. It unmarshals MiddlewareParams, builds the
// handler via Middleware, and registers it with the runner.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal body limit middleware parameters: %w", err)
	}

	mw := &bodyLimitMiddleware{
		handler: Middleware(params.MaxBytes),
	}
	runner.AddMiddleware(MiddlewareType, mw)
	return nil
}

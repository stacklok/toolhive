// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package recovery adapts toolhive-core's panic recovery middleware to
// ToolHive's middleware factory, wiring in ToolHive's observability:
// OTel span error recording and Sentry issue reporting.
package recovery

import (
	"net/http"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	corerecovery "github.com/stacklok/toolhive-core/recovery"
	sentrypkg "github.com/stacklok/toolhive/pkg/sentry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type constant for recovery middleware
const MiddlewareType = "recovery"

// panicHandler wires core's recovery hooks to ToolHive's OTel spans and
// Sentry reporting.
type panicHandler struct{}

// RecordError records a sanitized error on the request's span. The generic
// message is deliberate: panic values may embed credentials or internal
// state that must not reach external telemetry backends. Full details are
// in the log and in Sentry.
func (panicHandler) RecordError(r *http.Request, err error) {
	span := trace.SpanFromContext(r.Context())
	span.RecordError(err)
	span.SetStatus(codes.Error, "panic recovered")
}

// ReportPanic reports the raw panic value to Sentry. The Sentry span
// processor only creates transactions; reporting explicitly makes panics
// also appear as Issues in the Sentry Issues tab.
func (panicHandler) ReportPanic(r *http.Request, v any) {
	sentrypkg.RecoverPanic(r, v)
}

// Middleware is an HTTP middleware that recovers from panics.
// When a panic occurs, it logs the error and returns
// a 500 Internal Server Error response to the client.
func Middleware(next http.Handler) http.Handler {
	return corerecovery.Middleware(next, corerecovery.WithPanicHandler(panicHandler{}))
}

// FactoryMiddleware wraps recovery middleware functionality for the factory pattern.
type FactoryMiddleware struct{}

// Handler returns the middleware function used by the proxy.
func (FactoryMiddleware) Handler() types.MiddlewareFunction {
	return Middleware
}

// Close cleans up any resources used by the middleware.
func (FactoryMiddleware) Close() error {
	// Recovery middleware doesn't need cleanup
	return nil
}

// CreateMiddleware is the factory function for recovery middleware.
// It creates and registers the recovery middleware with the runner.
func CreateMiddleware(_ *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	recoveryMw := &FactoryMiddleware{}
	runner.AddMiddleware(MiddlewareType, recoveryMw)
	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package healthcontext provides a lightweight, dependency-free context marker
// for identifying health check requests. Keeping this in a separate package
// allows packages like pkg/vmcp/client and pkg/vmcp/auth/strategies to use
// the marker without pulling in the heavyweight pkg/vmcp/health dependencies
// (e.g. k8s.io/apimachinery).
package healthcontext

import "context"

// healthCheckContextKey is an unexported key type for the health check marker.
type healthCheckContextKey struct{}

// WithHealthCheckMarker marks a context as a health check request.
// Authentication layers can use IsHealthCheck to identify and skip authentication
// for health check requests.
func WithHealthCheckMarker(ctx context.Context) context.Context {
	return context.WithValue(ctx, healthCheckContextKey{}, true)
}

// IsHealthCheck returns true if the context is marked as a health check.
// Authentication strategies use this to bypass authentication for health checks,
// since health checks verify backend availability and should not require user credentials.
// Returns false for nil contexts.
func IsHealthCheck(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	val, ok := ctx.Value(healthCheckContextKey{}).(bool)
	return ok && val
}

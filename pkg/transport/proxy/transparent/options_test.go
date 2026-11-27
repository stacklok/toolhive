// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import "time"

// Test-only options that shorten the health-check and shutdown timings so
// tests do not wait on production defaults.

// withHealthCheckInterval sets the health check interval.
// Ignores non-positive intervals; default will be used.
func withHealthCheckInterval(interval time.Duration) Option {
	return func(p *TransparentProxy) {
		if interval > 0 {
			p.healthCheckInterval = interval
		}
	}
}

// withHealthCheckRetryDelay sets the health check retry delay.
// Ignores non-positive delays; default will be used.
func withHealthCheckRetryDelay(delay time.Duration) Option {
	return func(p *TransparentProxy) {
		if delay > 0 {
			p.healthCheckRetryDelay = delay
		}
	}
}

// withHealthCheckPingTimeout sets the health check ping timeout.
// Ignores non-positive timeouts; default will be used.
func withHealthCheckPingTimeout(timeout time.Duration) Option {
	return func(p *TransparentProxy) {
		if timeout > 0 {
			p.healthCheckPingTimeout = timeout
		}
	}
}

// withHealthCheckFailureThreshold sets the consecutive failure count that triggers
// onHealthCheckFailed.
// Ignores non-positive values; default will be used.
func withHealthCheckFailureThreshold(threshold int) Option {
	return func(p *TransparentProxy) {
		if threshold > 0 {
			p.healthCheckFailureThreshold = threshold
		}
	}
}

// withShutdownTimeout sets the graceful shutdown timeout for the HTTP server.
// Ignores non-positive timeouts; default will be used.
func withShutdownTimeout(timeout time.Duration) Option {
	return func(p *TransparentProxy) {
		if timeout > 0 {
			p.shutdownTimeout = timeout
		}
	}
}

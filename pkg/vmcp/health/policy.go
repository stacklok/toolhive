// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import "github.com/stacklok/toolhive/pkg/vmcp"

// ShouldAdvertise reports whether a backend in this status may contribute
// capabilities to the advertised view (tools/list and friends).
//
// Degraded backends are included: they are slow but working, and hiding their
// tools would be a worse outcome for the caller than serving them. An empty
// status means health monitoring is disabled, which is treated as healthy so a
// deployment without a monitor behaves as it did before monitoring existed.
//
// Excluded: unhealthy (not responding), unknown (not yet probed), and
// unauthenticated (operator misconfiguration).
func ShouldAdvertise(status vmcp.BackendHealthStatus) bool {
	return status == "" ||
		status == vmcp.BackendHealthy ||
		status == vmcp.BackendDegraded
}

// ShouldOpenSession reports whether a new session should attempt to open a
// connection to a backend in this status.
//
// This is deliberately STRICTER than ShouldAdvertise: it excludes degraded.
//
// The asymmetry is the fix for #5861. Advertising a degraded backend's tools is
// cheap, but blocking `initialize` on it is not: session creation waits for
// every backend it attempts (session.makeBaseSession's wg.Wait), so a single
// slow backend sets the floor for the entire tenant's session-establishment
// latency. Worse, the handshake makes several sequential round trips, so the
// cost is a multiple of the backend's per-request latency, not one unit of it.
//
// A backend is marked degraded precisely because it is slow — which is exactly
// the property that must stay off the session-establishment critical path. Its
// tools remain advertised and callable (ShouldAdvertise still admits it); only
// the blocking per-session connect is skipped, and the health monitor keeps
// probing it on its own schedule so recovery is picked up normally.
func ShouldOpenSession(status vmcp.BackendHealthStatus) bool {
	return status == "" || status == vmcp.BackendHealthy
}

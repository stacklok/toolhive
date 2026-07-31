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
// This is deliberately STRICTER than ShouldAdvertise in one specific way: it
// excludes degraded. It is NOT a general "only healthy" predicate — it skips
// only statuses that positively establish the backend is a bad bet, and admits
// everything else, including not-yet-classified.
//
// The degraded asymmetry is the fix for #5861. Advertising a degraded backend's
// tools is cheap, but blocking `initialize` on it is not: session creation waits
// for every backend it attempts (session.makeBaseSession's wg.Wait), so a single
// slow backend sets the floor for the entire tenant's session-establishment
// latency. Worse, the handshake makes several sequential round trips, so the
// cost is a multiple of the backend's per-request latency, not one unit of it.
//
// A backend is marked degraded precisely because it is slow — which is exactly
// the property that must stay off the session-establishment critical path. Its
// tools remain advertised and callable (ShouldAdvertise still admits it); only
// the blocking per-session connect is skipped, and the health monitor keeps
// probing it on its own schedule so recovery is picked up normally.
//
// Unknown is admitted, unlike in ShouldAdvertise. The two filters answer
// different questions and must diverge here. Advertising a tool from a backend
// of unknown health risks surfacing a capability that cannot be served, so
// aggregation waits for confirmation. Session establishment has the opposite
// default: serving is not gated on the first health check completing (only the
// status reporter calls WaitForInitialHealthChecks), so sessions are routinely
// created while backends are still Unknown — during pod startup, and for a
// backend whose first check failed below the unhealthy threshold, which the
// monitor records as Unknown with a non-zero failure count
// (health/status.go RecordFailure). Skipping those would connect a session to
// zero backends during the startup window, which is both a regression against
// the pre-#5861 behaviour and a worse failure than the one being fixed. "Not yet
// known to be bad" must therefore fail open.
func ShouldOpenSession(status vmcp.BackendHealthStatus) bool {
	// Skip only confirmed-bad statuses; everything else — including Unknown and
	// the empty zero value — is attempted.
	return status != vmcp.BackendDegraded &&
		status != vmcp.BackendUnhealthy &&
		status != vmcp.BackendUnauthenticated
}

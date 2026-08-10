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

// ShouldOpenSession reports whether a session should attempt to open a
// connection to a backend in this status.
//
// It skips only statuses that positively establish the backend is a bad bet and
// admits everything else, including not-yet-classified. This is the fix for
// #5861: session establishment waits for every backend it attempts
// (session.makeBaseSession's wg.Wait), so a backend the monitor already knows is
// bad sets the floor for the whole tenant's session-establishment latency. Worse,
// the handshake makes several sequential round trips, so the cost is a multiple
// of the backend's per-request latency, not one unit of it. The reported
// backend's 10-25s latency exceeds the 10s probe timeout, so its checks fail and
// it reaches Unhealthy after UnhealthyThreshold consecutive failures — which is
// the status this predicate skips.
//
// Degraded is deliberately ADMITTED. The tempting reading — "degraded means slow,
// so don't block on it" — does not survive the status's three producers
// (see vmcp.BackendDegraded):
//
//   - Slow probe (healthChecker.CheckHealth): a successful check slower than
//     DegradedThreshold. Genuinely slow, and the only producer where skipping
//     would buy latency.
//   - Recovering (statusTracker.RecordSuccess): ANY success recorded while
//     consecutiveFailures > 0 is forced to degraded, overriding the check's own
//     verdict. The backend just answered, possibly in microseconds, and stays
//     labelled degraded for up to one CheckInterval (30s default).
//   - Auth retrying (workloads.mapWorkloadStatusToVMCPHealth): a transient
//     OAuth-refresh failure, with no latency component at all.
//
// Skipping degraded would exclude a fast, working, just-recovered backend from
// every session created in the ~30s after it recovers, making recovery slower to
// take effect — a worse and more surprising failure than the one #5861 reports.
// Telling the producers apart needs a degradation reason the enum does not carry;
// until it does, the safe reading of degraded at session-open time is "still
// worth attempting".
//
// The residual cost is accepted knowingly: a backend slow enough to be degraded
// but faster than the probe timeout (DegradedThreshold..Timeout) stays on the
// session-establishment path. That window is bounded by the probe timeout, where
// #5861's backend was not.
//
// Unknown is admitted for a separate reason: serving is not gated on the first
// health check completing (only the status reporter calls
// WaitForInitialHealthChecks), so sessions are routinely created while backends
// are still Unknown — during pod startup, and for a backend whose first check
// failed below the unhealthy threshold, which the monitor records as Unknown with
// a non-zero failure count (statusTracker.RecordFailure). Skipping those would
// connect a session to zero backends during the startup window: both a
// regression against the pre-#5861 behaviour and a worse failure than the one
// being fixed. "Not yet known to be bad" must fail open.
func ShouldOpenSession(status vmcp.BackendHealthStatus) bool {
	// Skip only confirmed-bad statuses; everything else — including Degraded,
	// Unknown, and the empty zero value — is attempted.
	return status != vmcp.BackendUnhealthy &&
		status != vmcp.BackendUnauthenticated
}

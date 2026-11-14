// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestShouldAdvertiseAndShouldOpenSession pins both health predicates against
// every BackendHealthStatus constant, plus the empty zero value and an
// unrecognized value.
//
// The two are asserted together because their relationship is the contract that
// matters, and it is not a simple ordering. ShouldOpenSession is LOOSER for
// unknown (session establishment must not fail closed before the first health
// check completes), and the two now COINCIDE on degraded — the deliberate outcome
// of #5861's review: that status conflates "slow", "recovering" and "auth
// retrying", so it cannot be read as "slow" at session-open time. Testing them
// side by side makes an accidental change to either one visible as a change in
// the pairing.
//
// The unrecognized-value row pins a subtlety worth stating explicitly: the two
// predicates have opposite defaults for a status neither knows about.
// ShouldAdvertise is an allow-list, so an unrecognized status is NOT advertised
// (fails closed — a capability that may not be servable is withheld).
// ShouldOpenSession is a deny-list, so it IS attempted (fails open — better to
// connect to a backend of uncertain health than to strand a session with none).
// Each default is the conservative choice for its own question, but they point
// in opposite directions, so anyone adding a status must consider both.
func TestShouldAdvertiseAndShouldOpenSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		status          vmcp.BackendHealthStatus
		wantAdvertise   bool
		wantOpenSession bool
	}{
		{
			name:            "empty means health monitoring disabled: assume usable",
			status:          "",
			wantAdvertise:   true,
			wantOpenSession: true,
		},
		{
			name:            "healthy",
			status:          vmcp.BackendHealthy,
			wantAdvertise:   true,
			wantOpenSession: true,
		},
		{
			// Degraded is attempted, not skipped. Only one of its three producers
			// is latency; the "recovering" producer forces degraded onto a backend
			// that just answered successfully, so skipping it would sideline a
			// fast, working backend for up to one check interval.
			name:            "degraded is advertised AND attempted",
			status:          vmcp.BackendDegraded,
			wantAdvertise:   true,
			wantOpenSession: true,
		},
		{
			name:            "unhealthy",
			status:          vmcp.BackendUnhealthy,
			wantAdvertise:   false,
			wantOpenSession: false,
		},
		{
			// The asymmetry that remains: aggregation waits for confirmation,
			// session establishment must not, or a cold monitor connects sessions
			// to zero backends during pod startup.
			name:            "unknown is not advertised but is still attempted",
			status:          vmcp.BackendUnknown,
			wantAdvertise:   false,
			wantOpenSession: true,
		},
		{
			name:            "unauthenticated (operator misconfiguration)",
			status:          vmcp.BackendUnauthenticated,
			wantAdvertise:   false,
			wantOpenSession: false,
		},
		{
			// ShouldAdvertise is an allow-list (fails closed); ShouldOpenSession is
			// a deny-list (fails open). Opposite defaults, deliberately — see the
			// doc comment above.
			name:            "unrecognized status: not advertised, but still attempted",
			status:          vmcp.BackendHealthStatus("some-future-status"),
			wantAdvertise:   false,
			wantOpenSession: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantAdvertise, ShouldAdvertise(tt.status),
				"ShouldAdvertise(%q)", tt.status)
			assert.Equal(t, tt.wantOpenSession, ShouldOpenSession(tt.status),
				"ShouldOpenSession(%q)", tt.status)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// slidingTTLRegressionTTL is the short sliding-window TTL used by the
// regression tests below, paired with miniredis.FastForward to deterministically
// simulate elapsed time without slowing down the test suite.
const slidingTTLRegressionTTL = 200 * time.Millisecond

// slidingTTLRegressionEpsilon is added to/subtracted from
// slidingTTLRegressionTTL when fast-forwarding, so assertions are not flaky
// against exact-boundary timing.
const slidingTTLRegressionEpsilon = 50 * time.Millisecond

// ---------------------------------------------------------------------------
// Regression tests: sliding-TTL renewal on session-manager Validate (§14)
// ---------------------------------------------------------------------------

// TestRegression_Validate_SlidingTTL_SurvivesUnderActivity pins that
// Manager.Validate's storage.Load (Redis GETEX) renews the session's TTL, so
// a session under regular activity never expires even though the total
// elapsed time far exceeds the configured TTL. This is the positive half of
// the sliding-TTL pair; TestRegression_Validate_NoActivity_ExpiresAfterTTL is
// the negative half proving the fast-forward actually simulates expiry
// (without it, this test could pass vacuously if renewal were broken).
func TestRegression_Validate_SlidingTTL_SurvivesUnderActivity(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorageWithTTL(t, mr, slidingTTLRegressionTTL)
	backend := startMCPBackend(t, "backend-alpha", "echo")
	sm := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})

	sessionID := createSession(t, sm, nil)

	// Repeatedly validate the session, advancing the fake clock by just under
	// the TTL between each call. If Validate's Load renews the TTL (via
	// GETEX), the session must never be reported as terminated, even though
	// the cumulative elapsed time is many multiples of the configured TTL.
	const iterations = 5
	for i := range iterations {
		mr.FastForward(slidingTTLRegressionTTL - slidingTTLRegressionEpsilon)

		isTerminated, err := sm.Validate(sessionID)
		require.NoError(t, err, "iteration %d: Validate must not error", i)
		require.False(t, isTerminated,
			"iteration %d: session must survive under repeated activity (sliding TTL renewal)", i)
	}
}

// TestRegression_Validate_NoActivity_ExpiresAfterTTL pins that a session
// which receives no Validate (or other Load) calls does expire once the
// sliding-window TTL elapses. Paired with
// TestRegression_Validate_SlidingTTL_SurvivesUnderActivity so the survival
// test cannot pass vacuously (e.g. if TTL enforcement were disabled
// entirely, the survival test alone would still pass).
func TestRegression_Validate_NoActivity_ExpiresAfterTTL(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorageWithTTL(t, mr, slidingTTLRegressionTTL)
	backend := startMCPBackend(t, "backend-alpha", "echo")
	sm := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})

	sessionID := createSession(t, sm, nil)

	// No Validate/Load calls occur here — advance the fake clock straight past
	// the TTL.
	mr.FastForward(slidingTTLRegressionTTL + slidingTTLRegressionEpsilon)

	isTerminated, err := sm.Validate(sessionID)
	require.NoError(t, err)
	assert.True(t, isTerminated, "session with no activity must be reported terminated once the TTL elapses")
}

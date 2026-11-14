// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"time"
)

// ValidateActorWithLimiterForTest exposes validateActor to the external test package, which
// needs the generated mock package that itself imports tokenexchange.
var ValidateActorWithLimiterForTest = validateActor

// NewActorLimiterForTest exposes newActorLimiter. The limiter type is unexported;
// tests only pass the value on.
var NewActorLimiterForTest = newActorLimiter

// ActorLimiterInUseForTest reports how many slots of l are held.
func ActorLimiterInUseForTest(l *actorLimiter) int { return len(l.slots) }

// AcquireActorLimiterForTest calls l.acquire.
func AcquireActorLimiterForTest(ctx context.Context, l *actorLimiter) error { return l.acquire(ctx) }

// DefaultActorValidatorCapacityForTest is the production limiter capacity.
const DefaultActorValidatorCapacityForTest = defaultActorValidatorCapacity

// ValidateActorForTest calls validateActor with a limiter of the default capacity
// that is private to the call, for tests that are not about the limiter.
func ValidateActorForTest(
	ctx context.Context,
	v ActorTokenValidator,
	req ActorRequest,
	clock func() time.Time,
	timeout time.Duration,
) (ActorResult, time.Time, string, error) {
	limiter, err := newActorLimiter(defaultActorValidatorCapacity)
	if err != nil {
		return ActorResult{}, time.Time{}, "", err
	}
	return validateActor(ctx, v, limiter, req, clock, timeout)
}

// ActorValidationTimeoutForTest is the production validation budget, so tests
// that are not about the timeout run with the same budget the server uses.
const ActorValidationTimeoutForTest = actorValidationTimeout

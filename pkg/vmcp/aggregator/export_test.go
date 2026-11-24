// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import "time"

// NewCachingAggregatorWithClockForTest is NewCachingAggregator with an injected clock, so
// TTL and pruning tests advance time explicitly instead of sleeping.
func NewCachingAggregatorWithClockForTest(next Aggregator, ttl time.Duration, now func() time.Time) Aggregator {
	return newCachingAggregator(next, ttl, now)
}

// CachedEntriesForTest returns how many entries a caching aggregator holds, expired ones
// included. It returns -1 if a is not a caching aggregator.
func CachedEntriesForTest(a Aggregator) int {
	c, ok := a.(*cachingAggregator)
	if !ok {
		return -1
	}
	return c.cache.Len()
}

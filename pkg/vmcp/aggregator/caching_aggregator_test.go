// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
)

func ctxWithSubject(subject string) context.Context {
	return auth.WithIdentity(context.Background(),
		&auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: subject}})
}

var testBackends = []vmcp.Backend{{ID: "b1"}, {ID: "b2"}}

// TestCachingAggregator_CacheHitWithinTTL: two calls with the same identity and backend set
// within the TTL hit the cache, so the wrapped aggregator sweeps only once.
func TestCachingAggregator_CacheHitWithinTTL(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	caps := &aggregator.AggregatedCapabilities{}
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil).Times(1)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	ctx := ctxWithSubject("alice")

	first, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	second, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	assert.Same(t, first, second, "the cached view must be returned on a hit")
	assert.Same(t, caps, second)
}

// TestCachingAggregator_KeyedByIdentityAndHeaders: distinct subjects, and distinct forwarded
// credentials for the same subject, are distinct cache keys — each sweeps the backends. This
// is the security-critical property: one caller's view is never served to another.
func TestCachingAggregator_KeyedByIdentityAndHeaders(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	// alice, bob, and alice-with-a-forwarded-token are three distinct keys → three sweeps.
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(3)

	c := aggregator.NewCachingAggregator(mock, time.Hour)

	_, err := c.AggregateCapabilities(ctxWithSubject("alice"), testBackends)
	require.NoError(t, err)
	_, err = c.AggregateCapabilities(ctxWithSubject("bob"), testBackends)
	require.NoError(t, err)
	aliceWithToken := headerforward.WithForwardedHeaders(ctxWithSubject("alice"),
		map[string]string{"Authorization": "Bearer xyz"})
	_, err = c.AggregateCapabilities(aliceWithToken, testBackends)
	require.NoError(t, err)
}

// TestCachingAggregator_TTLExpiry: once the TTL elapses, the next call re-sweeps.
func TestCachingAggregator_TTLExpiry(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(2)

	c := aggregator.NewCachingAggregator(mock, 20*time.Millisecond)
	ctx := ctxWithSubject("alice")

	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
}

// TestCachingAggregator_DisabledWhenTTLNonPositive: a ttl <= 0 returns the wrapped aggregator
// unwrapped, so callers cannot silently get a permanently-stale cache.
func TestCachingAggregator_DisabledWhenTTLNonPositive(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)

	assert.Same(t, mock, aggregator.NewCachingAggregator(mock, 0))
	assert.Same(t, mock, aggregator.NewCachingAggregator(mock, -time.Second))

	// A nil aggregator is returned as-is so the downstream nil check (core.New) still fires
	// rather than being masked by a non-nil caching wrapper.
	var nilAgg aggregator.Aggregator
	assert.Nil(t, aggregator.NewCachingAggregator(nilAgg, time.Hour))
}

// TestCachingAggregator_ErrorNotCached: a failed sweep is not cached, so the next call retries
// the wrapped aggregator.
func TestCachingAggregator_ErrorNotCached(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	gomock.InOrder(
		mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom")),
		mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
			Return(&aggregator.AggregatedCapabilities{}, nil),
	)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	ctx := ctxWithSubject("alice")

	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.Error(t, err)
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err, "the error must not have been cached")
}

// TestCachingAggregator_InvalidateBackend_ScopedEviction verifies that
// InvalidateBackend evicts only the cache entries whose backend set includes
// the invalidated backend, leaving entries scoped to unrelated backends
// (or unrelated identities over the SAME backend set) untouched.
func TestCachingAggregator_InvalidateBackend_ScopedEviction(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)

	b1, b2 := []vmcp.Backend{{ID: "b1"}}, []vmcp.Backend{{ID: "b2"}}
	aliceB1 := ctxWithSubject("alice")
	bobB2 := ctxWithSubject("bob")

	// One sweep to populate each of the two distinct cache entries; a second
	// sweep per key proves the corresponding entry was (or was not) evicted.
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(3)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	inv, ok := c.(aggregator.CacheInvalidator)
	require.True(t, ok, "cachingAggregator must implement CacheInvalidator")

	// Populate both entries.
	_, err := c.AggregateCapabilities(aliceB1, b1)
	require.NoError(t, err)
	_, err = c.AggregateCapabilities(bobB2, b2)
	require.NoError(t, err)

	// Invalidate only b1's entry.
	inv.InvalidateBackend("b1")

	// alice/b1 must re-sweep (evicted) — this is the 3rd AggregateCapabilities call.
	_, err = c.AggregateCapabilities(aliceB1, b1)
	require.NoError(t, err)

	// bob/b2 must still be a cache hit — no further AggregateCapabilities call is
	// permitted by the mock (Times(3) is already exhausted), so gomock fails the
	// test if this call reaches the wrapped aggregator instead of hitting cache.
	_, err = c.AggregateCapabilities(bobB2, b2)
	require.NoError(t, err)
}

// TestCachingAggregator_InvalidateBackend_MultiBackendEntry verifies the
// multi-member backendIDs lookup: an entry aggregated over {b1,b2} is evicted
// when EITHER of its member backends is invalidated (here b1), exercising the
// set-membership branch that a single-backend entry does not.
func TestCachingAggregator_InvalidateBackend_MultiBackendEntry(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)

	// One sweep to populate the {b1,b2} entry, plus one more after eviction.
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(2)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	inv, ok := c.(aggregator.CacheInvalidator)
	require.True(t, ok)

	ctx := ctxWithSubject("alice")
	multi := []vmcp.Backend{{ID: "b1"}, {ID: "b2"}}

	_, err := c.AggregateCapabilities(ctx, multi)
	require.NoError(t, err)

	// Invalidating a member (b1) of the {b1,b2} entry must evict it.
	inv.InvalidateBackend("b1")

	// Re-sweep required (this is the 2nd AggregateCapabilities call).
	_, err = c.AggregateCapabilities(ctx, multi)
	require.NoError(t, err)
}

// TestCachingAggregator_InvalidateBackend_NoMatchIsNoop verifies that
// invalidating a backend ID no cached entry references is a harmless no-op:
// the existing entry survives and the next call is still a cache hit.
func TestCachingAggregator_InvalidateBackend_NoMatchIsNoop(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(1)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	inv, ok := c.(aggregator.CacheInvalidator)
	require.True(t, ok)

	ctx := ctxWithSubject("alice")
	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)

	inv.InvalidateBackend("unrelated-backend")

	// Still a cache hit: the mock's Times(1) would fail this test otherwise.
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
}

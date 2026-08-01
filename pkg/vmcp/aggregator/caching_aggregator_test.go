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

// TestCachingAggregator_InvalidateAll verifies the CacheInvalidator seam
// (#5748): the returned Aggregator satisfies aggregator.CacheInvalidator, and
// calling InvalidateAll purges the entire cache so a subsequent call for the
// SAME identity re-sweeps rather than serving the (still TTL-fresh) cached
// view.
func TestCachingAggregator_InvalidateAll(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(2)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	invalidator, ok := c.(aggregator.CacheInvalidator)
	require.True(t, ok, "cachingAggregator must implement CacheInvalidator")

	ctx := ctxWithSubject("alice")
	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)

	invalidator.InvalidateAll()

	// Well within the TTL, so a hit would prove the purge did not happen; the
	// mock's Times(2) expectation is the actual re-sweep assertion.
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err, "the second call after InvalidateAll must re-sweep, not hit a stale entry")
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

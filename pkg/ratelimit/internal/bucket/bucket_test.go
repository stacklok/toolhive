// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package bucket

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) (redis.Cmdable, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client, mr
}

// consume is a test helper that calls ConsumeAll with a single bucket.
func consume(ctx context.Context, t *testing.T, client redis.Cmdable, b *TokenBucket) bool {
	t.Helper()
	idx, err := ConsumeAll(ctx, client, []*TokenBucket{b})
	require.NoError(t, err)
	return idx == -1 // -1 means allowed
}

func TestConsumeAll_SingleBucket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		maxTokens int32
		refill    time.Duration
		calls     int
		wantLast  bool
	}{
		{
			name:      "all requests within capacity succeed",
			maxTokens: 3,
			refill:    time.Minute,
			calls:     3,
			wantLast:  true,
		},
		{
			name:      "request exceeding capacity is rejected",
			maxTokens: 3,
			refill:    time.Minute,
			calls:     4,
			wantLast:  false,
		},
		{
			name:      "single token bucket",
			maxTokens: 1,
			refill:    time.Second,
			calls:     2,
			wantLast:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, _ := newTestClient(t)
			ctx := context.Background()
			b := New("ns", "srv", "test:"+tc.name, tc.maxTokens, tc.refill)

			var lastResult bool
			for range tc.calls {
				lastResult = consume(ctx, t, client, b)
			}
			assert.Equal(t, tc.wantLast, lastResult)
		})
	}
}

func TestConsumeAll_MultiBucket_Atomic(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := context.Background()

	server := New("ns", "srv", "test:server", 5, time.Minute)
	tool := New("ns", "srv", "test:tool", 2, time.Minute)

	// Two calls pass both buckets.
	for range 2 {
		idx, err := ConsumeAll(ctx, client, []*TokenBucket{server, tool})
		require.NoError(t, err)
		assert.Equal(t, -1, idx, "should be allowed")
	}

	// Third call: tool bucket exhausted. Server bucket must NOT be consumed.
	idx, err := ConsumeAll(ctx, client, []*TokenBucket{server, tool})
	require.NoError(t, err)
	assert.Equal(t, 1, idx, "should be rejected by tool bucket (index 1)")

	// Server bucket should still have 3 tokens (5 - 2 consumed = 3).
	// Verify by making a server-only call.
	for range 3 {
		idx, err = ConsumeAll(ctx, client, []*TokenBucket{server})
		require.NoError(t, err)
		assert.Equal(t, -1, idx, "server bucket should still have tokens")
	}
	// Now server is exhausted.
	idx, err = ConsumeAll(ctx, client, []*TokenBucket{server})
	require.NoError(t, err)
	assert.Equal(t, 0, idx, "server bucket should now be exhausted")
}

func TestConsumeAll_EmptyBuckets(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	idx, err := ConsumeAll(context.Background(), client, nil)
	require.NoError(t, err)
	assert.Equal(t, -1, idx)
}

func TestConsumeAll_RefillAfterTime(t *testing.T) {
	t.Parallel()
	client, mr := newTestClient(t)
	ctx := context.Background()

	b := New("ns", "srv", "test:refill", 1, time.Second)

	// Drain the bucket.
	require.True(t, consume(ctx, t, client, b))
	require.False(t, consume(ctx, t, client, b))

	// Advance time past the refill period.
	mr.FastForward(2 * time.Second)

	// Should succeed now.
	assert.True(t, consume(ctx, t, client, b))
}

func TestConsumeAll_KeyAutoExpiration(t *testing.T) {
	t.Parallel()
	client, mr := newTestClient(t)
	ctx := context.Background()

	refillPeriod := 10 * time.Second
	b := New("ns", "srv", "test:expiry", 5, refillPeriod)

	key := "thv:rl:{ns:srv}:test:expiry"
	consume(ctx, t, client, b)
	assert.True(t, mr.Exists(key))

	mr.FastForward(3 * refillPeriod)
	assert.False(t, mr.Exists(key))
}

func TestTokenBucket_RetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxTokens    int32
		refillPeriod time.Duration
		wantRetry    time.Duration
	}{
		{
			name:         "1 token per second",
			maxTokens:    60,
			refillPeriod: 60 * time.Second,
			wantRetry:    time.Second,
		},
		{
			name:         "0.1 token per second rounds up to 10s",
			maxTokens:    10,
			refillPeriod: 100 * time.Second,
			wantRetry:    10 * time.Second,
		},
		{
			name:         "high rate clamps to minimum 1s",
			maxTokens:    1000,
			refillPeriod: time.Second,
			wantRetry:    time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := New("ns", "srv", "test:retry", tc.maxTokens, tc.refillPeriod)
			assert.Equal(t, tc.wantRetry, b.RetryAfter())
		})
	}
}

func TestDeriveKeyPrefix(t *testing.T) {
	t.Parallel()
	// Verify key format by checking the key assigned to a bucket.
	b := New("default", "my-server", "shared", 1, time.Second)
	assert.Equal(t, "thv:rl:{default:my-server}:shared", b.key)
}

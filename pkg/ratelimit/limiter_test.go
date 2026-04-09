// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func newTestClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client, mr
}

func TestNewLimiter_NilCRDReturnsNoop(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	l, err := NewLimiter(client, "ns", "srv", nil)
	require.NoError(t, err)

	d, err := l.Allow(t.Context(), "anything", "user-a")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestNewLimiter_ZeroMaxTokens(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	crd := &v1alpha1.RateLimitConfig{
		Shared: &v1alpha1.RateLimitBucket{
			MaxTokens:    0,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	}

	_, err := NewLimiter(client, "ns", "srv", crd)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxTokens must be >= 1")
}

func TestNewLimiter_ZeroDuration(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	crd := &v1alpha1.RateLimitConfig{
		Shared: &v1alpha1.RateLimitBucket{
			MaxTokens:    100,
			RefillPeriod: metav1.Duration{Duration: 0},
		},
	}

	_, err := NewLimiter(client, "ns", "srv", crd)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "refillPeriod must be positive")
}

func TestLimiter_ServerGlobalExhausted(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	crd := &v1alpha1.RateLimitConfig{
		Shared: &v1alpha1.RateLimitBucket{MaxTokens: 2, RefillPeriod: metav1.Duration{Duration: time.Minute}},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	for range 2 {
		d, err := l.Allow(ctx, "", "")
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}

	d, err := l.Allow(ctx, "", "")
	require.NoError(t, err)
	assert.False(t, d.Allowed)
	assert.Greater(t, d.RetryAfter, time.Duration(0))
}

func TestLimiter_PerToolIsolation(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	crd := &v1alpha1.RateLimitConfig{
		Tools: []v1alpha1.ToolRateLimitConfig{
			{
				Name:   "search",
				Shared: &v1alpha1.RateLimitBucket{MaxTokens: 1, RefillPeriod: metav1.Duration{Duration: time.Minute}},
			},
		},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	d, err := l.Allow(ctx, "search", "")
	require.NoError(t, err)
	require.True(t, d.Allowed)

	d, err = l.Allow(ctx, "search", "")
	require.NoError(t, err)
	assert.False(t, d.Allowed)

	// Other tool is unaffected.
	d, err = l.Allow(ctx, "execute", "")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestLimiter_ServerAndPerToolBothRequired(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	crd := &v1alpha1.RateLimitConfig{
		Shared: &v1alpha1.RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: time.Minute}},
		Tools: []v1alpha1.ToolRateLimitConfig{
			{
				Name:   "search",
				Shared: &v1alpha1.RateLimitBucket{MaxTokens: 2, RefillPeriod: metav1.Duration{Duration: time.Minute}},
			},
		},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	for range 2 {
		d, err := l.Allow(ctx, "search", "")
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}

	// Third "search" rejected by per-tool limit (server still has 3 tokens).
	d, err := l.Allow(ctx, "search", "")
	require.NoError(t, err)
	assert.False(t, d.Allowed)

	// "list" has no per-tool limit — still allowed.
	d, err = l.Allow(ctx, "list", "")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestLimiter_RedisUnavailableReturnsError(t *testing.T) {
	t.Parallel()
	client, mr := newTestClient(t)

	crd := &v1alpha1.RateLimitConfig{
		Shared: &v1alpha1.RateLimitBucket{MaxTokens: 10, RefillPeriod: metav1.Duration{Duration: time.Minute}},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	mr.Close()

	_, err = l.Allow(t.Context(), "", "")
	assert.Error(t, err)
}

func TestLimiter_PerUserServerLevel(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	crd := &v1alpha1.RateLimitConfig{
		PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 2, RefillPeriod: metav1.Duration{Duration: time.Minute}},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	// User A exhausts their 2 tokens.
	for range 2 {
		d, err := l.Allow(ctx, "", "user-a")
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	d, err := l.Allow(ctx, "", "user-a")
	require.NoError(t, err)
	assert.False(t, d.Allowed)
	assert.Greater(t, d.RetryAfter, time.Duration(0))

	// User B is independent — still has full budget.
	d, err = l.Allow(ctx, "", "user-b")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestLimiter_PerToolPerUserIsolation(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	crd := &v1alpha1.RateLimitConfig{
		Tools: []v1alpha1.ToolRateLimitConfig{
			{
				Name:    "search",
				PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 1, RefillPeriod: metav1.Duration{Duration: time.Minute}},
			},
		},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	// User A uses their 1 token for "search".
	d, err := l.Allow(ctx, "search", "user-a")
	require.NoError(t, err)
	require.True(t, d.Allowed)

	// User A rejected for "search".
	d, err = l.Allow(ctx, "search", "user-a")
	require.NoError(t, err)
	assert.False(t, d.Allowed)

	// User B can still use "search".
	d, err = l.Allow(ctx, "search", "user-b")
	require.NoError(t, err)
	assert.True(t, d.Allowed)

	// User A can use a different tool (no limit configured for "list").
	d, err = l.Allow(ctx, "list", "user-a")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestLimiter_ServerAndToolPerUserBothRequired(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	crd := &v1alpha1.RateLimitConfig{
		PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: time.Minute}},
		Tools: []v1alpha1.ToolRateLimitConfig{
			{
				Name:    "search",
				PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 2, RefillPeriod: metav1.Duration{Duration: time.Minute}},
			},
		},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	// User A makes 2 "search" calls — both pass.
	for range 2 {
		d, err := l.Allow(ctx, "search", "user-a")
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}

	// Third "search" rejected by per-tool per-user limit (server per-user still has 3).
	d, err := l.Allow(ctx, "search", "user-a")
	require.NoError(t, err)
	assert.False(t, d.Allowed)

	// "list" (no per-tool limit) still allowed for user A.
	d, err = l.Allow(ctx, "list", "user-a")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestLimiter_PerUserRejectionDoesNotDrainShared(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := t.Context()

	// Shared: 3 tokens, PerUser: 1 token.
	// A noisy user hitting their per-user limit must not consume shared tokens.
	crd := &v1alpha1.RateLimitConfig{
		Shared:  &v1alpha1.RateLimitBucket{MaxTokens: 3, RefillPeriod: metav1.Duration{Duration: time.Minute}},
		PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 1, RefillPeriod: metav1.Duration{Duration: time.Minute}},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	// User A: first call passes (shared=2, user-a=0).
	d, err := l.Allow(ctx, "", "user-a")
	require.NoError(t, err)
	require.True(t, d.Allowed)

	// User A: second call rejected by per-user limit. Shared must NOT be drained.
	d, err = l.Allow(ctx, "", "user-a")
	require.NoError(t, err)
	assert.False(t, d.Allowed)

	// Users B and C should each succeed (shared still has 2 tokens).
	d, err = l.Allow(ctx, "", "user-b")
	require.NoError(t, err)
	assert.True(t, d.Allowed, "user-b should not be blocked — shared bucket should not have been drained by user-a's rejected request")

	d, err = l.Allow(ctx, "", "user-c")
	require.NoError(t, err)
	assert.True(t, d.Allowed, "user-c should not be blocked — shared bucket should still have tokens")

	// Now shared is exhausted (3 consumed: a, b, c). User D is rejected by shared.
	d, err = l.Allow(ctx, "", "user-d")
	require.NoError(t, err)
	assert.False(t, d.Allowed, "user-d should be rejected — shared bucket is now exhausted")
}

func TestLimiter_RedisUnavailablePerUser(t *testing.T) {
	t.Parallel()
	client, mr := newTestClient(t)

	crd := &v1alpha1.RateLimitConfig{
		PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 10, RefillPeriod: metav1.Duration{Duration: time.Minute}},
	}
	l, err := NewLimiter(client, "ns", "srv", crd)
	require.NoError(t, err)

	mr.Close()

	_, err = l.Allow(t.Context(), "", "user-a")
	assert.Error(t, err)
}

func TestNewLimiter_PerUserZeroMaxTokens(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	crd := &v1alpha1.RateLimitConfig{
		PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 0, RefillPeriod: metav1.Duration{Duration: time.Minute}},
	}
	_, err := NewLimiter(client, "ns", "srv", crd)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "perUser bucket: maxTokens must be >= 1")
}

func TestNewLimiter_ToolPerUserZeroDuration(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	crd := &v1alpha1.RateLimitConfig{
		Tools: []v1alpha1.ToolRateLimitConfig{
			{
				Name:    "search",
				PerUser: &v1alpha1.RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: 0}},
			},
		},
	}
	_, err := NewLimiter(client, "ns", "srv", crd)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `tool "search" perUser bucket: refillPeriod must be positive`)
}

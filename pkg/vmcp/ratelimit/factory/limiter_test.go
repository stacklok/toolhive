// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/ratelimit"
	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestNewLimiterDisabledWithoutConfig(t *testing.T) {
	t.Parallel()

	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:  "default",
		ServerName: "vmcp",
	})

	require.NoError(t, err)
	assert.Nil(t, limiter)
	assert.Nil(t, cleanup)
}

func TestNewLimiterRequiresRedisSessionStorage(t *testing.T) {
	t.Parallel()

	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires Redis session storage")
	assert.Nil(t, limiter)
	assert.Nil(t, cleanup)
}

func TestNewLimiterRequiresRedisAddress(t *testing.T) {
	t.Parallel()

	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(),
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires Redis session storage address")
	assert.Nil(t, limiter)
	assert.Nil(t, cleanup)
}

func TestNewLimiterRedisPingFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	limiter, cleanup, err := NewLimiter(ctx, Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(),
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  "127.0.0.1:1",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to Redis")
	assert.Nil(t, limiter)
	assert.Nil(t, cleanup)
}

func TestNewLimiterInvalidRateLimitConfig(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:  "default",
		ServerName: "vmcp",
		RateLimiting: &ratelimittypes.RateLimitConfig{
			Shared: &ratelimittypes.RateLimitBucket{
				MaxTokens:    0,
				RefillPeriod: metav1.Duration{Duration: time.Minute},
			},
		},
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  mr.Addr(),
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create rate limiter")
	assert.Nil(t, limiter)
	assert.Nil(t, cleanup)
}

func TestNewLimiterReturnsUsableLimiter(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:    "default",
		ServerName:   "vmcp",
		RateLimiting: sharedRateLimitConfig(),
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  mr.Addr(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, limiter)
	require.NotNil(t, cleanup)
	t.Cleanup(func() {
		require.NoError(t, cleanup(context.Background()))
	})

	err = ratelimit.Allow(t.Context(), limiter, nil, "backend_a_echo")
	require.NoError(t, err)

	err = ratelimit.Allow(t.Context(), limiter, nil, "backend_a_echo")
	var rateLimited *ratelimit.RateLimitedError
	require.ErrorAs(t, err, &rateLimited)
}

func TestNewLimiterPerUserSharedAcrossTools(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:  "default",
		ServerName: "vmcp",
		RateLimiting: &ratelimittypes.RateLimitConfig{
			PerUser: &ratelimittypes.RateLimitBucket{
				MaxTokens:    1,
				RefillPeriod: metav1.Duration{Duration: time.Minute},
			},
		},
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  mr.Addr(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, limiter)
	t.Cleanup(func() {
		require.NoError(t, cleanup(context.Background()))
	})

	first, err := limiter.Allow(t.Context(), "backend_a_echo", "alice")
	require.NoError(t, err)
	require.True(t, first.Allowed)

	second, err := limiter.Allow(t.Context(), "backend_b_echo", "alice")
	require.NoError(t, err)
	assert.False(t, second.Allowed)
}

func TestNewLimiterUsesPostAggregationToolNames(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace:  "default",
		ServerName: "vmcp",
		RateLimiting: &ratelimittypes.RateLimitConfig{
			Tools: []ratelimittypes.ToolRateLimitConfig{
				{
					Name: "backend_a_echo",
					Shared: &ratelimittypes.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
		},
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  mr.Addr(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, limiter)
	t.Cleanup(func() {
		require.NoError(t, cleanup(context.Background()))
	})

	first, err := limiter.Allow(t.Context(), "backend_a_echo", "")
	require.NoError(t, err)
	require.True(t, first.Allowed)

	otherTool, err := limiter.Allow(t.Context(), "backend_b_echo", "")
	require.NoError(t, err)
	require.True(t, otherTool.Allowed)

	secondMatchingTool, err := limiter.Allow(t.Context(), "backend_a_echo", "")
	require.NoError(t, err)
	assert.False(t, secondMatchingTool.Allowed)
}

func sharedRateLimitConfig() *ratelimittypes.RateLimitConfig {
	return &ratelimittypes.RateLimitConfig{
		Shared: &ratelimittypes.RateLimitBucket{
			MaxTokens:    1,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	}
}

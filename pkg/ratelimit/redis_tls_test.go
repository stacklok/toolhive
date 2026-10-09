// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/redisconfig"
	"github.com/stacklok/toolhive/test/testkit/redistls"
)

func TestNewRedisLimiterTLS(t *testing.T) {
	t.Setenv(redisPasswordEnvVar, "redis-password")
	redis, caFile := redistls.Start(t)
	redis.RequireAuth("redis-password")
	for _, tc := range []struct {
		name      string
		tls       *redisconfig.TLSConfig
		wantError string
	}{
		{name: "verified custom CA", tls: &redisconfig.TLSConfig{CACertFile: caFile}},
		{name: "untrusted certificate", tls: &redisconfig.TLSConfig{}, wantError: "certificate"},
		{name: "missing configured CA", tls: &redisconfig.TLSConfig{CACertFile: caFile + ".missing"}, wantError: "failed to read Redis CA cert file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(redisPasswordEnvVar, "redis-password")
			limiter, closer, err := NewRedisLimiter(MiddlewareParams{
				Namespace: "default", ServerName: "tls-server", RedisAddr: redis.Addr(), RedisTLS: tc.tls,
				Config: &v1beta1.RateLimitConfig{Shared: &v1beta1.RateLimitBucket{
					MaxTokens: 1, RefillPeriod: metav1.Duration{Duration: time.Minute},
				}},
			})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, limiter)
				assert.Nil(t, closer)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, closer.Close()) })
			decision, err := limiter.Allow(t.Context(), "tool", "user")
			require.NoError(t, err)
			assert.True(t, decision.Allowed)
			decision, err = limiter.Allow(t.Context(), "tool", "user")
			require.NoError(t, err)
			assert.False(t, decision.Allowed)
		})
	}
}

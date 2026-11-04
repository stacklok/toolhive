// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/redisconfig"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/testkit/redistls"
)

func TestNewLimiterUsesSessionStorageTLS(t *testing.T) {
	t.Setenv(vmcpconfig.RedisPasswordEnvVar, "redis-password")
	redis, ca := redistls.Start(t)
	redis.RequireAuth("redis-password")
	for _, tc := range []struct {
		name      string
		tls       *redisconfig.TLSConfig
		wantError string
	}{
		{name: "verified custom CA", tls: &redisconfig.TLSConfig{CACertFile: ca}},
		{name: "omitted TLS cannot reach a TLS-only server", wantError: "redis: failed to connect"},
		{name: "untrusted certificate", tls: &redisconfig.TLSConfig{}, wantError: "failed to verify certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(vmcpconfig.RedisPasswordEnvVar, "redis-password")
			limiter, cleanup, err := NewLimiter(t.Context(), Config{
				Namespace: "default", ServerName: "vmcp", RateLimiting: sharedRateLimitConfig(),
				SessionStorage: &vmcpconfig.SessionStorageConfig{
					Provider: "redis", Address: redis.Addr(), TLS: tc.tls,
				},
			})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, limiter)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, cleanup(t.Context())) })
			decision, err := limiter.Allow(t.Context(), "tool", "user")
			require.NoError(t, err)
			assert.True(t, decision.Allowed)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/redisconfig"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/helpers/redistls"
)

func TestNewLimiterUsesSessionStorageTLS(t *testing.T) {
	t.Setenv(vmcpconfig.RedisPasswordEnvVar, "redis-password")
	redis, ca := redistls.Start(t)
	redis.RequireAuth("redis-password")
	limiter, cleanup, err := NewLimiter(t.Context(), Config{
		Namespace: "default", ServerName: "vmcp", RateLimiting: sharedRateLimitConfig(),
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis", Address: redis.Addr(), TLS: &redisconfig.TLSConfig{CACertFile: ca},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup(t.Context())) })
	decision, err := limiter.Allow(t.Context(), "tool", "user")
	require.NoError(t, err)
	assert.True(t, decision.Allowed)
}

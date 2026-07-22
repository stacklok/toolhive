// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

func untrustedTestBackend(id string) vmcp.Backend {
	return vmcp.Backend{
		ID:   id,
		Name: id,
		Metadata: map[string]string{
			untrusted.MetadataKeyUntrusted:    "true",
			untrusted.MetadataKeyMCPServerUID: "uid-1",
		},
	}
}

// trustedTestBackend returns a backend with no untrusted metadata.
func trustedTestBackend() vmcp.Backend {
	return vmcp.Backend{ID: "a", Name: "a", Metadata: map[string]string{}}
}

func TestGroupHasUntrustedBackend(t *testing.T) {
	t.Parallel()

	assert.False(t, groupHasUntrustedBackend(nil), "no backends = off")
	assert.False(t, groupHasUntrustedBackend([]vmcp.Backend{trustedTestBackend()}), "trusted-only = off")
	assert.True(t, groupHasUntrustedBackend(
		[]vmcp.Backend{trustedTestBackend(), untrustedTestBackend("b")}), "any untrusted = on")
}

//nolint:paralleltest // t.Setenv modifies the process environment; subtests cannot run in parallel.
func TestResolveTokenStoreConfig(t *testing.T) {
	t.Run("absent addr returns nil (broker fails closed)", func(t *testing.T) {
		// No THV_UNTRUSTED_TOKEN_STORE_REDIS_ADDR set.
		assert.Nil(t, resolveTokenStoreConfig("toolhive", "my-vmcp"))
	})

	t.Run("addr present derives the per-tenant prefix from vMCP identity", func(t *testing.T) {
		t.Setenv(untrustedTokenStoreAddrEnvVar, "redis.auth:6379")
		ts := resolveTokenStoreConfig("toolhive", "my-vmcp")
		require.NotNil(t, ts)
		assert.Equal(t, "redis.auth:6379", ts.RedisAddr)
		// DeriveKeyPrefix(ns, name) — the same prefix the embedded auth server uses.
		assert.Equal(t, "thv:auth:{toolhive:my-vmcp}:", ts.KeyPrefix)
		assert.Nil(t, ts.KEKSecretRef, "no KEK unless encryption is configured")
	})
}

// TestBuildUntrustedStack_Gating pins the startup feature gate: the stack is
// only wired when the group actually contains an untrusted backend, and the
// hard prerequisites (Redis session storage, resolvable namespace) fail loudly
// rather than silently degrading.
func TestBuildUntrustedStack_Gating(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	redisCfg := &config.Config{
		Group: "g",
		SessionStorage: &config.SessionStorageConfig{
			Provider: "redis",
			Address:  "127.0.0.1:0", // unroutable; only reached when the gate opens
		},
	}

	t.Run("off when no untrusted backend (nil, no error)", func(t *testing.T) {
		t.Parallel()
		bundle, err := buildUntrustedStack(ctx, redisCfg, []vmcp.Backend{trustedTestBackend()}, "toolhive", "vmcp", nil)
		require.NoError(t, err)
		assert.Nil(t, bundle)
	})

	t.Run("off ignores missing session storage when gate is closed", func(t *testing.T) {
		t.Parallel()
		noStorage := &config.Config{Group: "g"}
		bundle, err := buildUntrustedStack(ctx, noStorage, []vmcp.Backend{trustedTestBackend()}, "toolhive", "vmcp", nil)
		require.NoError(t, err)
		assert.Nil(t, bundle)
	})

	t.Run("untrusted backend + non-Redis storage is a hard error", func(t *testing.T) {
		t.Parallel()
		noStorage := &config.Config{Group: "g"}
		_, err := buildUntrustedStack(ctx, noStorage, []vmcp.Backend{untrustedTestBackend("b")}, "toolhive", "vmcp", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sessionStorage.provider=redis")
	})

	t.Run("untrusted backend + unresolvable namespace is a hard error", func(t *testing.T) {
		t.Parallel()
		_, err := buildUntrustedStack(ctx, redisCfg, []vmcp.Backend{untrustedTestBackend("b")}, "local", "vmcp", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "namespace")
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"
	"time"

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
		assert.Empty(t, ts.KEKSecret, "no KEK unless encryption is configured")
		assert.Empty(t, ts.KEKActiveID)
		assert.Empty(t, ts.KEKIDs)
	})

	t.Run("KEK coordinates populate the multi-key set the sidecar clones", func(t *testing.T) {
		t.Setenv(untrustedTokenStoreAddrEnvVar, "redis.auth:6379")
		t.Setenv(untrustedTokenStoreKEKSecretEnvVar, "my-vmcp-kek")
		t.Setenv(untrustedTokenStoreKEKKeyEnvVar, "kek-2")
		t.Setenv(untrustedTokenStoreKEKIDsEnvVar, "kek-1,kek-2")
		ts := resolveTokenStoreConfig("toolhive", "my-vmcp")
		require.NotNil(t, ts)
		assert.Equal(t, "my-vmcp-kek", ts.KEKSecret)
		assert.Equal(t, "kek-2", ts.KEKActiveID)
		assert.Equal(t, []string{"kek-1", "kek-2"}, ts.KEKIDs)
	})

	t.Run("partial KEK coordinates render no KEK config", func(t *testing.T) {
		t.Setenv(untrustedTokenStoreAddrEnvVar, "redis.auth:6379")
		t.Setenv(untrustedTokenStoreKEKSecretEnvVar, "my-vmcp-kek")
		ts := resolveTokenStoreConfig("toolhive", "my-vmcp")
		require.NotNil(t, ts)
		assert.Empty(t, ts.KEKSecret)
		assert.Empty(t, ts.KEKActiveID)
		assert.Empty(t, ts.KEKIDs)
	})

	t.Run("active ID missing from the ID set renders no KEK config", func(t *testing.T) {
		t.Setenv(untrustedTokenStoreAddrEnvVar, "redis.auth:6379")
		t.Setenv(untrustedTokenStoreKEKSecretEnvVar, "my-vmcp-kek")
		t.Setenv(untrustedTokenStoreKEKKeyEnvVar, "kek-9")
		t.Setenv(untrustedTokenStoreKEKIDsEnvVar, "kek-1,kek-2")
		ts := resolveTokenStoreConfig("toolhive", "my-vmcp")
		require.NotNil(t, ts)
		assert.Empty(t, ts.KEKSecret)
		assert.Empty(t, ts.KEKActiveID)
		assert.Empty(t, ts.KEKIDs)
	})
}

// TestResolveUntrustedTunables pins the Wave-5 platform knobs: defaults when
// unset, overrides honored, and startup-fatal on unparseable/zero/negative
// values. t.Setenv serializes subtests (no t.Parallel).
//
//nolint:paralleltest // t.Setenv modifies the process environment.
func TestResolveUntrustedTunables(t *testing.T) {
	t.Run("defaults when no env is set", func(t *testing.T) {
		tb, err := resolveUntrustedTunables()
		require.NoError(t, err)
		assert.Equal(t, 30*time.Minute, tb.idleTTL)
		assert.Equal(t, 120*time.Second, tb.readinessTimeout)
		assert.Equal(t, 10, tb.perUserQuota)
		assert.Equal(t, 200, tb.perServerCap)
		assert.InDelta(t, 0.8, tb.globalCapRatio, 1e-9)
		assert.Empty(t, tb.images.EnvoyProxy)
		assert.Empty(t, tb.images.EgressBroker)
		assert.Equal(t, 1.0, tb.sidecarResources.CPUMultiplier)
		assert.Equal(t, 1.0, tb.sidecarResources.MemoryMultiplier)
	})

	t.Run("overrides are honored", func(t *testing.T) {
		t.Setenv(envUntrustedIdleTTL, "15m")
		t.Setenv(envUntrustedReadinessTimeout, "60s")
		t.Setenv(envUntrustedPerUserQuota, "5")
		t.Setenv(envUntrustedPerServerCap, "50")
		t.Setenv(envUntrustedGlobalCapRatio, "0.5")
		t.Setenv(envUntrustedEnvoyImage, "mirror.local/envoy:v1")
		t.Setenv(envUntrustedBrokerImage, "mirror.local/broker:v2")
		t.Setenv(envUntrustedSidecarCPU, "2")
		t.Setenv(envUntrustedSidecarMem, "1.5")

		tb, err := resolveUntrustedTunables()
		require.NoError(t, err)
		assert.Equal(t, 15*time.Minute, tb.idleTTL)
		assert.Equal(t, 60*time.Second, tb.readinessTimeout)
		assert.Equal(t, 5, tb.perUserQuota)
		assert.Equal(t, 50, tb.perServerCap)
		assert.InDelta(t, 0.5, tb.globalCapRatio, 1e-9)
		assert.Equal(t, "mirror.local/envoy:v1", tb.images.EnvoyProxy)
		assert.Equal(t, "mirror.local/broker:v2", tb.images.EgressBroker)
		assert.Equal(t, 2.0, tb.sidecarResources.CPUMultiplier)
		assert.Equal(t, 1.5, tb.sidecarResources.MemoryMultiplier)
	})

	t.Run("partial multiplier override leaves the other dimension at the default", func(t *testing.T) {
		t.Setenv(envUntrustedSidecarCPU, "2")
		tb, err := resolveUntrustedTunables()
		require.NoError(t, err)
		assert.Equal(t, 2.0, tb.sidecarResources.CPUMultiplier)
		assert.Equal(t, 1.0, tb.sidecarResources.MemoryMultiplier)
	})

	t.Run("partial image override leaves the other image pinned (empty = default)", func(t *testing.T) {
		t.Setenv(envUntrustedBrokerImage, "mirror.local/broker@sha256:abc")
		tb, err := resolveUntrustedTunables()
		require.NoError(t, err)
		assert.Empty(t, tb.images.EnvoyProxy, "unset envoy image resolves to the pinned default at clone time")
		assert.Equal(t, "mirror.local/broker@sha256:abc", tb.images.EgressBroker)
	})

	t.Run("invalid values are startup-fatal", func(t *testing.T) {
		cases := []struct {
			name string
			env  map[string]string
		}{
			{"unparseable idle TTL", map[string]string{envUntrustedIdleTTL: "not-a-duration"}},
			{"zero idle TTL", map[string]string{envUntrustedIdleTTL: "0s"}},
			{"negative readiness timeout", map[string]string{envUntrustedReadinessTimeout: "-5s"}},
			{"zero per-user quota", map[string]string{envUntrustedPerUserQuota: "0"}},
			{"negative per-server cap", map[string]string{envUntrustedPerServerCap: "-3"}},
			{"unparseable per-server cap", map[string]string{envUntrustedPerServerCap: "abc"}},
			{"zero global cap ratio", map[string]string{envUntrustedGlobalCapRatio: "0"}},
			{"negative global cap ratio", map[string]string{envUntrustedGlobalCapRatio: "-0.5"}},
			{"NaN global cap ratio", map[string]string{envUntrustedGlobalCapRatio: "NaN"}},
			{"negative sidecar cpu", map[string]string{envUntrustedSidecarCPU: "-1"}},
			{"unparseable sidecar mem", map[string]string{envUntrustedSidecarMem: "lots"}},
			{"NaN sidecar cpu", map[string]string{envUntrustedSidecarCPU: "NaN"}},
			{"+Inf sidecar mem", map[string]string{envUntrustedSidecarMem: "+Inf"}},
			{"-Inf sidecar cpu", map[string]string{envUntrustedSidecarCPU: "-Inf"}},
			{"sidecar cpu above the bound", map[string]string{envUntrustedSidecarCPU: "101"}},
			{"sidecar mem absurdly large", map[string]string{envUntrustedSidecarMem: "1e9"}},
			{"broker image pinned to :latest", map[string]string{envUntrustedBrokerImage: "mirror.local/broker:latest"}},
			{"envoy image pinned to :latest", map[string]string{envUntrustedEnvoyImage: "envoyproxy/envoy:latest"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				_, err := resolveUntrustedTunables()
				require.Error(t, err)
			})
		}
	})

	t.Run("multiplier at the bound is admitted", func(t *testing.T) {
		t.Setenv(envUntrustedSidecarCPU, "100")
		tb, err := resolveUntrustedTunables()
		require.NoError(t, err)
		assert.Equal(t, 100.0, tb.sidecarResources.CPUMultiplier)
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

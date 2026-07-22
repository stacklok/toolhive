// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
)

// TestVmcpDiscoveredRBACRules_PodsRule pins the untrusted-mode pods grant:
// vMCP (discovered mode is the untrusted-mode front) needs get/list/watch/
// create/delete on pods, and pods only, in its namespaced Role.
func TestVmcpDiscoveredRBACRules_PodsRule(t *testing.T) {
	t.Parallel()

	var podsRules []rbacv1.PolicyRule
	for _, rule := range vmcpDiscoveredRBACRules {
		for _, res := range rule.Resources {
			if res == "pods" {
				podsRules = append(podsRules, rule)
			}
		}
	}

	require.Len(t, podsRules, 1, "expected exactly one pods rule in vmcpDiscoveredRBACRules")
	rule := podsRules[0]
	assert.Equal(t, []string{""}, rule.APIGroups)
	assert.Equal(t, []string{"pods"}, rule.Resources, "pods rule must be pods-only")
	assert.ElementsMatch(t, []string{"get", "list", "watch", "create", "delete"}, rule.Verbs)
	assert.NotContains(t, rule.Verbs, "update", "no update: pods are created/deleted, never mutated")
	assert.NotContains(t, rule.Verbs, "patch", "no patch: pods are created/deleted, never mutated")
}

// TestVmcpInlineRBACRules_NoPodsRule pins that inline mode (no discovery)
// does NOT gain pod-write: untrusted mode requires discovered backends.
func TestVmcpInlineRBACRules_NoPodsRule(t *testing.T) {
	t.Parallel()

	for _, rule := range vmcpInlineRBACRules {
		assert.NotContains(t, rule.Resources, "pods")
	}
}

// TestVmcpDiscoveredRBACRules_StatefulSetsRule pins the untrusted-mode
// statefulsets grant: the pod lifecycle resolves the backend StatefulSet by
// label selector (toolhive=true + mcpserver-uid) to clone its pod template.
// The grant is read-only — vMCP never writes StatefulSets.
func TestVmcpDiscoveredRBACRules_StatefulSetsRule(t *testing.T) {
	t.Parallel()

	var stsRules []rbacv1.PolicyRule
	for _, rule := range vmcpDiscoveredRBACRules {
		for _, res := range rule.Resources {
			if res == "statefulsets" {
				stsRules = append(stsRules, rule)
			}
		}
	}

	require.Len(t, stsRules, 1, "expected exactly one statefulsets rule in vmcpDiscoveredRBACRules")
	rule := stsRules[0]
	assert.Equal(t, []string{"apps"}, rule.APIGroups)
	assert.Equal(t, []string{"statefulsets"}, rule.Resources, "statefulsets rule must be statefulsets-only")
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, rule.Verbs)
	assert.NotContains(t, rule.Verbs, "create", "read-only: vMCP never writes StatefulSets")
	assert.NotContains(t, rule.Verbs, "update", "read-only: vMCP never writes StatefulSets")
	assert.NotContains(t, rule.Verbs, "patch", "read-only: vMCP never writes StatefulSets")
	assert.NotContains(t, rule.Verbs, "delete", "read-only: vMCP never writes StatefulSets")
}

// untrustedRedisStorage is the shared fixture for the token-store env tests:
// standalone/cluster Redis storage with the given address and token-encryption
// config.
func untrustedRedisStorage(addr string, te *mcpv1beta1.TokenEncryptionConfig) *mcpv1beta1.EmbeddedAuthServerConfig {
	return &mcpv1beta1.EmbeddedAuthServerConfig{
		Storage: &mcpv1beta1.AuthServerStorageConfig{
			Type: mcpv1beta1.AuthServerStorageTypeRedis,
			Redis: &mcpv1beta1.RedisStorageConfig{
				Addr: addr,
				ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
					PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-creds", Key: "password"},
				},
			},
			TokenEncryption: te,
		},
	}
}

// newTokenStoreTestReconciler builds a reconciler over a fake client seeded
// with objects. The status manager is nil: none of the address-only paths
// touch it (only the Sentinel+tokenEncryption condition does, and that test
// supplies a mock).
func newTokenStoreTestReconciler(t *testing.T, objects ...*corev1.Secret) *VirtualMCPServerReconciler {
	t.Helper()
	scheme := testutil.NewScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range objects {
		builder = builder.WithObjects(o)
	}
	return &VirtualMCPServerReconciler{Client: builder.Build(), Scheme: scheme}
}

// TestBuildUntrustedTokenStoreEnvVars pins the Wave-3 token-store coordinate
// injection: the vMCP Deployment gets the (non-secret) auth-server Redis
// address when the embedded auth server uses standalone/cluster Redis storage,
// and nothing otherwise. The KEK values are never injected here — they stay
// Secret-only and reach sidecars as SecretKeyRef envs resolved at clone time.
func TestBuildUntrustedTokenStoreEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		authCfg   *mcpv1beta1.EmbeddedAuthServerConfig
		wantAddr  string
		wantEmpty bool
	}{
		{
			name:      "nil auth server config produces no env var",
			authCfg:   nil,
			wantEmpty: true,
		},
		{
			name: "memory storage produces no env var",
			authCfg: &mcpv1beta1.EmbeddedAuthServerConfig{
				Storage: &mcpv1beta1.AuthServerStorageConfig{Type: mcpv1beta1.AuthServerStorageTypeMemory},
			},
			wantEmpty: true,
		},
		{
			name:      "redis storage with addr produces the address env var",
			authCfg:   untrustedRedisStorage("redis.auth:6379", nil),
			wantAddr:  "redis.auth:6379",
			wantEmpty: false,
		},
		{
			name: "redis storage without addr (sentinel) produces no env var",
			authCfg: &mcpv1beta1.EmbeddedAuthServerConfig{
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type:  mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{},
				},
			},
			wantEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
				v1beta1test.WithVMCPAuthServerConfig(tc.authCfg))
			r := newTokenStoreTestReconciler(t)
			env, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
			require.NoError(t, err)
			if tc.wantEmpty {
				assert.Empty(t, env)
				return
			}
			require.Len(t, env, 3, "addr + the Redis ACL password Secret coordinates")
			assert.Equal(t, untrustedTokenStoreAddrEnvVar, env[0].Name)
			assert.Equal(t, tc.wantAddr, env[0].Value)
			assert.Nil(t, env[0].ValueFrom, "address is non-secret; must be a plain literal, never a Secret ref")
			byName := map[string]corev1.EnvVar{}
			for _, e := range env {
				byName[e.Name] = e
				assert.Nil(t, e.ValueFrom, "%s must be a literal coordinate, never a Secret ref", e.Name)
			}
			assert.Equal(t, "redis-creds", byName[untrustedTokenStorePasswordSecretEnvVar].Value)
			assert.Equal(t, "password", byName[untrustedTokenStorePasswordKeyEnvVar].Value)
		})
	}
}

// TestBuildUntrustedTokenStoreEnvVars_PasswordMissing pins the loud-failure
// seam: standalone Redis storage WITHOUT ACL password coordinates still
// renders the address (the vMCP/broker fail loud downstream), but flags the
// misconfiguration with a Warning event so it is never silently dropped.
func TestBuildUntrustedTokenStoreEnvVars_PasswordMissing(t *testing.T) {
	t.Parallel()

	cfg := &mcpv1beta1.EmbeddedAuthServerConfig{
		Storage: &mcpv1beta1.AuthServerStorageConfig{
			Type:  mcpv1beta1.AuthServerStorageTypeRedis,
			Redis: &mcpv1beta1.RedisStorageConfig{Addr: "redis.auth:6379"},
		},
	}
	vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
		v1beta1test.WithVMCPAuthServerConfig(cfg))

	recorder := events.NewFakeRecorder(10)
	scheme := testutil.NewScheme(t)
	r := &VirtualMCPServerReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:   scheme,
		Recorder: recorder,
	}

	env, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
	require.NoError(t, err, "the misconfiguration must not error the reconcile (the broker fails loud)")
	require.Len(t, env, 1, "only the address renders — no password coordinates exist to forward")
	assert.Equal(t, untrustedTokenStoreAddrEnvVar, env[0].Name)

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "TokenStorePasswordMissing")
	default:
		t.Fatal("expected a Warning event for Redis storage without ACL password coordinates")
	}
}

// TestBuildUntrustedTokenStoreEnvVars_KEK pins the Wave-5 KEK wiring: when the
// auth-server storage carries tokenEncryption, the vMCP Deployment gets the
// KEK Secret coordinates (Secret name + active key ID + the full key-ID set
// read from the Secret) as plain literals — the vMCP turns them into one
// SecretKeyRef env per key ID on every cloned sidecar, so the KEK values
// themselves never appear in any pod spec.
func TestBuildUntrustedTokenStoreEnvVars_KEK(t *testing.T) {
	t.Parallel()

	redisStorage := func(te *mcpv1beta1.TokenEncryptionConfig) *mcpv1beta1.EmbeddedAuthServerConfig {
		return untrustedRedisStorage("redis.auth:6379", te)
	}

	t.Run("tokenEncryption set emits the KEK coordinate env vars (full key set)", func(t *testing.T) {
		t.Parallel()
		cfg := redisStorage(&mcpv1beta1.TokenEncryptionConfig{
			ActiveKeyID:  "kek-2",
			KeySecretRef: corev1.LocalObjectReference{Name: "my-vmcp-kek"},
		})
		vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
			v1beta1test.WithVMCPAuthServerConfig(cfg))
		r := newTokenStoreTestReconciler(t, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-vmcp-kek", Namespace: "default"},
			Data: map[string][]byte{
				"kek-1": []byte("retired-key-bytes"),
				"kek-2": []byte("active-key-bytes"),
			},
		})
		env, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
		require.NoError(t, err)
		require.Len(t, env, 6, "addr + password coordinates + KEK coordinates")

		byName := map[string]corev1.EnvVar{}
		for _, e := range env {
			byName[e.Name] = e
		}
		assert.Equal(t, "redis.auth:6379", byName[untrustedTokenStoreAddrEnvVar].Value)
		assert.Equal(t, "redis-creds", byName[untrustedTokenStorePasswordSecretEnvVar].Value)
		assert.Equal(t, "password", byName[untrustedTokenStorePasswordKeyEnvVar].Value)
		assert.Equal(t, "my-vmcp-kek", byName[untrustedTokenStoreKEKSecretEnvVar].Value)
		assert.Equal(t, "kek-2", byName[untrustedTokenStoreKEKKeyEnvVar].Value)
		assert.Equal(t, "kek-1,kek-2", byName[untrustedTokenStoreKEKIDsEnvVar].Value,
			"the full key-ID set (active + retired) rides along so rotation never orphans ciphertext")
		for _, e := range env {
			assert.Nil(t, e.ValueFrom, "%s must be a literal coordinate, never a Secret ref", e.Name)
		}
	})

	t.Run("tokenEncryption nil emits the address and password coordinate env vars", func(t *testing.T) {
		t.Parallel()
		vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
			v1beta1test.WithVMCPAuthServerConfig(redisStorage(nil)))
		r := newTokenStoreTestReconciler(t)
		env, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
		require.NoError(t, err)
		require.Len(t, env, 3)
		assert.Equal(t, untrustedTokenStoreAddrEnvVar, env[0].Name)
	})

	t.Run("memory storage with tokenEncryption emits nothing (CEL guards admission)", func(t *testing.T) {
		t.Parallel()
		// The CEL rule rejects this at admission; the builder must still not
		// emit KEK coordinates for non-Redis storage (defense in depth).
		cfg := &mcpv1beta1.EmbeddedAuthServerConfig{
			Storage: &mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeMemory,
				TokenEncryption: &mcpv1beta1.TokenEncryptionConfig{
					ActiveKeyID:  "kek-1",
					KeySecretRef: corev1.LocalObjectReference{Name: "my-vmcp-kek"},
				},
			},
		}
		vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
			v1beta1test.WithVMCPAuthServerConfig(cfg))
		r := newTokenStoreTestReconciler(t)
		env, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
		require.NoError(t, err)
		assert.Empty(t, env)
	})

	t.Run("unresolvable KEK Secret is an error (fail closed, coordinates dropped)", func(t *testing.T) {
		t.Parallel()
		cfg := redisStorage(&mcpv1beta1.TokenEncryptionConfig{
			ActiveKeyID:  "kek-1",
			KeySecretRef: corev1.LocalObjectReference{Name: "my-vmcp-kek"},
		})
		vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
			v1beta1test.WithVMCPAuthServerConfig(cfg))
		// No KEK Secret seeded — the resolution must fail.
		r := newTokenStoreTestReconciler(t)
		_, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
		require.Error(t, err)
	})

	t.Run("sentinel storage with tokenEncryption emits a Warning event and no env", func(t *testing.T) {
		t.Parallel()
		cfg := untrustedRedisStorage("", &mcpv1beta1.TokenEncryptionConfig{
			ActiveKeyID:  "kek-1",
			KeySecretRef: corev1.LocalObjectReference{Name: "my-vmcp-kek"},
		})
		vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
			v1beta1test.WithVMCPAuthServerConfig(cfg))

		recorder := events.NewFakeRecorder(10)
		scheme := testutil.NewScheme(t)
		r := &VirtualMCPServerReconciler{
			Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
			Scheme:   scheme,
			Recorder: recorder,
		}

		env, err := r.buildUntrustedTokenStoreEnvVars(context.Background(), vmcp)
		require.NoError(t, err, "the Sentinel misconfiguration must not error the reconcile")
		assert.Empty(t, env, "no token-store env for Sentinel-backed storage")

		select {
		case event := <-recorder.Events:
			assert.Contains(t, event, "TokenEncryptionNotSupportedForUntrusted")
		default:
			t.Fatal("expected a Warning event for Sentinel-backed storage with tokenEncryption")
		}
	})
}

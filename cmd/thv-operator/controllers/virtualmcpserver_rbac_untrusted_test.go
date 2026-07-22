// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
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

// TestBuildUntrustedTokenStoreEnvVars pins the Wave-3 token-store coordinate
// injection: the vMCP Deployment gets the (non-secret) auth-server Redis
// address when the embedded auth server uses standalone/cluster Redis storage,
// and nothing otherwise. The KEK is never injected here — it is a sidecar-only
// Secret reference resolved at clone time.
func TestBuildUntrustedTokenStoreEnvVars(t *testing.T) {
	t.Parallel()

	redisStorage := func(addr string) *mcpv1beta1.EmbeddedAuthServerConfig {
		cfg := &mcpv1beta1.EmbeddedAuthServerConfig{
			Storage: &mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeRedis,
				Redis: &mcpv1beta1.RedisStorageConfig{
					Addr: addr,
					ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
						PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-creds", Key: "password"},
					},
				},
			},
		}
		return cfg
	}

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
			authCfg:   redisStorage("redis.auth:6379"),
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
			env := buildUntrustedTokenStoreEnvVars(vmcp)
			if tc.wantEmpty {
				assert.Empty(t, env)
				return
			}
			require.Len(t, env, 1)
			assert.Equal(t, untrustedTokenStoreAddrEnvVar, env[0].Name)
			assert.Equal(t, tc.wantAddr, env[0].Value)
			assert.Nil(t, env[0].ValueFrom, "address is non-secret; must be a plain literal, never a Secret ref")
		})
	}
}

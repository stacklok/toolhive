// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	base := map[string]string{
		egressbroker.EnvPolicyFile:    "/etc/thv-policy/policy.yaml",
		egressbroker.EnvCAFile:        "/ca/ca.crt",
		egressbroker.EnvCAKeyFile:     "/ca/ca.key",
		egressbroker.EnvIssuer:        "https://issuer.example.com",
		egressbroker.EnvSubjectRaw:    "user-123",
		egressbroker.EnvSessionID:     "session-abc",
		egressbroker.EnvMCPServer:     "github-mcp",
		egressbroker.EnvDialAllowlist: "140.82.112.0/20,185.199.108.133",
	}

	t.Run("valid env loads with defaults", func(t *testing.T) {
		t.Parallel()
		cfg, err := egressbroker.LoadConfig(envFrom(base))
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1", cfg.ListenAddress)
		assert.Equal(t, 9001, cfg.ListenPort)
		assert.Equal(t, []string{"140.82.112.0/20", "185.199.108.133"}, cfg.DialAllowlist)
	})

	t.Run("missing required values fail closed", func(t *testing.T) {
		t.Parallel()
		for _, key := range []string{
			egressbroker.EnvPolicyFile, egressbroker.EnvCAFile, egressbroker.EnvCAKeyFile,
			egressbroker.EnvIssuer, egressbroker.EnvSubjectRaw,
			egressbroker.EnvSessionID, egressbroker.EnvMCPServer,
		} {
			values := map[string]string{}
			for k, v := range base {
				values[k] = v
			}
			delete(values, key)
			_, err := egressbroker.LoadConfig(envFrom(values))
			require.Error(t, err, "%s must be required", key)
		}
	})

	t.Run("invalid port and invalid allowlist fail", func(t *testing.T) {
		t.Parallel()
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		values[egressbroker.EnvListenPort] = "not-a-port"
		_, err := egressbroker.LoadConfig(envFrom(values))
		require.Error(t, err)

		values[egressbroker.EnvListenPort] = "9001"
		values[egressbroker.EnvDialAllowlist] = "not-an-ip"
		_, err = egressbroker.LoadConfig(envFrom(values))
		require.Error(t, err)
	})
}

func TestConfigResolveDialAllowlist(t *testing.T) {
	t.Parallel()

	cfg := &egressbroker.Config{}

	t.Run("falls back to the policy document's dialAllowlist", func(t *testing.T) {
		t.Parallel()
		policy := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
dialAllowlist: ["140.82.112.0/20"]
`)
		out, err := cfg.ResolveDialAllowlist(policy)
		require.NoError(t, err)
		assert.Equal(t, []string{"140.82.112.0/20"}, out)
	})

	t.Run("env override wins", func(t *testing.T) {
		t.Parallel()
		cfgWithEnv := &egressbroker.Config{DialAllowlist: []string{"1.2.3.4/32"}}
		policy := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
dialAllowlist: ["140.82.112.0/20"]
`)
		out, err := cfgWithEnv.ResolveDialAllowlist(policy)
		require.NoError(t, err)
		assert.Equal(t, []string{"1.2.3.4/32"}, out)
	})

	t.Run("no allowlist anywhere fails closed", func(t *testing.T) {
		t.Parallel()
		policy := mustParse(t, testPolicyYAML)
		_, err := cfg.ResolveDialAllowlist(policy)
		require.Error(t, err)
		_, err = cfg.ResolveDialAllowlist(nil)
		require.Error(t, err)
	})
}

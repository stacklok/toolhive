// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func TestResolveDialAllowlist(t *testing.T) {
	t.Parallel()

	lookup := func(ips map[string][]net.IP) func(string) ([]net.IP, error) {
		return func(host string) ([]net.IP, error) {
			if ips, ok := ips[host]; ok {
				return ips, nil
			}
			return nil, fmt.Errorf("no such host: %s", host)
		}
	}

	t.Run("resolves policy hosts to deduplicated IP prefixes", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
`)
		out, err := egressbroker.ResolveDialAllowlist(p, lookup(map[string][]net.IP{
			"api.github.com": {net.ParseIP("140.82.114.26"), net.ParseIP("140.82.114.26")},
		}))
		require.NoError(t, err)
		assert.Equal(t, []string{"140.82.114.26/32"}, out)
	})

	t.Run("output is sorted regardless of DNS answer order (anti-churn)", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com", "*.githubusercontent.com"]
`)
		resolve := func(ips map[string][]net.IP) []string {
			out, err := egressbroker.ResolveDialAllowlist(p, lookup(ips))
			require.NoError(t, err)
			return out
		}
		shuffled := map[string][]net.IP{
			"api.github.com":        {net.ParseIP("140.82.116.6"), net.ParseIP("140.82.112.5"), net.ParseIP("140.82.114.26")},
			"githubusercontent.com": {net.ParseIP("185.199.110.133"), net.ParseIP("185.199.108.133")},
		}
		reversed := map[string][]net.IP{
			"api.github.com":        {net.ParseIP("140.82.114.26"), net.ParseIP("140.82.112.5"), net.ParseIP("140.82.116.6")},
			"githubusercontent.com": {net.ParseIP("185.199.108.133"), net.ParseIP("185.199.110.133")},
		}
		first := resolve(shuffled)
		assert.Equal(t, first, resolve(reversed), "DNS answer order must not change the rendered allowlist")
		assert.IsIncreasingf(t, first, "allowlist must be sorted (NetworkPolicy/ConfigMap anti-churn)")
	})

	t.Run("wildcard resolves the base domain", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["*.githubusercontent.com"]
`)
		out, err := egressbroker.ResolveDialAllowlist(p, lookup(map[string][]net.IP{
			"githubusercontent.com": {net.ParseIP("185.199.108.133")},
		}))
		require.NoError(t, err)
		assert.Equal(t, []string{"185.199.108.133/32"}, out)
	})

	t.Run("DNS failure wraps ErrDNSResolution (transient: the operator retries)", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
`)
		_, err := egressbroker.ResolveDialAllowlist(p, lookup(map[string][]net.IP{}))
		require.Error(t, err)
		assert.ErrorIs(t, err, egressbroker.ErrDNSResolution)
	})

	t.Run("rebinding-suspect DNS answers never widen the allowlist", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
`)
		_, err := egressbroker.ResolveDialAllowlist(p, lookup(map[string][]net.IP{
			"api.github.com": {net.ParseIP("10.0.0.5"), net.ParseIP("127.0.0.1"), net.ParseIP("169.254.169.254")},
		}))
		require.Error(t, err, "a host resolving only to non-global IPs must fail, not allowlist them")
		assert.Contains(t, err.Error(), "empty dial allowlist")
	})

	t.Run("mixed global + private answers keep only the global ones", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
`)
		out, err := egressbroker.ResolveDialAllowlist(p, lookup(map[string][]net.IP{
			"api.github.com": {net.ParseIP("10.0.0.5"), net.ParseIP("140.82.114.26")},
		}))
		require.NoError(t, err)
		assert.Equal(t, []string{"140.82.114.26/32"}, out)
	})

	t.Run("nil inputs fail loudly", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, `
providers:
- provider: github
  allowedHosts: ["api.github.com"]
`)
		_, err := egressbroker.ResolveDialAllowlist(nil, net.LookupIP)
		require.Error(t, err)
		_, err = egressbroker.ResolveDialAllowlist(p, nil)
		require.Error(t, err)
	})
}

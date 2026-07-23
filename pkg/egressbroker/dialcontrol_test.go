// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func TestIPAllowlist(t *testing.T) {
	t.Parallel()

	// Not exported: behavior is verified through Config validation + the
	// exported ParseIPAllowlist/DialControl surface.
	allow, err := egressbroker.ParseIPAllowlist([]string{"140.82.112.0/20", "185.199.108.133"})
	require.NoError(t, err)

	t.Run("constructor validation", func(t *testing.T) {
		t.Parallel()
		_, err := egressbroker.ParseIPAllowlist(nil)
		require.Error(t, err, "empty allowlist must fail closed")
		_, err = egressbroker.ParseIPAllowlist([]string{"not-an-ip"})
		require.Error(t, err)
		_, err = egressbroker.ParseIPAllowlist([]string{"10.0.0.0/33"})
		require.Error(t, err)
		_, err = egressbroker.ParseIPAllowlist([]string{""})
		require.Error(t, err)
	})

	t.Run("resolved IP not in allowlist → dial refused", func(t *testing.T) {
		t.Parallel()
		err := allow.DialControl("tcp", "8.8.8.8:443", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "allowlist")
	})

	t.Run("resolved IP in allowlist CIDR → dial allowed", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, allow.DialControl("tcp", "140.82.114.26:443", nil))
	})

	t.Run("single-host entry matches exactly", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, allow.DialControl("tcp", "185.199.108.133:443", nil))
		require.Error(t, allow.DialControl("tcp", "185.199.108.134:443", nil))
	})

	t.Run("rebinding: allowlisted name resolving to internal IP → refused", func(t *testing.T) {
		t.Parallel()
		for _, internal := range []string{
			"10.0.0.5:443", "192.168.1.1:443", "172.16.0.1:443",
			"169.254.169.254:80", // cloud metadata
			"100.64.0.1:443",     // CGNAT
			"[fd00::1]:443",      // IPv6 ULA
		} {
			require.Error(t, allow.DialControl("tcp", internal, nil),
				"dial to %s must be refused", internal)
		}
	})

	t.Run("IPv4-mapped IPv6 is unmapped before matching", func(t *testing.T) {
		t.Parallel()
		// ::ffff:140.82.114.26 must match the IPv4 CIDR (not slip past it, not
		// be wrongly refused either).
		require.NoError(t, allow.DialControl("tcp", "[::ffff:140.82.114.26]:443", nil))
		// And a mapped internal address must still be refused.
		require.Error(t, allow.DialControl("tcp", "[::ffff:10.0.0.5]:443", nil))
	})

	t.Run("unparsable target fails closed", func(t *testing.T) {
		t.Parallel()
		require.Error(t, allow.DialControl("tcp", "not-an-ip-literal:443", nil))
	})

	t.Run("loopback refused unless explicitly allowlisted", func(t *testing.T) {
		t.Parallel()
		require.Error(t, allow.DialControl("tcp", "127.0.0.1:9001", nil))
		require.Error(t, allow.DialControl("tcp", "[::1]:9001", nil))

		withLoopback, err := egressbroker.ParseIPAllowlist([]string{"127.0.0.1/32"})
		require.NoError(t, err)
		require.NoError(t, withLoopback.DialControl("tcp", "127.0.0.1:9001", nil))
		require.Error(t, withLoopback.DialControl("tcp", "127.0.0.2:9001", nil))
	})
}

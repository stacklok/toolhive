// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package netproxy

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
)

func TestSelectedBackend(t *testing.T) {
	tests := []struct {
		name      string
		envVal    string
		want      string
		wantError bool
	}{
		{name: "empty defaults to squid", envVal: "", want: BackendSquid},
		{name: "explicit squid", envVal: "squid", want: BackendSquid},
		{name: "explicit envoy", envVal: "envoy", want: BackendEnvoy},
		{name: "invalid backend", envVal: "haproxy", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_NETWORK_PROXY", tc.envVal)
			got, err := SelectedBackend()
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestProxyImages(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("TOOLHIVE_EGRESS_IMAGE", "")
		t.Setenv("TOOLHIVE_ENVOY_IMAGE", "")
		assert.Equal(t, DefaultSquidImage, GetSquidImage())
		assert.Equal(t, DefaultEnvoyImage, GetEnvoyImage())
	})

	t.Run("overrides", func(t *testing.T) {
		t.Setenv("TOOLHIVE_EGRESS_IMAGE", "custom/squid:1")
		t.Setenv("TOOLHIVE_ENVOY_IMAGE", "custom/envoy:1")
		assert.Equal(t, "custom/squid:1", GetSquidImage())
		assert.Equal(t, "custom/envoy:1", GetEnvoyImage())
	})
}

func TestAddEgressEnvVars(t *testing.T) {
	t.Parallel()

	original := map[string]string{"EXISTING": "value"}
	result := AddEgressEnvVars(original, "192.168.64.2")

	assert.Equal(t, map[string]string{"EXISTING": "value"}, original, "caller map must not be mutated")
	assert.Equal(t, "value", result["EXISTING"])
	assert.Equal(t, "http://192.168.64.2:3128", result["HTTP_PROXY"])
	assert.Equal(t, "http://192.168.64.2:3128", result["HTTPS_PROXY"])
	assert.Equal(t, "http://192.168.64.2:3128", result["http_proxy"])
	assert.Equal(t, "http://192.168.64.2:3128", result["https_proxy"])
	assert.Equal(t, "localhost,127.0.0.1,::1", result["NO_PROXY"])
	assert.Equal(t, "localhost,127.0.0.1,::1", result["no_proxy"])

	fromNil := AddEgressEnvVars(nil, "::1")
	assert.Equal(t, "http://[::1]:3128", fromNil["HTTP_PROXY"])
}

func TestMergeEnvVars(t *testing.T) {
	t.Parallel()

	base := map[string]string{"A": "1", "B": "2"}
	extra := map[string]string{"B": "override", "C": "3"}

	merged := MergeEnvVars(base, extra)
	assert.Equal(t, map[string]string{"A": "1", "B": "override", "C": "3"}, merged)
	assert.Equal(t, "2", base["B"], "base map must not be mutated")
	assert.NotContains(t, extra, "A", "extra map must not be mutated")
}

func TestBuildSquidConfigs(t *testing.T) {
	t.Parallel()

	gatewayIP := netip.MustParseAddr("192.168.64.1")
	netPerms := &permissions.NetworkPermissions{
		Outbound: &permissions.OutboundNetworkPermissions{
			AllowHost: []string{"api.example.com"},
			AllowPort: []int{443},
		},
	}

	egressDenied := BuildEgressSquidConf(netPerms, "my-mcp", false, []netip.Addr{gatewayIP})
	assert.Contains(t, egressDenied, "192.168.64.1")
	assert.Contains(t, egressDenied, "http_access deny docker_gateway_hosts")
	assert.Contains(t, egressDenied, "http_access deny docker_gateway_ip")
	assert.Contains(t, egressDenied, "api.example.com")

	egressAllowed := BuildEgressSquidConf(netPerms, "my-mcp", true, []netip.Addr{gatewayIP})
	assert.Contains(t, egressAllowed, "http_access allow docker_gateway_hosts")
	assert.Contains(t, egressAllowed, "http_access allow docker_gateway_ip")

	egressDeniedNoIPs := BuildEgressSquidConf(netPerms, "my-mcp", false, nil)
	assert.Contains(t, egressDeniedNoIPs, "http_access deny docker_gateway_hosts")
	assert.NotContains(t, egressDeniedNoIPs, "docker_gateway_ip")

	egressAllowedNoIPs := BuildEgressSquidConf(netPerms, "my-mcp", true, nil)
	assert.Contains(t, egressAllowedNoIPs, "http_access allow docker_gateway_hosts")
	assert.NotContains(t, egressAllowedNoIPs, "docker_gateway_ip")

	ingress := BuildIngressSquidConf("my-mcp", "192.168.65.10", 8080, 18080, netPerms)
	assert.Contains(t, ingress, "192.168.65.10")
	assert.Contains(t, ingress, "8080")
	assert.Contains(t, ingress, "18080")
}

func TestBuildEnvoyBootstraps(t *testing.T) {
	t.Parallel()

	gatewayIP := netip.MustParseAddr("192.168.64.1")
	spec := Spec{
		WorkloadName: "my-mcp",
		UpstreamHost: "192.168.65.10",
		UpstreamPort: 8080,
		GatewayIPs:   []netip.Addr{gatewayIP},
		Permissions: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{
				AllowHost: []string{"api.example.com"},
				AllowPort: []int{443},
			},
		},
	}

	egress := BuildEgressBootstrap(spec)
	require.Len(t, egress.StaticResources.Listeners, 1)
	require.Len(t, egress.StaticResources.Clusters, 1)
	assert.Equal(t, 3128, egress.StaticResources.Listeners[0].Address.SocketAddress.PortValue)

	ingress := BuildIngressBootstrap(spec, 18080)
	require.Len(t, ingress.StaticResources.Listeners, 1)
	require.Len(t, ingress.StaticResources.Clusters, 1)
	assert.Equal(t, 18080, ingress.StaticResources.Listeners[0].Address.SocketAddress.PortValue)
	endpoints := ingress.StaticResources.Clusters[0].LoadAssignment.Endpoints
	require.Len(t, endpoints, 1)
	require.Len(t, endpoints[0].LBEndpoints, 1)
	addr := endpoints[0].LBEndpoints[0].Endpoint.Address.SocketAddress
	assert.Equal(t, "192.168.65.10", addr.Address)
	assert.Equal(t, 8080, addr.PortValue)
}

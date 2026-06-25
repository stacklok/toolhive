// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
)

// Compile-time assertion: envoyProxy must satisfy networkProxy.
var _ networkProxy = (*envoyProxy)(nil)

// findRBACFilter walks the filter chain in a listener looking for the first
// filter whose name contains "rbac". It returns nil if none is found.
// The exact field layout mirrors the typed structs that envoy.go will define.
func findRBACFilter(listener envoyListener) *envoyRBACFilter {
	for _, fc := range listener.FilterChains {
		for i := range fc.Filters {
			if fc.Filters[i].TypedConfig != nil {
				rbac, ok := fc.Filters[i].TypedConfig.(*envoyRBACFilter)
				if ok {
					return rbac
				}
			}
		}
	}
	return nil
}

// TestBuildEgressListener_AllowlistAndDefaultDeny exercises the RBAC policy
// generation logic of buildEgressListener across the main permission scenarios.
func TestBuildEgressListener_AllowlistAndDefaultDeny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		spec                  proxySpec
		wantRBACPresent       bool
		wantRBACAction        string // "ALLOW" or "DENY"
		wantPolicies          []string
		wantGatewayDenyAbsent bool
		wantGatewayDenyL3     bool // CIDR deny on GatewayIP
		wantGatewayDenyL7     bool // authority deny on host.docker.internal
	}{
		{
			name: "nil permissions, InsecureAllowAll=false produces deny-all RBAC",
			spec: proxySpec{
				WorkloadName:       "myserver",
				Permissions:        nil,
				AllowDockerGateway: false,
				GatewayIP:          dockerDefaultBridgeGatewayIP,
			},
			wantRBACPresent:   true,
			wantRBACAction:    "ALLOW",
			wantPolicies:      nil, // no policies → Envoy deny-all
			wantGatewayDenyL3: true,
			wantGatewayDenyL7: true,
		},
		{
			name: "InsecureAllowAll=true produces allow-all RBAC policy",
			spec: proxySpec{
				WorkloadName: "myserver",
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						InsecureAllowAll: true,
					},
				},
				AllowDockerGateway: false,
				GatewayIP:          dockerDefaultBridgeGatewayIP,
			},
			wantRBACPresent:   true,
			wantRBACAction:    "ALLOW",
			wantPolicies:      []string{"allow-all"},
			wantGatewayDenyL3: true,
			wantGatewayDenyL7: true,
		},
		{
			name: "AllowHost list produces per-host RBAC policies",
			spec: proxySpec{
				WorkloadName: "myserver",
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						AllowHost: []string{"example.com", "api.example.com"},
					},
				},
				AllowDockerGateway: false,
				GatewayIP:          dockerDefaultBridgeGatewayIP,
			},
			wantRBACPresent:   true,
			wantRBACAction:    "ALLOW",
			wantPolicies:      []string{"example.com", "api.example.com"},
			wantGatewayDenyL3: true,
			wantGatewayDenyL7: true,
		},
		{
			name: "AllowDockerGateway=true omits gateway deny rules",
			spec: proxySpec{
				WorkloadName: "myserver",
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						InsecureAllowAll: true,
					},
				},
				AllowDockerGateway: true,
				GatewayIP:          dockerDefaultBridgeGatewayIP,
			},
			wantRBACPresent:       true,
			wantRBACAction:        "ALLOW",
			wantPolicies:          []string{"allow-all"},
			wantGatewayDenyAbsent: true,
		},
		{
			name: "wildcard AllowHost produces correct authority pattern",
			spec: proxySpec{
				WorkloadName: "myserver",
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						AllowHost: []string{"*.example.com"},
					},
				},
				AllowDockerGateway: false,
				GatewayIP:          dockerDefaultBridgeGatewayIP,
			},
			wantRBACPresent:   true,
			wantRBACAction:    "ALLOW",
			wantPolicies:      []string{"*.example.com"},
			wantGatewayDenyL3: true,
			wantGatewayDenyL7: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listener := buildEgressListener(tt.spec)

			// The listener must have at least one filter chain.
			require.NotEmpty(t, listener.FilterChains, "expected at least one filter chain")

			if tt.wantRBACPresent {
				rbac := findRBACFilter(listener)
				require.NotNil(t, rbac, "expected RBAC filter to be present in the listener")
				assert.Equal(t, tt.wantRBACAction, rbac.Rules.Action,
					"RBAC action mismatch")

				if tt.wantPolicies == nil {
					assert.Empty(t, rbac.Rules.Policies,
						"expected empty policy set for deny-all")
				} else {
					for _, policyName := range tt.wantPolicies {
						_, ok := rbac.Rules.Policies[policyName]
						assert.True(t, ok, "expected RBAC policy %q to be present", policyName)
					}
				}
			}

			if tt.wantGatewayDenyAbsent {
				// Serialize to JSON and verify neither gateway hostname nor the
				// gateway CIDR deny appear anywhere in the config.
				raw, err := json.Marshal(listener)
				require.NoError(t, err)
				s := string(raw)
				assert.NotContains(t, s, dockerGatewayHostname,
					"docker gateway hostname should be absent when AllowDockerGateway=true")
				assert.NotContains(t, s, dockerDefaultBridgeGatewayIP,
					"docker gateway IP should be absent when AllowDockerGateway=true")
			}

			if tt.wantGatewayDenyL3 {
				raw, err := json.Marshal(listener)
				require.NoError(t, err)
				assert.Contains(t, string(raw), dockerDefaultBridgeGatewayIP,
					"expected L3 CIDR deny on gateway IP")
			}

			if tt.wantGatewayDenyL7 {
				raw, err := json.Marshal(listener)
				require.NoError(t, err)
				assert.Contains(t, string(raw), dockerGatewayHostname,
					"expected L7 authority deny for host.docker.internal")
				assert.Contains(t, string(raw), dockerAltGatewayHostname,
					"expected L7 authority deny for gateway.docker.internal")
			}
		})
	}
}

// TestBuildEgressListener_EmptyAllowHostDenyAll is a mandatory regression guard:
// buildEgressListener with an empty AllowHost and InsecureAllowAll=false must
// produce a listener where the RBAC filter is present with action=ALLOW and
// zero policies. This is Envoy's deny-all behavior. The test guards against a
// serialization bug that silently omits the RBAC filter and produces allow-all.
func TestBuildEgressListener_EmptyAllowHostDenyAll(t *testing.T) {
	t.Parallel()

	spec := proxySpec{
		WorkloadName: "guard-test",
		Permissions: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{
				InsecureAllowAll: false,
				AllowHost:        []string{},
			},
		},
		AllowDockerGateway: false,
		GatewayIP:          dockerDefaultBridgeGatewayIP,
	}

	listener := buildEgressListener(spec)
	require.NotEmpty(t, listener.FilterChains)

	rbac := findRBACFilter(listener)
	require.NotNil(t, rbac,
		"RBAC filter must be present — its absence would silently allow all traffic")
	assert.Equal(t, "ALLOW", rbac.Rules.Action,
		"action must be ALLOW; an empty policy set under ALLOW is Envoy's deny-all")
	assert.Empty(t, rbac.Rules.Policies,
		"policy set must be empty to achieve deny-all semantics")

	// Also verify the config round-trips as valid JSON so we catch any
	// serialization bug that would silently drop the RBAC filter.
	raw, err := json.Marshal(listener)
	require.NoError(t, err)
	var roundTripped envoyListener
	require.NoError(t, json.Unmarshal(raw, &roundTripped),
		"listener must round-trip through JSON without error")
	rbacAfter := findRBACFilter(roundTripped)
	require.NotNil(t, rbacAfter,
		"RBAC filter must survive JSON round-trip; omitempty on empty map is the classic culprit")
}

// TestBuildIngressListener_PortAndHostGating verifies that buildIngressListener
// wires the upstream port, host-port binding, and virtual-host domain gating
// correctly for several input scenarios.
func TestBuildIngressListener_PortAndHostGating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		spec              proxySpec
		hostPort          int
		wantUpstreamRef   string // substring that must appear in the listener JSON
		wantHostPortBound int    // the listener's bind port
		wantDomains       []string
	}{
		{
			name: "sse transport binds hostPort and routes to upstream",
			spec: proxySpec{
				WorkloadName:  "myserver",
				UpstreamPort:  8080,
				TransportType: "sse",
				Permissions:   nil,
			},
			hostPort:          18080,
			wantUpstreamRef:   "myserver:8080",
			wantHostPortBound: 18080,
		},
		{
			name: "inbound AllowHost restricts virtual host domains",
			spec: proxySpec{
				WorkloadName:  "svc",
				UpstreamPort:  9090,
				TransportType: "streamable-http",
				Permissions: &permissions.NetworkPermissions{
					Inbound: &permissions.InboundNetworkPermissions{
						AllowHost: []string{"app.example.com"},
					},
				},
			},
			hostPort:          19090,
			wantUpstreamRef:   "svc:9090",
			wantHostPortBound: 19090,
			wantDomains:       []string{"app.example.com"},
		},
		{
			name: "nil permissions defaults to permissive localhost gating",
			spec: proxySpec{
				WorkloadName:  "tool",
				UpstreamPort:  7070,
				TransportType: "sse",
				Permissions:   nil,
			},
			hostPort:          17070,
			wantUpstreamRef:   "tool:7070",
			wantHostPortBound: 17070,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listener := buildIngressListener(tt.spec, tt.hostPort)

			// Serialize to JSON for substring assertions — this is simpler
			// than deeply navigating the typed structs and more resilient to
			// internal refactors that rename unexported fields.
			raw, err := json.Marshal(listener)
			require.NoError(t, err)
			s := string(raw)

			assert.Contains(t, s, tt.wantUpstreamRef,
				"listener config must reference upstream %s", tt.wantUpstreamRef)

			if tt.wantDomains != nil {
				for _, domain := range tt.wantDomains {
					assert.Contains(t, s, domain,
						"listener config must contain domain restriction %q", domain)
				}
			}

			// The listener must not be bound on port 0.
			assert.NotContains(t, s, `"port_value":0`,
				"listener must not bind on port 0")
		})
	}
}

// TestWriteEnvoyBootstrap_FileMode verifies that writeEnvoyBootstrap writes a
// valid JSON bootstrap file at mode 0600.
func TestWriteEnvoyBootstrap_FileMode(t *testing.T) {
	t.Parallel()

	b := envoyBootstrap{
		Admin: &envoyAdmin{
			Address: envoyAddress{
				SocketAddress: envoySocketAddress{
					Address:   "127.0.0.1",
					PortValue: 9901,
				},
			},
		},
		StaticResources: envoyStatic{},
	}

	path, err := writeEnvoyBootstrap(b)
	require.NoError(t, err)
	require.NotEmpty(t, path, "returned path must be non-empty")

	t.Cleanup(func() { _ = os.Remove(path) })

	// File must exist and be readable.
	info, err := os.Stat(path)
	require.NoError(t, err)

	// Mode must be 0600 — not 0644 — so that other processes cannot read the
	// bootstrap config (which may contain sensitive socket addresses).
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"bootstrap file must be written at mode 0600")

	// File must contain valid JSON that deserializes back into envoyBootstrap.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, data, "bootstrap file must not be empty")

	var roundTripped envoyBootstrap
	require.NoError(t, json.Unmarshal(data, &roundTripped),
		"bootstrap file must contain valid JSON")
}

// TestEnvoyAdmin_LoopbackOnly asserts that the admin block written by
// writeEnvoyBootstrap binds only on the loopback address and never on
// 0.0.0.0 or an empty address that would expose admin to all interfaces.
func TestEnvoyAdmin_LoopbackOnly(t *testing.T) {
	t.Parallel()

	b := envoyBootstrap{
		Admin: &envoyAdmin{
			Address: envoyAddress{
				SocketAddress: envoySocketAddress{
					Address:   "127.0.0.1",
					PortValue: 9901,
				},
			},
		},
		StaticResources: envoyStatic{},
	}

	path, err := writeEnvoyBootstrap(b)
	require.NoError(t, err)
	require.NotEmpty(t, path)
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(data)

	assert.Contains(t, s, "127.0.0.1",
		"admin address must be loopback 127.0.0.1")
	assert.NotContains(t, s, "0.0.0.0",
		"admin address must NOT bind on 0.0.0.0")
}

// TestGetEnvoyImage verifies that getEnvoyImage returns the default image when
// the override env var is unset and the override when it is set.
// NOTE: t.Setenv is used so t.Parallel() is intentionally omitted here — env
// mutations are global state and are incompatible with parallel execution.
func TestGetEnvoyImage(t *testing.T) {
	tests := []struct {
		name      string
		envValue  string
		wantImage string
		wantEnvoy bool // true: assert the result contains "envoy"
	}{
		{
			name:      "empty env returns non-empty default containing envoy",
			envValue:  "",
			wantEnvoy: true,
		},
		{
			name:      "custom image override is returned verbatim",
			envValue:  "my-custom-envoy:latest",
			wantImage: "my-custom-envoy:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_ENVOY_IMAGE", tt.envValue)

			got := getEnvoyImage()
			require.NotEmpty(t, got, "getEnvoyImage must never return an empty string")

			if tt.wantEnvoy {
				assert.Contains(t, got, "envoy",
					"default image must contain 'envoy' to be identifiable")
			}

			if tt.wantImage != "" {
				assert.Equal(t, tt.wantImage, got)
			}
		})
	}
}

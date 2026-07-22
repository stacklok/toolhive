// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
)

// Compile-time assertion: envoyProxy must satisfy networkProxy.
var _ networkProxy = (*envoyProxy)(nil)

// findRBACFilter walks the HTTP filters inside the first HCM in a listener's
// first filter chain and returns the first RBAC filter with action == "ALLOW".
// It returns nil if no matching filter is found.
func findRBACFilter(listener envoyListener) *envoyHTTPRBAC {
	if len(listener.FilterChains) == 0 {
		return nil
	}
	fc := listener.FilterChains[0]
	if len(fc.Filters) == 0 {
		return nil
	}
	hcm, ok := fc.Filters[0].TypedConfig.(*envoyHCM)
	if !ok {
		return nil
	}
	for _, f := range hcm.HTTPFilters {
		rbac, ok := f.TypedConfig.(*envoyHTTPRBAC)
		if ok && rbac.Rules.Action == "ALLOW" {
			return rbac
		}
	}
	return nil
}

// collectAuthorityRegexes walks every policy/permission in an RBAC filter
// (including or_rules) and returns all :authority safe_regex patterns.
func collectAuthorityRegexes(rbac *envoyHTTPRBAC) []string {
	var out []string
	var walk func(perms []envoyPermission)
	walk = func(perms []envoyPermission) {
		for _, p := range perms {
			if p.Header != nil && p.Header.Name == ":authority" &&
				p.Header.StringMatch != nil && p.Header.StringMatch.SafeRegex != nil {
				out = append(out, p.Header.StringMatch.SafeRegex.Regex)
			}
			if p.OrRules != nil {
				walk(p.OrRules.Rules)
			}
		}
	}
	for _, pol := range rbac.Rules.Policies {
		walk(pol.Permissions)
	}
	return out
}

// authorityMatched reports whether any of the given RE2 patterns matches the
// authority, replicating Envoy's full-string anchoring.
func authorityMatched(patterns []string, authority string) bool {
	for _, p := range patterns {
		if regexp.MustCompile("^(?:" + p + ")$").MatchString(authority) {
			return true
		}
	}
	return false
}

// findDenyRBACFilter returns the first RBAC filter with action == "DENY" from
// the HCM inside the listener's first filter chain.
func findDenyRBACFilter(listener envoyListener) *envoyHTTPRBAC {
	if len(listener.FilterChains) == 0 {
		return nil
	}
	fc := listener.FilterChains[0]
	if len(fc.Filters) == 0 {
		return nil
	}
	hcm, ok := fc.Filters[0].TypedConfig.(*envoyHCM)
	if !ok {
		return nil
	}
	for _, f := range hcm.HTTPFilters {
		rbac, ok := f.TypedConfig.(*envoyHTTPRBAC)
		if ok && rbac.Rules.Action == "DENY" {
			return rbac
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
				// --allow-docker-gateway: no DENY filter, and the ALLOW filter must
				// explicitly permit the gateway (omitting the deny alone is not
				// enough under a default-deny allowlist — see #2).
				assert.Nil(t, findDenyRBACFilter(listener),
					"no DENY RBAC filter should be present when AllowDockerGateway=true")
				allow := findRBACFilter(listener)
				require.NotNil(t, allow)
				pats := collectAuthorityRegexes(allow)
				assert.True(t, authorityMatched(pats, dockerDefaultBridgeGatewayIP),
					"ALLOW filter must permit the gateway IP when AllowDockerGateway=true")
				assert.True(t, authorityMatched(pats, dockerGatewayHostname),
					"ALLOW filter must permit host.docker.internal when AllowDockerGateway=true")
			}

			// wantGatewayDenyL3/L7: the DENY filter must block the gateway by
			// :authority — the IP literal (with/without port) AND the hostnames,
			// case-insensitively — without a destination_ip rule (inert for a
			// forward proxy). See #1/#4.
			if tt.wantGatewayDenyL3 || tt.wantGatewayDenyL7 {
				deny := findDenyRBACFilter(listener)
				require.NotNil(t, deny, "expected a DENY RBAC filter for the gateway")
				pats := collectAuthorityRegexes(deny)

				assert.True(t, authorityMatched(pats, dockerDefaultBridgeGatewayIP),
					"deny must match the gateway IP")
				assert.True(t, authorityMatched(pats, dockerDefaultBridgeGatewayIP+":8080"),
					"deny must match the gateway IP with a port (raw-IP bypass)")
				assert.True(t, authorityMatched(pats, dockerGatewayHostname),
					"deny must match host.docker.internal")
				assert.True(t, authorityMatched(pats, dockerAltGatewayHostname),
					"deny must match gateway.docker.internal")
				assert.True(t, authorityMatched(pats, "HOST.DOCKER.INTERNAL"),
					"deny must be case-insensitive (uppercase must not bypass)")
				assert.False(t, authorityMatched(pats, dockerGatewayHostname+".attacker.com"),
					"deny must be anchored (lookalike suffix must not match)")

				// The dead destination_ip L3 rule must be gone.
				raw, err := json.Marshal(listener)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "destination_ip",
					"destination_ip is inert for a forward proxy and must not be used")
			}
		})
	}
}

// TestHostMatchRegex verifies that AllowHost entries translate to :authority
// patterns matching Squid's dstdomain semantics: a leading dot (or "*.") matches
// the apex and all subdomains, a bare host matches exactly, and every form
// tolerates an optional ":port" (HTTPS CONNECT authorities carry the port).
// Crucially, no form may match a lookalike parent domain like
// "example.com.attacker.com".
func TestHostMatchRegex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		host      string
		matches   []string
		noMatches []string
	}{
		{
			name:      "exact host, no subdomains",
			host:      "example.com",
			matches:   []string{"example.com", "example.com:443", "EXAMPLE.COM"},
			noMatches: []string{"www.example.com", "notexample.com", "example.com.attacker.com"},
		},
		{
			name:      "leading dot matches apex and subdomains",
			host:      ".example.com",
			matches:   []string{"example.com", "www.example.com", "a.b.example.com", "www.example.com:8080"},
			noMatches: []string{"notexample.com", "example.com.attacker.com", "evil.com"},
		},
		{
			name:      "asterisk-dot is an alias for leading dot",
			host:      "*.example.com",
			matches:   []string{"example.com", "api.example.com", "api.example.com:443"},
			noMatches: []string{"attacker-example.com", "example.com.attacker.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Envoy anchors safe_regex fully; replicate that with ^...$ here.
			re := regexp.MustCompile("^(?:" + hostMatchRegex(tt.host) + ")$")
			for _, m := range tt.matches {
				assert.True(t, re.MatchString(m), "expected %q to match AllowHost %q", m, tt.host)
			}
			for _, nm := range tt.noMatches {
				assert.False(t, re.MatchString(nm), "expected %q NOT to match AllowHost %q", nm, tt.host)
			}
		})
	}
}

// TestBuildEgressListener_EmptyAllowHostDenyAll is a mandatory regression guard:
// buildEgressListener with an empty AllowHost and InsecureAllowAll=false must
// produce a listener where the RBAC filter is present with action=ALLOW and
// zero policies. This is Envoy's deny-all behavior. The test guards against a
// bug that silently omits the RBAC filter and produces allow-all.
//
// Note: a JSON round-trip assertion is intentionally omitted here. The
// envoyNetworkFilter.TypedConfig field is typed as any, so concrete pointer
// types (*envoyHTTPRBAC, etc.) do not survive JSON round-trip — the unmarshaled
// value becomes map[string]any. Behavioral correctness is verified directly on
// the Go struct returned by buildEgressListener.
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

	// Verify the config serialises to valid JSON.
	raw, err := json.Marshal(listener)
	require.NoError(t, err)
	assert.NotEmpty(t, raw, "serialized listener must not be empty")
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
			wantUpstreamRef:   "myserver",
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
			wantUpstreamRef:   "svc",
			wantHostPortBound: 19090,
			wantDomains:       []string{"app.example.com"},
		},
		{
			// No inbound AllowHost → wildcard virtual host. This is safe because
			// the ingress listener is bound to 127.0.0.1 on the host, so only the
			// local proxy runner can reach it regardless of the vhost domain.
			name: "nil permissions defaults to wildcard virtual host",
			spec: proxySpec{
				WorkloadName:  "tool",
				UpstreamPort:  7070,
				TransportType: "sse",
				Permissions:   nil,
			},
			hostPort:          17070,
			wantUpstreamRef:   "tool",
			wantHostPortBound: 17070,
			wantDomains:       []string{`"*"`},
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

// TestBuildIngressCluster_UpstreamAddress verifies that buildIngressCluster
// produces a STRICT_DNS cluster pointing at the correct workload address.
func TestBuildIngressCluster_UpstreamAddress(t *testing.T) {
	t.Parallel()

	spec := proxySpec{
		WorkloadName: "myserver",
		UpstreamPort: 8080,
	}

	cluster := buildIngressCluster(spec)

	assert.Equal(t, ingressClusterName, cluster.Name)
	assert.Equal(t, "STRICT_DNS", cluster.Type)
	require.NotNil(t, cluster.LoadAssignment)
	require.NotEmpty(t, cluster.LoadAssignment.Endpoints)
	require.NotEmpty(t, cluster.LoadAssignment.Endpoints[0].LBEndpoints)

	ep := cluster.LoadAssignment.Endpoints[0].LBEndpoints[0]
	assert.Equal(t, "myserver", ep.Endpoint.Address.SocketAddress.Address)
	assert.Equal(t, 8080, ep.Endpoint.Address.SocketAddress.PortValue)
}

// TestBuildEgressCluster_DFPConfig verifies that buildEgressCluster produces a
// dynamic_forward_proxy cluster with the expected configuration.
func TestBuildEgressCluster_DFPConfig(t *testing.T) {
	t.Parallel()

	cluster := buildEgressCluster()

	assert.Equal(t, dfpClusterName, cluster.Name)
	assert.Equal(t, "CLUSTER_PROVIDED", cluster.LbPolicy)
	require.NotNil(t, cluster.ClusterType)
	assert.Equal(t, "envoy.clusters.dynamic_forward_proxy", cluster.ClusterType.Name)

	dfp, ok := cluster.ClusterType.TypedConfig.(*envoyDFPClusterConfig)
	require.True(t, ok, "ClusterType.TypedConfig must be *envoyDFPClusterConfig")
	assert.Equal(t, typeDFPCluster, dfp.Type)
	assert.Equal(t, dfpCacheName, dfp.DNSCacheConfig.Name)
	assert.Equal(t, "V4_ONLY", dfp.DNSCacheConfig.DNSLookupFamily)
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
		StaticResources: envoyStaticResources{},
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
		StaticResources: envoyStaticResources{},
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

// TestEgressListenerHCMTypeURLs verifies that the egress listener serializes
// with correct protobuf @type URLs so Envoy can parse it.
func TestEgressListenerHCMTypeURLs(t *testing.T) {
	t.Parallel()

	spec := proxySpec{
		WorkloadName: "myserver",
		Permissions: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{
				AllowHost: []string{"example.com"},
			},
		},
		AllowDockerGateway: false,
		GatewayIP:          dockerDefaultBridgeGatewayIP,
	}

	listener := buildEgressListener(spec)
	raw, err := json.Marshal(listener)
	require.NoError(t, err)
	s := string(raw)

	assert.Contains(t, s, typeHCM, "@type for HCM must be present in serialized JSON")
	assert.Contains(t, s, typeHTTPRBAC, "@type for RBAC must be present in serialized JSON")
	assert.Contains(t, s, typeDFPFilter, "@type for DFP filter must be present in serialized JSON")
	assert.Contains(t, s, typeRouter, "@type for router must be present in serialized JSON")
}

// TestEgressClusterTypeURL verifies that the egress cluster serializes with the
// correct DFP cluster @type URL.
func TestEgressClusterTypeURL(t *testing.T) {
	t.Parallel()

	cluster := buildEgressCluster()
	raw, err := json.Marshal(cluster)
	require.NoError(t, err)
	s := string(raw)

	assert.Contains(t, s, typeDFPCluster,
		"@type for DFP cluster config must be present in serialized JSON")
}

// TestEnvoyBootstrap_ValidatesAgainstRealEnvoy asserts that the generated
// bootstrap is accepted by a real Envoy (`--mode validate`), across the deny,
// allow-gateway, and allowlist permutations. Hand-rolled protobuf-JSON can pass
// Go-level unit tests yet be rejected by Envoy (wrong @type, bad matcher shape),
// so this closes that gap. Skips when docker is unavailable.
func TestEnvoyBootstrap_ValidatesAgainstRealEnvoy(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available; skipping real-Envoy validation")
	}

	cases := []struct {
		name string
		spec proxySpec
	}{
		{
			name: "allowlist + gateway deny + ingress",
			spec: proxySpec{
				WorkloadName: "demo", TransportType: "streamable-http", UpstreamPort: 8080,
				GatewayIP: dockerDefaultBridgeGatewayIP,
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{AllowHost: []string{"example.com", ".github.com"}},
					Inbound:  &permissions.InboundNetworkPermissions{AllowHost: []string{"app.internal"}},
				},
			},
		},
		{
			name: "allow-docker-gateway + insecure-allow-all + stdio (egress only)",
			spec: proxySpec{
				WorkloadName: "demo", TransportType: "stdio", AllowDockerGateway: true,
				GatewayIP:   dockerDefaultBridgeGatewayIP,
				Permissions: &permissions.NetworkPermissions{Outbound: &permissions.OutboundNetworkPermissions{InsecureAllowAll: true}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			listeners := []envoyListener{buildEgressListener(tc.spec)}
			clusters := []envoyCluster{buildEgressCluster()}
			if tc.spec.TransportType != "stdio" {
				listeners = append(listeners, buildIngressListener(tc.spec, 18080))
				clusters = append(clusters, buildIngressCluster(tc.spec))
			}
			b := envoyBootstrap{
				Admin:           &envoyAdmin{Address: envoyAddress{SocketAddress: envoySocketAddress{Address: "127.0.0.1", PortValue: 9901}}},
				StaticResources: envoyStaticResources{Listeners: listeners, Clusters: clusters},
			}
			path, err := writeEnvoyBootstrap(b)
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.Remove(path) })

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			//nolint:gosec // G204: fixed args; path is a test-controlled temp file
			out, err := exec.CommandContext(ctx, "docker", "run", "--rm",
				"-v", path+":/etc/envoy/envoy.json:ro",
				defaultEnvoyImage, "--mode", "validate", "-c", "/etc/envoy/envoy.json").CombinedOutput()
			require.NoError(t, err, "envoy rejected the generated config:\n%s", out)
			assert.Contains(t, string(out), "configuration '/etc/envoy/envoy.json' OK")
		})
	}
}

// TestEnvoyProxy_SetupOrchestration covers the container-orchestration branch
// logic of SetupEgress/SetupIngress without a live daemon: the egress container
// is always created (named <name>-egress), a non-stdio workload reserves an
// ingress port that SetupIngress returns, and a stdio workload reserves none.
func TestEnvoyProxy_SetupOrchestration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		transport    string
		upstreamPort int
		wantIngress  bool
	}{
		{name: "non-stdio reserves an ingress port", transport: "streamable-http", upstreamPort: 8080, wantIngress: true},
		{name: "stdio reserves no ingress port", transport: "stdio", upstreamPort: 0, wantIngress: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var createdName string
			api := &fakeDockerAPI{
				createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, name string) (container.CreateResponse, error) {
					createdName = name
					return container.CreateResponse{ID: "cid-envoy"}, nil
				},
				startFunc: func(_ context.Context, _ string, _ mobyclient.ContainerStartOptions) error { return nil },
			}
			c := &Client{
				api:          api,
				imageManager: &fakeImageManager{availableImages: map[string]struct{}{defaultEnvoyImage: {}}},
			}
			e := &envoyProxy{client: c}

			spec := proxySpec{
				WorkloadName:  "app",
				TransportType: tt.transport,
				UpstreamPort:  tt.upstreamPort,
				GatewayIP:     dockerDefaultBridgeGatewayIP,
				Endpoints:     map[string]*network.EndpointSettings{},
			}

			egress, err := e.SetupEgress(t.Context(), spec)
			require.NoError(t, err)
			assert.Equal(t, "app-egress", createdName, "envoy container must reuse the -egress name")
			assert.Equal(t, "http://app-egress:3128", egress.EnvVars["HTTP_PROXY"])

			ingressPort, err := e.SetupIngress(t.Context(), spec, egress)
			require.NoError(t, err)
			if tt.wantIngress {
				assert.Positive(t, ingressPort, "non-stdio must reserve an ingress port")
				assert.Equal(t, egress.ingressPort, ingressPort, "SetupIngress must return the port reserved in SetupEgress")
			} else {
				assert.Zero(t, ingressPort, "stdio must not reserve an ingress port")
			}
		})
	}
}

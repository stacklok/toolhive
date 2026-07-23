// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
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

// listenerAllowsAuthority returns true when the ALLOW RBAC filter in the given
// listener would permit a request with the given :authority value.
func listenerAllowsAuthority(listener envoyListener, authority string) bool {
	rbac := findRBACFilter(listener)
	if rbac == nil {
		return false
	}
	for _, policy := range rbac.Rules.Policies {
		allMatch := true
		for _, perm := range policy.Permissions {
			if !permMatchesAuthority(perm, authority) {
				allMatch = false
				break
			}
		}
		if allMatch {
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
				assert.True(t, authorityMatched(pats, dockerGatewayHostname+"."),
					"deny must match a trailing-dot FQDN (host.docker.internal.)")
				assert.True(t, authorityMatched(pats, dockerDefaultBridgeGatewayIP+"."),
					"deny must match a trailing-dot gateway IP (172.17.0.1.)")
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
			name: "exact host, no subdomains",
			host: "example.com",
			// A trailing-dot FQDN resolves identically and must match, or it becomes
			// a bypass on the deny path.
			matches:   []string{"example.com", "example.com:443", "EXAMPLE.COM", "example.com.", "example.com.:443"},
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
			// Inbound.AllowHost is intentionally ignored for Envoy's ingress virtual
			// host domains. The transparent proxy rewrites Host to include the port
			// ("127.0.0.1:19090"), which would not match a bare hostname list. The
			// inbound access restriction is enforced by the 127.0.0.1 port binding.
			name: "inbound AllowHost is ignored — ingress always uses wildcard domain",
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
			wantDomains:       []string{`"*"`},
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
	// V4_ONLY prevents slow AAAA lookup timeouts when IPv6 is unavailable.
	// A regression dropping this would fail silently (extra latency only).
	assert.Equal(t, "V4_ONLY", cluster.DnsLookupFamily,
		"ingress STRICT_DNS cluster must use V4_ONLY to avoid IPv6 lookup timeouts")
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

	// Mode must be 0644: world-readable so the Envoy distroless container
	// (UID 101) can read the bind-mounted file on Linux Docker Engine, where
	// strict POSIX permissions prevent a different UID from reading 0600 files.
	// The bootstrap contains no secrets, so world-readable is safe.
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(),
		"bootstrap file must be written at mode 0644")

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
			// Always include the transparent listener and original_dst cluster.
			listeners = append(listeners, buildTransparentListener(tc.spec))
			clusters = append(clusters, buildOriginalDstCluster())
			b := envoyBootstrap{
				Admin:           &envoyAdmin{Address: envoyAddress{SocketAddress: envoySocketAddress{Address: "127.0.0.1", PortValue: 9901}}},
				StaticResources: envoyStaticResources{Listeners: listeners, Clusters: clusters},
			}
			cfg, err := json.Marshal(b)
			require.NoError(t, err)

			// Pass the config inline via --config-yaml (JSON is valid YAML) rather
			// than bind-mounting a file: the bootstrap is written 0600 and envoy
			// runs as nonroot, so a bind-mounted file is unreadable to the
			// container on native-Linux CI. Inline config sidesteps that entirely.
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			//nolint:gosec // G204: fixed args; cfg is test-generated JSON
			out, err := exec.CommandContext(ctx, "docker", "run", "--rm",
				defaultEnvoyImage, "--mode", "validate", "--config-yaml", string(cfg)).CombinedOutput()
			require.NoError(t, err, "envoy rejected the generated config:\n%s", out)
			assert.Contains(t, string(out), "configuration '' OK")
		})
	}
}

// TestBuildTransparentListener verifies that buildTransparentListener produces a
// listener with transparent:true, the correct network RBAC filter sequence, and
// a tcp_proxy filter routing to original_dst_cluster. Uses table-driven cases to
// cover nil permissions (deny-all) and AllowPort configurations.
func TestBuildTransparentListener(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		spec                proxySpec
		wantTransparent     bool
		wantDenyGateway     bool // DENY filter must have a "gateway" policy
		wantAllowPolicies   []string
		wantEmptyAllow      bool // ALLOW filter must have empty policies (deny-all)
		wantAllowPortKeys   []string
		wantEnvoyInternalIP string // for gateway DENY check
	}{
		{
			name: "nil permissions produces deny-all ALLOW and gateway DENY",
			spec: proxySpec{
				WorkloadName: "srv",
				GatewayIP:    dockerDefaultBridgeGatewayIP,
				Permissions:  nil,
			},
			wantTransparent: true,
			wantDenyGateway: true,
			wantEmptyAllow:  true,
		},
		{
			name: "AllowPort list produces port policies in ALLOW filter",
			spec: proxySpec{
				WorkloadName: "srv",
				GatewayIP:    dockerDefaultBridgeGatewayIP,
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						AllowPort: []int{443, 5432},
					},
				},
			},
			wantTransparent:   true,
			wantDenyGateway:   true,
			wantAllowPortKeys: []string{"port-443", "port-5432"},
		},
		{
			name: "InsecureAllowAll produces single wildcard policy in ALLOW filter",
			spec: proxySpec{
				WorkloadName: "srv",
				GatewayIP:    dockerDefaultBridgeGatewayIP,
				Permissions: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						InsecureAllowAll: true,
					},
				},
			},
			wantTransparent:   true,
			wantDenyGateway:   true,
			wantAllowPolicies: []string{"allow-all"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listener := buildTransparentListener(tt.spec)

			// transparent:true must appear in the serialized JSON.
			raw, err := json.Marshal(listener)
			require.NoError(t, err)
			s := string(raw)
			if tt.wantTransparent {
				assert.Contains(t, s, `"transparent":true`,
					"transparent listener must serialize transparent:true")
			}

			// There must be exactly one filter chain with at least 3 filters.
			require.Len(t, listener.FilterChains, 1)
			filters := listener.FilterChains[0].Filters
			require.GreaterOrEqual(t, len(filters), 3,
				"transparent filter chain must have at least DENY RBAC, ALLOW RBAC, and TCP proxy")

			// First filter: network RBAC DENY
			denyRBAC, ok := filters[0].TypedConfig.(*envoyNetworkRBAC)
			require.True(t, ok, "first network filter must be *envoyNetworkRBAC")
			assert.Equal(t, "DENY", denyRBAC.Rules.Action)
			assert.Equal(t, typeNetworkRBAC, denyRBAC.Type)

			if tt.wantDenyGateway && tt.spec.GatewayIP != "" {
				gatewayPolicy, present := denyRBAC.Rules.Policies["gateway"]
				assert.True(t, present, "DENY filter must have a 'gateway' policy")
				if present {
					require.NotEmpty(t, gatewayPolicy.Permissions)
					require.NotNil(t, gatewayPolicy.Permissions[0].DestinationIP,
						"gateway DENY permission must use destination_ip (L3 rule)")
					assert.Equal(t, tt.spec.GatewayIP,
						gatewayPolicy.Permissions[0].DestinationIP.AddressPrefix,
						"gateway DENY must match the gateway IP")
					assert.Equal(t, uint32(32),
						gatewayPolicy.Permissions[0].DestinationIP.PrefixLen,
						"gateway DENY must use /32 for exact-host matching")
				}
			}

			// Second filter: network RBAC ALLOW
			allowRBAC, ok := filters[1].TypedConfig.(*envoyNetworkRBAC)
			require.True(t, ok, "second network filter must be *envoyNetworkRBAC")
			assert.Equal(t, "ALLOW", allowRBAC.Rules.Action)
			assert.Equal(t, typeNetworkRBAC, allowRBAC.Type)
			// policies must not be nil — only absent field is allow-all; {} is deny-all
			assert.NotNil(t, allowRBAC.Rules.Policies,
				"ALLOW filter policies must not be nil (nil would serialize as null, not {})")

			if tt.wantEmptyAllow {
				assert.Empty(t, allowRBAC.Rules.Policies,
					"nil permissions must produce empty policy map (Envoy deny-all)")
			}
			for _, key := range tt.wantAllowPolicies {
				_, present := allowRBAC.Rules.Policies[key]
				assert.True(t, present, "expected ALLOW policy %q", key)
			}
			for _, portKey := range tt.wantAllowPortKeys {
				pol, present := allowRBAC.Rules.Policies[portKey]
				assert.True(t, present, "expected ALLOW port policy %q", portKey)
				if present {
					require.NotEmpty(t, pol.Permissions)
					require.NotNil(t, pol.Permissions[0].DestinationPort,
						"port policy must use destination_port")
					assert.Equal(t, pol.Permissions[0].DestinationPort.Start,
						pol.Permissions[0].DestinationPort.End,
						"exact port match requires Start == End")
				}
			}

			// Third filter: TCP proxy targeting original_dst_cluster.
			tcpProxy, ok := filters[2].TypedConfig.(*envoyTCPProxy)
			require.True(t, ok, "third network filter must be *envoyTCPProxy")
			assert.Equal(t, typeTCPProxy, tcpProxy.Type)
			assert.Equal(t, originalDstClusterName, tcpProxy.Cluster,
				"tcp_proxy must route to original_dst_cluster")

			// Verify the allow-all policy carries Any:true when present.
			if allowAllPol, ok := allowRBAC.Rules.Policies["allow-all"]; ok {
				require.NotEmpty(t, allowAllPol.Permissions)
				require.NotNil(t, allowAllPol.Permissions[0].Any)
				assert.True(t, *allowAllPol.Permissions[0].Any,
					"allow-all policy must use Any:true permission")
			}
		})
	}
}

// TestBuildOriginalDstCluster verifies that buildOriginalDstCluster produces an
// ORIGINAL_DST cluster with CLUSTER_PROVIDED lb_policy.
func TestBuildOriginalDstCluster(t *testing.T) {
	t.Parallel()

	cluster := buildOriginalDstCluster()

	assert.Equal(t, originalDstClusterName, cluster.Name)
	assert.Equal(t, "ORIGINAL_DST", cluster.Type,
		"original_dst cluster must use ORIGINAL_DST discovery type")
	assert.Equal(t, "CLUSTER_PROVIDED", cluster.LbPolicy,
		"ORIGINAL_DST cluster requires CLUSTER_PROVIDED lb_policy")
	assert.NotEmpty(t, cluster.ConnectTimeout,
		"connect_timeout must be set")

	// Verify JSON serialization contains the required fields.
	raw, err := json.Marshal(cluster)
	require.NoError(t, err)
	s := string(raw)
	assert.Contains(t, s, `"ORIGINAL_DST"`)
	assert.Contains(t, s, `"CLUSTER_PROVIDED"`)
	assert.Contains(t, s, originalDstClusterName)
}

// TestEnvoyProxy_SetupOrchestration covers the container-orchestration branch
// logic of SetupEgress/SetupIngress without a live daemon. SetupEgress must NOT
// create any container — it only returns env vars. SetupIngress creates the
// Envoy container (named <name>-egress) and returns the ingress port (0 for
// stdio).
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

			// SetupEgress must NOT create the container — it only returns env vars.
			// Container creation is deferred to SetupIngress so the MCP hostname
			// resolves on first probe (see #5922).
			egress, err := e.SetupEgress(t.Context(), spec)
			require.NoError(t, err)
			assert.Empty(t, createdName, "SetupEgress must not create the container")
			assert.Equal(t, "http://app-egress:3128", egress.EnvVars["HTTP_PROXY"])

			// SetupIngress creates the container and returns the ingress port.
			ingressPort, err := e.SetupIngress(t.Context(), spec, egress)
			require.NoError(t, err)
			assert.Equal(t, "app-egress", createdName, "SetupIngress must create the -egress container")
			if tt.wantIngress {
				assert.Positive(t, ingressPort, "non-stdio must reserve an ingress port")
			} else {
				assert.Zero(t, ingressPort, "stdio must not reserve an ingress port")
			}
		})
	}
}

// TestListenerTimeoutsDisabledForStreaming guards against Envoy's 15s default
// RouteAction.timeout (a hard total-response cap) and 5m stream_idle_timeout
// truncating long-lived MCP streams (SSE / streamable-http). Both the ingress
// reverse-proxy route and the egress plain-HTTP route must disable the route
// timeout, and both HCMs must disable the idle timeout.
func TestListenerTimeoutsDisabledForStreaming(t *testing.T) {
	t.Parallel()

	spec := proxySpec{
		WorkloadName:  "app",
		TransportType: "streamable-http",
		UpstreamPort:  8080,
		GatewayIP:     dockerDefaultBridgeGatewayIP,
	}

	hcmOf := func(l envoyListener) *envoyHCM {
		hcm, ok := l.FilterChains[0].Filters[0].TypedConfig.(*envoyHCM)
		require.True(t, ok, "first network filter must be the HCM")
		return hcm
	}
	// routeTimeouts returns the Timeout of every route action in an HCM.
	routeTimeouts := func(hcm *envoyHCM) []string {
		var out []string
		for _, vh := range hcm.RouteConfig.VirtualHosts {
			for _, r := range vh.Routes {
				if r.Route != nil {
					out = append(out, r.Route.Timeout)
				}
			}
		}
		return out
	}

	ingress := hcmOf(buildIngressListener(spec, 18080))
	assert.Equal(t, "0s", ingress.StreamIdleTimeout, "ingress HCM must disable the idle timeout")
	for _, tmo := range routeTimeouts(ingress) {
		assert.Equal(t, "0s", tmo, "ingress route must disable the 15s total-response cap")
	}

	egress := hcmOf(buildEgressListener(spec))
	assert.Equal(t, "0s", egress.StreamIdleTimeout, "egress HCM must disable the idle timeout")
	// The plain-HTTP egress route must disable the cap; the CONNECT route is
	// unaffected by the default (no end-of-stream while tunneling) so it need not.
	var sawPlainHTTP bool
	for _, vh := range egress.RouteConfig.VirtualHosts {
		for _, r := range vh.Routes {
			if r.Match.Prefix == "/" && r.Route != nil {
				sawPlainHTTP = true
				assert.Equal(t, "0s", r.Route.Timeout, "egress plain-HTTP route must disable the 15s cap")
			}
		}
	}
	assert.True(t, sawPlainHTTP, "egress must have a plain-HTTP route")
}

// TestBuildAllowlistPolicies_AllowPort verifies that AllowPort entries are
// translated into combined host+port regex matchers, matching Squid's
// allowed_ports ACL behaviour including plain-HTTP bare-hostname support.
// Tests cover:
//   - AllowPort alone (any host on listed ports, bare hostname allowed when port 80 in list)
//   - AllowHost + AllowPort (both must match; bare hostname allowed when port 80 in list)
//   - AllowHost without AllowPort (any port, original behaviour)
//   - InsecureAllowAll ignores port constraints
func TestBuildAllowlistPolicies_AllowPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  *permissions.OutboundNetworkPermissions
		// authority strings that must be allowed / denied
		allowed []string
		denied  []string
	}{
		{
			name: "AllowPort only — any host on listed ports; bare hostname allowed when port 80 listed",
			out:  &permissions.OutboundNetworkPermissions{AllowPort: []int{80, 443}},
			// Bare "example.com" = plain HTTP = port 80 → allowed
			allowed: []string{"example.com:80", "anything.io:443", "example.com"},
			denied:  []string{"example.com:8080", "example.com:22"},
		},
		{
			name: "AllowHost + AllowPort — host AND port must match",
			out: &permissions.OutboundNetworkPermissions{
				AllowHost: []string{"example.com"},
				AllowPort: []int{443},
			},
			allowed: []string{"example.com:443"},
			denied: []string{
				"example.com:80", // right host, wrong port
				"other.com:443",  // right port, wrong host
				"other.com:80",   // neither
			},
		},
		{
			name: "AllowHost without AllowPort — any port allowed",
			out: &permissions.OutboundNetworkPermissions{
				AllowHost: []string{"example.com"},
			},
			allowed: []string{"example.com:443", "example.com:8080", "example.com:22"},
			denied:  []string{"other.com:443"},
		},
		{
			name:    "InsecureAllowAll ignores AllowPort",
			out:     &permissions.OutboundNetworkPermissions{InsecureAllowAll: true, AllowPort: []int{443}},
			allowed: []string{"example.com:80", "anything.io:9999"},
			denied:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := proxySpec{
				WorkloadName: "app",
				GatewayIP:    dockerDefaultBridgeGatewayIP,
				Permissions:  &permissions.NetworkPermissions{Outbound: tt.out},
			}
			listener := buildEgressListener(spec)
			for _, a := range tt.allowed {
				assert.True(t, listenerAllowsAuthority(listener, a),
					"authority %q should be allowed", a)
			}
			for _, a := range tt.denied {
				assert.False(t, listenerAllowsAuthority(listener, a),
					"authority %q should be denied", a)
			}
		})
	}
}

// permMatchesAuthority tests a single envoyPermission against an authority
// string, replicating Envoy's full-string anchoring for safe_regex and
// exact/suffix/prefix string matches.
func permMatchesAuthority(perm envoyPermission, authority string) bool {
	if perm.Any != nil && *perm.Any {
		return true
	}
	if perm.Header != nil && perm.Header.Name == ":authority" && perm.Header.StringMatch != nil {
		sm := perm.Header.StringMatch
		switch {
		case sm.SafeRegex != nil:
			return regexp.MustCompile("^(?:" + sm.SafeRegex.Regex + ")$").MatchString(authority)
		case sm.Exact != "":
			return authority == sm.Exact
		case sm.Suffix != "":
			return strings.HasSuffix(authority, sm.Suffix)
		case sm.Prefix != "":
			return strings.HasPrefix(authority, sm.Prefix)
		}
	}
	if perm.OrRules != nil {
		for _, r := range perm.OrRules.Rules {
			if permMatchesAuthority(r, authority) {
				return true
			}
		}
	}
	return false
}

// TestBuildAllowlistPolicies_AllowPort_Structure guards against a wrong
// permMatchesAuthority re-implementation masking a config regression: it checks
// the actual RBAC policy shape (single SafeRegex combining host + port) rather
// than just whether the helper says the authority is allowed or denied (F6).
func TestBuildAllowlistPolicies_AllowPort_Structure(t *testing.T) {
	t.Parallel()
	spec := proxySpec{
		WorkloadName: "app",
		GatewayIP:    dockerDefaultBridgeGatewayIP,
		Permissions: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{
				AllowHost: []string{"example.com"},
				AllowPort: []int{80, 443},
			},
		},
	}
	listener := buildEgressListener(spec)
	rbac := findRBACFilter(listener)
	require.NotNil(t, rbac)
	policy, ok := rbac.Rules.Policies["example.com"]
	require.True(t, ok, "expected policy keyed 'example.com'")
	// Must have exactly 1 permission: a single SafeRegex encoding both host and port.
	require.Len(t, policy.Permissions, 1,
		"AllowHost+AllowPort policy must have exactly 1 combined safe_regex permission")
	perm := policy.Permissions[0]
	require.NotNil(t, perm.Header, "permission must be a header matcher")
	require.NotNil(t, perm.Header.StringMatch, "permission must have a string match")
	require.NotNil(t, perm.Header.StringMatch.SafeRegex, "permission must use safe_regex")
	// The regex must actually match the expected authorities — including bare
	// hostname (plain HTTP) because port 80 is listed. Evaluating the full regex
	// here rather than inspecting its text guards against a reimplementation that
	// produces a different but equivalent string (N2: the previous assertion of
	// assert.Contains(re,"?") was vacuous because "?" appears in non-capturing groups).
	re := perm.Header.StringMatch.SafeRegex.Regex
	compile := func(r string) *regexp.Regexp { return regexp.MustCompile("^(?:" + r + ")$") }
	assert.True(t, compile(re).MatchString("example.com"), "bare hostname must match (plain HTTP, port 80 implied)")
	assert.True(t, compile(re).MatchString("example.com:80"), "explicit port 80 must match")
	assert.True(t, compile(re).MatchString("example.com:443"), "explicit port 443 must match")
	assert.False(t, compile(re).MatchString("example.com:8080"), "non-listed port must not match")
	assert.False(t, compile(re).MatchString("other.com:443"), "wrong host must not match")
}

// TestBuildAllowlistPolicies_AdditionalCases covers edge cases not in the main
// AllowPort table (F7): multi-port OR shape, port-less authority divergence from
// Squid, and gateway-deny ordering under AllowPort-only (F8).
func TestBuildAllowlistPolicies_AdditionalCases(t *testing.T) {
	t.Parallel()

	// F7a: AllowHost + multi-port — authority with a non-listed port is denied.
	t.Run("AllowHost+multi-port denies non-listed port", func(t *testing.T) {
		t.Parallel()
		spec := proxySpec{
			WorkloadName: "app",
			GatewayIP:    dockerDefaultBridgeGatewayIP,
			Permissions: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{80, 443},
				},
			},
		}
		l := buildEgressListener(spec)
		assert.True(t, listenerAllowsAuthority(l, "example.com:443"), "port 443 must be allowed")
		assert.True(t, listenerAllowsAuthority(l, "example.com:80"), "port 80 must be allowed")
		assert.False(t, listenerAllowsAuthority(l, "example.com:8080"), "port 8080 must be denied")
		assert.False(t, listenerAllowsAuthority(l, "other.com:443"), "wrong host must be denied")
	})

	// F7b: Port-less authority (plain HTTP default port) — Squid parity.
	// Plain HTTP omits the port from :authority ("example.com" not "example.com:80").
	// When port 80 is in AllowPort the port suffix is made optional so both the
	// bare form and the explicit ":80" form are accepted, matching Squid behaviour.
	t.Run("port-less authority is allowed when port 80 is in AllowPort (Squid parity)", func(t *testing.T) {
		t.Parallel()
		spec := proxySpec{
			WorkloadName: "app",
			GatewayIP:    dockerDefaultBridgeGatewayIP,
			Permissions: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{80},
				},
			},
		}
		l := buildEgressListener(spec)
		// Bare hostname (plain HTTP) must be allowed when port 80 is listed.
		assert.True(t, listenerAllowsAuthority(l, "example.com"),
			"port-less authority must be allowed when port 80 is in AllowPort (plain HTTP parity with Squid)")
		assert.True(t, listenerAllowsAuthority(l, "example.com:80"),
			"explicit port 80 must be allowed")
		assert.False(t, listenerAllowsAuthority(l, "example.com:443"),
			"port 443 must not be allowed when only port 80 is listed")
		// Bare hostname must be denied when port 80 is NOT in the list.
		spec443 := proxySpec{
			WorkloadName: "app",
			GatewayIP:    dockerDefaultBridgeGatewayIP,
			Permissions: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{443},
				},
			},
		}
		l443 := buildEgressListener(spec443)
		assert.False(t, listenerAllowsAuthority(l443, "example.com"),
			"port-less authority must be denied when port 80 is NOT in AllowPort")
		assert.True(t, listenerAllowsAuthority(l443, "example.com:443"),
			"explicit port 443 must be allowed")
	})

	// F8: Gateway-deny DENY filter is evaluated before the ALLOW filter even
	// when AllowPort-only is configured (deny-before-allow ordering).
	t.Run("gateway-deny precedes AllowPort-only allow", func(t *testing.T) {
		t.Parallel()
		spec := proxySpec{
			WorkloadName: "app",
			GatewayIP:    dockerDefaultBridgeGatewayIP,
			Permissions: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowPort: []int{443},
				},
			},
		}
		l := buildEgressListener(spec)
		// The gateway deny filter must precede the allow filter.
		deny := findDenyRBACFilter(l)
		require.NotNil(t, deny, "DENY filter must be present")
		hcm := l.FilterChains[0].Filters[0].TypedConfig.(*envoyHCM)
		var denyIdx, allowIdx int
		for i, f := range hcm.HTTPFilters {
			if rbac, ok := f.TypedConfig.(*envoyHTTPRBAC); ok {
				if rbac.Rules.Action == "DENY" {
					denyIdx = i
				} else {
					allowIdx = i
				}
			}
		}
		assert.Less(t, denyIdx, allowIdx, "DENY filter must appear before ALLOW filter")
		// Gateway IP must be denied even though port 443 is in AllowPort.
		gatewayPats := collectAuthorityRegexes(deny)
		assert.True(t, authorityMatched(gatewayPats, dockerDefaultBridgeGatewayIP+":443"),
			"gateway IP on port 443 must be caught by the DENY filter")
	})

	// N3: AllowPort-only with port 443 (no port 80) — bare hostname denied,
	// explicit :443 allowed. Exercises the includes80=false branch of anyHostPortRegex.
	t.Run("AllowPort-only port-443 — bare hostname denied, explicit port allowed", func(t *testing.T) {
		t.Parallel()
		spec := proxySpec{
			WorkloadName: "app",
			GatewayIP:    dockerDefaultBridgeGatewayIP,
			Permissions: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowPort: []int{443},
				},
			},
		}
		l := buildEgressListener(spec)
		assert.True(t, listenerAllowsAuthority(l, "anything.io:443"), "port 443 must be allowed")
		assert.False(t, listenerAllowsAuthority(l, "anything.io"), "bare hostname must be denied (port 80 not listed)")
		assert.False(t, listenerAllowsAuthority(l, "anything.io:80"), "port 80 must be denied")
	})

	// N4: Wildcard host + AllowPort — the subdomain (.*\.)? path in hostPortMatchRegex.
	t.Run("wildcard AllowHost + AllowPort matches subdomains on listed ports only", func(t *testing.T) {
		t.Parallel()
		spec := proxySpec{
			WorkloadName: "app",
			GatewayIP:    dockerDefaultBridgeGatewayIP,
			Permissions: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"*.example.com"},
					AllowPort: []int{443},
				},
			},
		}
		l := buildEgressListener(spec)
		assert.True(t, listenerAllowsAuthority(l, "api.example.com:443"), "subdomain on allowed port must match")
		assert.True(t, listenerAllowsAuthority(l, "example.com:443"), "apex on allowed port must match")
		assert.False(t, listenerAllowsAuthority(l, "api.example.com:80"), "subdomain on disallowed port must not match")
		assert.False(t, listenerAllowsAuthority(l, "api.example.com"), "bare subdomain must not match (port 80 not listed)")
		assert.False(t, listenerAllowsAuthority(l, "other.com:443"), "non-matching host must not match")
	})
}

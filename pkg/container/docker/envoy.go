// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"k8s.io/utils/ptr"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// defaultEnvoyImage is pinned by tag+digest for supply-chain integrity.
	// Override with TOOLHIVE_ENVOY_IMAGE (accepts any docker pull reference).
	defaultEnvoyImage = "envoyproxy/envoy-distroless:v1.32.3@sha256:375aab0d80b3c0e1b42a776b4cb1743ed79012032051d2da19cbc93ea884fb81"

	// Protobuf type URLs required by Envoy's protobuf-JSON bootstrap format.
	// Every typed_config field must carry an @type URL or Envoy will reject the
	// config on startup.
	typeHCM        = "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
	typeHTTPRBAC   = "type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC"
	typeDFPFilter  = "type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig"
	typeRouter     = "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"
	typeDFPCluster = "type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig"

	// typeNetworkRBAC is the protobuf @type URL for the network-layer RBAC filter.
	// Unlike typeHTTPRBAC (which operates on HTTP headers inside HCM), this filter
	// operates at the TCP/L3 level and is placed directly in an envoyFilterChain.
	typeNetworkRBAC = "type.googleapis.com/envoy.extensions.filters.network.rbac.v3.RBAC"

	// typeTCPProxy is the protobuf @type URL for the TCP proxy filter, which
	// forwards raw TCP connections to an upstream cluster.
	typeTCPProxy = "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy"

	// dfpCacheName is the shared DNS cache config name used by both the DFP
	// HTTP filter and the DFP cluster extension.
	dfpCacheName = "dynamic_forward_proxy_cache_config"

	// dfpClusterName is the cluster referenced by the egress route config.
	dfpClusterName = "dynamic_forward_proxy_cluster"

	// ingressClusterName is the cluster referenced by the ingress route config.
	ingressClusterName = "ingress_upstream"

	// originalDstClusterName is the cluster used by the transparent listener to
	// forward connections to their original destination via SO_ORIGINAL_DST.
	originalDstClusterName = "original_dst_cluster"

	// defaultTransparentPort is the port Envoy's transparent listener binds on
	// inside the Envoy container when spec.TransparentPort is not set.
	defaultTransparentPort = 15001
)

func getEnvoyImage() string {
	if img := os.Getenv("TOOLHIVE_ENVOY_IMAGE"); img != "" {
		return img
	}
	return defaultEnvoyImage
}

// ── Bootstrap ────────────────────────────────────────────────────────────────

// envoyBootstrap is the top-level Envoy bootstrap configuration.
// The admin interface is intentionally omitted: Envoy does not start an admin
// server when the field is absent, eliminating the attack surface at runtime.
type envoyBootstrap struct {
	StaticResources envoyStaticResources `json:"static_resources"`
}

// envoyStaticResources holds the static listeners and clusters.
type envoyStaticResources struct {
	Listeners []envoyListener `json:"listeners,omitempty"`
	Clusters  []envoyCluster  `json:"clusters,omitempty"`
}

// ── Address types ────────────────────────────────────────────────────────────

// envoyAddress wraps a socket address for Envoy config.
type envoyAddress struct {
	SocketAddress envoySocketAddress `json:"socket_address"`
}

// envoySocketAddress is an IP + port pair used throughout Envoy config.
type envoySocketAddress struct {
	Address   string `json:"address"`
	PortValue int    `json:"port_value"`
}

// ── Listener / filter chain ──────────────────────────────────────────────────

// envoyListener is an Envoy listener binding on a socket address.
type envoyListener struct {
	Name    string       `json:"name"`
	Address envoyAddress `json:"address"`
	// Transparent, when true, enables SO_ORIGINAL_DST on accepted sockets so the
	// transparent listener can recover the original destination address overwritten
	// by iptables DNAT. Must be true for the original_dst_cluster to work correctly.
	Transparent  bool               `json:"transparent,omitempty"`
	FilterChains []envoyFilterChain `json:"filter_chains"`
}

// envoyFilterChain is a sequence of network-level filters applied to matching
// connections.
type envoyFilterChain struct {
	Filters []envoyNetworkFilter `json:"filters"`
}

// envoyNetworkFilter is a named network filter whose typed config is an
// arbitrary protobuf-JSON object (e.g. envoyHCM). The any type lets us embed
// concrete structs directly so that JSON serialisation includes @type.
type envoyNetworkFilter struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// ── HttpConnectionManager ───────────────────────────────────────────────────

// envoyHCM is the typed config for
// envoy.filters.network.http_connection_manager.
type envoyHCM struct {
	Type       string           `json:"@type"`
	StatPrefix string           `json:"stat_prefix"`
	AccessLog  []envoyAccessLog `json:"access_log,omitempty"`
	// StreamIdleTimeout caps how long a stream may be idle. Envoy defaults it to
	// 5m, which would reap a sparse-but-open SSE stream; "0s" disables it.
	StreamIdleTimeout string               `json:"stream_idle_timeout,omitempty"`
	UpgradeConfigs    []envoyUpgradeConfig `json:"upgrade_configs,omitempty"`
	HTTPFilters       []envoyHTTPFilter    `json:"http_filters"`
	RouteConfig       *envoyRouteConfig    `json:"route_config,omitempty"`
}

// envoyAccessLog configures request access logging for an HCM.
type envoyAccessLog struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// envoyStdoutAccessLog is the typed config for envoy.access_loggers.stdout.
type envoyStdoutAccessLog struct {
	Type string `json:"@type"`
}

const typeStdoutAccessLog = "type.googleapis.com/envoy.extensions.access_loggers.stream.v3.StdoutAccessLog"

func stdoutAccessLog() []envoyAccessLog {
	return []envoyAccessLog{{
		Name:        "envoy.access_loggers.stdout",
		TypedConfig: &envoyStdoutAccessLog{Type: typeStdoutAccessLog},
	}}
}

// envoyUpgradeConfig enables HTTP upgrade protocols (e.g. CONNECT) in HCM.
type envoyUpgradeConfig struct {
	UpgradeType   string              `json:"upgrade_type"`
	ConnectConfig *envoyConnectConfig `json:"connect_config,omitempty"`
}

// envoyHTTPFilter is a named HTTP-layer filter embedded inside HCM.
type envoyHTTPFilter struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// ── RBAC ─────────────────────────────────────────────────────────────────────

// envoyHTTPRBAC is the typed config for envoy.filters.http.rbac.
type envoyHTTPRBAC struct {
	Type  string         `json:"@type"`
	Rules envoyRBACRules `json:"rules"`
}

// envoyRBACRules holds the RBAC action and policy map.
// CRITICAL: Policies must NOT have omitempty. An empty map serializes as {} and
// is intentional — an absent field would silently turn deny-all into allow-all.
type envoyRBACRules struct {
	Action   string                     `json:"action"`
	Policies map[string]envoyRBACPolicy `json:"policies"`
}

// envoyRBACPolicy pairs permissions with principals for a single RBAC policy.
type envoyRBACPolicy struct {
	Permissions []envoyPermission `json:"permissions"`
	Principals  []envoyPrincipal  `json:"principals"`
}

// envoyPermission matches a request by various criteria. Exactly one field
// should be set per permission entry.
type envoyPermission struct {
	Any     *bool               `json:"any,omitempty"`
	Header  *envoyHeaderMatcher `json:"header,omitempty"`
	OrRules *envoyOrRules       `json:"or_rules,omitempty"`
}

// envoyOrRules composes multiple permissions with logical OR.
type envoyOrRules struct {
	Rules []envoyPermission `json:"rules"`
}

// envoyHeaderMatcher matches an HTTP header value.
type envoyHeaderMatcher struct {
	Name        string            `json:"name"`
	StringMatch *envoyStringMatch `json:"string_match,omitempty"`
}

// envoyStringMatch matches a string by exact value, prefix, suffix, or regex.
type envoyStringMatch struct {
	Exact     string          `json:"exact,omitempty"`
	Prefix    string          `json:"prefix,omitempty"`
	Suffix    string          `json:"suffix,omitempty"`
	SafeRegex *envoySafeRegex `json:"safe_regex,omitempty"`
}

// envoySafeRegex is an RE2 regex matcher. Envoy anchors the pattern fully
// (implicit ^ and $), so the pattern must match the entire input.
type envoySafeRegex struct {
	Regex string `json:"regex"`
}

// envoyPrincipal matches a downstream principal. Any:true is a wildcard.
type envoyPrincipal struct {
	Any bool `json:"any"`
}

// ── Network RBAC (L3/L4) ─────────────────────────────────────────────────────

// envoyNetworkRBAC is the typed config for envoy.filters.network.rbac.
// It operates at the TCP level — distinct from envoyHTTPRBAC which lives inside
// an HCM and matches HTTP headers. Network RBAC matches on IP/port tuples and
// is placed directly in an envoyFilterChain, not inside an HCM.
//
// In the transparent listener, destination_ip correctly reflects the upstream's
// real address (via SO_ORIGINAL_DST), unlike the forward-proxy listener where
// destination_ip resolves to Envoy's own socket and is inert.
type envoyNetworkRBAC struct {
	Type       string                `json:"@type"`
	StatPrefix string                `json:"stat_prefix"`
	Rules      envoyNetworkRBACRules `json:"rules"`
}

// envoyNetworkRBACRules holds the action and policy map for a network RBAC filter.
// CRITICAL: Policies must NOT have omitempty. An empty map serializes as {} and
// is intentional — an absent field would silently turn deny-all into allow-all
// under the ALLOW action.
type envoyNetworkRBACRules struct {
	Action   string                        `json:"action"`
	Policies map[string]envoyNetworkPolicy `json:"policies"`
}

// envoyNetworkPolicy pairs L3/L4 permissions with principals for a single
// network RBAC policy entry.
type envoyNetworkPolicy struct {
	Permissions []envoyNetworkPermission `json:"permissions"`
	Principals  []envoyPrincipal         `json:"principals"`
}

// envoyNetworkPermission is an L3/L4 permission matching on destination IP or
// port. Different from envoyPermission which uses HTTP header matchers. Exactly
// one field should be set per permission entry.
type envoyNetworkPermission struct {
	// DestinationIP matches the connection's destination CIDR range. For the
	// transparent listener, this is the real upstream address (via SO_ORIGINAL_DST),
	// making it effective for gateway-deny rules.
	DestinationIP *envoyCIDRRange `json:"destination_ip,omitempty"`
	// DestinationPort matches the connection's destination port. A PortRange with
	// Start == End matches a single exact port.
	DestinationPort *envoyPortRange `json:"destination_port,omitempty"`
	// OrRules composes multiple network permissions with logical OR.
	OrRules *envoyNetworkOrRules `json:"or_rules,omitempty"`
	// Any, when true, matches all connections (wildcard permission).
	Any *bool `json:"any,omitempty"`
}

// envoyCIDRRange is a CIDR IP range matcher used by network RBAC destination_ip.
type envoyCIDRRange struct {
	AddressPrefix string `json:"address_prefix"`
	PrefixLen     uint32 `json:"prefix_len"`
}

// envoyPortRange matches a destination port range. For exact port matching,
// set Start == End. Envoy's network RBAC permission.destination_port maps to
// envoy.config.rbac.v3.Permission.destination_port (type: PortRange).
type envoyPortRange struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
}

// envoyNetworkOrRules composes multiple network permissions with logical OR.
type envoyNetworkOrRules struct {
	Rules []envoyNetworkPermission `json:"rules"`
}

// ── TCP proxy ─────────────────────────────────────────────────────────────────

// envoyTCPProxy is the typed config for envoy.filters.network.tcp_proxy.
// It forwards raw TCP connections to the named upstream cluster. Used in the
// transparent listener's filter chain to route allowed connections to
// original_dst_cluster after the network RBAC filters have made their decision.
type envoyTCPProxy struct {
	Type       string `json:"@type"`
	StatPrefix string `json:"stat_prefix"`
	Cluster    string `json:"cluster"`
}

// ── DFP filter ───────────────────────────────────────────────────────────────

// envoyDFPFilter is the typed config for
// envoy.filters.http.dynamic_forward_proxy.
type envoyDFPFilter struct {
	Type           string        `json:"@type"`
	DNSCacheConfig envoyDNSCache `json:"dns_cache_config"`
}

// envoyDNSCache is the shared DNS cache config referenced by both the DFP
// HTTP filter and the DFP cluster extension.
type envoyDNSCache struct {
	Name            string `json:"name"`
	DNSLookupFamily string `json:"dns_lookup_family"`
}

// ── Router ────────────────────────────────────────────────────────────────────

// envoyRouter is the typed config for envoy.filters.http.router.
type envoyRouter struct {
	Type string `json:"@type"`
}

// ── Route config ─────────────────────────────────────────────────────────────

// envoyRouteConfig holds the list of virtual hosts for a listener.
type envoyRouteConfig struct {
	VirtualHosts []envoyVirtualHost `json:"virtual_hosts"`
}

// envoyVirtualHost matches requests by domain and dispatches to a cluster.
type envoyVirtualHost struct {
	Name    string       `json:"name"`
	Domains []string     `json:"domains"`
	Routes  []envoyRoute `json:"routes"`
}

// envoyRoute matches a request prefix and forwards it to a cluster.
type envoyRoute struct {
	Match envoyRouteMatch   `json:"match"`
	Route *envoyRouteAction `json:"route,omitempty"`
}

// envoyRouteMatch matches incoming requests by URI prefix or CONNECT method.
// Exactly one of Prefix or ConnectMatcher must be set.
type envoyRouteMatch struct {
	Prefix         string               `json:"prefix,omitempty"`
	ConnectMatcher *envoyConnectMatcher `json:"connect_matcher,omitempty"`
}

// envoyConnectMatcher matches HTTP CONNECT requests (used for HTTPS tunneling).
type envoyConnectMatcher struct{}

// envoyRouteAction forwards matched requests to an upstream cluster.
type envoyRouteAction struct {
	Cluster string `json:"cluster"`
	// Timeout is the route's total request→response cap. Envoy defaults it to
	// 15s, which truncates long-lived MCP streams (SSE, streamable-http); set
	// "0s" to disable it for proxied traffic.
	Timeout        string               `json:"timeout,omitempty"`
	UpgradeConfigs []envoyUpgradeConfig `json:"upgrade_configs,omitempty"`
}

// envoyConnectConfig is the per-route CONNECT tunnel configuration.
type envoyConnectConfig struct{}

// ── Cluster ───────────────────────────────────────────────────────────────────

// envoyCluster is an Envoy upstream cluster definition.
type envoyCluster struct {
	Name           string `json:"name"`
	ConnectTimeout string `json:"connect_timeout"`
	LbPolicy       string `json:"lb_policy,omitempty"`
	Type           string `json:"type,omitempty"`
	// DnsLookupFamily restricts DNS resolution to IPv4 only. Without this,
	// STRICT_DNS clusters attempt AAAA lookups first, which adds latency in
	// environments where IPv6 is unavailable or times out.
	DnsLookupFamily string               `json:"dns_lookup_family,omitempty"`
	ClusterType     *envoyClusterType    `json:"cluster_type,omitempty"`
	LoadAssignment  *envoyLoadAssignment `json:"load_assignment,omitempty"`
}

// envoyClusterType is the custom cluster discovery extension (e.g. DFP).
type envoyClusterType struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// envoyDFPClusterConfig is the typed config for
// envoy.clusters.dynamic_forward_proxy.
type envoyDFPClusterConfig struct {
	Type           string        `json:"@type"`
	DNSCacheConfig envoyDNSCache `json:"dns_cache_config"`
}

// envoyLoadAssignment is an EDS-style static load assignment.
type envoyLoadAssignment struct {
	ClusterName string          `json:"cluster_name"`
	Endpoints   []envoyEndpoint `json:"endpoints"`
}

// envoyEndpoint is a group of LB endpoints.
type envoyEndpoint struct {
	LBEndpoints []envoyLBEndpoint `json:"lb_endpoints"`
}

// envoyLBEndpoint is a single upstream endpoint.
type envoyLBEndpoint struct {
	Endpoint envoyEndpointAddress `json:"endpoint"`
}

// envoyEndpointAddress wraps an address for an LB endpoint.
type envoyEndpointAddress struct {
	Address envoyAddress `json:"address"`
}

// ── Builder functions ─────────────────────────────────────────────────────────

// buildEgressListener builds the Envoy listener config for outbound traffic.
//
// The resulting HCM HTTP filter chain is:
//  1. (when !spec.AllowDockerGateway) RBAC DENY on gateway IP and hostnames
//  2. RBAC ALLOW allowlist — empty policies map is Envoy's deny-all
//  3. dynamic_forward_proxy HTTP filter
//  4. router
//
// CONNECT upgrades are enabled so that HTTPS CONNECT tunnels pass through.
func buildEgressListener(spec proxySpec) envoyListener {
	httpFilters := buildEgressHTTPFilters(spec)

	hcm := &envoyHCM{
		Type:              typeHCM,
		StatPrefix:        "egress_http",
		AccessLog:         stdoutAccessLog(),
		StreamIdleTimeout: "0s", // don't reap sparse long-lived outbound streams
		UpgradeConfigs:    []envoyUpgradeConfig{{UpgradeType: "CONNECT"}},
		HTTPFilters:       httpFilters,
		RouteConfig: &envoyRouteConfig{
			VirtualHosts: []envoyVirtualHost{
				{
					Name:    "local_service",
					Domains: []string{"*"},
					Routes: []envoyRoute{
						// CONNECT match must come first: handles HTTPS tunneling.
						// Without this, CONNECT requests don't match prefix "/" and get 404.
						{
							Match: envoyRouteMatch{ConnectMatcher: &envoyConnectMatcher{}},
							Route: &envoyRouteAction{
								Cluster: dfpClusterName,
								UpgradeConfigs: []envoyUpgradeConfig{
									{UpgradeType: "CONNECT", ConnectConfig: &envoyConnectConfig{}},
								},
							},
						},
						// Prefix match handles plain HTTP requests. Timeout "0s"
						// disables Envoy's 15s default so long-lived plain-HTTP
						// outbound streams aren't truncated (CONNECT tunnels are
						// unaffected by the default, but disabling is harmless there).
						{
							Match: envoyRouteMatch{Prefix: "/"},
							Route: &envoyRouteAction{Cluster: dfpClusterName, Timeout: "0s"},
						},
					},
				},
			},
		},
	}

	return envoyListener{
		Name: fmt.Sprintf("%s-egress", spec.WorkloadName),
		Address: envoyAddress{
			SocketAddress: envoySocketAddress{
				Address:   "0.0.0.0",
				PortValue: 3128,
			},
		},
		FilterChains: []envoyFilterChain{
			{
				Filters: []envoyNetworkFilter{
					{
						Name:        "envoy.filters.network.http_connection_manager",
						TypedConfig: hcm,
					},
				},
			},
		},
	}
}

// buildEgressHTTPFilters constructs the ordered list of HTTP filters for the
// egress HCM. Gateway deny rules (when !spec.AllowDockerGateway) are placed
// first so they are evaluated before the allowlist.
func buildEgressHTTPFilters(spec proxySpec) []envoyHTTPFilter {
	var filters []envoyHTTPFilter

	if !spec.AllowDockerGateway {
		filters = append(filters, envoyHTTPFilter{
			Name:        "envoy.filters.http.rbac",
			TypedConfig: buildGatewayDenyRBAC(spec.GatewayIP),
		})
	}

	filters = append(filters,
		envoyHTTPFilter{
			Name:        "envoy.filters.http.rbac",
			TypedConfig: buildAllowlistRBAC(spec),
		},
		envoyHTTPFilter{
			Name: "envoy.filters.http.dynamic_forward_proxy",
			TypedConfig: &envoyDFPFilter{
				Type: typeDFPFilter,
				DNSCacheConfig: envoyDNSCache{
					Name:            dfpCacheName,
					DNSLookupFamily: "V4_ONLY",
				},
			},
		},
		envoyHTTPFilter{
			Name:        "envoy.filters.http.router",
			TypedConfig: &envoyRouter{Type: typeRouter},
		},
	)

	return filters
}

// buildGatewayDenyRBAC builds an RBAC DENY filter that blocks the Docker bridge
// gateway — both the resolved gateway IP and the Docker-internal hostnames —
// matched on the :authority header. This filter must precede the allowlist.
//
// Matching is on :authority (not destination_ip): for a forward proxy the RBAC
// destination_ip resolves to Envoy's own listener socket, not the proxied
// target, so an L3 CIDR rule never matches the upstream. The forward-proxy
// target is carried in :authority for both plain HTTP and HTTPS CONNECT, so
// authority matching — including the gateway IP literal — is what actually
// blocks it (mirroring Squid's `dst`/`dstdomain` denies).
func buildGatewayDenyRBAC(gatewayIP string) *envoyHTTPRBAC {
	return &envoyHTTPRBAC{
		Type: typeHTTPRBAC,
		Rules: envoyRBACRules{
			Action: "DENY",
			Policies: map[string]envoyRBACPolicy{
				"gateway": {
					Permissions: gatewayAuthorityPermissions(gatewayIP),
					Principals:  []envoyPrincipal{{Any: true}},
				},
			},
		},
	}
}

// gatewayAuthorityPermissions returns RBAC permissions that match the Docker
// gateway on the :authority header — the resolved gateway IP literal plus the
// two Docker-internal hostnames. Each is matched with hostMatchRegex, so the
// match is anchored, case-insensitive (HOST.DOCKER.INTERNAL cannot bypass it),
// and tolerant of an optional :port. Shared by the deny path (gateway blocked)
// and the allow path (--allow-docker-gateway).
func gatewayAuthorityPermissions(gatewayIP string) []envoyPermission {
	patterns := []string{
		hostMatchRegex(gatewayIP),
		hostMatchRegex(dockerGatewayHostname),
		hostMatchRegex(dockerAltGatewayHostname),
	}
	rules := make([]envoyPermission, 0, len(patterns))
	for _, re := range patterns {
		rules = append(rules, envoyPermission{
			Header: &envoyHeaderMatcher{
				Name:        ":authority",
				StringMatch: &envoyStringMatch{SafeRegex: &envoySafeRegex{Regex: re}},
			},
		})
	}
	return []envoyPermission{{OrRules: &envoyOrRules{Rules: rules}}}
}

// buildAllowlistRBAC builds an RBAC ALLOW filter encoding the outbound
// allowlist. An empty Policies map is Envoy's deny-all under ALLOW action.
func buildAllowlistRBAC(spec proxySpec) *envoyHTTPRBAC {
	return &envoyHTTPRBAC{
		Type: typeHTTPRBAC,
		Rules: envoyRBACRules{
			Action:   "ALLOW",
			Policies: buildAllowlistPolicies(spec),
		},
	}
}

// buildAllowlistPolicies returns the policy map for the egress RBAC ALLOW
// filter. An empty map (deny-all) is returned when:
//   - spec.Permissions is nil
//   - spec.Permissions.Outbound is nil
//
// InsecureAllowAll produces a single wildcard policy. Each AllowHost entry
// becomes a policy matching the :authority header via hostMatchRegex (Squid
// dstdomain semantics). When AllowPort is also set, each host policy AND-s a
// port-suffix permission so that host AND port must both match — mirroring
// Squid's "allowed_ports AND allowed_dsts" combination.
//
// Known divergence from Squid: plain-HTTP requests omit the port from
// :authority (e.g. "example.com" not "example.com:80"). A port-suffix matcher
// for ":80" will not match the portless authority, so AllowPort:[80] with plain
// HTTP is more restrictive than Squid's allowed_ports ACL. This is fail-closed
// (over-blocks) rather than a bypass.
//
// When spec.AllowDockerGateway is set, an explicit ALLOW policy for the gateway
// is added: omitting the deny filter alone is not enough, because the allowlist
// is default-deny, so the gateway would still be blocked under a non-permissive
// profile. This mirrors Squid, which emits explicit allow rules for the flag.
func buildAllowlistPolicies(spec proxySpec) map[string]envoyRBACPolicy {
	policies := make(map[string]envoyRBACPolicy)

	// --allow-docker-gateway: grant the gateway explicitly (see doc comment).
	// Harmless under InsecureAllowAll (allow-all already covers it).
	if spec.AllowDockerGateway {
		policies["gateway"] = envoyRBACPolicy{
			Permissions: gatewayAuthorityPermissions(spec.GatewayIP),
			Principals:  []envoyPrincipal{{Any: true}},
		}
	}

	if spec.Permissions == nil || spec.Permissions.Outbound == nil {
		return policies
	}
	out := spec.Permissions.Outbound
	if out.InsecureAllowAll {
		policies["allow-all"] = envoyRBACPolicy{
			Permissions: []envoyPermission{{Any: ptr.To(true)}},
			Principals:  []envoyPrincipal{{Any: true}},
		}
		return policies
	}
	if len(out.AllowHost) == 0 && len(out.AllowPort) > 0 {
		// AllowPort only — any host, but only on the listed ports.
		// Use a single regex so both the explicit-port ("host:80") and the
		// bare-hostname ("host", implying port 80) forms are handled correctly.
		policies["any-host-allowed-ports"] = envoyRBACPolicy{
			Permissions: []envoyPermission{{
				Header: &envoyHeaderMatcher{
					Name:        ":authority",
					StringMatch: &envoyStringMatch{SafeRegex: &envoySafeRegex{Regex: anyHostPortRegex(out.AllowPort)}},
				},
			}},
			Principals: []envoyPrincipal{{Any: true}},
		}
		return policies
	}

	for _, host := range out.AllowHost {
		var regex string
		if len(out.AllowPort) > 0 {
			// AllowHost + AllowPort: build a single regex that encodes both the
			// host pattern and the allowed ports. Using a combined regex rather
			// than separate AND-d permissions means plain-HTTP requests (where
			// :authority omits the default port 80) are handled correctly — a
			// bare "example.com" is permitted when port 80 is in the list.
			regex = hostPortMatchRegex(host, out.AllowPort)
		} else {
			// AllowHost only — any port permitted.
			regex = hostMatchRegex(host)
		}
		policies[host] = envoyRBACPolicy{
			Permissions: []envoyPermission{{
				Header: &envoyHeaderMatcher{
					Name:        ":authority",
					StringMatch: &envoyStringMatch{SafeRegex: &envoySafeRegex{Regex: regex}},
				},
			}},
			Principals: []envoyPrincipal{{Any: true}},
		}
	}
	return policies
}

// portGroupRegex builds the ":(?:port1|port2|…)" alternation for a list of
// allowed ports. Returns (group, includes80) where includes80 indicates whether
// port 80 is present (caller uses this to decide whether the port suffix is
// optional). Shared by hostPortMatchRegex and anyHostPortRegex.
func portGroupRegex(ports []int) (group string, includes80 bool) {
	alts := make([]string, 0, len(ports))
	for _, p := range ports {
		alts = append(alts, strconv.Itoa(p))
		if p == 80 {
			includes80 = true
		}
	}
	return ":(?:" + strings.Join(alts, "|") + ")", includes80
}

// hostBasePattern returns the escaped, case-insensitive host portion of an
// :authority regex — without any trailing port group. It mirrors hostMatchRegex
// semantics: leading dot / *. = subdomain wildcard, optional trailing dot.
// Shared by hostPortMatchRegex and hostMatchRegex.
func hostBasePattern(host string) string {
	subdomains := false
	switch {
	case strings.HasPrefix(host, "*."):
		host = host[2:]
		subdomains = true
	case strings.HasPrefix(host, "."):
		host = host[1:]
		subdomains = true
	}
	p := regexp.QuoteMeta(host)
	if subdomains {
		p = `(.*\.)?` + p
	}
	return p
}

// hostPortMatchRegex builds a single anchored RE2 pattern that encodes both a
// host allowlist entry and a port allowlist, so that AllowHost+AllowPort can be
// expressed as one safe_regex permission rather than AND-ing two separate matchers.
//
// Port 80 is treated as the HTTP default: when 80 is in the ports list the port
// suffix is optional, so a bare "example.com" authority (plain HTTP) is permitted
// alongside "example.com:80". For all other ports the port suffix is required.
//
// The host portion mirrors hostMatchRegex semantics (leading dot / *. = subdomain
// wildcard, case-insensitive, optional trailing dot, no port in the base pattern).
func hostPortMatchRegex(host string, ports []int) string {
	portGroup, includes80 := portGroupRegex(ports)
	hostPattern := hostBasePattern(host)
	if includes80 {
		// Port is optional: bare "example.com" counts as port 80.
		return `(?i)` + hostPattern + `\.?(` + portGroup + `)?`
	}
	// Port is required: bare hostname is not accepted.
	return `(?i)` + hostPattern + `\.?` + portGroup
}

// anyHostPortRegex builds a regex matching any hostname on the given ports.
// When port 80 is in the list the port suffix is optional (bare hostname implies
// port 80 for plain HTTP). For other ports the port suffix is required.
//
// [^:]+ matches any hostname without an explicit port; IPv6 bracket addresses
// (which contain colons) are outside the supported scope for this proxy backend.
func anyHostPortRegex(ports []int) string {
	portGroup, includes80 := portGroupRegex(ports)
	if includes80 {
		return `(?i)[^:]+(` + portGroup + `)?`
	}
	return `(?i)[^:]+` + portGroup
}

// hostMatchRegex builds an anchored, case-insensitive RE2 pattern that matches
// an AllowHost entry against the HTTP :authority header. It tolerates an
// optional ":port" suffix because HTTPS CONNECT authorities include the port
// (e.g. "example.com:443"), and it mirrors Squid's dstdomain semantics:
//
//   - ".example.com" or "*.example.com" — matches the apex "example.com" AND
//     any subdomain ("www.example.com", "a.b.example.com").
//   - "example.com" — matches that host exactly (no subdomains).
//
// The domain is regex-escaped so dots are literal, and the pattern is anchored
// by Envoy so it cannot match "example.com.attacker.com".
func hostMatchRegex(host string) string {
	pattern := hostBasePattern(host)
	// (?i) case-insensitive (hostnames are); \.? tolerates a trailing-dot FQDN
	// ("host.docker.internal." resolves identically and would otherwise bypass a
	// deny); (:[0-9]+)? optional port.
	return `(?i)` + pattern + `\.?(:[0-9]+)?`
}

// buildEgressCluster returns the dynamic_forward_proxy cluster required by the
// egress listener's route config.
func buildEgressCluster() envoyCluster {
	return envoyCluster{
		Name:           dfpClusterName,
		ConnectTimeout: "10s",
		LbPolicy:       "CLUSTER_PROVIDED",
		ClusterType: &envoyClusterType{
			Name: "envoy.clusters.dynamic_forward_proxy",
			TypedConfig: &envoyDFPClusterConfig{
				Type: typeDFPCluster,
				DNSCacheConfig: envoyDNSCache{
					Name:            dfpCacheName,
					DNSLookupFamily: "V4_ONLY",
				},
			},
		},
	}
}

// buildTransparentListener returns an Envoy listener for transparent TCP
// interception. It binds on 0.0.0.0:transparentPort with transparent:true,
// which enables SO_ORIGINAL_DST so each accepted socket carries the real
// upstream destination address (as recovered via conntrack from the iptables
// DNAT rule installed by SetupTransparent).
//
// The filter chain has three network filters in order:
//  1. Network RBAC DENY — blocks traffic to the Docker gateway by destination_ip
//     CIDR. SO_ORIGINAL_DST makes this accurate (unlike the forward-proxy listener
//     where destination_ip resolves to Envoy's own socket).
//  2. Network RBAC ALLOW — permits traffic to configured destinations by port.
//     Empty policies map is Envoy's deny-all under the ALLOW action.
//  3. TCP proxy — forwards allowed connections to original_dst_cluster, which
//     routes them to their original destinations using SO_ORIGINAL_DST.
func buildTransparentListener(spec proxySpec) envoyListener {
	tport := transparentPort(spec)
	filters := buildTransparentNetworkFilters(spec)

	return envoyListener{
		Name: fmt.Sprintf("%s-transparent", spec.WorkloadName),
		Address: envoyAddress{
			SocketAddress: envoySocketAddress{
				Address:   "0.0.0.0",
				PortValue: tport,
			},
		},
		Transparent: true,
		FilterChains: []envoyFilterChain{
			{Filters: filters},
		},
	}
}

// buildTransparentNetworkFilters constructs the ordered network filter list for
// the transparent listener's single filter chain. The order is:
//  1. DENY gateway by destination_ip (effective here: SO_ORIGINAL_DST gives the
//     real upstream address, not Envoy's own socket as in the forward proxy).
//  2. ALLOW by port (empty map = Envoy deny-all under ALLOW action).
//  3. TCP proxy forwarding to original_dst_cluster.
func buildTransparentNetworkFilters(spec proxySpec) []envoyNetworkFilter {
	return []envoyNetworkFilter{
		{
			Name:        "envoy.filters.network.rbac",
			TypedConfig: buildTransparentGatewayDenyRBAC(spec.GatewayIP),
		},
		{
			Name:        "envoy.filters.network.rbac",
			TypedConfig: buildTransparentAllowRBAC(spec),
		},
		{
			Name: "envoy.filters.network.tcp_proxy",
			TypedConfig: &envoyTCPProxy{
				Type:       typeTCPProxy,
				StatPrefix: "transparent",
				Cluster:    originalDstClusterName,
			},
		},
	}
}

// buildTransparentGatewayDenyRBAC builds a network-layer RBAC DENY filter that
// blocks connections whose destination_ip matches the Docker bridge gateway CIDR.
// This is correct for the transparent listener (unlike the forward-proxy listener)
// because SO_ORIGINAL_DST provides the actual upstream address, not Envoy's own
// socket address.
func buildTransparentGatewayDenyRBAC(gatewayIP string) *envoyNetworkRBAC {
	var gatewayPolicies map[string]envoyNetworkPolicy
	if gatewayIP != "" {
		gatewayPolicies = map[string]envoyNetworkPolicy{
			"gateway": {
				Permissions: []envoyNetworkPermission{
					{
						DestinationIP: &envoyCIDRRange{
							AddressPrefix: gatewayIP,
							PrefixLen:     32,
						},
					},
				},
				Principals: []envoyPrincipal{{Any: true}},
			},
		}
	} else {
		gatewayPolicies = map[string]envoyNetworkPolicy{}
	}

	return &envoyNetworkRBAC{
		Type:       typeNetworkRBAC,
		StatPrefix: "transparent_gateway_deny",
		Rules: envoyNetworkRBACRules{
			Action:   "DENY",
			Policies: gatewayPolicies,
		},
	}
}

// buildTransparentAllowRBAC builds a network-layer RBAC ALLOW filter for the
// transparent listener. An empty Policies map is Envoy's deny-all under ALLOW.
//
// When spec.Permissions.Outbound.AllowPort is non-empty, one policy per port is
// added using destination_port with Start==End for exact matching. When AllowPort
// is absent or permissions are nil, the empty map produces deny-all semantics,
// blocking all outbound TCP.
func buildTransparentAllowRBAC(spec proxySpec) *envoyNetworkRBAC {
	policies := buildTransparentAllowPolicies(spec)
	return &envoyNetworkRBAC{
		Type:       typeNetworkRBAC,
		StatPrefix: "transparent_allow",
		Rules: envoyNetworkRBACRules{
			Action:   "ALLOW",
			Policies: policies,
		},
	}
}

// buildTransparentAllowPolicies returns the policy map for the transparent
// listener's ALLOW RBAC filter. An empty map (deny-all) is returned when:
//   - spec.Permissions is nil
//   - spec.Permissions.Outbound is nil
//   - spec.Permissions.Outbound.AllowPort is empty
//
// InsecureAllowAll produces a single wildcard policy permitting all connections.
// Each AllowPort entry produces a policy matching that exact destination port.
func buildTransparentAllowPolicies(spec proxySpec) map[string]envoyNetworkPolicy {
	policies := make(map[string]envoyNetworkPolicy)
	if spec.Permissions == nil || spec.Permissions.Outbound == nil {
		return policies
	}
	out := spec.Permissions.Outbound
	if out.InsecureAllowAll {
		boolTrue := true
		policies["allow-all"] = envoyNetworkPolicy{
			Permissions: []envoyNetworkPermission{{Any: &boolTrue}},
			Principals:  []envoyPrincipal{{Any: true}},
		}
		return policies
	}
	for _, port := range out.AllowPort {
		p := uint32(port) //nolint:gosec // G115: port is always a valid port number
		portKey := fmt.Sprintf("port-%d", port)
		policies[portKey] = envoyNetworkPolicy{
			Permissions: []envoyNetworkPermission{
				{DestinationPort: &envoyPortRange{Start: p, End: p}},
			},
			Principals: []envoyPrincipal{{Any: true}},
		}
	}
	return policies
}

// buildOriginalDstCluster returns the Envoy cluster that routes connections to
// their original destination, read from SO_ORIGINAL_DST via conntrack. This
// cluster is referenced by the transparent listener's tcp_proxy filter.
//
// ORIGINAL_DST type requires lb_policy: CLUSTER_PROVIDED — Envoy reads the
// destination from the socket metadata (set by SO_ORIGINAL_DST) rather than
// load-balancing across a static endpoint list.
func buildOriginalDstCluster() envoyCluster {
	return envoyCluster{
		Name:           originalDstClusterName,
		Type:           "ORIGINAL_DST",
		ConnectTimeout: "30s",
		LbPolicy:       "CLUSTER_PROVIDED",
	}
}

// buildIngressListener builds the Envoy listener config for inbound (ingress)
// traffic. It binds on 0.0.0.0:hostPort inside the container so that Docker's
// port forwarding (host:127.0.0.1:<hostPort> → container:<hostPort>) can reach
// it. The host-side HostIP:"127.0.0.1" in the port binding provides the
// localhost-only restriction; the container-side address must be 0.0.0.0 or
// Docker's bridge forwarding cannot deliver traffic to the listener.
//
// The virtual host domain is always "*". Inbound host filtering is enforced
// by the egress RBAC `:authority` matcher (hostMatchRegex), not the ingress
// virtual host list — the transparent proxy sends "127.0.0.1:<port>" as the
// Host header (port included), which would not match bare hostnames in a
// domain list.
func buildIngressListener(spec proxySpec, hostPort int) envoyListener {
	domains := ingressDomains(spec)

	hcm := &envoyHCM{
		Type:       typeHCM,
		StatPrefix: "ingress_http",
		AccessLog:  stdoutAccessLog(),
		// Disable the idle timer so a sparse-but-open SSE stream isn't reaped.
		StreamIdleTimeout: "0s",
		HTTPFilters: []envoyHTTPFilter{
			{
				Name:        "envoy.filters.http.router",
				TypedConfig: &envoyRouter{Type: typeRouter},
			},
		},
		RouteConfig: &envoyRouteConfig{
			VirtualHosts: []envoyVirtualHost{
				{
					Name:    "ingress_service",
					Domains: domains,
					Routes: []envoyRoute{
						{
							Match: envoyRouteMatch{Prefix: "/"},
							// Timeout "0s" disables Envoy's 15s default total-response
							// cap, which would otherwise truncate SSE / streamable-http
							// MCP streams. This is the primary transport path.
							Route: &envoyRouteAction{Cluster: ingressClusterName, Timeout: "0s"},
						},
					},
				},
			},
		},
	}

	return envoyListener{
		Name: fmt.Sprintf("%s-ingress", spec.WorkloadName),
		Address: envoyAddress{
			SocketAddress: envoySocketAddress{
				Address:   "0.0.0.0", // must be 0.0.0.0; Docker port forwarding targets the bridge IP, not container loopback
				PortValue: hostPort,
			},
		},
		FilterChains: []envoyFilterChain{
			{
				Filters: []envoyNetworkFilter{
					{
						Name:        "envoy.filters.network.http_connection_manager",
						TypedConfig: hcm,
					},
				},
			},
		},
	}
}

// ingressDomains returns the virtual host domain list for the ingress listener.
// Always returns a wildcard: the Inbound.AllowHost list contains bare hostnames
// (e.g. "localhost", "127.0.0.1") but the transparent proxy sends a Host header
// with a port suffix ("127.0.0.1:22354"), which Envoy would not match against
// a bare hostname. The inbound access restriction is instead enforced by the
// host-side port binding to 127.0.0.1, which already limits the ingress to
// local connections only.
func ingressDomains(_ proxySpec) []string {
	return []string{"*"}
}

// buildIngressCluster returns the STRICT_DNS upstream cluster for the ingress
// listener, pointing at spec.WorkloadName:spec.UpstreamPort.
func buildIngressCluster(spec proxySpec) envoyCluster {
	return envoyCluster{
		Name:            ingressClusterName,
		ConnectTimeout:  "10s",
		Type:            "STRICT_DNS",
		DnsLookupFamily: "V4_ONLY",
		LoadAssignment: &envoyLoadAssignment{
			ClusterName: ingressClusterName,
			Endpoints: []envoyEndpoint{
				{
					LBEndpoints: []envoyLBEndpoint{
						{
							Endpoint: envoyEndpointAddress{
								Address: envoyAddress{
									SocketAddress: envoySocketAddress{
										Address:   spec.WorkloadName,
										PortValue: spec.UpstreamPort,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// writeEnvoyBootstrap marshals b to JSON and writes it to a temporary file at
// mode 0600, returning the path. On success the file must outlive this call —
// the Envoy container bind-mounts it read-only — so it is not removed here; the
// caller removes it if setup fails afterward. On any error during writing, the
// partially-created file is removed before returning.
func writeEnvoyBootstrap(b envoyBootstrap) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("failed to marshal envoy bootstrap: %w", err)
	}
	tmpFile, err := os.CreateTemp("", "envoy-bootstrap-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create envoy bootstrap temp file: %w", err)
	}
	created := tmpFile.Name()
	defer func() {
		if cerr := tmpFile.Close(); cerr != nil {
			slog.Warn("failed to close envoy bootstrap temp file", "error", cerr)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = os.Remove(created)
		return "", fmt.Errorf("failed to write envoy bootstrap: %w", err)
	}
	// 0o644: world-readable so the Envoy distroless container (UID 101) can
	// read the bind-mounted file. On Linux Docker Engine, strict POSIX
	// permissions apply — 0o600 prevents the container user from reading the
	// file, causing Envoy to crash-loop. The bootstrap contains no secrets
	// (only network topology: hostnames, ports, RBAC rules).
	if err := tmpFile.Chmod(0o644); err != nil {
		_ = os.Remove(created)
		return "", fmt.Errorf("failed to set envoy bootstrap file permissions: %w", err)
	}
	return created, nil
}

// ── envoyProxy ────────────────────────────────────────────────────────────────

// envoyProxy implements networkProxy using Envoy as the proxy backend.
// It creates a single Envoy container that handles both egress (forward proxy
// on :3128) and ingress (reverse proxy) as separate listeners, reducing aux
// container count from 3 (Squid: egress + ingress + dns) to 2 (Envoy: combined
// + dns).
type envoyProxy struct {
	client *Client
}

// SetupEgress implements networkProxy for the Envoy backend.
//
// Only the egress proxy env vars are returned here — no container is created
// yet. Container creation is deferred to SetupIngress (which runs after
// createMcpContainer) so that the MCP container's hostname is resolvable the
// moment the Envoy STRICT_DNS ingress cluster first probes it. Creating Envoy
// before the MCP container caused the ingress cluster to cache a negative DNS
// response on Linux Docker Engine, preventing the server from ever becoming
// ready within the readiness window (see #5922).
func (*envoyProxy) SetupEgress(_ context.Context, spec proxySpec) (egressResult, error) {
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)
	return egressResult{EnvVars: addEgressEnvVars(nil, egressContainerName)}, nil
}

// SetupIngress implements networkProxy for the Envoy backend.
//
// Creates the Envoy container after the MCP container exists, with both the
// egress forward-proxy listener and (for non-stdio transports) the ingress
// reverse-proxy listener. Running after createMcpContainer ensures the
// STRICT_DNS upstream cluster resolves the MCP hostname on the first probe,
// avoiding the Linux Docker Engine readiness failure described in #5922.
func (e *envoyProxy) SetupIngress(ctx context.Context, spec proxySpec, _ egressResult) (int, error) {
	// The container is named <name>-egress (not <name>-proxy or similar) to
	// preserve parity with the Squid backend: addEgressEnvVars and the suffix-
	// based cleanup loop both key off this name.
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)

	bootstrap := envoyBootstrap{
		StaticResources: envoyStaticResources{
			Listeners: []envoyListener{buildEgressListener(spec)},
			Clusters:  []envoyCluster{buildEgressCluster()},
		},
	}

	var ingressPort int
	if spec.TransportType != "stdio" && spec.UpstreamPort > 0 {
		port, err := networking.FindOrUsePort(spec.UpstreamPort + 1)
		if err != nil {
			return 0, fmt.Errorf("failed to find ingress port: %w", err)
		}
		ingressPort = port
		bootstrap.StaticResources.Listeners = append(
			bootstrap.StaticResources.Listeners,
			buildIngressListener(spec, ingressPort),
		)
		bootstrap.StaticResources.Clusters = append(
			bootstrap.StaticResources.Clusters,
			buildIngressCluster(spec),
		)
	}

	// Add transparent listener and original_dst cluster for L3/L4 interception.
	// The transparent listener uses SO_ORIGINAL_DST (set by iptables DNAT rules
	// installed by SetupTransparent) to recover the real upstream destination and
	// route connections through the network RBAC filters before forwarding.
	bootstrap.StaticResources.Listeners = append(
		bootstrap.StaticResources.Listeners,
		buildTransparentListener(spec),
	)
	bootstrap.StaticResources.Clusters = append(
		bootstrap.StaticResources.Clusters,
		buildOriginalDstCluster(),
	)

	configPath, err := writeEnvoyBootstrap(bootstrap)
	if err != nil {
		return 0, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(configPath)
		}
	}()

	envoyImage := getEnvoyImage()
	//nolint:gosec // G706: envoy image name from config
	slog.Debug("setting up envoy container", "name", egressContainerName, "image", envoyImage)

	if err := e.client.imageManager.PullImage(ctx, envoyImage); err != nil {
		exists, inspectErr := e.client.imageManager.ImageExists(ctx, envoyImage)
		if inspectErr != nil || !exists {
			return 0, fmt.Errorf("failed to pull envoy image: %w", err)
		}
		//nolint:gosec // G706: envoy image name from config
		slog.Debug("envoy image exists locally, continuing despite pull failure", "image", envoyImage)
	}

	envoyLabels := map[string]string{}
	lb.AddStandardLabels(envoyLabels, egressContainerName, egressContainerName, "stdio", 80)
	envoyLabels[ToolhiveAuxiliaryWorkloadLabel] = LabelValueTrue

	containerConfig := &container.Config{
		Image:  envoyImage,
		Cmd:    []string{"-c", "/etc/envoy/envoy.json"},
		Labels: envoyLabels,
	}

	mounts := []runtime.Mount{
		{Source: configPath, Target: "/etc/envoy/envoy.json", ReadOnly: true},
	}

	var exposedPorts map[string]struct{}
	var portBindings map[string][]runtime.PortBinding
	if ingressPort > 0 {
		portStr := strconv.Itoa(ingressPort)
		portKey := portStr + "/tcp"
		exposedPorts = map[string]struct{}{portKey: {}}
		portBindings = map[string][]runtime.PortBinding{
			portKey: {{HostIP: "127.0.0.1", HostPort: portStr}},
		}
	}

	hostConfig := &container.HostConfig{
		Mounts:      convertMounts(mounts),
		NetworkMode: container.NetworkMode("bridge"),
		SecurityOpt: []string{"label:disable"},
		CapDrop:     []string{"ALL"},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}
	if portBindings != nil {
		if err := setupPortBindings(hostConfig, portBindings); err != nil {
			return 0, fmt.Errorf("failed to setup port bindings: %w", err)
		}
	}
	if err := setupExposedPorts(containerConfig, exposedPorts); err != nil {
		return 0, fmt.Errorf("failed to setup exposed ports: %w", err)
	}

	if _, err := e.client.createContainer(ctx, egressContainerName, containerConfig, hostConfig, spec.Endpoints); err != nil {
		return 0, fmt.Errorf("failed to create envoy container: %w", err)
	}

	success = true
	return ingressPort, nil
}

// SetupTransparent runs the iptables init container in the workload's network
// namespace to redirect all outbound TCP to Envoy's transparent listener.
// Full implementation: see runTransparentInitContainer.
func (e *envoyProxy) SetupTransparent(ctx context.Context, spec proxySpec, workloadContainerID string) error {
	return runTransparentInitContainer(ctx, e.client, workloadContainerID, spec.EnvoyInternalIP, transparentPort(spec))
}

// transparentPort returns the transparent listener port from the spec,
// defaulting to defaultTransparentPort (15001).
func transparentPort(spec proxySpec) int {
	if spec.TransparentPort > 0 {
		return spec.TransparentPort
	}
	return defaultTransparentPort
}

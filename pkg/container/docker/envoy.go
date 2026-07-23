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
	// defaultEnvoyImage is pinned by tag. Digest pinning is tracked in #5903.
	// Override with TOOLHIVE_ENVOY_IMAGE.
	defaultEnvoyImage = "envoyproxy/envoy-distroless:v1.32.3"

	// Protobuf type URLs required by Envoy's protobuf-JSON bootstrap format.
	// Every typed_config field must carry an @type URL or Envoy will reject the
	// config on startup.
	typeHCM        = "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
	typeHTTPRBAC   = "type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC"
	typeDFPFilter  = "type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig"
	typeRouter     = "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"
	typeDFPCluster = "type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig"

	// dfpCacheName is the shared DNS cache config name used by both the DFP
	// HTTP filter and the DFP cluster extension.
	dfpCacheName = "dynamic_forward_proxy_cache_config"

	// dfpClusterName is the cluster referenced by the egress route config.
	dfpClusterName = "dynamic_forward_proxy_cluster"

	// ingressClusterName is the cluster referenced by the ingress route config.
	ingressClusterName = "ingress_upstream"
)

func getEnvoyImage() string {
	if img := os.Getenv("TOOLHIVE_ENVOY_IMAGE"); img != "" {
		return img
	}
	return defaultEnvoyImage
}

// ── Bootstrap ────────────────────────────────────────────────────────────────

// envoyBootstrap is the top-level Envoy bootstrap configuration.
type envoyBootstrap struct {
	Admin           *envoyAdmin          `json:"admin,omitempty"`
	StaticResources envoyStaticResources `json:"static_resources"`
}

// envoyAdmin configures the Envoy admin API endpoint.
type envoyAdmin struct {
	Address envoyAddress `json:"address"`
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
	Name         string             `json:"name"`
	Address      envoyAddress       `json:"address"`
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
// becomes a policy matching the :authority header via hostMatchRegex, which
// mirrors Squid's dstdomain semantics (see hostMatchRegex for the syntax).
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
	for _, host := range out.AllowHost {
		policies[host] = envoyRBACPolicy{
			Permissions: []envoyPermission{
				{
					Header: &envoyHeaderMatcher{
						Name: ":authority",
						StringMatch: &envoyStringMatch{
							SafeRegex: &envoySafeRegex{Regex: hostMatchRegex(host)},
						},
					},
				},
			},
			Principals: []envoyPrincipal{{Any: true}},
		}
	}
	return policies
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
	subdomains := false
	switch {
	case strings.HasPrefix(host, "*."):
		host = host[2:]
		subdomains = true
	case strings.HasPrefix(host, "."):
		host = host[1:]
		subdomains = true
	}
	pattern := regexp.QuoteMeta(host)
	if subdomains {
		// Optional "sub." prefix at any depth, plus the apex itself.
		pattern = `(.*\.)?` + pattern
	}
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

// buildIngressListener builds the Envoy listener config for inbound (ingress)
// traffic. It binds on 0.0.0.0:hostPort inside the container so that Docker's
// port forwarding (host:127.0.0.1:<hostPort> → container:<hostPort>) can reach
// it. The host-side HostIP:"127.0.0.1" in the port binding provides the
// localhost-only restriction; the container-side address must be 0.0.0.0 or
// Docker's bridge forwarding cannot deliver traffic to the listener.
//
// When spec.Permissions.Inbound.AllowHost is set the virtual host domain list
// is restricted to those entries; otherwise a wildcard domain ("*") is used.
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
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)

	bootstrap := envoyBootstrap{
		Admin: &envoyAdmin{
			Address: envoyAddress{
				SocketAddress: envoySocketAddress{
					Address:   "127.0.0.1", // loopback only — never 0.0.0.0
					PortValue: 9901,
				},
			},
		},
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

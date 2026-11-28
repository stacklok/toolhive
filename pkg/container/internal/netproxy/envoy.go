// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package netproxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/utils/ptr"
)

const (
	// TypeHCM is the Envoy protobuf type URL for HttpConnectionManager.
	TypeHCM = "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
	// TypeHTTPRBAC is the Envoy protobuf type URL for HTTP RBAC filter.
	TypeHTTPRBAC = "type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC"
	// TypeDFPFilter is the Envoy protobuf type URL for Dynamic Forward Proxy HTTP filter.
	TypeDFPFilter = "type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig"
	// TypeRouter is the Envoy protobuf type URL for HTTP Router filter.
	TypeRouter = "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"
	// TypeDFPCluster is the Envoy protobuf type URL for Dynamic Forward Proxy cluster extension.
	TypeDFPCluster = "type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig"
	// TypeStdoutAccessLog is the Envoy protobuf type URL for StdoutAccessLog.
	TypeStdoutAccessLog = "type.googleapis.com/envoy.extensions.access_loggers.stream.v3.StdoutAccessLog"

	// DFPCacheName is the shared DNS cache config name used by both the DFP
	// HTTP filter and the DFP cluster extension.
	DFPCacheName = "dynamic_forward_proxy_cache_config"

	// DFPClusterName is the cluster referenced by the egress route config.
	DFPClusterName = "dynamic_forward_proxy_cluster"

	// IngressClusterName is the cluster referenced by the ingress route config.
	IngressClusterName = "ingress_upstream"
)

// EnvoyBootstrap is the top-level Envoy bootstrap configuration.
// The admin interface is intentionally omitted to eliminate runtime attack surface.
type EnvoyBootstrap struct {
	StaticResources EnvoyStaticResources `json:"static_resources"`
}

// EnvoyStaticResources holds the static listeners and clusters.
type EnvoyStaticResources struct {
	Listeners []EnvoyListener `json:"listeners,omitempty"`
	Clusters  []EnvoyCluster  `json:"clusters,omitempty"`
}

// EnvoyAddress wraps a socket address for Envoy config.
type EnvoyAddress struct {
	SocketAddress EnvoySocketAddress `json:"socket_address"`
}

// EnvoySocketAddress is an IP + port pair used throughout Envoy config.
type EnvoySocketAddress struct {
	Address   string `json:"address"`
	PortValue int    `json:"port_value"`
}

// EnvoyListener is an Envoy listener binding on a socket address.
type EnvoyListener struct {
	Name         string             `json:"name"`
	Address      EnvoyAddress       `json:"address"`
	FilterChains []EnvoyFilterChain `json:"filter_chains"`
}

// EnvoyFilterChain is a sequence of network-level filters applied to matching connections.
type EnvoyFilterChain struct {
	Filters []EnvoyNetworkFilter `json:"filters"`
}

// EnvoyNetworkFilter is a named network filter with a typed protobuf-JSON config.
type EnvoyNetworkFilter struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// EnvoyHCM is the typed config for envoy.filters.network.http_connection_manager.
type EnvoyHCM struct {
	Type              string               `json:"@type"`
	StatPrefix        string               `json:"stat_prefix"`
	AccessLog         []EnvoyAccessLog     `json:"access_log,omitempty"`
	StreamIdleTimeout string               `json:"stream_idle_timeout,omitempty"`
	UpgradeConfigs    []EnvoyUpgradeConfig `json:"upgrade_configs,omitempty"`
	HTTPFilters       []EnvoyHTTPFilter    `json:"http_filters"`
	RouteConfig       *EnvoyRouteConfig    `json:"route_config,omitempty"`
}

// EnvoyAccessLog configures request access logging for an HCM.
type EnvoyAccessLog struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// EnvoyStdoutAccessLog is the typed config for envoy.access_loggers.stdout.
type EnvoyStdoutAccessLog struct {
	Type string `json:"@type"`
}

// EnvoyUpgradeConfig enables HTTP upgrade protocols (e.g. CONNECT) in HCM.
type EnvoyUpgradeConfig struct {
	UpgradeType   string              `json:"upgrade_type"`
	ConnectConfig *EnvoyConnectConfig `json:"connect_config,omitempty"`
}

// EnvoyHTTPFilter is a named HTTP-layer filter embedded inside HCM.
type EnvoyHTTPFilter struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// EnvoyHTTPRBAC is the typed config for envoy.filters.http.rbac.
type EnvoyHTTPRBAC struct {
	Type  string         `json:"@type"`
	Rules EnvoyRBACRules `json:"rules"`
}

// EnvoyRBACRules holds the RBAC action and policy map.
// CRITICAL: Policies must NOT have omitempty. An empty map serializes as {} and
// is intentional — an absent field would silently turn deny-all into allow-all.
type EnvoyRBACRules struct {
	Action   string                     `json:"action"`
	Policies map[string]EnvoyRBACPolicy `json:"policies"`
}

// EnvoyRBACPolicy pairs permissions with principals for a single RBAC policy.
type EnvoyRBACPolicy struct {
	Permissions []EnvoyPermission `json:"permissions"`
	Principals  []EnvoyPrincipal  `json:"principals"`
}

// EnvoyPermission matches a request by various criteria.
type EnvoyPermission struct {
	Any     *bool               `json:"any,omitempty"`
	Header  *EnvoyHeaderMatcher `json:"header,omitempty"`
	OrRules *EnvoyOrRules       `json:"or_rules,omitempty"`
}

// EnvoyOrRules composes multiple permissions with logical OR.
type EnvoyOrRules struct {
	Rules []EnvoyPermission `json:"rules"`
}

// EnvoyHeaderMatcher matches an HTTP header value.
type EnvoyHeaderMatcher struct {
	Name        string            `json:"name"`
	StringMatch *EnvoyStringMatch `json:"string_match,omitempty"`
}

// EnvoyStringMatch matches a string by exact value, prefix, suffix, or regex.
type EnvoyStringMatch struct {
	Exact     string          `json:"exact,omitempty"`
	Prefix    string          `json:"prefix,omitempty"`
	Suffix    string          `json:"suffix,omitempty"`
	SafeRegex *EnvoySafeRegex `json:"safe_regex,omitempty"`
}

// EnvoySafeRegex is an RE2 regex matcher (fully anchored by Envoy).
type EnvoySafeRegex struct {
	Regex string `json:"regex"`
}

// EnvoyPrincipal matches a downstream principal.
type EnvoyPrincipal struct {
	Any bool `json:"any"`
}

// EnvoyDFPFilter is the typed config for envoy.filters.http.dynamic_forward_proxy.
type EnvoyDFPFilter struct {
	Type           string        `json:"@type"`
	DNSCacheConfig EnvoyDNSCache `json:"dns_cache_config"`
}

// EnvoyDNSCache is the shared DNS cache config referenced by both the DFP
// HTTP filter and the DFP cluster extension.
type EnvoyDNSCache struct {
	Name                  string               `json:"name"`
	DNSLookupFamily       string               `json:"dns_lookup_family"`
	ResolvedAddressFilter *EnvoyAddressMatcher `json:"resolved_address_filter,omitempty"`
}

// EnvoyAddressMatcher filters resolved DNS addresses against exact gateway CIDRs.
type EnvoyAddressMatcher struct {
	Ranges []EnvoyCIDRRange `json:"ranges"`
}

// EnvoyCIDRRange represents a single CIDR prefix entry.
type EnvoyCIDRRange struct {
	AddressPrefix string `json:"address_prefix"`
	PrefixLen     int    `json:"prefix_len"`
}

// EnvoyRouter is the typed config for envoy.filters.http.router.
type EnvoyRouter struct {
	Type string `json:"@type"`
}

// EnvoyRouteConfig holds the list of virtual hosts for a listener.
type EnvoyRouteConfig struct {
	VirtualHosts []EnvoyVirtualHost `json:"virtual_hosts"`
}

// EnvoyVirtualHost matches requests by domain and dispatches to a cluster.
type EnvoyVirtualHost struct {
	Name    string       `json:"name"`
	Domains []string     `json:"domains"`
	Routes  []EnvoyRoute `json:"routes"`
}

// EnvoyRoute matches a request prefix and forwards it to a cluster.
type EnvoyRoute struct {
	Match EnvoyRouteMatch   `json:"match"`
	Route *EnvoyRouteAction `json:"route,omitempty"`
}

// EnvoyRouteMatch matches incoming requests by URI prefix or CONNECT method.
type EnvoyRouteMatch struct {
	Prefix         string               `json:"prefix,omitempty"`
	ConnectMatcher *EnvoyConnectMatcher `json:"connect_matcher,omitempty"`
}

// EnvoyConnectMatcher matches HTTP CONNECT requests (used for HTTPS tunneling).
type EnvoyConnectMatcher struct{}

// EnvoyRouteAction forwards matched requests to an upstream cluster.
type EnvoyRouteAction struct {
	Cluster        string               `json:"cluster"`
	Timeout        string               `json:"timeout,omitempty"`
	UpgradeConfigs []EnvoyUpgradeConfig `json:"upgrade_configs,omitempty"`
}

// EnvoyConnectConfig is the per-route CONNECT tunnel configuration.
type EnvoyConnectConfig struct{}

// EnvoyCluster is an Envoy upstream cluster definition.
type EnvoyCluster struct {
	Name            string               `json:"name"`
	ConnectTimeout  string               `json:"connect_timeout"`
	LbPolicy        string               `json:"lb_policy,omitempty"`
	Type            string               `json:"type,omitempty"`
	DnsLookupFamily string               `json:"dns_lookup_family,omitempty"`
	ClusterType     *EnvoyClusterType    `json:"cluster_type,omitempty"`
	LoadAssignment  *EnvoyLoadAssignment `json:"load_assignment,omitempty"`
}

// EnvoyClusterType is the custom cluster discovery extension (e.g. DFP).
type EnvoyClusterType struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"typed_config"`
}

// EnvoyDFPClusterConfig is the typed config for envoy.clusters.dynamic_forward_proxy.
type EnvoyDFPClusterConfig struct {
	Type           string        `json:"@type"`
	DNSCacheConfig EnvoyDNSCache `json:"dns_cache_config"`
}

// EnvoyLoadAssignment is an EDS-style static load assignment.
type EnvoyLoadAssignment struct {
	ClusterName string          `json:"cluster_name"`
	Endpoints   []EnvoyEndpoint `json:"endpoints"`
}

// EnvoyEndpoint is a group of LB endpoints.
type EnvoyEndpoint struct {
	LBEndpoints []EnvoyLBEndpoint `json:"lb_endpoints"`
}

// EnvoyLBEndpoint is a single upstream endpoint.
type EnvoyLBEndpoint struct {
	Endpoint EnvoyEndpointAddress `json:"endpoint"`
}

// EnvoyEndpointAddress wraps an address for an LB endpoint.
type EnvoyEndpointAddress struct {
	Address EnvoyAddress `json:"address"`
}

// BuildEgressBootstrap returns an Envoy bootstrap with only the egress forward
// proxy listener and dynamic forward proxy cluster.
func BuildEgressBootstrap(spec Spec) EnvoyBootstrap {
	return EnvoyBootstrap{
		StaticResources: EnvoyStaticResources{
			Listeners: []EnvoyListener{BuildEgressListener(spec)},
			Clusters:  []EnvoyCluster{BuildEgressCluster(spec)},
		},
	}
}

// BuildIngressBootstrap returns an Envoy bootstrap with only the ingress
// reverse proxy listener and upstream cluster.
func BuildIngressBootstrap(spec Spec, hostPort int) EnvoyBootstrap {
	return EnvoyBootstrap{
		StaticResources: EnvoyStaticResources{
			Listeners: []EnvoyListener{BuildIngressListener(spec, hostPort)},
			Clusters:  []EnvoyCluster{BuildIngressCluster(spec)},
		},
	}
}

// BuildDFPDNSCache returns the shared DFP DNS cache config with optional
// resolved-address gateway filtering.
func BuildDFPDNSCache(spec Spec) EnvoyDNSCache {
	cache := EnvoyDNSCache{Name: DFPCacheName, DNSLookupFamily: "V4_ONLY"}
	if spec.AllowDockerGateway {
		return cache
	}
	ranges := make([]EnvoyCIDRRange, len(spec.GatewayIPs))
	for i, gateway := range spec.GatewayIPs {
		gateway = gateway.Unmap()
		ranges[i] = EnvoyCIDRRange{AddressPrefix: gateway.String(), PrefixLen: gateway.BitLen()}
	}
	cache.ResolvedAddressFilter = &EnvoyAddressMatcher{Ranges: ranges}
	return cache
}

// BuildEgressListener builds the Envoy listener config for outbound traffic.
func BuildEgressListener(spec Spec) EnvoyListener {
	httpFilters := BuildEgressHTTPFilters(spec)

	hcm := &EnvoyHCM{
		Type:              TypeHCM,
		StatPrefix:        "egress_http",
		AccessLog:         stdoutAccessLog(),
		StreamIdleTimeout: "0s",
		UpgradeConfigs:    []EnvoyUpgradeConfig{{UpgradeType: "CONNECT"}},
		HTTPFilters:       httpFilters,
		RouteConfig: &EnvoyRouteConfig{
			VirtualHosts: []EnvoyVirtualHost{
				{
					Name:    "local_service",
					Domains: []string{"*"},
					Routes: []EnvoyRoute{
						{
							Match: EnvoyRouteMatch{ConnectMatcher: &EnvoyConnectMatcher{}},
							Route: &EnvoyRouteAction{
								Cluster: DFPClusterName,
								UpgradeConfigs: []EnvoyUpgradeConfig{
									{UpgradeType: "CONNECT", ConnectConfig: &EnvoyConnectConfig{}},
								},
							},
						},
						{
							Match: EnvoyRouteMatch{Prefix: "/"},
							Route: &EnvoyRouteAction{Cluster: DFPClusterName, Timeout: "0s"},
						},
					},
				},
			},
		},
	}

	return EnvoyListener{
		Name: fmt.Sprintf("%s-egress", spec.WorkloadName),
		Address: EnvoyAddress{
			SocketAddress: EnvoySocketAddress{
				Address:   "0.0.0.0",
				PortValue: 3128,
			},
		},
		FilterChains: []EnvoyFilterChain{
			{
				Filters: []EnvoyNetworkFilter{
					{
						Name:        "envoy.filters.network.http_connection_manager",
						TypedConfig: hcm,
					},
				},
			},
		},
	}
}

// BuildEgressHTTPFilters constructs the ordered list of HTTP filters for the egress HCM.
func BuildEgressHTTPFilters(spec Spec) []EnvoyHTTPFilter {
	var filters []EnvoyHTTPFilter

	if !spec.AllowDockerGateway {
		filters = append(filters, EnvoyHTTPFilter{
			Name:        "envoy.filters.http.rbac",
			TypedConfig: BuildGatewayDenyRBAC(spec.GatewayIPs),
		})
	}

	filters = append(filters,
		EnvoyHTTPFilter{
			Name:        "envoy.filters.http.rbac",
			TypedConfig: BuildAllowlistRBAC(spec),
		},
		EnvoyHTTPFilter{
			Name: "envoy.filters.http.dynamic_forward_proxy",
			TypedConfig: &EnvoyDFPFilter{
				Type:           TypeDFPFilter,
				DNSCacheConfig: BuildDFPDNSCache(spec),
			},
		},
		EnvoyHTTPFilter{
			Name:        "envoy.filters.http.router",
			TypedConfig: &EnvoyRouter{Type: TypeRouter},
		},
	)

	return filters
}

// BuildGatewayDenyRBAC builds an RBAC DENY filter blocking host gateway IPs and hostnames.
func BuildGatewayDenyRBAC(gatewayIPs []netip.Addr) *EnvoyHTTPRBAC {
	return &EnvoyHTTPRBAC{
		Type: TypeHTTPRBAC,
		Rules: EnvoyRBACRules{
			Action: "DENY",
			Policies: map[string]EnvoyRBACPolicy{
				"gateway": {
					Permissions: GatewayAuthorityPermissions(gatewayIPs),
					Principals:  []EnvoyPrincipal{{Any: true}},
				},
			},
		},
	}
}

// GatewayAuthorityPermissions matches protected host gateway IPs and shared host aliases on :authority.
func GatewayAuthorityPermissions(gatewayIPs []netip.Addr) []EnvoyPermission {
	patterns := make([]string, 0, len(gatewayIPs)+len(HostGatewayNames))
	for _, gateway := range gatewayIPs {
		patterns = append(patterns, gatewayAuthorityRegexes(gateway)...)
	}
	for _, hostname := range HostGatewayNames {
		patterns = append(patterns, HostMatchRegex(hostname))
	}
	rules := make([]EnvoyPermission, 0, len(patterns))
	for _, re := range patterns {
		rules = append(rules, EnvoyPermission{
			Header: &EnvoyHeaderMatcher{
				Name:        ":authority",
				StringMatch: &EnvoyStringMatch{SafeRegex: &EnvoySafeRegex{Regex: re}},
			},
		})
	}
	return []EnvoyPermission{{OrRules: &EnvoyOrRules{Rules: rules}}}
}

// BuildAllowlistRBAC builds an RBAC ALLOW filter encoding the outbound allowlist.
func BuildAllowlistRBAC(spec Spec) *EnvoyHTTPRBAC {
	return &EnvoyHTTPRBAC{
		Type: TypeHTTPRBAC,
		Rules: EnvoyRBACRules{
			Action:   "ALLOW",
			Policies: BuildAllowlistPolicies(spec),
		},
	}
}

// BuildAllowlistPolicies returns the policy map for the egress RBAC ALLOW filter.
func BuildAllowlistPolicies(spec Spec) map[string]EnvoyRBACPolicy {
	policies := make(map[string]EnvoyRBACPolicy)

	if spec.AllowDockerGateway {
		policies["gateway"] = EnvoyRBACPolicy{
			Permissions: GatewayAuthorityPermissions(spec.GatewayIPs),
			Principals:  []EnvoyPrincipal{{Any: true}},
		}
	}

	if spec.Permissions == nil || spec.Permissions.Outbound == nil {
		return policies
	}
	out := spec.Permissions.Outbound
	if out.InsecureAllowAll {
		policies["allow-all"] = EnvoyRBACPolicy{
			Permissions: []EnvoyPermission{{Any: ptr.To(true)}},
			Principals:  []EnvoyPrincipal{{Any: true}},
		}
		return policies
	}
	if len(out.AllowHost) == 0 && len(out.AllowPort) > 0 {
		policies["any-host-allowed-ports"] = EnvoyRBACPolicy{
			Permissions: []EnvoyPermission{{
				Header: &EnvoyHeaderMatcher{
					Name:        ":authority",
					StringMatch: &EnvoyStringMatch{SafeRegex: &EnvoySafeRegex{Regex: AnyHostPortRegex(out.AllowPort)}},
				},
			}},
			Principals: []EnvoyPrincipal{{Any: true}},
		}
		return policies
	}

	for _, host := range out.AllowHost {
		var regex string
		if len(out.AllowPort) > 0 {
			regex = HostPortMatchRegex(host, out.AllowPort)
		} else {
			regex = HostMatchRegex(host)
		}
		policies[host] = EnvoyRBACPolicy{
			Permissions: []EnvoyPermission{{
				Header: &EnvoyHeaderMatcher{
					Name:        ":authority",
					StringMatch: &EnvoyStringMatch{SafeRegex: &EnvoySafeRegex{Regex: regex}},
				},
			}},
			Principals: []EnvoyPrincipal{{Any: true}},
		}
	}
	return policies
}

// HostPortMatchRegex builds an anchored RE2 pattern matching host and allowed ports.
func HostPortMatchRegex(host string, ports []int) string {
	portGroup, includes80 := portGroupRegex(ports)
	hostPattern := hostBasePattern(host)
	if includes80 {
		return `(?i)` + hostPattern + `\.?(` + portGroup + `)?`
	}
	return `(?i)` + hostPattern + `\.?` + portGroup
}

// AnyHostPortRegex builds a regex matching any hostname on the given ports.
func AnyHostPortRegex(ports []int) string {
	portGroup, includes80 := portGroupRegex(ports)
	if includes80 {
		return `(?i)[^:]+(` + portGroup + `)?`
	}
	return `(?i)[^:]+` + portGroup
}

// HostMatchRegex builds an anchored, case-insensitive RE2 pattern mirroring Squid dstdomain semantics.
func HostMatchRegex(host string) string {
	pattern := hostBasePattern(host)
	return `(?i)` + pattern + `\.?(:[0-9]+)?`
}

// BuildEgressCluster returns the dynamic_forward_proxy cluster for the egress listener.
func BuildEgressCluster(spec Spec) EnvoyCluster {
	return EnvoyCluster{
		Name:           DFPClusterName,
		ConnectTimeout: "10s",
		LbPolicy:       "CLUSTER_PROVIDED",
		ClusterType: &EnvoyClusterType{
			Name: "envoy.clusters.dynamic_forward_proxy",
			TypedConfig: &EnvoyDFPClusterConfig{
				Type:           TypeDFPCluster,
				DNSCacheConfig: BuildDFPDNSCache(spec),
			},
		},
	}
}

// BuildIngressListener builds the Envoy listener config for inbound traffic.
func BuildIngressListener(spec Spec, hostPort int) EnvoyListener {
	domains := ingressDomains(spec)

	hcm := &EnvoyHCM{
		Type:              TypeHCM,
		StatPrefix:        "ingress_http",
		AccessLog:         stdoutAccessLog(),
		StreamIdleTimeout: "0s",
		HTTPFilters: []EnvoyHTTPFilter{
			{
				Name:        "envoy.filters.http.router",
				TypedConfig: &EnvoyRouter{Type: TypeRouter},
			},
		},
		RouteConfig: &EnvoyRouteConfig{
			VirtualHosts: []EnvoyVirtualHost{
				{
					Name:    "ingress_service",
					Domains: domains,
					Routes: []EnvoyRoute{
						{
							Match: EnvoyRouteMatch{Prefix: "/"},
							Route: &EnvoyRouteAction{Cluster: IngressClusterName, Timeout: "0s"},
						},
					},
				},
			},
		},
	}

	return EnvoyListener{
		Name: fmt.Sprintf("%s-ingress", spec.WorkloadName),
		Address: EnvoyAddress{
			SocketAddress: EnvoySocketAddress{
				Address:   "0.0.0.0",
				PortValue: hostPort,
			},
		},
		FilterChains: []EnvoyFilterChain{
			{
				Filters: []EnvoyNetworkFilter{
					{
						Name:        "envoy.filters.network.http_connection_manager",
						TypedConfig: hcm,
					},
				},
			},
		},
	}
}

// BuildIngressCluster returns the STRICT_DNS upstream cluster for the ingress listener.
func BuildIngressCluster(spec Spec) EnvoyCluster {
	upstreamHost := spec.UpstreamHost
	if upstreamHost == "" {
		upstreamHost = spec.WorkloadName
	}
	return EnvoyCluster{
		Name:            IngressClusterName,
		ConnectTimeout:  "10s",
		Type:            "STRICT_DNS",
		DnsLookupFamily: "V4_ONLY",
		LoadAssignment: &EnvoyLoadAssignment{
			ClusterName: IngressClusterName,
			Endpoints: []EnvoyEndpoint{
				{
					LBEndpoints: []EnvoyLBEndpoint{
						{
							Endpoint: EnvoyEndpointAddress{
								Address: EnvoyAddress{
									SocketAddress: EnvoySocketAddress{
										Address:   upstreamHost,
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

// WriteEnvoyBootstrap marshals b to JSON and writes it to a temporary file at mode 0644.
func WriteEnvoyBootstrap(b EnvoyBootstrap) (string, error) {
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
	if err := tmpFile.Chmod(0o644); err != nil {
		_ = os.Remove(created)
		return "", fmt.Errorf("failed to set envoy bootstrap file permissions: %w", err)
	}
	return created, nil
}

func stdoutAccessLog() []EnvoyAccessLog {
	return []EnvoyAccessLog{{
		Name:        "envoy.access_loggers.stdout",
		TypedConfig: &EnvoyStdoutAccessLog{Type: TypeStdoutAccessLog},
	}}
}

func ingressDomains(_ Spec) []string {
	return []string{"*"}
}

func gatewayAuthorityRegexes(addr netip.Addr) []string {
	addr = addr.Unmap()
	if addr.Is4() {
		return append([]string{HostMatchRegex(addr.String())}, ipv4MappedAuthorityRegexes(addr.As4())...)
	}
	return ipv6AuthorityRegexes(addr)
}

func ipv4MappedAuthorityRegexes(v4 [4]byte) []string {
	hexTail := `0*` + strconv.FormatUint(uint64(uint16(v4[0])<<8|uint16(v4[1])), 16) +
		`:0*` + strconv.FormatUint(uint64(uint16(v4[2])<<8|uint16(v4[3])), 16)
	dottedTail := strings.Join([]string{
		strconv.Itoa(int(v4[0])), strconv.Itoa(int(v4[1])),
		strconv.Itoa(int(v4[2])), strconv.Itoa(int(v4[3])),
	}, `\.`)
	prefixes := []string{
		`(?:0+:){5}0*ffff:`,
		`(?:0+(?::0+){0,3})?::(?:0+:){0,4}0*ffff:`,
	}
	patterns := make([]string, 0, 4)
	for _, prefix := range prefixes {
		patterns = append(patterns,
			`(?i)\[`+prefix+hexTail+`\](:[0-9]+)?`,
			`(?i)\[`+prefix+dottedTail+`\](:[0-9]+)?`,
		)
	}
	return patterns
}

func ipv6AuthorityRegexes(addr netip.Addr) []string {
	raw := addr.As16()
	words := make([]uint16, 8)
	for i := range words {
		words[i] = uint16(raw[2*i])<<8 | uint16(raw[2*i+1])
	}
	patterns := ipv6TextPatterns(words, "")
	dotted := strings.Join([]string{
		strconv.Itoa(int(raw[12])), strconv.Itoa(int(raw[13])),
		strconv.Itoa(int(raw[14])), strconv.Itoa(int(raw[15])),
	}, `\.`)
	patterns = append(patterns, ipv6TextPatterns(words[:6], dotted)...)
	for i, pattern := range patterns {
		patterns[i] = `(?i)\[` + pattern + `\](:[0-9]+)?`
	}
	return patterns
}

func ipv6TextPatterns(words []uint16, suffix string) []string {
	tokens := make([]string, len(words))
	for i, word := range words {
		if word == 0 {
			tokens[i] = `0{1,4}`
		} else {
			tokens[i] = `0{0,3}` + strconv.FormatUint(uint64(word), 16)
		}
	}
	patterns := []string{strings.Join(append(slices.Clone(tokens), suffixIfSet(suffix)...), ":")}
	for start := range words {
		for end := start + 1; end <= len(words); end++ {
			allZero := true
			for _, word := range words[start:end] {
				allZero = allZero && word == 0
			}
			if !allZero {
				break
			}
			left := strings.Join(tokens[:start], ":")
			rightTokens := slices.Clone(tokens[end:])
			rightTokens = append(rightTokens, suffixIfSet(suffix)...)
			right := strings.Join(rightTokens, ":")
			patterns = append(patterns, left+"::"+right)
		}
	}
	return patterns
}

func suffixIfSet(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

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

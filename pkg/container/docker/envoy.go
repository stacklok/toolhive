// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/moby/moby/api/types/container"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// defaultEnvoyImage is pinned by digest to prevent unexpected updates.
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

// boolPtr returns a pointer to b, used for the RBAC any permission field which
// requires a pointer to distinguish "unset" from "false".
func boolPtr(b bool) *bool { return &b }

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
	Type           string               `json:"@type"`
	StatPrefix     string               `json:"stat_prefix"`
	AccessLog      []envoyAccessLog     `json:"access_log,omitempty"`
	UpgradeConfigs []envoyUpgradeConfig `json:"upgrade_configs,omitempty"`
	HTTPFilters    []envoyHTTPFilter    `json:"http_filters"`
	RouteConfig    *envoyRouteConfig    `json:"route_config,omitempty"`
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
	Any           *bool               `json:"any,omitempty"`
	Header        *envoyHeaderMatcher `json:"header,omitempty"`
	DestinationIP *envoyCIDRRange     `json:"destination_ip,omitempty"`
	OrRules       *envoyOrRules       `json:"or_rules,omitempty"`
}

// envoyOrRules composes multiple permissions with logical OR.
type envoyOrRules struct {
	Rules []envoyPermission `json:"rules"`
}

// envoyCIDRRange matches destination IPs against a CIDR prefix.
type envoyCIDRRange struct {
	AddressPrefix string `json:"address_prefix"`
	PrefixLen     uint32 `json:"prefix_len"`
}

// envoyHeaderMatcher matches an HTTP header value.
type envoyHeaderMatcher struct {
	Name        string            `json:"name"`
	StringMatch *envoyStringMatch `json:"string_match,omitempty"`
}

// envoyStringMatch matches a string by exact value, prefix, or suffix.
type envoyStringMatch struct {
	Exact  string `json:"exact,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
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
	Cluster        string               `json:"cluster"`
	UpgradeConfigs []envoyUpgradeConfig `json:"upgrade_configs,omitempty"`
}

// envoyConnectConfig is the per-route CONNECT tunnel configuration.
type envoyConnectConfig struct{}

// ── Cluster ───────────────────────────────────────────────────────────────────

// envoyCluster is an Envoy upstream cluster definition.
type envoyCluster struct {
	Name           string               `json:"name"`
	ConnectTimeout string               `json:"connect_timeout"`
	LbPolicy       string               `json:"lb_policy,omitempty"`
	Type           string               `json:"type,omitempty"`
	ClusterType    *envoyClusterType    `json:"cluster_type,omitempty"`
	LoadAssignment *envoyLoadAssignment `json:"load_assignment,omitempty"`
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
		Type:           typeHCM,
		StatPrefix:     "egress_http",
		AccessLog:      stdoutAccessLog(),
		UpgradeConfigs: []envoyUpgradeConfig{{UpgradeType: "CONNECT"}},
		HTTPFilters:    httpFilters,
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
						// Prefix match handles plain HTTP requests.
						{
							Match: envoyRouteMatch{Prefix: "/"},
							Route: &envoyRouteAction{Cluster: dfpClusterName},
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

// buildGatewayDenyRBAC builds an RBAC DENY filter that blocks the Docker
// bridge gateway IP (L3 CIDR) and the Docker-internal hostnames (L7 authority
// header). This filter must precede the allowlist filter.
func buildGatewayDenyRBAC(gatewayIP string) *envoyHTTPRBAC {
	return &envoyHTTPRBAC{
		Type: typeHTTPRBAC,
		Rules: envoyRBACRules{
			Action: "DENY",
			Policies: map[string]envoyRBACPolicy{
				"gateway-ip": {
					Permissions: []envoyPermission{
						{
							DestinationIP: &envoyCIDRRange{
								AddressPrefix: gatewayIP,
								PrefixLen:     32,
							},
						},
					},
					Principals: []envoyPrincipal{{Any: true}},
				},
				"gateway-hostnames": {
					Permissions: []envoyPermission{
						{
							// Prefix match covers both plain HTTP (:authority = "host.docker.internal")
							// and HTTPS CONNECT (:authority = "host.docker.internal:443").
							OrRules: &envoyOrRules{
								Rules: []envoyPermission{
									{
										Header: &envoyHeaderMatcher{
											Name: ":authority",
											StringMatch: &envoyStringMatch{
												Prefix: dockerGatewayHostname,
											},
										},
									},
									{
										Header: &envoyHeaderMatcher{
											Name: ":authority",
											StringMatch: &envoyStringMatch{
												Prefix: dockerAltGatewayHostname,
											},
										},
									},
								},
							},
						},
					},
					Principals: []envoyPrincipal{{Any: true}},
				},
			},
		},
	}
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
// InsecureAllowAll produces a single wildcard policy. AllowHost entries become
// :authority header matchers (exact for plain hostnames, suffix for *.prefix).
func buildAllowlistPolicies(spec proxySpec) map[string]envoyRBACPolicy {
	if spec.Permissions == nil || spec.Permissions.Outbound == nil {
		return make(map[string]envoyRBACPolicy)
	}
	out := spec.Permissions.Outbound
	if out.InsecureAllowAll {
		return map[string]envoyRBACPolicy{
			"allow-all": {
				Permissions: []envoyPermission{{Any: boolPtr(true)}},
				Principals:  []envoyPrincipal{{Any: true}},
			},
		}
	}
	policies := make(map[string]envoyRBACPolicy)
	for _, host := range out.AllowHost {
		var match envoyStringMatch
		if strings.HasPrefix(host, "*.") {
			match.Suffix = host[1:] // "*.example.com" → ".example.com"
		} else {
			match.Exact = host
		}
		policies[host] = envoyRBACPolicy{
			Permissions: []envoyPermission{
				{
					Header: &envoyHeaderMatcher{
						Name:        ":authority",
						StringMatch: &match,
					},
				},
			},
			Principals: []envoyPrincipal{{Any: true}},
		}
	}
	return policies
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
							Route: &envoyRouteAction{Cluster: ingressClusterName},
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
// When Inbound.AllowHost is configured those entries are used; otherwise a
// wildcard ("*") is returned so all hostnames are accepted.
func ingressDomains(spec proxySpec) []string {
	if spec.Permissions != nil && spec.Permissions.Inbound != nil &&
		len(spec.Permissions.Inbound.AllowHost) > 0 {
		return spec.Permissions.Inbound.AllowHost
	}
	return []string{"*"}
}

// buildIngressCluster returns the STRICT_DNS upstream cluster for the ingress
// listener, pointing at spec.WorkloadName:spec.UpstreamPort.
func buildIngressCluster(spec proxySpec) envoyCluster {
	return envoyCluster{
		Name:           ingressClusterName,
		ConnectTimeout: "10s",
		Type:           "STRICT_DNS",
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
// mode 0600. Returns the file path. The caller is responsible for cleanup.
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
	// 0600: only the owner can read — the file may contain network topology.
	if err := tmpFile.Chmod(0o600); err != nil {
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

// SetupProxies implements networkProxy for the Envoy backend.
func (e *envoyProxy) SetupProxies(ctx context.Context, spec proxySpec) (proxyResult, error) {
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
			return proxyResult{}, fmt.Errorf("failed to find ingress port: %w", err)
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
		return proxyResult{}, fmt.Errorf("failed to write envoy bootstrap: %w", err)
	}

	envoyImage := getEnvoyImage()
	slog.Debug("setting up envoy container", "name", egressContainerName, "image", envoyImage)

	if err := e.client.imageManager.PullImage(ctx, envoyImage); err != nil {
		_, inspectErr := e.client.imageManager.ImageExists(ctx, envoyImage)
		if inspectErr != nil {
			return proxyResult{}, fmt.Errorf("failed to pull envoy image: %w", err)
		}
		slog.Debug("envoy image exists locally, continuing despite pull failure", "image", envoyImage)
	}

	envoyLabels := map[string]string{}
	lb.AddStandardLabels(envoyLabels, egressContainerName, egressContainerName, "stdio", 80)
	envoyLabels[ToolhiveAuxiliaryWorkloadLabel] = LabelValueTrue

	config := &container.Config{
		Image:  envoyImage,
		Cmd:    []string{"-c", "/etc/envoy/envoy.json"},
		Labels: envoyLabels,
	}

	mounts := []runtime.Mount{
		{
			Source:   configPath,
			Target:   "/etc/envoy/envoy.json",
			ReadOnly: true,
		},
	}

	var exposedPorts map[string]struct{}
	var portBindings map[string][]runtime.PortBinding
	if ingressPort > 0 {
		portKey := fmt.Sprintf("%d/tcp", ingressPort)
		exposedPorts = map[string]struct{}{portKey: {}}
		portBindings = map[string][]runtime.PortBinding{
			portKey: {{HostIP: "127.0.0.1", HostPort: fmt.Sprintf("%d", ingressPort)}},
		}
	}

	hostConfig := &container.HostConfig{
		Mounts:      convertMounts(mounts),
		NetworkMode: container.NetworkMode("bridge"),
		SecurityOpt: []string{"label:disable"},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}
	if portBindings != nil {
		if err := setupPortBindings(hostConfig, portBindings); err != nil {
			return proxyResult{}, fmt.Errorf("failed to setup port bindings: %w", err)
		}
	}
	if err := setupExposedPorts(config, exposedPorts); err != nil {
		return proxyResult{}, fmt.Errorf("failed to setup exposed ports: %w", err)
	}

	if _, err := e.client.createContainer(ctx, egressContainerName, config, hostConfig, spec.Endpoints); err != nil {
		return proxyResult{}, fmt.Errorf("failed to create envoy container: %w", err)
	}

	return proxyResult{
		IngressHostPort: ingressPort,
		EnvVars:         addEgressEnvVars(nil, egressContainerName),
	}, nil
}

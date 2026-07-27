// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvoyConfigParams carries the rendered Envoy bootstrap's variable parts.
type EnvoyConfigParams struct {
	// ExtAuthzAddress is the broker's gRPC listen address (loopback-only);
	// ext_authz AND on-demand SDS are served on the same socket.
	ExtAuthzAddress string
	// ExtAuthzPort is the broker's gRPC listen port.
	ExtAuthzPort int
	// ProxyPort is the explicit-proxy listener the backend reaches via
	// HTTP_PROXY/HTTPS_PROXY (loopback-only).
	ProxyPort int
	// AllowedHosts is the policy's host allowlist; requests to any other host
	// get no route → connection refused (D6b). Also the set of SNI names the
	// SDS server will mint bump certs for — anything else fails the
	// downstream handshake.
	AllowedHosts []string
	// ScanFailOpen renders the ext_proc response scanner's failure_mode_allow
	// (ADR D6c): true = pass responses when the scanner is down/errored
	// (documented default); false = fail closed (502) for high-security
	// tenants. ext_authz stays failure_mode_allow: false either way.
	ScanFailOpen bool
	// ScanMaxBodyBytes is the body cap the broker's scanner enforces in-band
	// (must match the broker's scanner config; default 1 MiB). It does NOT
	// appear in the rendered bootstrap: ext_proc has no per-filter byte cap,
	// so Envoy buffers the whole response body and the broker refuses to scan
	// past the cap (recorded as a skip, headers still scanned). The cap is a
	// cost bound, not a security boundary — the allowlist + destination
	// binding are the boundary (ADR-0001 §5).
	ScanMaxBodyBytes int64
}

// Validate fails loudly on an unrenderable bootstrap (constructor
// validation: misconfiguration must fail at startup).
func (p EnvoyConfigParams) Validate() error {
	if p.ExtAuthzAddress == "" {
		return fmt.Errorf("egressbroker: ext_authz address must not be empty")
	}
	if p.ExtAuthzPort <= 0 || p.ExtAuthzPort > 65535 {
		return fmt.Errorf("egressbroker: ext_authz port %d out of range", p.ExtAuthzPort)
	}
	if p.ProxyPort <= 0 || p.ProxyPort > 65535 {
		return fmt.Errorf("egressbroker: proxy port %d out of range", p.ProxyPort)
	}
	if len(p.AllowedHosts) == 0 {
		return fmt.Errorf("egressbroker: allowed hosts must not be empty")
	}
	if p.ScanMaxBodyBytes <= 0 {
		return fmt.Errorf("egressbroker: scan body cap must be positive")
	}
	return nil
}

// envoyBootstrapTemplate is the explicit-forward-proxy TLS-bump bootstrap
// (D9/D10). The backend reaches the proxy via HTTP_PROXY/HTTPS_PROXY; CONNECT
// tunnels are TLS-bumped with per-SNI certs fetched on demand over SDS from
// the broker process, then re-encrypted upstream to the real destination.
//
// Bump mechanics (the request path, top to bottom):
//  1. tls_inspector reads the CONNECT authority / tunneled ClientHello SNI.
//  2. sni_dynamic_forward_proxy sets the upstream host from SNI.
//  3. The CONNECT route upgrade (connect_config) terminates the tunnel: the
//     downstream_tls_context has NO static cert, so Envoy on-demand-fetches
//     the SDS resource "host:<SNI>" from the broker's sds_grpc cluster. The
//     broker mints the cert only for policy-allowlisted SNI; anything else
//     gets no cert → handshake failure (fail closed).
//  4. Tunneled requests pass ext_authz (credential injection, D5) and route
//     to dynamic_forward_proxy_cluster, whose upstream TLS context
//     re-encrypts to the destination, SNI from the CONNECT authority,
//     validated against the system CA bundle (real upstream certs).
//
// Security invariants baked into the template (do not relax):
//   - ext_authz failure_mode_allow: false — a dead injector denies, never
//     passes unauthenticated traffic.
//   - A Lua HTTP filter runs BEFORE ext_authz/ext_proc and copies
//     x-request-id and :authority into dynamic metadata namespace
//     io.toolhive.egress. This is the ONLY working D6c correlation path:
//     route filter_metadata values are stored literally (Envoy performs no
//     formatter substitution there), and the response-path ext_proc cannot
//     see request headers. ext_proc forwards the namespace per its
//     metadata_options.forwarding_namespaces.
//   - ext_proc (D6c response scanner) runs response headers+body only
//     (request phase is pass-through; injection stays in ext_authz), with
//     message_timeout 2s and failure_mode_allow wired from broker config
//     (documented fail-open default; fail-closed for high-security tenants).
//     Body mode is BUFFERED: ext_proc has no per-filter byte cap, so Envoy
//     buffers the whole body; the byte cap lives in the broker's in-band
//     scanner (over-cap bodies pass with a skip metric, headers still
//     scanned). The cap is a cost bound, not a security boundary — the
//     allowlist + destination binding are the boundary (ADR-0001 §5).
//   - The route table matches only allowlisted hosts; anything else has no
//     route → connection refused before ext_authz is even consulted.
//   - preserve_external_request_id is false (the Envoy default, pinned by
//     test): the untrusted server controls its own outbound x-request-id, so
//     client-supplied ids are always discarded and Envoy generates one —
//     collision poisoning and premature-consumption evasion against the D6c
//     correlation map are impossible.
//   - No redirect-following filter exists: Envoy as a forward proxy passes
//     3xx responses through untouched (D6a).
//   - Listeners and the broker cluster bind loopback only; the SDS secret
//     stream never leaves the pod.
//   - upstream_validation_context pins trust to the system CA bundle — the
//     bump CA is only ever presented to the backend container, never used to
//     validate real upstreams.
const envoyBootstrapTemplate = `node:
  id: thv-egress-sidecar
  cluster: thv-untrusted-egress
static_resources:
  listeners:
  - name: https_proxy
    address:
      socket_address:
        address: 127.0.0.1
        port_value: 0 # set from params at render time
    listener_filters:
    - name: envoy.filters.listener.tls_inspector
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
    filter_chains:
    - filters:
      - name: envoy.filters.network.sni_dynamic_forward_proxy
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.sni_dynamic_forward_proxy.v3.FilterConfig
          port_value: 443
          dns_cache_config:
            name: dynamic_forward_proxy_cache
            dns_lookup_family: V4_ONLY
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: egress_broker
          http2_protocol_options:
            allow_connect: true
          upgrade_configs:
          - upgrade_type: CONNECT
            enabled: true
          request_id_extension:
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.request_id.uuid.v3.UuidRequestIdConfig
          route_config:
            name: egress_routes
            virtual_hosts: [] # one vhost per allowlisted host, built as data at render time
          http_filters:
          - name: envoy.filters.http.lua
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua
              default_source_code:
                inline_string: |
                  -- Copies the D6c correlation values into dynamic metadata so
                  -- the response-path ext_proc scanner can find the injection's
                  -- scan record (it cannot see request headers, and route
                  -- filter_metadata values are stored literally — no formatter
                  -- substitution happens there).
                  function envoy_on_request(request_handle)
                    local metadata = request_handle:metadata()
                    metadata:set("io.toolhive.egress", "request_id", request_handle:headers():get("x-request-id") or "")
                    metadata:set("io.toolhive.egress", "host", request_handle:headers():get(":authority") or "")
                  end
          - name: envoy.filters.http.ext_authz
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
              failure_mode_allow: false
              grpc_service:
                envoy_grpc:
                  cluster_name: egress_broker
                timeout: 5s
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              failure_mode_allow: true # wired from broker scan config at render time
              message_timeout: 2s
              # NOTE: ext_proc has no per-filter body byte cap (no max_bytes or
              # allow_partial_message field exists on this filter in the pinned
              # Envoy v1.36 / go-control-plane v1.37): Envoy buffers the whole
              # response body per the BUFFERED mode below. The byte cap is
              # enforced in-band by the broker's scanner (over-cap bodies pass
              # with a skip metric; headers are always scanned).
              grpc_service:
                envoy_grpc:
                  cluster_name: egress_broker
                timeout: 2s
              processing_mode:
                request_header_mode: SKIP
                request_body_mode: NONE
                request_trailer_mode: SKIP
                response_header_mode: SEND
                response_body_mode: BUFFERED
                response_trailer_mode: SKIP
              metadata_options:
                forwarding_namespaces:
                  typed:
                  - io.toolhive.egress
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
      transport_socket:
        name: envoy.transport_sockets.tls
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
          common_tls_context:
            tls_certificate_sds_secret_configs:
            - name: envoy.transport_sockets.tls.v3.SdsSecretConfig
              sds_config:
                api_config_source:
                  api_type: GRPC
                  transport_api_version: V3
                  grpc_services:
                  - envoy_grpc:
                      cluster_name: egress_broker
  clusters:
  - name: egress_broker
    type: STATIC
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
        explicit_http_config:
          http2_protocol_options: {}
    load_assignment:
      cluster_name: egress_broker
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: 127.0.0.1 # set from params at render time
                port_value: 0      # set from params at render time
  - name: dynamic_forward_proxy_cluster
    lb_policy: CLUSTER_PROVIDED
    cluster_type:
      name: envoy.clusters.dynamic_forward_proxy
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig
        dns_cache_config:
          name: dynamic_forward_proxy_cache
          dns_lookup_family: V4_ONLY
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        common_tls_context:
          validation_context:
            trusted_ca:
              filename: /etc/ssl/certs/ca-certificates.crt
`

// bootstrapDoc is the typed mirror of the rendered bootstrap: only the
// config-derived values are typed fields; everything else round-trips through
// raw yaml.Nodes (gopkg.in/yaml.v3 cannot decode into *yaml.Node sequence
// elements, so every raw list is a single wrapping node).
type bootstrapDoc struct {
	// Node carries the Envoy node id/cluster required by the SDS API. Kept as
	// a yaml.Node (not typed fields) so it round-trips verbatim through the
	// unmarshal/mutate/marshal render path — a typed struct that omits it
	// silently drops the section and Envoy rejects the bootstrap with
	// "node 'id' and 'cluster' are required".
	Node            yaml.Node `yaml:"node"`
	StaticResources struct {
		Listeners []struct {
			Name    string `yaml:"name"`
			Address struct {
				SocketAddress struct {
					Address   string `yaml:"address"`
					PortValue int    `yaml:"port_value"`
				} `yaml:"socket_address"`
			} `yaml:"address"`
			ListenerFilters yaml.Node `yaml:"listener_filters"`
			FilterChains    []struct {
				Filters         yaml.Node `yaml:"filters"`
				TransportSocket yaml.Node `yaml:"transport_socket"`
			} `yaml:"filter_chains"`
		} `yaml:"listeners"`
		Clusters []clusterDoc `yaml:"clusters"`
	} `yaml:"static_resources"`
}

// clusterDoc mirrors one static cluster; the broker cluster's endpoint address
// is config-derived (navigated into LoadAssignment), the rest is static.
type clusterDoc struct {
	Name                          string    `yaml:"name"`
	Type                          string    `yaml:"type,omitempty"`
	TypedExtensionProtocolOptions yaml.Node `yaml:"typed_extension_protocol_options,omitempty"`
	LoadAssignment                yaml.Node `yaml:"load_assignment,omitempty"`
	LBPolicy                      string    `yaml:"lb_policy,omitempty"`
	ClusterType                   yaml.Node `yaml:"cluster_type,omitempty"`
	TransportSocket               yaml.Node `yaml:"transport_socket,omitempty"`
}

// RenderEnvoyBootstrap renders the Envoy bootstrap YAML for params. The
// result contains no secrets — the bump-CA material is delivered to Envoy
// via SDS at runtime, not through the bootstrap file. AllowedHosts is sorted
// so the rendered document is deterministic.
//
// Hybrid render (mirrors the operator's renderEgressPolicyYAML approach): the
// static listener/filter skeleton stays a YAML template; EVERY config-derived
// value (ports, addresses, scan fail-open, the allowlisted-host route table)
// is built as typed data and re-emitted through yaml.Marshal — no
// string substitution ever touches a value an operator could influence.
func RenderEnvoyBootstrap(params EnvoyConfigParams) ([]byte, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	var doc bootstrapDoc
	if err := yaml.Unmarshal([]byte(envoyBootstrapTemplate), &doc); err != nil {
		return nil, fmt.Errorf("egressbroker: failed to parse envoy bootstrap template: %w", err)
	}

	doc.StaticResources.Listeners[0].Address.SocketAddress.PortValue = params.ProxyPort

	filters := doc.StaticResources.Listeners[0].FilterChains[0].Filters
	hcm, err := nodeByName(filters.Content, "envoy.filters.network.http_connection_manager")
	if err != nil {
		return nil, err
	}
	if err := setNodeMapping(hcm, "typed_config.route_config.virtual_hosts",
		buildVirtualHosts(params.AllowedHosts)); err != nil {
		return nil, err
	}
	httpFilters, err := nodeByPath(hcm, "typed_config", "http_filters")
	if err != nil {
		return nil, err
	}
	if httpFilters.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("egressbroker: bootstrap template: http_filters is not a sequence")
	}
	extProc, err := nodeByName(httpFilters.Content, "envoy.filters.http.ext_proc")
	if err != nil {
		return nil, err
	}
	if err := setNodeMapping(extProc, "typed_config.failure_mode_allow",
		boolNode(params.ScanFailOpen)); err != nil {
		return nil, err
	}

	broker, err := clusterByName(doc.StaticResources.Clusters, "egress_broker")
	if err != nil {
		return nil, err
	}
	socketAddr, err := nodeByPath(&broker.LoadAssignment, "endpoints")
	if err != nil {
		return nil, err
	}
	socketAddr, err = nodeByIndex(socketAddr, 0, "lb_endpoints", 0, "endpoint", "address", "socket_address")
	if err != nil {
		return nil, err
	}
	if err := setNodeMapping(socketAddr, "address", scalarNode(params.ExtAuthzAddress)); err != nil {
		return nil, err
	}
	if err := setNodeMapping(socketAddr, "port_value", intNode(params.ExtAuthzPort)); err != nil {
		return nil, err
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to marshal envoy bootstrap: %w", err)
	}
	return out, nil
}

// buildVirtualHosts builds one allowlisted-host route entry per host as data.
// The CONNECT upgrade terminates the tunnel downstream via the filter chain's
// on-demand SDS cert (resource "host:<SNI>") and re-originates upstream
// through the dynamic forward proxy; every request — the CONNECT itself and
// the decrypted tunneled requests — passes ext_authz (no per-route disable:
// the credential injection on tunneled traffic IS the feature). Non-CONNECT
// routes (plain absolute-form HTTP through the proxy) share the same
// upstream. The D6c correlation metadata is NOT set here: route
// filter_metadata values are stored literally (no %REQ()% formatter
// substitution) — the Lua HTTP filter upstream in the chain sets them
// instead. Host patterns are sorted so the render is deterministic.
func buildVirtualHosts(allowedHosts []string) yaml.Node {
	hosts := slices.Clone(allowedHosts)
	slices.Sort(hosts)
	vhosts := make([]any, 0, len(hosts))
	for i, host := range hosts {
		vhosts = append(vhosts, map[string]any{
			"name":    sanitizeRouteName(host, i),
			"domains": []string{host},
			"routes": []any{
				map[string]any{
					"match": map[string]any{"connect_matcher": map[string]any{}},
					"route": map[string]any{
						"cluster": "dynamic_forward_proxy_cluster",
						"upgrade_configs": []any{
							map[string]any{
								"upgrade_type":   "CONNECT",
								"connect_config": map[string]any{},
							},
						},
					},
				},
				map[string]any{
					"match": map[string]any{"prefix": "/"},
					"route": map[string]any{"cluster": "dynamic_forward_proxy_cluster"},
				},
			},
		})
	}
	var node yaml.Node
	// The value tree is fully static-typed above; a marshal failure is
	// unreachable.
	_ = node.Encode(vhosts)
	return node
}

// nodeByName finds the entry of a YAML sequence of {"name": ..., ...} maps
// whose name matches (the elements are the same *yaml.Node pointers the
// caller's struct holds, so mutations through the result are visible on
// re-marshal).
func nodeByName(seq []*yaml.Node, name string) (*yaml.Node, error) {
	for _, entry := range seq {
		if entry == nil || entry.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(entry.Content); i += 2 {
			if entry.Content[i].Value == "name" && entry.Content[i+1].Value == name {
				return entry, nil
			}
		}
	}
	return nil, fmt.Errorf("egressbroker: bootstrap template: entry %q not found", name)
}

// nodeByPath walks mapping keys from node.
func nodeByPath(node *yaml.Node, path ...string) (*yaml.Node, error) {
	cur := node
	for _, key := range path {
		next, err := mappingValue(cur, key)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

// nodeByIndex walks sequence indexes (interleaved with mapping keys).
func nodeByIndex(node *yaml.Node, index int, path ...any) (*yaml.Node, error) {
	cur := node
	if cur.Kind != yaml.SequenceNode || index >= len(cur.Content) {
		return nil, fmt.Errorf("egressbroker: bootstrap template: missing sequence index %d", index)
	}
	cur = cur.Content[index]
	for _, step := range path {
		switch s := step.(type) {
		case string:
			next, err := mappingValue(cur, s)
			if err != nil {
				return nil, err
			}
			cur = next
		case int:
			if cur.Kind != yaml.SequenceNode || s >= len(cur.Content) {
				return nil, fmt.Errorf("egressbroker: bootstrap template: missing sequence index %d", s)
			}
			cur = cur.Content[s]
		}
	}
	return cur, nil
}

// mappingValue returns the value node for key in a mapping node.
func mappingValue(node *yaml.Node, key string) (*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("egressbroker: bootstrap template: expected mapping at %q, got kind %d", key, node.Kind)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], nil
		}
	}
	return nil, fmt.Errorf("egressbroker: bootstrap template: key %q not found", key)
}

// setNodeMapping replaces the value at the end of a mapping-key path.
func setNodeMapping(node *yaml.Node, path string, value yaml.Node) error {
	parent := node
	keys := strings.Split(path, ".")
	for _, key := range keys[:len(keys)-1] {
		next, err := mappingValue(parent, key)
		if err != nil {
			return err
		}
		parent = next
	}
	if parent.Kind != yaml.MappingNode {
		return fmt.Errorf("egressbroker: bootstrap template: expected mapping at %q", path)
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == keys[len(keys)-1] {
			v := value
			parent.Content[i+1] = &v
			return nil
		}
	}
	return fmt.Errorf("egressbroker: bootstrap template: key %q not found", path)
}

// clusterByName finds a static cluster by name.
func clusterByName(clusters []clusterDoc, name string) (*clusterDoc, error) {
	for i := range clusters {
		if clusters[i].Name == name {
			return &clusters[i], nil
		}
	}
	return nil, fmt.Errorf("egressbroker: bootstrap template: cluster %q not found", name)
}

func boolNode(b bool) yaml.Node {
	var node yaml.Node
	_ = node.Encode(b) // bool encode cannot fail
	return node
}

func intNode(i int) yaml.Node {
	var node yaml.Node
	_ = node.Encode(i) // int encode cannot fail
	return node
}

func scalarNode(s string) yaml.Node {
	var node yaml.Node
	_ = node.Encode(s) // string encode cannot fail
	return node
}

// WriteEnvoyBootstrap renders and writes the bootstrap to path with
// owner-only permissions.
func WriteEnvoyBootstrap(path string, params EnvoyConfigParams) error {
	data, err := RenderEnvoyBootstrap(params)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("egressbroker: failed to write envoy bootstrap: %w", err)
	}
	return nil
}

// sanitizeRouteName makes a valid Envoy route/virtual-host name from a host
// pattern. Names carry no security semantics (domains do the matching), so
// collisions are broken with the index.
func sanitizeRouteName(host string, i int) string {
	name := strings.NewReplacer("*", "wildcard", ":", "_").Replace(host)
	return fmt.Sprintf("host-%d-%s", i, name)
}

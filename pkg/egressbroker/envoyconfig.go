// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
//   - The route table matches only allowlisted hosts; anything else has no
//     route → connection refused before ext_authz is even consulted.
//   - No redirect-following filter exists: Envoy as a forward proxy passes
//     3xx responses through untouched (D6a).
//   - Listeners and the broker cluster bind loopback only; the SDS secret
//     stream never leaves the pod.
//   - upstream_validation_context pins trust to the system CA bundle — the
//     bump CA is only ever presented to the backend container, never used to
//     validate real upstreams.
const envoyBootstrapTemplate = `static_resources:
  listeners:
  - name: https_proxy
    address:
      socket_address:
        address: 127.0.0.1
        port_value: {{PROXY_PORT}}
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
          route_config:
            name: egress_routes
            virtual_hosts:
{{VHOSTS}}
          http_filters:
          - name: envoy.filters.http.ext_authz
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
              failure_mode_allow: false
              with_request_body:
                max_request_bytes: 0
              grpc_service:
                envoy_grpc:
                  cluster_name: egress_broker
                timeout: 5s
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
                address: {{EXT_AUTHZ_ADDRESS}}
                port_value: {{EXT_AUTHZ_PORT}}
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

// vhostTemplate is one allowlisted-host route entry. The CONNECT upgrade
// terminates the tunnel downstream via the filter chain's on-demand SDS cert
// (resource "host:<SNI>") and re-originates upstream through the dynamic
// forward proxy; every request — the CONNECT itself and the decrypted tunneled
// requests — passes ext_authz (no per-route disable: the credential injection
// on tunneled traffic IS the feature). Non-CONNECT routes (plain absolute-form
// HTTP through the proxy) share the same upstream.
const vhostTemplate = `            - name: {{HOST_NAME}}
              domains: ["{{HOST}}"]
              routes:
              - match:
                  connect_matcher: {}
                route:
                  cluster: dynamic_forward_proxy_cluster
                  upgrade_configs:
                  - upgrade_type: CONNECT
                    connect_config:
                      terminate_connect: true
              - match:
                  prefix: /
                route:
                  cluster: dynamic_forward_proxy_cluster
`

// RenderEnvoyBootstrap renders the Envoy bootstrap YAML for params. The
// result contains no secrets — the bump-CA material is delivered to Envoy
// via SDS at runtime, not through the bootstrap file. AllowedHosts is sorted
// so the rendered document is deterministic.
func RenderEnvoyBootstrap(params EnvoyConfigParams) ([]byte, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	hosts := make([]string, len(params.AllowedHosts))
	copy(hosts, params.AllowedHosts)
	sort.Strings(hosts)

	var vhosts strings.Builder
	for i, host := range hosts {
		entry := strings.ReplaceAll(vhostTemplate, "{{HOST_NAME}}", sanitizeRouteName(host, i))
		entry = strings.ReplaceAll(entry, "{{HOST}}", host)
		vhosts.WriteString(entry)
	}
	out := strings.ReplaceAll(envoyBootstrapTemplate, "{{PROXY_PORT}}", fmt.Sprintf("%d", params.ProxyPort))
	out = strings.ReplaceAll(out, "{{EXT_AUTHZ_ADDRESS}}", params.ExtAuthzAddress)
	out = strings.ReplaceAll(out, "{{EXT_AUTHZ_PORT}}", fmt.Sprintf("%d", params.ExtAuthzPort))
	out = strings.ReplaceAll(out, "{{VHOSTS}}", vhosts.String())
	return []byte(out), nil
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package netproxy provides runtime-agnostic network proxy configuration
// builders (Squid and Envoy) shared across local container runtimes.
package netproxy

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"os"

	"github.com/stacklok/toolhive-core/permissions"
)

const (
	// AuxiliaryWorkloadLabel marks helper containers (egress/ingress/dns proxies)
	// that should be excluded from normal workload listings.
	AuxiliaryWorkloadLabel = "toolhive-auxiliary-workload"

	// LabelValueTrue is the string value for boolean true labels.
	LabelValueTrue = "true"

	// BackendSquid selects the Squid egress/ingress proxy backend.
	BackendSquid = "squid"

	// BackendEnvoy selects the Envoy egress/ingress proxy backend.
	BackendEnvoy = "envoy"

	// DefaultSquidImage is the default container image used for Squid proxies.
	DefaultSquidImage = "ghcr.io/stacklok/toolhive/egress-proxy:latest"

	// DefaultEnvoyImage is pinned by tag+digest for supply-chain integrity.
	// TOOLHIVE_ENVOY_IMAGE overrides must use Envoy v1.39+ with DFP
	// resolved_address_filter support.
	DefaultEnvoyImage = "envoyproxy/envoy:distroless-v1.39.1@sha256:eb2c01c13125d1629637cb4e4cce7207009fb7cc2c8027f9742758549d15b6f4"

	// PodmanGatewayHostname is Podman's host-gateway alias.
	PodmanGatewayHostname = "host.containers.internal"

	// DockerGatewayHostname is Docker's primary host-gateway alias.
	DockerGatewayHostname = "host.docker.internal"

	// DockerAltGatewayHostname is Docker's alternate host-gateway alias.
	DockerAltGatewayHostname = "gateway.docker.internal"
)

// HostGatewayNames lists the container-runtime host-gateway aliases blocked by
// default in egress proxies to prevent unintended host access.
var HostGatewayNames = [...]string{
	DockerGatewayHostname,
	DockerAltGatewayHostname,
	PodmanGatewayHostname,
}

// Spec contains the runtime-agnostic parameters needed to generate egress and
// ingress proxy configurations for an isolated workload.
type Spec struct {
	// WorkloadName is the base name of the MCP container (e.g. "myserver").
	WorkloadName string
	// UpstreamHost is the address the ingress reverse proxy connects to for the
	// MCP container. It is typically the container's resolved IP on the internal
	// network (not its name) so the ingress proxy has no DNS dependency: under
	// concurrent startup the container's DNS record can lag, and a hostname-based
	// Squid cache_peer caches the failed lookup and never recovers within the
	// readiness window (see #6063). Empty falls back to WorkloadName.
	UpstreamHost string
	// Permissions holds the network permission profile governing allowed
	// outbound and inbound traffic.
	Permissions *permissions.NetworkPermissions
	// AllowDockerGateway explicitly allows container host gateway IPs and
	// aliases on any port, replacing the default deny rules.
	AllowDockerGateway bool
	// GatewayIPs is the canonical set of host/bridge gateway addresses to block
	// (or allow when AllowDockerGateway is true).
	GatewayIPs []netip.Addr
	// TransportType is the MCP transport in use (e.g. "stdio", "sse",
	// "streamable-http"). A value of "stdio" suppresses ingress proxy creation.
	TransportType string
	// UpstreamPort is the container port the MCP server listens on. Ignored
	// when TransportType is "stdio" or the value is 0.
	UpstreamPort int
	// AttachStdio controls whether the proxy containers attach stdio streams.
	AttachStdio bool
}

// SelectedBackend reads TOOLHIVE_NETWORK_PROXY and returns the canonical
// backend name ("squid" or "envoy"). An empty value defaults to "squid".
func SelectedBackend() (string, error) {
	val := os.Getenv("TOOLHIVE_NETWORK_PROXY")
	switch val {
	case "", BackendSquid:
		return BackendSquid, nil
	case BackendEnvoy:
		return BackendEnvoy, nil
	default:
		return "", fmt.Errorf("unknown TOOLHIVE_NETWORK_PROXY value %q: supported values are \"squid\" and \"envoy\"", val)
	}
}

// GetSquidImage returns the configured Squid proxy image, respecting
// TOOLHIVE_EGRESS_IMAGE.
func GetSquidImage() string {
	if egressImage := os.Getenv("TOOLHIVE_EGRESS_IMAGE"); egressImage != "" {
		return egressImage
	}
	return DefaultSquidImage
}

// GetEnvoyImage returns the configured Envoy proxy image, respecting
// TOOLHIVE_ENVOY_IMAGE.
func GetEnvoyImage() string {
	if img := os.Getenv("TOOLHIVE_ENVOY_IMAGE"); img != "" {
		return img
	}
	return DefaultEnvoyImage
}

// AddEgressEnvVars returns a new map containing all entries from envVars plus
// the HTTP_PROXY/HTTPS_PROXY/NO_PROXY variables pointing at proxyHost:3128.
// The caller's map is never mutated.
func AddEgressEnvVars(envVars map[string]string, proxyHost string) map[string]string {
	egressHost := "http://" + net.JoinHostPort(proxyHost, "3128")
	result := maps.Clone(envVars)
	if result == nil {
		result = make(map[string]string)
	}
	result["HTTP_PROXY"] = egressHost
	result["HTTPS_PROXY"] = egressHost
	result["http_proxy"] = egressHost
	result["https_proxy"] = egressHost
	result["NO_PROXY"] = "localhost,127.0.0.1,::1"
	result["no_proxy"] = "localhost,127.0.0.1,::1"
	return result
}

// MergeEnvVars returns a new map containing all entries from base with all
// entries from extra added (extra values overwrite base on conflict). Neither
// input map is mutated.
func MergeEnvVars(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"os"

	"github.com/moby/moby/api/types/network"

	"github.com/stacklok/toolhive-core/permissions"
)

// networkProxy is the single enforcement point for outbound allowlisting,
// docker-gateway blocking, and inbound reverse-proxying for isolated workloads.
//
// What it enables: all egress and ingress proxy containers are created through
// this interface, ensuring a consistent policy-enforcement seam that can be
// swapped at startup via TOOLHIVE_NETWORK_PROXY.
//
// What it does NOT solve: non-cooperative egress is contained by the
// Internal:true network blackhole created by createNetwork, not by this proxy.
// True L3/L4 traffic interception is a separate Phase 2 concern and is not
// addressed here.
type networkProxy interface {
	// SetupEgress provisions egress enforcement BEFORE the MCP container is
	// created and returns the environment variables to inject into the workload
	// (HTTP_PROXY etc.). A per-container backend (squid) creates its egress
	// container here; a consolidated backend (envoy) creates its single
	// dual-listener container here and reserves the ingress port, carried back in
	// the egressResult for SetupIngress to return.
	//
	// It must run before createMcpContainer so the returned env vars land in the
	// workload's environment and the egress proxy has a head start.
	SetupEgress(ctx context.Context, spec proxySpec) (egressResult, error)

	// SetupIngress finalizes the ingress proxy AFTER the MCP container exists and
	// returns the host-side ingress port (0 for stdio / UpstreamPort==0). A
	// per-container backend (squid) creates its ingress container here — now that
	// the MCP container's hostname resolves, avoiding a cached negative DNS lookup
	// that would leave the reverse proxy permanently unable to reach the upstream.
	// A consolidated backend returns the port it reserved in SetupEgress.
	//
	// It must run after createMcpContainer.
	SetupIngress(ctx context.Context, spec proxySpec, egress egressResult) (int, error)
}

// proxySpec contains all the parameters needed to set up proxy containers for
// an isolated workload.
type proxySpec struct {
	// WorkloadName is the base name of the MCP container (e.g. "myserver").
	WorkloadName string
	// Permissions holds the network permission profile from the workload's
	// permission profile, governing what outbound traffic is allowed.
	Permissions *permissions.NetworkPermissions
	// AllowDockerGateway, when true, skips the docker-gateway deny rules in the
	// egress proxy configuration.
	AllowDockerGateway bool
	// GatewayIP is the Docker bridge gateway IP resolved at runtime.
	GatewayIP string
	// TransportType is the MCP transport in use (e.g. "stdio", "sse",
	// "streamable-http"). A value of "stdio" suppresses ingress proxy creation.
	TransportType string
	// UpstreamPort is the container port the MCP server listens on. Ignored
	// when TransportType is "stdio" or the value is 0.
	UpstreamPort int
	// AttachStdio controls whether the proxy containers attach stdio streams.
	AttachStdio bool
	// Endpoints is the set of network endpoints the proxy containers should
	// join, keyed by network name.
	Endpoints map[string]*network.EndpointSettings
	// DNSServers is an optional list of DNS server IPs to configure on the proxy
	// containers. Used to inject the dnsmasq container IP so that Envoy's
	// STRICT_DNS cluster can resolve MCP container hostnames on the internal
	// Docker network (Linux Docker Engine requires explicit DNS configuration;
	// the default bridge DNS cannot resolve names from custom internal networks).
	DNSServers []string
}

// egressResult is the output of a successful SetupEgress call. It is passed to
// SetupIngress so backends can carry any state set up during egress forward
// without holding per-workload state on the shared proxy.
type egressResult struct {
	// EnvVars contains environment variables that must be merged into the MCP
	// container's environment (e.g. HTTP_PROXY, HTTPS_PROXY).
	EnvVars map[string]string
}

// newNetworkProxy reads the TOOLHIVE_NETWORK_PROXY environment variable and
// returns the corresponding networkProxy implementation. An empty value or
// "squid" selects the default squid-based proxy. Any other value returns an
// error so that misconfiguration is caught at startup.
func newNetworkProxy(c *Client) (networkProxy, error) {
	val := os.Getenv("TOOLHIVE_NETWORK_PROXY")
	switch val {
	case "", "squid":
		return &squidProxy{client: c}, nil
	case "envoy":
		return &envoyProxy{client: c}, nil
	default:
		return nil, fmt.Errorf("unknown TOOLHIVE_NETWORK_PROXY value %q: supported values are \"squid\" (default), \"envoy\"", val)
	}
}

// Compile-time assertion that squidProxy satisfies networkProxy.
var _ networkProxy = (*squidProxy)(nil)

// Compile-time assertion that envoyProxy satisfies networkProxy.
var _ networkProxy = (*envoyProxy)(nil)

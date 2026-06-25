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

	envoyHTTPRBACFilterName    = "envoy.filters.http.rbac"
	envoyGatewayDenyFilterName = "toolhive.gateway.deny"
)

func getEnvoyImage() string {
	if img := os.Getenv("TOOLHIVE_ENVOY_IMAGE"); img != "" {
		return img
	}
	return defaultEnvoyImage
}

// envoyBootstrap is the top-level Envoy bootstrap configuration.
type envoyBootstrap struct {
	Admin           *envoyAdmin `json:"admin,omitempty"`
	StaticResources envoyStatic `json:"static_resources"`
}

// envoyAdmin configures the Envoy admin API endpoint.
type envoyAdmin struct {
	Address envoyAddress `json:"address"`
}

// envoyAddress wraps a socket address for Envoy config.
type envoyAddress struct {
	SocketAddress envoySocketAddress `json:"socket_address"`
}

// envoySocketAddress is an IP + port pair used throughout Envoy config.
type envoySocketAddress struct {
	Address   string `json:"address"`
	PortValue int    `json:"port_value"`
}

// envoyStatic holds the static listeners and clusters.
type envoyStatic struct {
	Listeners []envoyListener `json:"listeners,omitempty"`
	Clusters  []envoyCluster  `json:"clusters,omitempty"`
}

// envoyListener is an Envoy listener binding on a socket address.
type envoyListener struct {
	Name         string             `json:"name"`
	Address      envoyAddress       `json:"address"`
	FilterChains []envoyFilterChain `json:"filter_chains"`
}

// envoyFilterChain is a sequence of filters applied to matching connections.
type envoyFilterChain struct {
	Filters []envoyFilter `json:"filters"`
}

// envoyFilter is a named filter whose typed config is serialized with custom
// marshal/unmarshal logic so that TypedConfig can be asserted as a concrete
// Go type (e.g. *envoyRBACFilter) while still round-tripping through JSON.
type envoyFilter struct {
	Name        string `json:"name"`
	TypedConfig any    `json:"-"`
}

// MarshalJSON serializes the filter including TypedConfig under "typed_config".
func (f envoyFilter) MarshalJSON() ([]byte, error) {
	type proxy struct {
		Name        string `json:"name"`
		TypedConfig any    `json:"typed_config,omitempty"`
	}
	return json.Marshal(proxy(f))
}

// UnmarshalJSON restores TypedConfig as the correct concrete Go type by
// switching on the filter name.
func (f *envoyFilter) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string          `json:"name"`
		TypedConfig json.RawMessage `json:"typed_config,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.Name = raw.Name
	if len(raw.TypedConfig) == 0 {
		return nil
	}
	switch raw.Name {
	case envoyHTTPRBACFilterName:
		var rbac envoyRBACFilter
		if err := json.Unmarshal(raw.TypedConfig, &rbac); err != nil {
			return err
		}
		f.TypedConfig = &rbac
	case envoyGatewayDenyFilterName:
		var deny envoyGatewayDenyConfig
		if err := json.Unmarshal(raw.TypedConfig, &deny); err != nil {
			return err
		}
		f.TypedConfig = &deny
	default:
		var m map[string]any
		if err := json.Unmarshal(raw.TypedConfig, &m); err != nil {
			return err
		}
		f.TypedConfig = m
	}
	return nil
}

// envoyRBACFilter is the HTTP RBAC filter for allowlisting outbound traffic.
// This is the type returned by findRBACFilter in tests.
type envoyRBACFilter struct {
	Rules envoyRBACRules `json:"rules"`
}

// envoyRBACRules holds the RBAC action and policy map.
// CRITICAL: Policies must NOT have omitempty. An empty map serializes as {} not
// be omitted — omitting it would silently turn deny-all into allow-all after a
// JSON round-trip.
type envoyRBACRules struct {
	Action   string                     `json:"action"`
	Policies map[string]envoyRBACPolicy `json:"policies"`
}

// envoyRBACPolicy pairs permissions with principals for a single RBAC policy.
type envoyRBACPolicy struct {
	Permissions []envoyPermission `json:"permissions"`
	Principals  []envoyPrincipal  `json:"principals"`
}

// envoyPermission matches a request by header or wildcard.
type envoyPermission struct {
	Header *envoyHeaderMatcher `json:"header,omitempty"`
	Any    bool                `json:"any,omitempty"`
}

// envoyHeaderMatcher matches an HTTP header by exact or suffix value.
type envoyHeaderMatcher struct {
	Name        string `json:"name"`
	ExactMatch  string `json:"exact_match,omitempty"`
	SuffixMatch string `json:"suffix_match,omitempty"`
}

// envoyPrincipal matches a downstream principal (wildcard only for now).
type envoyPrincipal struct {
	Any bool `json:"any,omitempty"`
}

// envoyGatewayDenyConfig encodes the two-layer docker-gateway block:
// L3 CIDR deny (GatewayIP) and L7 hostname deny (GatewayHostnames).
// This serializes to JSON that contains both the gateway IP and hostnames,
// satisfying the two-layer requirement from the design doc.
type envoyGatewayDenyConfig struct {
	GatewayIP        string   `json:"gateway_ip"`
	GatewayHostnames []string `json:"gateway_hostnames"`
}

// envoyCluster is an Envoy upstream cluster.
type envoyCluster struct {
	Name           string `json:"name"`
	ConnectTimeout string `json:"connect_timeout,omitempty"`
	Type           string `json:"type,omitempty"`
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

// buildEgressListener builds the Envoy listener config for outbound traffic.
// When !spec.AllowDockerGateway, a gateway-deny filter is prepended that
// contains the resolved GatewayIP (L3) and the Docker-internal hostnames (L7).
// The HTTP RBAC allowlist filter (action=ALLOW) follows; an empty policy set
// is Envoy's deny-all.
func buildEgressListener(spec proxySpec) envoyListener {
	var filters []envoyFilter

	// Gateway deny (two layers) must precede the allowlist.
	if !spec.AllowDockerGateway {
		filters = append(filters, envoyFilter{
			Name: envoyGatewayDenyFilterName,
			TypedConfig: &envoyGatewayDenyConfig{
				GatewayIP:        spec.GatewayIP,
				GatewayHostnames: []string{dockerGatewayHostname, dockerAltGatewayHostname},
			},
		})
	}

	// HTTP RBAC allowlist (action=ALLOW; empty policies = deny-all).
	filters = append(filters, envoyFilter{
		Name:        envoyHTTPRBACFilterName,
		TypedConfig: buildEgressRBACFilter(spec),
	})

	return envoyListener{
		Name: fmt.Sprintf("%s-egress", spec.WorkloadName),
		Address: envoyAddress{
			SocketAddress: envoySocketAddress{
				Address:   "0.0.0.0",
				PortValue: 3128,
			},
		},
		FilterChains: []envoyFilterChain{{Filters: filters}},
	}
}

func buildEgressRBACFilter(spec proxySpec) *envoyRBACFilter {
	rules := envoyRBACRules{
		Action:   "ALLOW",
		Policies: make(map[string]envoyRBACPolicy), // always init; never use omitempty
	}

	if spec.Permissions == nil || spec.Permissions.Outbound == nil {
		return &envoyRBACFilter{Rules: rules} // empty policies = deny-all
	}
	out := spec.Permissions.Outbound
	if out.InsecureAllowAll {
		rules.Policies["allow-all"] = envoyRBACPolicy{
			Permissions: []envoyPermission{{Any: true}},
			Principals:  []envoyPrincipal{{Any: true}},
		}
		return &envoyRBACFilter{Rules: rules}
	}
	for _, host := range out.AllowHost {
		matcher := &envoyHeaderMatcher{Name: ":authority"}
		if strings.HasPrefix(host, "*.") {
			matcher.SuffixMatch = host[1:] // "*.example.com" → ".example.com"
		} else {
			matcher.ExactMatch = host
		}
		rules.Policies[host] = envoyRBACPolicy{
			Permissions: []envoyPermission{{Header: matcher}},
			Principals:  []envoyPrincipal{{Any: true}},
		}
	}
	return &envoyRBACFilter{Rules: rules}
}

// buildIngressListener builds the Envoy listener config for inbound (ingress) traffic.
// It binds on 127.0.0.1:hostPort and routes to spec.WorkloadName:spec.UpstreamPort.
func buildIngressListener(spec proxySpec, hostPort int) envoyListener {
	upstreamRef := fmt.Sprintf("%s:%d", spec.WorkloadName, spec.UpstreamPort)

	domains := []string{"localhost", "127.0.0.1", spec.WorkloadName}
	if spec.Permissions != nil && spec.Permissions.Inbound != nil &&
		len(spec.Permissions.Inbound.AllowHost) > 0 {
		domains = spec.Permissions.Inbound.AllowHost
	}

	return envoyListener{
		Name: fmt.Sprintf("%s-ingress", spec.WorkloadName),
		Address: envoyAddress{
			SocketAddress: envoySocketAddress{
				Address:   "127.0.0.1",
				PortValue: hostPort,
			},
		},
		FilterChains: []envoyFilterChain{
			{
				Filters: []envoyFilter{
					{
						Name: "envoy.filters.network.http_connection_manager",
						TypedConfig: map[string]any{
							"upstream_cluster": upstreamRef,
							"virtual_hosts":    domains,
						},
					},
				},
			},
		},
	}
}

// envoyProxy implements networkProxy using Envoy as the proxy backend.
// It creates a single Envoy container that handles both egress (forward proxy
// on :3128) and ingress (reverse proxy) as separate listeners, reducing aux
// container count from 3 (Squid: egress + ingress + dns) to 2 (Envoy: combined + dns).
type envoyProxy struct {
	client *Client
}

// SetupProxies implements networkProxy for the Envoy backend.
func (e *envoyProxy) SetupProxies(ctx context.Context, spec proxySpec) (proxyResult, error) {
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)

	// Build Envoy bootstrap config.
	var listeners []envoyListener
	egressListener := buildEgressListener(spec)
	listeners = append(listeners, egressListener)

	var ingressPort int
	if spec.TransportType != "stdio" && spec.UpstreamPort > 0 {
		port, err := networking.FindOrUsePort(spec.UpstreamPort + 1)
		if err != nil {
			return proxyResult{}, fmt.Errorf("failed to find ingress port: %w", err)
		}
		ingressPort = port
		listeners = append(listeners, buildIngressListener(spec, ingressPort))
	}

	bootstrap := envoyBootstrap{
		Admin: &envoyAdmin{
			Address: envoyAddress{
				SocketAddress: envoySocketAddress{
					Address:   "127.0.0.1", // loopback only — never 0.0.0.0
					PortValue: 9901,
				},
			},
		},
		StaticResources: envoyStatic{
			Listeners: listeners,
		},
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

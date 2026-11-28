// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"

	"github.com/moby/moby/api/types/container"

	"github.com/stacklok/toolhive/pkg/container/internal/netproxy"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	defaultEnvoyImage  = netproxy.DefaultEnvoyImage
	typeHCM            = netproxy.TypeHCM
	typeHTTPRBAC       = netproxy.TypeHTTPRBAC
	typeDFPFilter      = netproxy.TypeDFPFilter
	typeRouter         = netproxy.TypeRouter
	typeDFPCluster     = netproxy.TypeDFPCluster
	dfpCacheName       = netproxy.DFPCacheName
	dfpClusterName     = netproxy.DFPClusterName
	ingressClusterName = netproxy.IngressClusterName
)

// Type aliases to pkg/container/internal/netproxy for package-internal callers and tests.
type (
	envoyBootstrap        = netproxy.EnvoyBootstrap
	envoyStaticResources  = netproxy.EnvoyStaticResources
	envoyListener         = netproxy.EnvoyListener
	envoyHCM              = netproxy.EnvoyHCM
	envoyHTTPFilter       = netproxy.EnvoyHTTPFilter
	envoyHTTPRBAC         = netproxy.EnvoyHTTPRBAC
	envoyRBACRules        = netproxy.EnvoyRBACRules
	envoyRBACPolicy       = netproxy.EnvoyRBACPolicy
	envoyPermission       = netproxy.EnvoyPermission
	envoyDFPFilter        = netproxy.EnvoyDFPFilter
	envoyDNSCache         = netproxy.EnvoyDNSCache
	envoyAddressMatcher   = netproxy.EnvoyAddressMatcher
	envoyCIDRRange        = netproxy.EnvoyCIDRRange
	envoyCluster          = netproxy.EnvoyCluster
	envoyDFPClusterConfig = netproxy.EnvoyDFPClusterConfig
)

func getEnvoyImage() string {
	return netproxy.GetEnvoyImage()
}

func buildDFPDNSCache(spec proxySpec) envoyDNSCache {
	return netproxy.BuildDFPDNSCache(spec.toNetProxySpec())
}

func buildEgressListener(spec proxySpec) envoyListener {
	return netproxy.BuildEgressListener(spec.toNetProxySpec())
}

func buildEgressHTTPFilters(spec proxySpec) []envoyHTTPFilter {
	return netproxy.BuildEgressHTTPFilters(spec.toNetProxySpec())
}

func gatewayAuthorityPermissions(gatewayIPs []netip.Addr) []envoyPermission {
	return netproxy.GatewayAuthorityPermissions(gatewayIPs)
}

func buildGatewayDenyRBAC(gatewayIPs []netip.Addr) *envoyHTTPRBAC {
	return netproxy.BuildGatewayDenyRBAC(gatewayIPs)
}

func buildAllowlistPolicies(spec proxySpec) map[string]envoyRBACPolicy {
	return netproxy.BuildAllowlistPolicies(spec.toNetProxySpec())
}

func hostMatchRegex(host string) string {
	return netproxy.HostMatchRegex(host)
}

func buildEgressCluster(spec proxySpec) envoyCluster {
	return netproxy.BuildEgressCluster(spec.toNetProxySpec())
}

func buildIngressListener(spec proxySpec, hostPort int) envoyListener {
	return netproxy.BuildIngressListener(spec.toNetProxySpec(), hostPort)
}

func buildIngressCluster(spec proxySpec) envoyCluster {
	return netproxy.BuildIngressCluster(spec.toNetProxySpec())
}

func writeEnvoyBootstrap(b envoyBootstrap) (string, error) {
	return netproxy.WriteEnvoyBootstrap(b)
}

// envoyProxy implements networkProxy using Envoy as the proxy backend.
type envoyProxy struct {
	client *Client
}

// SetupEgress implements networkProxy for the Envoy backend.
func (*envoyProxy) SetupEgress(_ context.Context, spec proxySpec) (egressResult, error) {
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)
	return egressResult{EnvVars: addEgressEnvVars(nil, egressContainerName)}, nil
}

// SetupIngress implements networkProxy for the Envoy backend.
func (e *envoyProxy) SetupIngress(ctx context.Context, spec proxySpec, _ egressResult) (int, error) {
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)
	npSpec := spec.toNetProxySpec()

	bootstrap := netproxy.BuildEgressBootstrap(npSpec)

	var ingressPort int
	if spec.TransportType != "stdio" && spec.UpstreamPort > 0 {
		port, err := networking.FindOrUsePort(0)
		if err != nil {
			return 0, fmt.Errorf("failed to find ingress port: %w", err)
		}
		ingressPort = port
		bootstrap.StaticResources.Listeners = append(
			bootstrap.StaticResources.Listeners,
			netproxy.BuildIngressListener(npSpec, ingressPort),
		)
		bootstrap.StaticResources.Clusters = append(
			bootstrap.StaticResources.Clusters,
			netproxy.BuildIngressCluster(npSpec),
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

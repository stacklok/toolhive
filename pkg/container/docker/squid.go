// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/internal/netproxy"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
)

const (
	defaultSquidImage        = netproxy.DefaultSquidImage
	podmanGatewayHostname    = netproxy.PodmanGatewayHostname
	dockerGatewayHostname    = netproxy.DockerGatewayHostname
	dockerAltGatewayHostname = netproxy.DockerAltGatewayHostname
)

// createIngressSquidContainer creates an instance of the squid proxy for ingress traffic.
func createIngressSquidContainer(
	ctx context.Context,
	c *Client,
	containerName string,
	upstreamHost string,
	squidContainerName string,
	attachStdio bool,
	upstreamPort int,
	squidPort int,
	exposedPorts map[string]struct{},
	endpointsConfig map[string]*network.EndpointSettings,
	portBindings map[string][]runtime.PortBinding,
	networkPermissions *permissions.NetworkPermissions,
) (string, error) {
	squidConfPath, err := createTempIngressSquidConf(containerName, upstreamHost, upstreamPort, squidPort, networkPermissions)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary squid.conf: %w", err)
	}

	return createSquidContainer(
		ctx,
		c,
		squidContainerName,
		attachStdio,
		exposedPorts,
		endpointsConfig,
		portBindings,
		squidConfPath,
	)
}

// createEgressSquidContainer creates an instance of the squid proxy for egress traffic.
func createEgressSquidContainer(
	ctx context.Context,
	c *Client,
	containerName string,
	squidContainerName string,
	attachStdio bool,
	exposedPorts map[string]struct{},
	endpointsConfig map[string]*network.EndpointSettings,
	perm *permissions.NetworkPermissions,
	allowDockerGateway bool,
	gatewayIPs []netip.Addr,
) (string, error) {
	squidConfPath, err := createTempEgressSquidConf(perm, containerName, allowDockerGateway, gatewayIPs)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary squid.conf: %w", err)
	}

	return createSquidContainer(
		ctx,
		c,
		squidContainerName,
		attachStdio,
		exposedPorts,
		endpointsConfig,
		nil,
		squidConfPath,
	)
}

// createSquidContainer contains the shared logic for creating a squid container.
func createSquidContainer(
	ctx context.Context,
	c *Client,
	squidContainerName string,
	attachStdio bool,
	exposedPorts map[string]struct{},
	endpointsConfig map[string]*network.EndpointSettings,
	portBindings map[string][]runtime.PortBinding,
	squidConfPath string,
) (string, error) {
	squidImage := getSquidImage()
	//nolint:gosec // G706: squid container name and image from config
	slog.Debug("setting up squid container", "name", squidContainerName, "image", squidImage)
	squidLabels := map[string]string{}
	lb.AddStandardLabels(squidLabels, squidContainerName, squidContainerName, "stdio", 80)
	squidLabels[ToolhiveAuxiliaryWorkloadLabel] = LabelValueTrue

	err := c.imageManager.PullImage(ctx, squidImage)
	if err != nil {
		_, inspectErr := c.imageManager.ImageExists(ctx, squidImage)
		if inspectErr == nil {
			//nolint:gosec // G706: squid image name from config
			slog.Debug("squid image exists locally, continuing despite pull failure", "image", squidImage)
		} else {
			return "", fmt.Errorf("failed to pull squid image: %w", err)
		}
	}

	config := &container.Config{
		Image:        squidImage,
		Cmd:          nil,
		Env:          nil,
		Labels:       squidLabels,
		AttachStdin:  attachStdio,
		AttachStdout: attachStdio,
		AttachStderr: attachStdio,
		OpenStdin:    attachStdio,
		Tty:          false,
	}

	mounts := []runtime.Mount{
		{
			Source:   squidConfPath,
			Target:   "/etc/squid/squid.conf",
			ReadOnly: true,
		},
	}

	squidHostConfig := &container.HostConfig{
		Mounts:      convertMounts(mounts),
		NetworkMode: container.NetworkMode("bridge"),
		CapAdd:      []string{"CAP_SETUID", "CAP_SETGID"},
		CapDrop:     nil,
		SecurityOpt: []string{"label:disable"},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	if portBindings != nil {
		if err := setupPortBindings(squidHostConfig, portBindings); err != nil {
			return "", NewContainerError(err, "", err.Error())
		}
	}

	if err := setupExposedPorts(config, exposedPorts); err != nil {
		return "", NewContainerError(err, "", err.Error())
	}

	squidContainerID, err := c.createContainer(ctx, squidContainerName, config, squidHostConfig, endpointsConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create egress container: %w", err)
	}

	return squidContainerID, nil
}

func createTempEgressSquidConf(
	networkPermissions *permissions.NetworkPermissions,
	serverHostname string,
	allowDockerGateway bool,
	gatewayIPs []netip.Addr,
) (string, error) {
	return netproxy.CreateTempEgressSquidConf(networkPermissions, serverHostname, allowDockerGateway, gatewayIPs)
}

func createTempIngressSquidConf(
	serverHostname string,
	upstreamHost string,
	upstreamPort int,
	squidPort int,
	networkPermissions *permissions.NetworkPermissions,
) (string, error) {
	return netproxy.CreateTempIngressSquidConf(serverHostname, upstreamHost, upstreamPort, squidPort, networkPermissions)
}

func getSquidImage() string {
	return netproxy.GetSquidImage()
}

// squidProxy is the default networkProxy implementation.
type squidProxy struct {
	client *Client
}

// SetupEgress creates the egress Squid container before the MCP container is
// created and returns the proxy env vars to inject into the workload.
func (s *squidProxy) SetupEgress(ctx context.Context, spec proxySpec) (egressResult, error) {
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)
	_, err := createEgressSquidContainer(
		ctx, s.client, spec.WorkloadName, egressContainerName,
		spec.AttachStdio, nil, spec.Endpoints, spec.Permissions,
		spec.AllowDockerGateway, spec.GatewayIPs,
	)
	if err != nil {
		return egressResult{}, fmt.Errorf("failed to create egress container: %w", err)
	}
	return egressResult{EnvVars: addEgressEnvVars(nil, egressContainerName)}, nil
}

// SetupIngress creates the ingress Squid container after the MCP container exists.
func (s *squidProxy) SetupIngress(ctx context.Context, spec proxySpec, _ egressResult) (int, error) {
	if spec.TransportType == "stdio" || spec.UpstreamPort == 0 {
		return 0, nil
	}
	upstreamHost := spec.UpstreamHost
	if upstreamHost == "" {
		upstreamHost = spec.WorkloadName
	}
	ingressPort, err := s.client.setupIngressContainer(
		ctx, spec.WorkloadName, upstreamHost, spec.UpstreamPort, spec.AttachStdio, spec.Endpoints, spec.Permissions,
	)
	if err != nil {
		return 0, err
	}
	return ingressPort, nil
}

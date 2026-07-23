// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package docker provides Docker-specific implementation of container runtime,
// including creating, starting, stopping, and monitoring containers.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	rt "runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

// DnsImage is the default DNS image used for network permissions
const DnsImage = "dockurr/dnsmasq:latest"

// RuntimeName is the name identifier for the Docker runtime
const RuntimeName = "docker"

// IsAvailable checks if Docker is available by attempting to connect to the Docker daemon
func IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := NewClient(ctx)
	return err == nil
}

// Workloads
const (
	ToolhiveAuxiliaryWorkloadLabel = "toolhive-auxiliary-workload"
	LabelValueTrue                 = "true"
)

// dockerAPI defines the minimal Docker client surface we need for unit-testing
// ListWorkloads/GetWorkloadInfo through an adapter without requiring a live daemon.
// The signatures mirror the methods on *mobyclient.Client so the real client can
// be assigned directly to Client.api.
type dockerAPI interface {
	ContainerList(ctx context.Context, options mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error)
	ContainerInspect(
		ctx context.Context,
		containerID string,
		options mobyclient.ContainerInspectOptions,
	) (mobyclient.ContainerInspectResult, error)
	ContainerStop(
		ctx context.Context,
		containerID string,
		options mobyclient.ContainerStopOptions,
	) (mobyclient.ContainerStopResult, error)
	ContainerCreate(ctx context.Context, options mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error)
	ContainerStart(
		ctx context.Context,
		containerID string,
		options mobyclient.ContainerStartOptions,
	) (mobyclient.ContainerStartResult, error)
	ContainerRemove(
		ctx context.Context,
		containerID string,
		options mobyclient.ContainerRemoveOptions,
	) (mobyclient.ContainerRemoveResult, error)
}

// deployOps defines the internal operations used by DeployWorkload.
// It allows unit tests to substitute a fake implementation to avoid hitting a real Docker daemon.
type deployOps interface {
	createExternalNetworks(ctx context.Context) error
	createNetwork(ctx context.Context, name string, labels map[string]string, internal bool) error
	createDnsContainer(
		ctx context.Context,
		dnsContainerName string,
		attachStdio bool,
		networkName string,
		endpointsConfig map[string]*network.EndpointSettings,
	) (string, string, error)
	createMcpContainer(
		ctx context.Context,
		name string,
		networkName string,
		image string,
		command []string,
		envVars map[string]string,
		labels map[string]string,
		attachStdio bool,
		permissionConfig *runtime.PermissionConfig,
		additionalDNS string,
		exposedPorts map[string]struct{},
		portBindings map[string][]runtime.PortBinding,
		isolateNetwork bool,
	) error
}

// Client implements the Deployer interface for Docker (and compatible runtimes)
type Client struct {
	runtimeType  runtime.Type
	socketPath   string
	client       *mobyclient.Client
	api          dockerAPI
	imageManager images.ImageManager
	ops          deployOps
	proxy        networkProxy // selected at construction time via newNetworkProxy
}

// NewClient creates a new container client
func NewClient(ctx context.Context) (*Client, error) {
	dockerClient, socketPath, runtimeType, err := sdk.NewDockerClient(ctx)
	if err != nil {
		return nil, err // there is already enough context in the error.
	}

	imageManager := images.NewRegistryImageManager(dockerClient)

	c := &Client{
		runtimeType:  runtimeType,
		socketPath:   socketPath,
		client:       dockerClient,
		api:          dockerClient,
		imageManager: imageManager,
	}
	// Default ops implementation uses the real client methods.
	c.ops = c

	proxy, err := newNetworkProxy(c)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize network proxy: %w", err)
	}
	c.proxy = proxy

	return c, nil
}

// DeployWorkload creates and starts a workload.
// It configures the workload based on the provided permission profile and transport type.
// If options is nil, default options will be used.
//
//nolint:gocyclo // This function has high complexity due to comprehensive workload setup
func (c *Client) DeployWorkload(
	ctx context.Context,
	image,
	name string,
	command []string,
	envVars,
	labels map[string]string,
	permissionProfile *permissions.Profile,
	transportType string,
	options *runtime.DeployWorkloadOptions,
	isolateNetwork bool,
) (int, error) {
	// Get permission config from profile
	var ignoreConfig *ignore.Config
	if options != nil {
		ignoreConfig = options.IgnoreConfig
	}
	permissionConfig, err := c.getPermissionConfigFromProfile(permissionProfile, transportType, ignoreConfig)
	if err != nil {
		return 0, fmt.Errorf("failed to get permission config: %w", err)
	}

	// Determine if we should attach stdio
	attachStdio := options == nil || options.AttachStdio

	// create networks
	var additionalDNS string
	networkName := fmt.Sprintf("toolhive-%s-internal", name)
	externalEndpointsConfig := map[string]*network.EndpointSettings{
		networkName: {},
	}

	// Network isolation is only enforceable in bridge mode. For non-bridge
	// modes (host/none/custom) the sidecars have no route out and the workload
	// bypasses them, so isolation must be dropped. See issue #5775.
	effectiveIsolation := isolateNetwork && networking.IsBridgeMode(permissionConfig.NetworkMode)
	if isolateNetwork && !effectiveIsolation {
		if permissionConfig.NetworkMode == "none" {
			// "none" is already maximally confined; isolation is merely redundant.
			//nolint:gosec // G706: network mode from permission config
			slog.Debug(networking.NetworkIsolationNoneRedundantMsg, "network_mode", permissionConfig.NetworkMode)
		} else {
			// host (or other non-bridge) is less restrictive than isolation, so
			// dropping it is a real reduction in confinement.
			//nolint:gosec // G706: network mode from permission config
			slog.Warn(networking.NetworkIsolationHostDroppedMsg, "network_mode", permissionConfig.NetworkMode)
		}
	}

	// Only create external networks and add endpoints if we're not using a custom network mode like "host" or "none"
	if networking.IsBridgeMode(permissionConfig.NetworkMode) {
		// Add toolhive-external to endpoints config for default networking modes
		externalEndpointsConfig["toolhive-external"] = &network.EndpointSettings{}

		err = c.ops.createExternalNetworks(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to create external networks: %w", err)
		}
	} else {
		//nolint:gosec // G706: network mode from permission config
		slog.Debug("skipping external network creation for custom network mode", "network_mode", permissionConfig.NetworkMode)
	}

	// For non-stdio isolated workloads, extract the upstream port so the ingress
	// proxy can be configured. Done here so a bad exposed-ports config fails fast.
	var upstreamPort int
	if transportType != "stdio" && effectiveIsolation {
		upstreamPort, err = extractFirstPort(options)
		if err != nil {
			return 0, err // extractFirstPort already wraps the error with context.
		}
	}

	networkIsolation := false
	// pspec and egress are populated in the isolation block and reused by
	// SetupIngress after the MCP container is created.
	var (
		pspec  proxySpec
		egress egressResult
	)
	if effectiveIsolation {
		networkIsolation = true

		internalNetworkLabels := map[string]string{}
		lb.AddNetworkLabels(internalNetworkLabels, networkName)
		err := c.ops.createNetwork(ctx, networkName, internalNetworkLabels, true)
		if err != nil {
			return 0, fmt.Errorf("failed to create internal network: %w", err)
		}

		// create dns container
		dnsContainerName := fmt.Sprintf("%s-dns", name)
		_, dnsContainerIP, err := c.ops.createDnsContainer(ctx, dnsContainerName, attachStdio, networkName, externalEndpointsConfig)
		if dnsContainerIP != "" {
			additionalDNS = dnsContainerIP
		}
		if err != nil {
			return 0, fmt.Errorf("failed to create dns container: %w", err)
		}

		pspec = proxySpec{
			WorkloadName:       name,
			Permissions:        permissionProfile.Network,
			AllowDockerGateway: options != nil && options.AllowDockerGateway,
			GatewayIP:          c.getDockerBridgeGatewayIP(ctx),
			TransportType:      transportType,
			UpstreamPort:       upstreamPort,
			AttachStdio:        attachStdio,
			Endpoints:          externalEndpointsConfig,
		}

		// SetupEgress runs before createMcpContainer so its env vars can be
		// injected into the workload and the egress proxy has a head start.
		egress, err = c.proxy.SetupEgress(ctx, pspec)
		if err != nil {
			return 0, fmt.Errorf("failed to set up egress proxy: %w", err)
		}
		envVars = mergeEnvVars(envVars, egress.EnvVars)
	} else {
		networkName = ""
	}

	// only remap if is not an auxiliary tool
	newPortBindings, hostPort, err := generatePortBindings(labels, options.PortBindings)
	if err != nil {
		return 0, fmt.Errorf("failed to generate port bindings: %w", err)
	}

	// Add a label to the MCP server indicating network isolation.
	// This allows other methods to determine whether it needs to care
	// about ingress/egress/dns containers.
	lb.AddNetworkIsolationLabel(labels, networkIsolation)

	err = c.ops.createMcpContainer(
		ctx,
		name,
		networkName,
		image,
		command,
		envVars,
		labels,
		attachStdio,
		permissionConfig,
		additionalDNS,
		options.ExposedPorts,
		newPortBindings,
		effectiveIsolation,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create mcp container: %w", err)
	}

	// Don't try and set up an ingress proxy if the transport type is stdio.
	if transportType == "stdio" {
		return 0, nil
	}

	firstPortInt, err := extractFirstPort(options)
	if err != nil {
		return 0, err // extractFirstPort already wraps the error with context.
	}
	if effectiveIsolation {
		// SetupIngress runs after createMcpContainer so the reverse-proxy upstream
		// resolves on first probe (a squid ingress created earlier would cache the
		// negative DNS lookup and never recover).
		hostPort, err = c.proxy.SetupIngress(ctx, pspec, egress)
		if err != nil {
			return 0, fmt.Errorf("failed to set up ingress proxy: %w", err)
		}
	}

	// NOTE: this is a hack to get the final port for the workload.
	// The intended behavior is the following
	// * if network is "bridge" (default) and network isolation is not enabled, then
	//   the Proxy should use the Docker assigned port
	// * if network is "bridge" (default) and network isolation is enabled, then
	//   the Proxy should use the egress container port
	// * if network is "host", then the Proxy should use the MCP server port
	//
	// The last case is not supported in VM-based installations like Docker Desktop.
	// Unfortunately, there's no reliable way to know if the user is using a VM-based
	// installation and we assume that Linux installations are Docker Engine installations.
	finalPort := calculateFinalPort(hostPort, firstPortInt, permissionConfig.NetworkMode)

	return finalPort, nil
}

// ListWorkloads lists workloads
func (c *Client) ListWorkloads(ctx context.Context) ([]runtime.ContainerInfo, error) {
	// Create filter for toolhive containers
	filterArgs := mobyclient.Filters{}.Add("label", "toolhive=true")

	// List containers
	listResult, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}
	containers := listResult.Items

	// Convert to our ContainerInfo format
	result := make([]runtime.ContainerInfo, 0, len(containers))
	for _, c := range containers {
		// Skip containers that have the auxiliary workload label set to "true"
		if val, ok := c.Labels["toolhive-auxiliary-workload"]; ok && val == "true" {
			continue
		}

		// Extract container name (remove leading slash)
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
			name = strings.TrimPrefix(name, "/")
		}

		// Extract port mappings
		ports := make([]runtime.PortMapping, 0, len(c.Ports))
		for _, p := range c.Ports {
			ports = append(ports, runtime.PortMapping{
				ContainerPort: int(p.PrivatePort),
				HostPort:      int(p.PublicPort),
				Protocol:      p.Type,
			})
		}

		// Convert creation time
		created := time.Unix(c.Created, 0)

		result = append(result, runtime.ContainerInfo{
			Name:    name,
			Image:   c.Image,
			Status:  c.Status,
			State:   dockerToDomainStatus(string(c.State)),
			Created: created,
			Labels:  c.Labels,
			Ports:   ports,
		})
	}

	return result, nil
}

// StopWorkload stops a workload
// If the workload is already stopped, it returns success
func (c *Client) StopWorkload(ctx context.Context, workloadName string) error {
	// Check if the workload is running
	info, err := c.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		// If the container doesn't exist, that's fine - it's already "stopped"
		if errors.Is(err, ErrContainerNotFound) {
			return nil
		}
		return err
	}

	// If the container is not running, return success
	if info.State != runtime.WorkloadStatusRunning {
		return nil
	}

	// Use a reasonable timeout
	timeoutSeconds := 30
	_, err = c.api.ContainerStop(ctx, workloadName, mobyclient.ContainerStopOptions{Timeout: &timeoutSeconds})
	if err != nil {
		return NewContainerError(err, workloadName, fmt.Sprintf("failed to stop workload: %v", err))
	}

	// If network isolation is not enabled, then there is nothing else to do.
	// NOTE: This check treats all workloads created before the introduction of
	// this label as having network isolation enabled. This is to ensure that they
	// get cleaned up properly during stop/rm.
	if !lb.HasNetworkIsolation(info.Labels) {
		return nil
	}

	// remove / from container name
	containerName := strings.TrimPrefix(info.Name, "/")
	egressContainerName := fmt.Sprintf("%s-egress", containerName)
	ingressContainerName := fmt.Sprintf("%s-ingress", containerName)
	dnsContainerName := fmt.Sprintf("%s-dns", containerName)

	// Attempt to stop each auxiliary container gracefully.
	// Treat any errors as non-fatal and log them.
	proxyContainers := []string{egressContainerName, ingressContainerName, dnsContainerName}
	for _, name := range proxyContainers {
		c.stopProxyContainer(ctx, name, timeoutSeconds)
	}

	return nil
}

// RemoveWorkload removes a workload
// If the workload doesn't exist, it returns success
func (c *Client) RemoveWorkload(ctx context.Context, workloadName string) error {
	// get container name from ID
	containerResponse, err := c.inspectContainerByName(ctx, workloadName)
	if err != nil {
		slog.Warn("failed to inspect container", "name", workloadName, "error", err)
	}

	// remove the / if it starts with it
	containerName := containerResponse.Name
	containerName = strings.TrimPrefix(containerName, "/")

	// remove the workload containers
	var labels map[string]string
	if containerResponse.Config != nil {
		labels = containerResponse.Config.Labels
	} else {
		labels = make(map[string]string)
	}
	err = c.removeContainer(ctx, containerResponse.ID)
	if err != nil {
		return err // removeContainer already wraps the error with context.
	}

	// Clean up any proxy containers associated with this workload.
	err = c.removeProxyContainers(ctx, containerName, labels)
	if err != nil {
		return err // removeProxyContainers already wraps the error with context.
	}

	// Clear up any networks associated with this workload.
	// This also deletes the external network if no other workloads are using it.
	err = c.deleteNetworks(ctx, containerName)
	if err != nil {
		slog.Warn("failed to delete networks for container", "name", containerName, "error", err)
	}
	return nil
}

// GetWorkloadLogs gets workload logs
func (c *Client) GetWorkloadLogs(ctx context.Context, workloadName string, follow bool, lines int) (string, error) {
	// follow=true means infinite streaming, lines>0 means finite limit - these are contradictory
	if follow && lines > 0 {
		return "", NewContainerError(
			fmt.Errorf("cannot use both follow and line limit"),
			workloadName,
			"follow mode streams logs indefinitely, which conflicts with line limiting",
		)
	}

	// Configure tail option based on lines parameter
	tail := "all"
	if lines > 0 {
		tail = fmt.Sprintf("%d", lines)
	}

	options := mobyclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	}

	workloadContainer, err := c.inspectContainerByName(ctx, workloadName)
	if err != nil {
		return "", err
	}

	logs, err := c.client.ContainerLogs(ctx, workloadContainer.ID, options)
	if err != nil {
		return "", NewContainerError(err, workloadName, fmt.Sprintf("failed to get workload logs: %v", err))
	}
	defer func() {
		if err := logs.Close(); err != nil {
			// Non-fatal: log stream cleanup failure
			slog.Debug("failed to close log stream", "error", err)
		}
	}()

	if follow {
		_, err = stdcopy.StdCopy(os.Stdout, os.Stderr, logs)
		if err != nil && err != io.EOF {
			slog.Error("error reading workload logs", "error", err)
			return "", NewContainerError(err, workloadName, fmt.Sprintf("failed to follow workload logs: %v", err))
		}
	}

	// Read logs
	var buf bytes.Buffer
	_, err = stdcopy.StdCopy(&buf, &buf, logs)
	if err != nil {
		return "", NewContainerError(err, workloadName, fmt.Sprintf("failed to read workload logs: %v", err))
	}

	return buf.String(), nil
}

// IsWorkloadRunning checks if a workload is running
func (c *Client) IsWorkloadRunning(ctx context.Context, workloadName string) (bool, error) {
	// Inspect workload
	info, err := c.inspectContainerByName(ctx, workloadName)
	if err != nil {
		// Check if the error is because the workload doesn't exist
		if errdefs.IsNotFound(err) {
			return false, NewContainerError(ErrContainerNotFound, workloadName, "workload not found")
		}
		return false, NewContainerError(err, workloadName, fmt.Sprintf("failed to inspect workload: %v", err))
	}

	return info.State.Running, nil
}

// GetWorkloadInfo gets workload information
func (c *Client) GetWorkloadInfo(ctx context.Context, workloadName string) (runtime.ContainerInfo, error) {
	// Inspect workload
	info, err := c.inspectContainerByName(ctx, workloadName)
	if err != nil {
		// Check if the error is because the workload doesn't exist
		if errdefs.IsNotFound(err) {
			return runtime.ContainerInfo{}, NewContainerError(ErrContainerNotFound, workloadName, "workload not found")
		}
		return runtime.ContainerInfo{}, NewContainerError(err, workloadName, fmt.Sprintf("failed to inspect workload: %v", err))
	}

	// Extract port mappings
	ports := make([]runtime.PortMapping, 0)
	for containerPort, bindings := range info.NetworkSettings.Ports {
		for _, binding := range bindings {
			hostPort := 0
			if _, err := fmt.Sscanf(binding.HostPort, "%d", &hostPort); err != nil {
				// If we can't parse the port, just use 0
				//nolint:gosec // G706: host port from container network settings
				slog.Warn("failed to parse host port", "host_port", binding.HostPort, "error", err)
			}

			ports = append(ports, runtime.PortMapping{
				ContainerPort: int(containerPort.Num()),
				HostPort:      hostPort,
				Protocol:      string(containerPort.Proto()),
			})
		}
	}

	// Convert creation time
	created, err := time.Parse(time.RFC3339, info.Created)
	if err != nil {
		created = time.Time{} // Use zero time if parsing fails
	}

	// Convert start time
	startedAt, err := time.Parse(time.RFC3339Nano, info.State.StartedAt)
	if err != nil {
		startedAt = time.Time{} // Use zero time if parsing fails
	}

	return runtime.ContainerInfo{
		Name:      strings.TrimPrefix(info.Name, "/"),
		Image:     info.Config.Image,
		Status:    string(info.State.Status),
		State:     dockerToDomainStatus(string(info.State.Status)),
		Created:   created,
		StartedAt: startedAt,
		Labels:    info.Config.Labels,
		Ports:     ports,
	}, nil
}

// AttachToWorkload attaches to a workload
func (c *Client) AttachToWorkload(ctx context.Context, workloadName string) (io.WriteCloser, io.ReadCloser, error) {
	// Check if workload exists and is running
	running, err := c.IsWorkloadRunning(ctx, workloadName)
	if err != nil {
		return nil, nil, err
	}
	if !running {
		return nil, nil, NewContainerError(ErrContainerNotRunning, workloadName, "workload is not running")
	}

	// Attach to workload
	resp, err := c.client.ContainerAttach(ctx, workloadName, mobyclient.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, nil, NewContainerError(ErrAttachFailed, workloadName, fmt.Sprintf("failed to attach to workload: %v", err))
	}

	stdoutReader, stdoutWriter := io.Pipe()

	go func() {
		defer func() {
			if err := stdoutWriter.Close(); err != nil {
				// Non-fatal: writer cleanup failure
				slog.Debug("failed to close stdout writer", "error", err)
			}
		}()
		defer resp.Close()

		// Use stdcopy to demultiplex the container streams
		_, err := stdcopy.StdCopy(stdoutWriter, io.Discard, resp.Reader)
		if err != nil && err != io.EOF {
			slog.Error("error demultiplexing container streams", "error", err)
		}
	}()

	return resp.Conn, stdoutReader, nil
}

// IsRunning checks the health of the container runtime.
// This is used to verify that the runtime is operational and can manage workloads.
func (c *Client) IsRunning(ctx context.Context) error {
	// Try to ping the Docker server
	_, err := c.client.Ping(ctx, mobyclient.PingOptions{})
	if err != nil {
		return fmt.Errorf("failed to ping Docker server: %w", err)
	}

	return nil
}

// getPermissionConfigFromProfile converts a permission profile to a container permission config
// with transport-specific settings (internal function)
// addReadOnlyMounts adds read-only mounts to the permission config
func (*Client) addReadOnlyMounts(
	config *runtime.PermissionConfig,
	mounts []permissions.MountDeclaration,
	ignoreConfig *ignore.Config,
) {
	for _, mountDecl := range mounts {
		source, target, err := mountDecl.Parse()
		if err != nil {
			// Skip invalid mounts
			slog.Warn("skipping invalid mount declaration", "mount", mountDecl, "error", err)
			continue
		}

		// Skip resource URIs for now (they need special handling)
		if strings.Contains(source, "://") {
			slog.Warn("resource URI mounts not yet supported", "source", source)
			continue
		}

		// Convert relative paths to absolute paths
		absPath, ok := convertRelativePathToAbsolute(source, mountDecl)
		if !ok {
			continue
		}

		config.Mounts = append(config.Mounts, runtime.Mount{
			Source:   absPath,
			Target:   target,
			ReadOnly: true,
			Type:     runtime.MountTypeBind,
		})

		// Process ignore patterns and add tmpfs overlays
		addIgnoreOverlays(config, absPath, target, ignoreConfig)
	}
}

// addReadWriteMounts adds read-write mounts to the permission config
func (*Client) addReadWriteMounts(
	config *runtime.PermissionConfig,
	mounts []permissions.MountDeclaration,
	ignoreConfig *ignore.Config,
) {
	for _, mountDecl := range mounts {
		source, target, err := mountDecl.Parse()
		if err != nil {
			// Skip invalid mounts
			slog.Warn("skipping invalid mount declaration", "mount", mountDecl, "error", err)
			continue
		}

		// Skip resource URIs for now (they need special handling)
		if strings.Contains(source, "://") {
			slog.Warn("resource URI mounts not yet supported", "source", source)
			continue
		}

		// Convert relative paths to absolute paths
		absPath, ok := convertRelativePathToAbsolute(source, mountDecl)
		if !ok {
			continue
		}

		// Check if the path is already mounted read-only
		alreadyMounted := false
		for i, m := range config.Mounts {
			if m.Target == target {
				// Update the mount to be read-write
				config.Mounts[i].ReadOnly = false
				alreadyMounted = true
				break
			}
		}

		// If not already mounted, add a new mount
		if !alreadyMounted {
			config.Mounts = append(config.Mounts, runtime.Mount{
				Source:   absPath,
				Target:   target,
				ReadOnly: false,
				Type:     runtime.MountTypeBind,
			})
		}

		// Process ignore patterns and add tmpfs overlays
		addIgnoreOverlays(config, absPath, target, ignoreConfig)
	}
}

// addIgnoreOverlays processes ignore patterns for a mount and adds overlay mounts
func addIgnoreOverlays(config *runtime.PermissionConfig, sourceDir, containerPath string, ignoreConfig *ignore.Config) {
	// Skip if no ignore configuration is provided
	if ignoreConfig == nil {
		return
	}

	// Create ignore processor with configuration
	ignoreProcessor := ignore.NewProcessor(ignoreConfig)

	// Load global ignore patterns if enabled
	if ignoreConfig.LoadGlobal {
		if err := ignoreProcessor.LoadGlobal(); err != nil {
			slog.Debug("failed to load global ignore patterns", "error", err)
			// Continue without global patterns
		}
	}

	// Load local ignore patterns from the source directory
	if err := ignoreProcessor.LoadLocal(sourceDir); err != nil {
		//nolint:gosec // G706: source directory from mount config
		slog.Debug("failed to load local ignore patterns", "dir", sourceDir, "error", err)
		// Continue without local patterns
	}

	// Get overlay mounts (all using bind mounts now)
	overlayMounts := ignoreProcessor.GetOverlayMounts(sourceDir, containerPath)

	// Add overlay mounts to the configuration
	for _, overlayMount := range overlayMounts {
		// All overlays now use bind mounts (no more tmpfs)
		config.Mounts = append(config.Mounts, runtime.Mount{
			Source:   overlayMount.HostPath,
			Target:   overlayMount.ContainerPath,
			ReadOnly: false,
			Type:     runtime.MountTypeBind,
		})
		//nolint:gosec // G706: overlay paths from ignore config processing
		slog.Debug("added bind overlay for ignored path",
			"host_path", overlayMount.HostPath,
			"container_path", overlayMount.ContainerPath)
	}
}

// convertRelativePathToAbsolute converts a relative path to an absolute path
// Returns the absolute path and a boolean indicating if the conversion was successful
func convertRelativePathToAbsolute(source string, mountDecl permissions.MountDeclaration) (string, bool) {
	// If it's already an absolute path, return it as is
	if filepath.IsAbs(source) {
		return source, true
	}

	// Special case for Windows: expand ~ to user profile directory.
	if rt.GOOS == "windows" && strings.HasPrefix(source, "~") {
		homeDir := os.Getenv("USERPROFILE")
		source = strings.Replace(source, "~", homeDir, 1)
	}

	absPath, err := filepath.Abs(source)
	if err != nil {
		slog.Warn("failed to convert to absolute path", "mount", mountDecl, "error", err)
		return "", false
	}

	//nolint:gosec // G706: file path from mount declaration config
	slog.Debug("converting relative path to absolute", "mount", mountDecl, "abs_path", absPath)
	return absPath, true
}

// getPermissionConfigFromProfile converts a permission profile to a container permission config
func (c *Client) getPermissionConfigFromProfile(
	profile *permissions.Profile,
	transportType string,
	ignoreConfig *ignore.Config,
) (*runtime.PermissionConfig, error) {
	// Get network mode from permission profile
	networkMode := ""
	if profile.Network != nil && profile.Network.Mode != "" {
		networkMode = profile.Network.Mode
	}

	// Start with a default permission config
	config := &runtime.PermissionConfig{
		Mounts:      []runtime.Mount{},
		NetworkMode: networkMode,
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{},
		SecurityOpt: []string{"label:disable"},
		Privileged:  profile.Privileged,
	}

	// Add mounts
	c.addReadOnlyMounts(config, profile.Read, ignoreConfig)
	c.addReadWriteMounts(config, profile.Write, ignoreConfig)

	// Validate transport type
	switch transportType {
	case "sse", "stdio", "inspector", "streamable-http":
		// valid, do nothing
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}

	return config, nil
}

// findExistingContainer finds a container with the exact name
// Uses container runtime's name filter for efficiency but then verifies exact match to prevent partial matching
func (c *Client) findExistingContainer(ctx context.Context, name string) (string, error) {
	// Use name filter to narrow results, then verify exact match
	listResult, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true, // Include stopped containers
		Filters: mobyclient.Filters{}.Add("name", name),
	})
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}

	// Verify exact name match from the filtered results (name filter does partial matching)
	for _, cont := range listResult.Items {
		for _, containerName := range cont.Names {
			// Container names in the API have a leading slash
			if containerName == "/"+name || containerName == name {
				return cont.ID, nil
			}
		}
	}

	return "", nil
}

// compareBasicConfig compares basic container configuration (image, command, env vars, labels, stdio settings)
func compareBasicConfig(existing *container.InspectResponse, desired *container.Config) bool {
	// Compare image
	if existing.Config.Image != desired.Image {
		return false
	}

	// Compare command
	if len(existing.Config.Cmd) != len(desired.Cmd) {
		return false
	}
	for i, cmd := range existing.Config.Cmd {
		if i >= len(desired.Cmd) || cmd != desired.Cmd[i] {
			return false
		}
	}

	// Compare environment variables
	if !compareEnvVars(existing.Config.Env, desired.Env) {
		return false
	}

	// Compare labels
	if !compareLabels(existing.Config.Labels, desired.Labels) {
		return false
	}

	// Compare stdio settings
	if existing.Config.AttachStdin != desired.AttachStdin ||
		existing.Config.AttachStdout != desired.AttachStdout ||
		existing.Config.AttachStderr != desired.AttachStderr ||
		existing.Config.OpenStdin != desired.OpenStdin {
		return false
	}

	return true
}

// compareEnvVars compares environment variables
func compareEnvVars(existingEnv, desiredEnv []string) bool {
	// Convert to maps for easier comparison
	existingMap := envSliceToMap(existingEnv)
	desiredMap := envSliceToMap(desiredEnv)

	// Check if all desired env vars are in existing env with correct values
	for k, v := range desiredMap {
		existingVal, exists := existingMap[k]
		if !exists || existingVal != v {
			return false
		}
	}

	return true
}

// envSliceToMap converts a slice of environment variables to a map
func envSliceToMap(env []string) map[string]string {
	result := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// compareLabels compares container labels
func compareLabels(existingLabels, desiredLabels map[string]string) bool {
	// Check if all desired labels are in existing labels with correct values
	for k, v := range desiredLabels {
		existingVal, exists := existingLabels[k]
		if !exists || existingVal != v {
			return false
		}
	}
	return true
}

// compareHostConfig compares host configuration (network mode, capabilities, security options)
func compareHostConfig(existing *container.InspectResponse, desired *container.HostConfig) bool {
	// Compare network mode
	if string(existing.HostConfig.NetworkMode) != string(desired.NetworkMode) {
		return false
	}

	// Compare capabilities
	if !compareStringSlices(existing.HostConfig.CapAdd, desired.CapAdd) {
		return false
	}
	if !compareStringSlices(existing.HostConfig.CapDrop, desired.CapDrop) {
		return false
	}

	// Compare security options
	if !compareStringSlices(existing.HostConfig.SecurityOpt, desired.SecurityOpt) {
		return false
	}

	// Compare privileged mode
	if existing.HostConfig.Privileged != desired.Privileged {
		return false
	}

	// Compare restart policy
	if existing.HostConfig.RestartPolicy.Name != desired.RestartPolicy.Name {
		return false
	}

	return true
}

// compareStringSlices compares two string slices
func compareStringSlices(existing, desired []string) bool {
	if len(existing) != len(desired) {
		return false
	}
	for i, s := range existing {
		if i >= len(desired) || s != desired[i] {
			return false
		}
	}
	return true
}

// compareMounts compares volume mounts
func compareMounts(existing *container.InspectResponse, desired *container.HostConfig) bool {
	if len(existing.HostConfig.Mounts) != len(desired.Mounts) {
		return false
	}

	// Create maps by target path for easier comparison
	existingMountsMap := make(map[string]mount.Mount)
	for _, m := range existing.HostConfig.Mounts {
		existingMountsMap[m.Target] = m
	}

	// Check if all desired mounts exist in the container with matching source and read-only flag
	for _, desiredMount := range desired.Mounts {
		existingMount, exists := existingMountsMap[desiredMount.Target]
		if !exists || existingMount.Source != desiredMount.Source || existingMount.ReadOnly != desiredMount.ReadOnly {
			return false
		}
	}

	return true
}

// comparePortConfig compares port configuration (exposed ports and port bindings)
func comparePortConfig(existing *container.InspectResponse, desired *container.Config, desiredHost *container.HostConfig) bool {
	// Compare exposed ports
	if len(existing.Config.ExposedPorts) != len(desired.ExposedPorts) {
		return false
	}
	for port := range desired.ExposedPorts {
		if _, exists := existing.Config.ExposedPorts[port]; !exists {
			return false
		}
	}

	// Compare port bindings
	if len(existing.HostConfig.PortBindings) != len(desiredHost.PortBindings) {
		return false
	}
	for port, bindings := range desiredHost.PortBindings {
		existingBindings, exists := existing.HostConfig.PortBindings[port]
		if !exists || len(existingBindings) != len(bindings) {
			return false
		}
		for i, binding := range bindings {
			if i >= len(existingBindings) ||
				existingBindings[i].HostIP != binding.HostIP ||
				existingBindings[i].HostPort != binding.HostPort {
				return false
			}
		}
	}

	return true
}

// compareContainerConfig compares an existing container's configuration with the desired configuration
func compareContainerConfig(
	existing *container.InspectResponse,
	desired *container.Config,
	desiredHost *container.HostConfig,
) bool {
	// Compare basic configuration
	if !compareBasicConfig(existing, desired) {
		return false
	}

	// Compare host configuration
	if !compareHostConfig(existing, desiredHost) {
		return false
	}

	// Compare mounts
	if !compareMounts(existing, desiredHost) {
		return false
	}

	// Compare port configuration
	if !comparePortConfig(existing, desired, desiredHost) {
		return false
	}

	// All checks passed, configurations match
	return true
}

// CreateNetwork creates a network following configuration.
func (c *Client) createNetwork(
	ctx context.Context,
	name string,
	labels map[string]string,
	internal bool,
) error {
	// Check if the network already exists
	// Use name filter for efficiency but verify exact match to avoid partial matching
	networks, err := c.client.NetworkList(ctx, mobyclient.NetworkListOptions{
		Filters: mobyclient.Filters{}.Add("name", name),
	})
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}
	// Verify exact name match from filtered results
	for _, n := range networks.Items {
		if n.Name == name {
			// Network already exists
			return nil
		}
	}

	networkCreate := mobyclient.NetworkCreateOptions{
		Driver:   "bridge",
		Internal: internal,
		Labels:   labels,
	}

	_, err = c.client.NetworkCreate(ctx, name, networkCreate)
	if err != nil {
		return err
	}
	return nil
}

// getDockerBridgeGatewayIP returns the gateway IP of the Docker default bridge
// network by inspecting it at runtime. This handles platform differences:
// Linux Docker Engine uses 172.17.0.1 by default, while Docker Desktop on macOS
// uses 192.168.65.1 and Colima typically uses 192.168.5.1 or similar. Querying
// the daemon directly is more accurate than hardcoding platform-specific IPs.
// Falls back to dockerDefaultBridgeGatewayIP on any error or when the underlying
// Docker client is unavailable.
func (c *Client) getDockerBridgeGatewayIP(ctx context.Context) string {
	if c.client == nil {
		return dockerDefaultBridgeGatewayIP
	}
	nr, err := c.client.NetworkInspect(ctx, "bridge", mobyclient.NetworkInspectOptions{})
	if err != nil {
		slog.Debug("failed to inspect bridge network, using default gateway IP", "error", err)
		return dockerDefaultBridgeGatewayIP
	}
	for _, cfg := range nr.Network.IPAM.Config {
		if cfg.Gateway.IsValid() {
			return cfg.Gateway.String()
		}
	}
	slog.Debug("bridge network has no gateway in IPAM config, using default gateway IP")
	return dockerDefaultBridgeGatewayIP
}

// DeleteNetwork deletes a network by name.
func (c *Client) deleteNetwork(ctx context.Context, name string) error {
	// find the network by name using filter for efficiency but verify exact match
	networks, err := c.client.NetworkList(ctx, mobyclient.NetworkListOptions{
		Filters: mobyclient.Filters{}.Add("name", name),
	})
	if err != nil {
		return err
	}

	// Verify exact name match from filtered results
	var networkToRemove *network.Summary
	for _, n := range networks.Items {
		if n.Name == name {
			networkToRemove = &n
			break
		}
	}

	// If the network does not exist, there is nothing to do here.
	if networkToRemove == nil {
		slog.Debug("network not found, nothing to delete", "name", name)
		return nil
	}

	if _, err := c.client.NetworkRemove(ctx, networkToRemove.ID, mobyclient.NetworkRemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove network %s: %w", name, err)
	}
	return nil
}

// removeContainer removes a container by ID, without removing any associated networks or proxy containers.
func (c *Client) removeContainer(ctx context.Context, containerID string) error {
	_, err := c.api.ContainerRemove(ctx, containerID, mobyclient.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil {
		// If the workload doesn't exist, that's fine - it's already removed
		if errdefs.IsNotFound(err) {
			return nil
		}
		return NewContainerError(err, containerID, fmt.Sprintf("failed to remove container: %v", err))
	}

	return nil
}

// removeProxyContainers removes the MCP server container and any proxy containers.
func (c *Client) removeProxyContainers(
	ctx context.Context,
	containerName string,
	workloadLabels map[string]string,
) error {
	// remove the / if it starts with it
	containerName = strings.TrimPrefix(containerName, "/")

	// If network isolation is not enabled, then there is nothing else to do.
	// NOTE: This check treats all workloads created before the introduction of
	// this label as having network isolation enabled. This is to ensure that they
	// get cleaned up properly during stop/rm. There may be some spurious warnings
	// from the following code, but they can be ignored.
	if !lb.HasNetworkIsolation(workloadLabels) {
		return nil
	}

	// remove egress, ingress, and dns containers
	suffixes := []string{"egress", "ingress", "dns"}

	for _, suffix := range suffixes {
		containerName := fmt.Sprintf("%s-%s", containerName, suffix)
		containerId, err := c.findExistingContainer(ctx, containerName)
		if err != nil {
			slog.Debug("failed to find container", "type", suffix, "name", containerName, "error", err)
			continue
		}
		if containerId == "" {
			continue
		}

		_, err = c.client.ContainerRemove(ctx, containerId, mobyclient.ContainerRemoveOptions{
			Force: true,
		})
		if err != nil {
			if errdefs.IsNotFound(err) {
				continue
			}
			return NewContainerError(err, containerId, fmt.Sprintf("failed to remove %s container: %v", suffix, err))
		}
	}

	return nil
}

// CreateContainer creates a container without starting it
// If options is nil, default options will be used
// convertEnvVars converts a map of environment variables to a slice
func convertEnvVars(envVars map[string]string) []string {
	env := make([]string, 0, len(envVars))
	for k, v := range envVars {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// convertMounts converts internal mount format to Docker mount format
func convertMounts(mounts []runtime.Mount) []mount.Mount {
	result := make([]mount.Mount, 0, len(mounts))
	for _, m := range mounts {
		// All mounts are now bind mounts (removed tmpfs support for overlays)
		result = append(result, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return result
}

// setupExposedPorts configures exposed ports for a container
func setupExposedPorts(config *container.Config, exposedPorts map[string]struct{}) error {
	if len(exposedPorts) == 0 {
		return nil
	}

	config.ExposedPorts = network.PortSet{}
	for port := range exposedPorts {
		networkPort, err := network.ParsePort(strings.Split(port, "/")[0])
		if err != nil {
			return fmt.Errorf("failed to parse port: %w", err)
		}
		config.ExposedPorts[networkPort] = struct{}{}
	}

	return nil
}

// setupPortBindings configures port bindings for a container
func setupPortBindings(hostConfig *container.HostConfig, portBindings map[string][]runtime.PortBinding) error {
	if len(portBindings) == 0 {
		return nil
	}

	hostConfig.PortBindings = network.PortMap{}
	for port, bindings := range portBindings {
		networkPort, err := network.ParsePort(strings.Split(port, "/")[0])
		if err != nil {
			return fmt.Errorf("failed to parse port: %w", err)
		}

		networkBindings := make([]network.PortBinding, len(bindings))
		for i, binding := range bindings {
			hostIP, err := parseHostIP(binding.HostIP)
			if err != nil {
				return fmt.Errorf("failed to parse host IP %q: %w", binding.HostIP, err)
			}
			networkBindings[i] = network.PortBinding{
				HostIP:   hostIP,
				HostPort: binding.HostPort,
			}
		}
		hostConfig.PortBindings[networkPort] = networkBindings
	}

	return nil
}

// parseHostIP converts a host IP string into a netip.Addr. An empty string
// (meaning "all interfaces") maps to the zero netip.Addr, preserving the
// previous behavior where an empty HostIP string was passed through verbatim.
func parseHostIP(hostIP string) (netip.Addr, error) {
	if hostIP == "" {
		return netip.Addr{}, nil
	}
	return netip.ParseAddr(hostIP)
}

// createContainerStopped creates a container without starting it and returns
// the container ID. It handles existing container reuse the same way as
// createContainer: if an existing container's config matches, it is returned
// as-is (but NOT started even if stopped); if the config mismatches, the old
// container is removed and a fresh one is created (also not started). The
// caller is responsible for starting the container via startContainer.
func (c *Client) createContainerStopped(
	ctx context.Context,
	containerName string,
	config *container.Config,
	hostConfig *container.HostConfig,
	endpointsConfig map[string]*network.EndpointSettings,
) (string, error) {
	existingID, err := c.findExistingContainer(ctx, containerName)
	if err != nil {
		return "", err
	}

	// If container exists, check if we need to recreate it
	if existingID != "" {
		canReuse, err := c.handleExistingContainerStopped(ctx, existingID, config, hostConfig)
		if err != nil {
			return "", err
		}

		if canReuse {
			// Container exists with the right configuration, return its ID without starting it.
			return existingID, nil
		}
		// Container was removed and needs to be recreated.
	}

	// network config
	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: endpointsConfig,
	}

	// Create the container but do not start it.
	resp, err := c.api.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Config:           config,
		HostConfig:       hostConfig,
		NetworkingConfig: networkConfig,
		Platform:         nil,
		Name:             containerName,
	})
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to create container: %v", err))
	}
	if resp.Warnings != nil {
		//nolint:gosec // G706: warnings from container API response
		slog.Debug("container creation warnings", "warnings", resp.Warnings)
	}

	return resp.ID, nil
}

// startContainer starts a previously created (but not yet started) container
// identified by containerID.
func (c *Client) startContainer(ctx context.Context, containerID string) error {
	_, err := c.api.ContainerStart(ctx, containerID, mobyclient.ContainerStartOptions{})
	if err != nil {
		return NewContainerError(err, containerID, fmt.Sprintf("failed to start container: %v", err))
	}
	return nil
}

// handleExistingContainerStopped is like handleExistingContainer but does NOT
// start a stopped container. It returns true when the existing container can be
// reused (regardless of its running state), and false when the container had to
// be removed so it can be recreated.
func (c *Client) handleExistingContainerStopped(
	ctx context.Context,
	containerID string,
	desiredConfig *container.Config,
	desiredHostConfig *container.HostConfig,
) (bool, error) {
	inspectResult, err := c.api.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return false, NewContainerError(err, containerID, fmt.Sprintf("failed to inspect container: %v", err))
	}
	info := inspectResult.Container

	if compareContainerConfig(&info, desiredConfig, desiredHostConfig) {
		// Configurations match — container can be reused as-is; caller will start it.
		return true, nil
	}

	// Configurations don't match; remove so the caller can recreate it.
	if err := c.removeContainer(ctx, containerID); err != nil {
		return false, err
	}
	return false, nil
}

func (c *Client) createContainer(
	ctx context.Context,
	containerName string,
	config *container.Config,
	hostConfig *container.HostConfig,
	endpointsConfig map[string]*network.EndpointSettings,
) (string, error) {
	id, err := c.createContainerStopped(ctx, containerName, config, hostConfig, endpointsConfig)
	if err != nil {
		return "", err
	}

	// createContainerStopped never starts the container, so we always start it
	// here — whether it was reused, newly created, or recreated after a config
	// mismatch. For an already-running reused container, ContainerStart is a
	// no-op on the Docker daemon side.
	if err := c.startContainer(ctx, id); err != nil {
		return "", err
	}

	return id, nil
}

func (c *Client) createDnsContainer(ctx context.Context, dnsContainerName string,
	attachStdio bool, networkName string, endpointsConfig map[string]*network.EndpointSettings) (string, string, error) {
	slog.Debug("setting up DNS container", "name", dnsContainerName, "image", DnsImage)
	dnsLabels := map[string]string{}
	lb.AddStandardLabels(dnsLabels, dnsContainerName, dnsContainerName, "stdio", 80)
	dnsLabels[ToolhiveAuxiliaryWorkloadLabel] = LabelValueTrue

	// pull the dns image if it is not already pulled
	err := c.imageManager.PullImage(ctx, DnsImage)
	if err != nil {
		// Check if the DNS image exists locally before failing
		_, inspectErr := c.client.ImageInspect(ctx, DnsImage)
		if inspectErr == nil {
			slog.Debug("dns image exists locally, continuing despite pull failure", "image", DnsImage)
		} else {
			return "", "", fmt.Errorf("failed to pull DNS image: %w", err)
		}
	}

	configDns := &container.Config{
		Image:        DnsImage,
		Cmd:          nil,
		Env:          nil,
		Labels:       dnsLabels,
		AttachStdin:  attachStdio,
		AttachStdout: attachStdio,
		AttachStderr: attachStdio,
		OpenStdin:    attachStdio,
		Tty:          false,
	}

	dnsHostConfig := &container.HostConfig{
		Mounts:      nil,
		NetworkMode: container.NetworkMode("bridge"),
		CapAdd:      nil,
		CapDrop:     nil,
		SecurityOpt: []string{"label:disable"},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	// now create the dns container
	dnsContainerId, err := c.createContainer(ctx, dnsContainerName, configDns, dnsHostConfig, endpointsConfig)
	if err != nil {
		return "", "", fmt.Errorf("failed to create dns container: %w", err)
	}

	dnsInspectResult, err := c.client.ContainerInspect(ctx, dnsContainerId, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect DNS container: %w", err)
	}

	dnsNetworkSettings, ok := dnsInspectResult.Container.NetworkSettings.Networks[networkName]
	if !ok {
		return "", "", fmt.Errorf("network %s not found in container's network settings", networkName)
	}
	// EndpointSettings.IPAddress is a netip.Addr; render it back to the string
	// form the callers expect, treating the zero value as an empty address.
	dnsContainerIP := ""
	if dnsNetworkSettings.IPAddress.IsValid() {
		dnsContainerIP = dnsNetworkSettings.IPAddress.String()
	}

	return dnsContainerId, dnsContainerIP, nil
}

func (c *Client) createMcpContainer(
	ctx context.Context,
	name string,
	networkName string,
	image string,
	command []string,
	envVars map[string]string,
	labels map[string]string,
	attachStdio bool,
	permissionConfig *runtime.PermissionConfig,
	additionalDNS string,
	exposedPorts map[string]struct{},
	portBindings map[string][]runtime.PortBinding,
	isolateNetwork bool,
) error {
	// Create container configuration
	config := &container.Config{
		Image:        image,
		Cmd:          command,
		Env:          convertEnvVars(envVars),
		Labels:       labels,
		AttachStdin:  attachStdio,
		AttachStdout: attachStdio,
		AttachStderr: attachStdio,
		OpenStdin:    attachStdio,
		Tty:          false,
	}

	// Create host configuration
	hostConfig := &container.HostConfig{
		Mounts:      convertMounts(permissionConfig.Mounts),
		NetworkMode: container.NetworkMode(permissionConfig.NetworkMode),
		CapAdd:      permissionConfig.CapAdd,
		CapDrop:     permissionConfig.CapDrop,
		SecurityOpt: permissionConfig.SecurityOpt,
		Privileged:  permissionConfig.Privileged,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}
	if additionalDNS != "" {
		dnsAddr, err := netip.ParseAddr(additionalDNS)
		if err != nil {
			return NewContainerError(err, "", fmt.Sprintf("invalid additional DNS address %q: %v", additionalDNS, err))
		}
		hostConfig.DNS = []netip.Addr{dnsAddr}
	}

	// Configure ports if options are provided
	// Setup exposed ports
	if err := setupExposedPorts(config, exposedPorts); err != nil {
		return NewContainerError(err, "", err.Error())
	}

	// Setup port bindings
	if err := setupPortBindings(hostConfig, portBindings); err != nil {
		return NewContainerError(err, "", err.Error())
	}

	// create mcp container
	internalEndpointsConfig := map[string]*network.EndpointSettings{}

	// Check if we have a custom network mode (e.g., "host", "none", etc.)
	if !networking.IsBridgeMode(permissionConfig.NetworkMode) {
		// For custom network modes like "host", "none", etc., don't add any endpoint configurations
		// The NetworkMode in hostConfig will handle the networking
		//nolint:gosec // G706: network mode from permission config
		slog.Debug("using custom network mode", "network_mode", permissionConfig.NetworkMode)
		// Leave internalEndpointsConfig as empty map
	} else if isolateNetwork {
		internalEndpointsConfig[networkName] = &network.EndpointSettings{
			NetworkID: networkName,
		}
	} else {
		// for other workloads such as inspector, add to external network
		internalEndpointsConfig["toolhive-external"] = &network.EndpointSettings{
			NetworkID: "toolhive-external",
		}
	}
	_, err := c.createContainer(ctx, name, config, hostConfig, internalEndpointsConfig)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	return nil

}

// addEgressEnvVars returns a new map containing all entries from envVars plus
// the HTTP_PROXY/HTTPS_PROXY/NO_PROXY variables for the given egress container.
// The caller's map is never mutated.
func addEgressEnvVars(envVars map[string]string, egressContainerName string) map[string]string {
	egressHost := fmt.Sprintf("http://%s:3128", egressContainerName)
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

// mergeEnvVars returns a new map containing all entries from base with all
// entries from extra added (extra values overwrite base on conflict). Neither
// input map is mutated.
func mergeEnvVars(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

// setupIngressContainer creates the ingress Squid reverse-proxy container for
// the workload and returns the host-side port it is bound on.
func (c *Client) setupIngressContainer(ctx context.Context, containerName string, upstreamPort int, attachStdio bool,
	externalEndpointsConfig map[string]*network.EndpointSettings, networkPermissions *permissions.NetworkPermissions) (int, error) {
	squidPort, err := networking.FindOrUsePort(upstreamPort + 1)
	if err != nil {
		return 0, fmt.Errorf("failed to find or use port %d: %w", squidPort, err)
	}
	squidExposedPorts := map[string]struct{}{
		fmt.Sprintf("%d/tcp", squidPort): {},
	}
	squidPortBindings := map[string][]runtime.PortBinding{
		fmt.Sprintf("%d/tcp", squidPort): {
			{
				HostIP:   "127.0.0.1",
				HostPort: fmt.Sprintf("%d", squidPort),
			},
		},
	}
	ingressContainerName := fmt.Sprintf("%s-ingress", containerName)
	_, err = createIngressSquidContainer(
		ctx,
		c,
		containerName,
		ingressContainerName,
		attachStdio,
		upstreamPort,
		squidPort,
		squidExposedPorts,
		externalEndpointsConfig,
		squidPortBindings,
		networkPermissions,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create ingress container: %w", err)
	}
	return squidPort, nil

}

func extractFirstPort(options *runtime.DeployWorkloadOptions) (int, error) {
	var firstPort string
	if len(options.ExposedPorts) == 0 {
		return 0, fmt.Errorf("no exposed ports specified in options.ExposedPorts")
	}
	for port := range options.ExposedPorts {
		firstPort = port

		// need to strip the protocol
		firstPort = strings.Split(firstPort, "/")[0]
		break // take only the first one
	}
	firstPortInt, err := strconv.Atoi(firstPort)
	if err != nil {
		return 0, fmt.Errorf("failed to convert port %s to int: %w", firstPort, err)
	}
	return firstPortInt, nil
}

func (c *Client) createExternalNetworks(ctx context.Context) error {
	externalNetworkLabels := map[string]string{}
	lb.AddNetworkLabels(externalNetworkLabels, "toolhive-external")
	err := c.createNetwork(ctx, "toolhive-external", externalNetworkLabels, false)
	if err != nil {
		return err
	}
	return nil
}

func generatePortBindings(labels map[string]string,
	portBindings map[string][]runtime.PortBinding) (map[string][]runtime.PortBinding, int, error) {
	// Clone portBindings so we never mutate the caller's map.
	portBindings = maps.Clone(portBindings)
	var hostPort int
	// check if we need to map to a random port of not
	if _, ok := labels[ToolhiveAuxiliaryWorkloadLabel]; ok && labels[ToolhiveAuxiliaryWorkloadLabel] == LabelValueTrue {
		// find first port
		var err error
		for _, bindings := range portBindings {
			if len(bindings) > 0 {
				hostPortStr := bindings[0].HostPort
				hostPort, err = strconv.Atoi(hostPortStr)
				if err != nil {
					return nil, 0, fmt.Errorf("failed to convert host port %s to int: %w", hostPortStr, err)
				}
				break
			}
		}
	} else {
		// first port binding needs to map to the host port
		// For consistency, we only use FindAvailable for the primary port if it's not already set
		for key, bindings := range portBindings {
			if len(bindings) > 0 {
				hostPortStr := bindings[0].HostPort
				if hostPortStr == "" || hostPortStr == "0" {
					hostPort = networking.FindAvailable()
					if hostPort == 0 {
						return nil, 0, fmt.Errorf("could not find an available port")
					}
					bindings[0].HostPort = fmt.Sprintf("%d", hostPort)
					portBindings[key] = bindings
				} else {
					var err error
					hostPort, err = strconv.Atoi(hostPortStr)
					if err != nil {
						return nil, 0, fmt.Errorf("failed to convert host port %s to int: %w", hostPortStr, err)
					}
				}
				break
			}
		}
	}

	return portBindings, hostPort, nil
}

func (c *Client) stopProxyContainer(ctx context.Context, containerName string, timeoutSeconds int) {
	containerId, err := c.findExistingContainer(ctx, containerName)
	if err != nil {
		slog.Debug("failed to find internal container", "name", containerName, "error", err)
		return
	}
	if containerId == "" {
		return
	}
	if _, err := c.api.ContainerStop(ctx, containerId, mobyclient.ContainerStopOptions{Timeout: &timeoutSeconds}); err != nil {
		slog.Debug("failed to stop internal container", "name", containerName, "error", err)
	}
}

func (c *Client) deleteNetworks(ctx context.Context, containerName string) error {
	// Delete networks if there are no containers using them.
	toolHiveContainers, err := c.client.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: mobyclient.Filters{}.Add("label", "toolhive=true"),
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Delete associated internal network
	networkName := fmt.Sprintf("toolhive-%s-internal", containerName)
	if err := c.deleteNetwork(ctx, networkName); err != nil {
		// just log the error and continue
		slog.Warn("failed to delete network", "name", networkName, "error", err)
	}

	if len(toolHiveContainers.Items) == 0 {
		// remove external network
		if err := c.deleteNetwork(ctx, "toolhive-external"); err != nil {
			// just log the error and continue
			slog.Warn("failed to delete network", "name", "toolhive-external", "error", err)
		}
	}
	return nil
}

func dockerToDomainStatus(status string) runtime.WorkloadStatus {
	// Reference: https://docs.docker.com/reference/cli/docker/container/ls/#status
	switch status {
	case "running":
		return runtime.WorkloadStatusRunning
	case "created", "restarting":
		return runtime.WorkloadStatusStarting
	case "paused", "exited", "dead":
		return runtime.WorkloadStatusStopped
	case "removing": // TODO: add handling new workload creation
		return runtime.WorkloadStatusRemoving
	}
	// We should not reach here.
	return runtime.WorkloadStatusUnknown
}

// findContainerByLabel finds a container by the base name label.
// Returns the container ID if found, empty string otherwise.
func (c *Client) findContainerByLabel(ctx context.Context, workloadName string) (string, error) {
	filterArgs := mobyclient.Filters{}.
		Add("label", "toolhive=true").
		Add("label", fmt.Sprintf("toolhive-basename=%s", workloadName))

	listResult, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}
	containers := listResult.Items

	if len(containers) == 0 {
		return "", nil
	}

	// If multiple containers have the same base name, prefer the running one
	var containerID string
	for _, cont := range containers {
		if cont.State == "running" {
			containerID = cont.ID
			break
		}
	}
	// If no running container found, use the first one
	if containerID == "" {
		containerID = containers[0].ID
	}

	return containerID, nil
}

// findContainerByExactName finds a container by exact name matching.
// Returns the container ID if found, empty string otherwise.
// Uses container runtime's name filter for efficiency but then verifies exact match to prevent partial matching
func (c *Client) findContainerByExactName(ctx context.Context, workloadName string) (string, error) {
	filterArgs := mobyclient.Filters{}.
		Add("label", "toolhive=true").
		Add("name", workloadName) // Use name filter for efficiency

	listResult, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}
	containers := listResult.Items

	if len(containers) == 0 {
		return "", nil
	}

	// Verify exact name match from the filtered results (name filter does partial matching)
	// The name in the API has a leading slash, so we need to search for that.
	prefixedName := "/" + workloadName
	for _, cont := range containers {
		// Check if any of the container names match exactly
		if slices.Contains(cont.Names, prefixedName) || slices.Contains(cont.Names, workloadName) {
			return cont.ID, nil
		}
	}

	return "", nil
}

// inspectContainerByName finds a container by the workload name and inspects it.
// It first tries to find by base name label, then falls back to exact name matching.
func (c *Client) inspectContainerByName(ctx context.Context, workloadName string) (container.InspectResponse, error) {
	empty := container.InspectResponse{}

	// First try to find container by base name label
	containerID, err := c.findContainerByLabel(ctx, workloadName)
	if err != nil {
		return empty, err
	}
	if containerID != "" {
		result, err := c.api.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
		return result.Container, err
	}

	// Fall back to exact name matching for backward compatibility
	containerID, err = c.findContainerByExactName(ctx, workloadName)
	if err != nil {
		return empty, err
	}
	if containerID == "" {
		return empty, NewContainerError(runtime.ErrWorkloadNotFound, workloadName, "no containers found")
	}

	result, err := c.api.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	return result.Container, err
}

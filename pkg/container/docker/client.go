// Package docker provides Docker-specific implementation of container runtime,
// including creating, starting, stopping, and monitoring containers.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/permissions"
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

// Client implements the Deployer interface for Docker (and compatible runtimes)
type Client struct {
	runtimeType  runtime.Type
	socketPath   string
	client       *client.Client
	imageManager images.ImageManager
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
		imageManager: imageManager,
	}

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
		networkName:         {},
		"toolhive-external": {},
	}

	err = c.createExternalNetworks(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to create external networks: %v", err)
	}

	networkIsolation := false
	if isolateNetwork {
		networkIsolation = true

		internalNetworkLabels := map[string]string{}
		lb.AddNetworkLabels(internalNetworkLabels, networkName)
		err := c.createNetwork(ctx, networkName, internalNetworkLabels, true)
		if err != nil {
			return 0, fmt.Errorf("failed to create internal network: %v", err)
		}

		// create dns container
		dnsContainerName := fmt.Sprintf("%s-dns", name)
		_, dnsContainerIP, err := c.createDnsContainer(ctx, dnsContainerName, attachStdio, networkName, externalEndpointsConfig)
		if dnsContainerIP != "" {
			additionalDNS = dnsContainerIP
		}
		if err != nil {
			return 0, fmt.Errorf("failed to create dns container: %v", err)
		}

		// create egress container
		egressContainerName := fmt.Sprintf("%s-egress", name)
		_, err = createEgressSquidContainer(
			ctx,
			c,
			name,
			egressContainerName,
			attachStdio,
			nil,
			externalEndpointsConfig,
			permissionProfile.Network,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to create egress container: %v", err)
		}

		envVars = addEgressEnvVars(envVars, egressContainerName)
	} else {
		networkName = ""
	}

	// only remap if is not an auxiliary tool
	newPortBindings, hostPort, err := generatePortBindings(labels, options.PortBindings)
	if err != nil {
		return 0, fmt.Errorf("failed to generate port bindings: %v", err)
	}

	// Add a label to the MCP server indicating network isolation.
	// This allows other methods to determine whether it needs to care
	// about ingress/egress/dns containers.
	lb.AddNetworkIsolationLabel(labels, networkIsolation)

	err = c.createMcpContainer(
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
		isolateNetwork,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create mcp container: %v", err)
	}

	// Don't try and set up an ingress proxy if the transport type is stdio.
	if transportType == "stdio" {
		return 0, nil
	}

	if isolateNetwork {
		// just extract the first exposed port
		firstPortInt, err := extractFirstPort(options)
		if err != nil {
			return 0, err // extractFirstPort already wraps the error with context.
		}
		hostPort, err = c.createIngressContainer(ctx, name, firstPortInt, attachStdio, externalEndpointsConfig)
		if err != nil {
			return 0, fmt.Errorf("failed to create ingress container: %v", err)
		}
	}

	return hostPort, nil
}

// ListWorkloads lists workloads
func (c *Client) ListWorkloads(ctx context.Context) ([]runtime.ContainerInfo, error) {
	// Create filter for toolhive containers
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "toolhive=true")

	// List containers
	containers, err := c.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}

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
			State:   dockerToDomainStatus(c.State),
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
	err = c.client.ContainerStop(ctx, workloadName, container.StopOptions{Timeout: &timeoutSeconds})
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
		logger.Warnf("Failed to inspect container %s: %v", workloadName, err)
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
		logger.Warnf("Failed to delete networks for container %s: %v", containerName, err)
	}
	return nil
}

// GetWorkloadLogs gets workload logs
func (c *Client) GetWorkloadLogs(ctx context.Context, workloadName string, follow bool) (string, error) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       "100",
	}

	workloadContainer, err := c.inspectContainerByName(ctx, workloadName)
	if err != nil {
		return "", err
	}

	logs, err := c.client.ContainerLogs(ctx, workloadContainer.ID, options)
	if err != nil {
		return "", NewContainerError(err, workloadName, fmt.Sprintf("failed to get workload logs: %v", err))
	}
	defer logs.Close()

	if follow {
		_, err = stdcopy.StdCopy(os.Stdout, os.Stderr, logs)
		if err != nil && err != io.EOF {
			logger.Errorf("Error reading workload logs: %v", err)
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
				logger.Warnf("Warning: Failed to parse host port %s: %v", binding.HostPort, err)
			}

			ports = append(ports, runtime.PortMapping{
				ContainerPort: containerPort.Int(),
				HostPort:      hostPort,
				Protocol:      containerPort.Proto(),
			})
		}
	}

	// Convert creation time
	created, err := time.Parse(time.RFC3339, info.Created)
	if err != nil {
		created = time.Time{} // Use zero time if parsing fails
	}

	return runtime.ContainerInfo{
		Name:    strings.TrimPrefix(info.Name, "/"),
		Image:   info.Config.Image,
		Status:  info.State.Status,
		State:   dockerToDomainStatus(info.State.Status),
		Created: created,
		Labels:  info.Config.Labels,
		Ports:   ports,
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
	resp, err := c.client.ContainerAttach(ctx, workloadName, container.AttachOptions{
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
		defer stdoutWriter.Close()
		defer resp.Close()

		// Use stdcopy to demultiplex the container streams
		_, err := stdcopy.StdCopy(stdoutWriter, io.Discard, resp.Reader)
		if err != nil && err != io.EOF {
			logger.Errorf("Error demultiplexing container streams: %v", err)
		}
	}()

	return resp.Conn, stdoutReader, nil
}

// IsRunning checks the health of the container runtime.
// This is used to verify that the runtime is operational and can manage workloads.
func (c *Client) IsRunning(ctx context.Context) error {
	// Try to ping the Docker server
	_, err := c.client.Ping(ctx)
	if err != nil {
		return fmt.Errorf("failed to ping Docker server: %v", err)
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
			logger.Warnf("Warning: Skipping invalid mount declaration: %s (%v)", mountDecl, err)
			continue
		}

		// Skip resource URIs for now (they need special handling)
		if strings.Contains(source, "://") {
			logger.Warnf("Warning: Resource URI mounts not yet supported: %s", source)
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
			logger.Warnf("Warning: Skipping invalid mount declaration: %s (%v)", mountDecl, err)
			continue
		}

		// Skip resource URIs for now (they need special handling)
		if strings.Contains(source, "://") {
			logger.Warnf("Warning: Resource URI mounts not yet supported: %s", source)
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
			logger.Debugf("Failed to load global ignore patterns: %v", err)
			// Continue without global patterns
		}
	}

	// Load local ignore patterns from the source directory
	if err := ignoreProcessor.LoadLocal(sourceDir); err != nil {
		logger.Debugf("Failed to load local ignore patterns from %s: %v", sourceDir, err)
		// Continue without local patterns
	}

	// Get overlay mounts (both tmpfs for directories and bind for files)
	overlayMounts := ignoreProcessor.GetOverlayMounts(sourceDir, containerPath)

	// Add overlay mounts to the configuration
	for _, overlayMount := range overlayMounts {
		var mountType runtime.MountType
		var source string

		if overlayMount.Type == "tmpfs" {
			mountType = runtime.MountTypeTmpfs
			source = "" // No source for tmpfs
		} else {
			mountType = runtime.MountTypeBind
			source = overlayMount.HostPath
		}

		config.Mounts = append(config.Mounts, runtime.Mount{
			Source:   source,
			Target:   overlayMount.ContainerPath,
			ReadOnly: false,
			Type:     mountType,
		})
		logger.Debugf("Added %s overlay for ignored path: %s -> %s", overlayMount.Type, source, overlayMount.ContainerPath)
	}
}

// convertRelativePathToAbsolute converts a relative path to an absolute path
// Returns the absolute path and a boolean indicating if the conversion was successful
func convertRelativePathToAbsolute(source string, mountDecl permissions.MountDeclaration) (string, bool) {
	// If it's already an absolute path, return it as is
	if filepath.IsAbs(source) {
		return source, true
	}

	// Get the current working directory
	cwd, err := os.Getwd()
	if err != nil {
		logger.Warnf("Warning: Failed to get current working directory: %v", err)
		return "", false
	}

	// Convert relative path to absolute path
	absPath := filepath.Join(cwd, source)
	logger.Infof("Converting relative path to absolute: %s -> %s", mountDecl, absPath)
	return absPath, true
}

// getPermissionConfigFromProfile converts a permission profile to a container permission config
func (c *Client) getPermissionConfigFromProfile(
	profile *permissions.Profile,
	transportType string,
	ignoreConfig *ignore.Config,
) (*runtime.PermissionConfig, error) {
	// Start with a default permission config
	config := &runtime.PermissionConfig{
		Mounts:      []runtime.Mount{},
		NetworkMode: "", // set to blank as podman is not recognizing the "none" value when we attach to other networks
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{},
		SecurityOpt: []string{},
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
func (c *Client) findExistingContainer(ctx context.Context, name string) (string, error) {
	containers, err := c.client.ContainerList(ctx, container.ListOptions{
		All: true, // Include stopped containers
		Filters: filters.NewArgs(
			filters.Arg("name", name),
		),
	})
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}

	// Find exact name match (filter can return partial matches)
	for _, cont := range containers {
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

// handleExistingContainer checks if an existing container's configuration matches the desired configuration
// Returns true if the container can be reused, false if it was removed and needs to be recreated
func (c *Client) handleExistingContainer(
	ctx context.Context,
	containerID string,
	desiredConfig *container.Config,
	desiredHostConfig *container.HostConfig,
) (bool, error) {
	// Get container info
	info, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, NewContainerError(err, containerID, fmt.Sprintf("failed to inspect container: %v", err))
	}

	// Compare configurations
	if compareContainerConfig(&info, desiredConfig, desiredHostConfig) {
		// Configurations match, container can be reused

		// Check if the container is running
		if !info.State.Running {
			// Container exists but is not running, start it
			err = c.client.ContainerStart(ctx, containerID, container.StartOptions{})
			if err != nil {
				return false, NewContainerError(err, containerID, fmt.Sprintf("failed to start existing container: %v", err))
			}
		}

		return true, nil
	}

	// Configurations don't match, we need to recreate the container.
	// Remove only this container, leave any associated networks and containers intact
	// Any proxy containers (like ingress/egress) will have already recreated themselves at this point
	if err := c.removeContainer(ctx, containerID); err != nil {
		return false, err
	}

	// Container was removed and needs to be recreated
	return false, nil
}

// CreateNetwork creates a network following configuration.
func (c *Client) createNetwork(
	ctx context.Context,
	name string,
	labels map[string]string,
	internal bool,
) error {
	// Check if the network already exists
	networks, err := c.client.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}
	if len(networks) > 0 {
		// Network already exists, return its ID
		return nil
	}

	networkCreate := network.CreateOptions{
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

// DeleteNetwork deletes a network by name.
func (c *Client) deleteNetwork(ctx context.Context, name string) error {
	// find the network by name
	networks, err := c.client.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return err
	}

	// If the network does not exist, there is nothing to do here.
	if len(networks) == 0 {
		logger.Debugf("network %s not found, nothing to delete", name)
		return nil
	}

	if err := c.client.NetworkRemove(ctx, networks[0].ID); err != nil {
		return fmt.Errorf("failed to remove network %s: %w", name, err)
	}
	return nil
}

// removeContainer removes a container by ID, without removing any associated networks or proxy containers.
func (c *Client) removeContainer(ctx context.Context, containerID string) error {
	err := c.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
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
			logger.Debugf("Failed to find %s container %s: %v", suffix, containerName, err)
			continue
		}
		if containerId == "" {
			continue
		}

		err = c.client.ContainerRemove(ctx, containerId, container.RemoveOptions{
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
		if m.Type == runtime.MountTypeTmpfs {
			// Create tmpfs mount to mask/hide sensitive directories
			result = append(result, mount.Mount{
				Type:   mount.TypeTmpfs,
				Target: m.Target,
				// No TmpfsOptions needed - default size is sufficient for masking
			})
		} else {
			result = append(result, mount.Mount{
				Type:     mount.TypeBind,
				Source:   m.Source,
				Target:   m.Target,
				ReadOnly: m.ReadOnly,
			})
		}
	}
	return result
}

// setupExposedPorts configures exposed ports for a container
func setupExposedPorts(config *container.Config, exposedPorts map[string]struct{}) error {
	if len(exposedPorts) == 0 {
		return nil
	}

	config.ExposedPorts = nat.PortSet{}
	for port := range exposedPorts {
		natPort, err := nat.NewPort("tcp", strings.Split(port, "/")[0])
		if err != nil {
			return fmt.Errorf("failed to parse port: %v", err)
		}
		config.ExposedPorts[natPort] = struct{}{}
	}

	return nil
}

// setupPortBindings configures port bindings for a container
func setupPortBindings(hostConfig *container.HostConfig, portBindings map[string][]runtime.PortBinding) error {
	if len(portBindings) == 0 {
		return nil
	}

	hostConfig.PortBindings = nat.PortMap{}
	for port, bindings := range portBindings {
		natPort, err := nat.NewPort("tcp", strings.Split(port, "/")[0])
		if err != nil {
			return fmt.Errorf("failed to parse port: %v", err)
		}

		natBindings := make([]nat.PortBinding, len(bindings))
		for i, binding := range bindings {
			natBindings[i] = nat.PortBinding{
				HostIP:   binding.HostIP,
				HostPort: binding.HostPort,
			}
		}
		hostConfig.PortBindings[natPort] = natBindings
	}

	return nil
}

func (c *Client) createContainer(
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
		canReuse, err := c.handleExistingContainer(ctx, existingID, config, hostConfig)
		if err != nil {
			return "", err
		}

		if canReuse {
			// Container exists with the right configuration, return its ID
			return existingID, nil
		}
		// Container was removed and needs to be recreated
	}

	// network config
	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: endpointsConfig,
	}

	// Create the container
	resp, err := c.client.ContainerCreate(
		ctx,
		config,
		hostConfig,
		networkConfig,
		nil,
		containerName,
	)
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to create container: %v", err))
	}

	// Start the container
	err = c.client.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		return "", NewContainerError(err, resp.ID, fmt.Sprintf("failed to start container: %v", err))
	}

	return resp.ID, nil
}

func (c *Client) createDnsContainer(ctx context.Context, dnsContainerName string,
	attachStdio bool, networkName string, endpointsConfig map[string]*network.EndpointSettings) (string, string, error) {
	logger.Infof("Setting up DNS container for %s with image %s...", dnsContainerName, DnsImage)
	dnsLabels := map[string]string{}
	lb.AddStandardLabels(dnsLabels, dnsContainerName, dnsContainerName, "stdio", 80)
	dnsLabels[ToolhiveAuxiliaryWorkloadLabel] = LabelValueTrue

	// pull the dns image if it is not already pulled
	err := c.imageManager.PullImage(ctx, DnsImage)
	if err != nil {
		// Check if the DNS image exists locally before failing
		_, inspectErr := c.client.ImageInspect(ctx, DnsImage)
		if inspectErr == nil {
			logger.Infof("DNS image %s exists locally, continuing despite pull failure", DnsImage)
		} else {
			return "", "", fmt.Errorf("failed to pull DNS image: %v", err)
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
		SecurityOpt: nil,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	// now create the dns container
	dnsContainerId, err := c.createContainer(ctx, dnsContainerName, configDns, dnsHostConfig, endpointsConfig)
	if err != nil {
		return "", "", fmt.Errorf("failed to create dns container: %v", err)
	}

	dnsContainerResponse, err := c.client.ContainerInspect(ctx, dnsContainerId)
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect DNS container: %v", err)
	}

	dnsNetworkSettings, ok := dnsContainerResponse.NetworkSettings.Networks[networkName]
	if !ok {
		return "", "", fmt.Errorf("network %s not found in container's network settings", networkName)
	}
	dnsContainerIP := dnsNetworkSettings.IPAddress

	return dnsContainerId, dnsContainerIP, nil
}

func (c *Client) createMcpContainer(ctx context.Context, name string, networkName string, image string, command []string,
	envVars map[string]string, labels map[string]string, attachStdio bool, permissionConfig *runtime.PermissionConfig,
	additionalDNS string, exposedPorts map[string]struct{}, portBindings map[string][]runtime.PortBinding,
	isolateNetwork bool) error {
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
		hostConfig.DNS = []string{additionalDNS}
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
	if isolateNetwork {
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
		return fmt.Errorf("failed to create container: %v", err)
	}

	return nil

}

// addEgressEnvVars adds environment variables for egress proxy configuration.
func addEgressEnvVars(envVars map[string]string, egressContainerName string) map[string]string {
	egressHost := fmt.Sprintf("http://%s:3128", egressContainerName)
	if envVars == nil {
		envVars = make(map[string]string)
	}
	envVars["HTTP_PROXY"] = egressHost
	envVars["HTTPS_PROXY"] = egressHost
	envVars["http_proxy"] = egressHost
	envVars["https_proxy"] = egressHost
	envVars["NO_PROXY"] = "localhost,127.0.0.1,::1"
	envVars["no_proxy"] = "localhost,127.0.0.1,::1"
	return envVars
}

func (c *Client) createIngressContainer(ctx context.Context, containerName string, upstreamPort int, attachStdio bool,
	externalEndpointsConfig map[string]*network.EndpointSettings) (int, error) {
	squidPort, err := networking.FindOrUsePort(upstreamPort + 1)
	if err != nil {
		return 0, fmt.Errorf("failed to find or use port %d: %v", squidPort, err)
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
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create ingress container: %v", err)
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
		return 0, fmt.Errorf("failed to convert port %s to int: %v", firstPort, err)
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
	var hostPort int
	// check if we need to map to a random port of not
	if _, ok := labels["toolhive-auxiliary"]; ok && labels["toolhive-auxiliary"] == "true" {
		// find first port
		var err error
		for _, bindings := range portBindings {
			if len(bindings) > 0 {
				hostPortStr := bindings[0].HostPort
				hostPort, err = strconv.Atoi(hostPortStr)
				if err != nil {
					return nil, 0, fmt.Errorf("failed to convert host port %s to int: %v", hostPortStr, err)
				}
				break
			}
		}
	} else {
		// bind to a random host port
		hostPort = networking.FindAvailable()
		if hostPort == 0 {
			return nil, 0, fmt.Errorf("could not find an available port")
		}

		// first port binding needs to map to the host port
		for key, bindings := range portBindings {
			if len(bindings) > 0 {
				bindings[0].HostPort = fmt.Sprintf("%d", hostPort)
				portBindings[key] = bindings
				break
			}
		}
	}

	return portBindings, hostPort, nil
}

func (c *Client) stopProxyContainer(ctx context.Context, containerName string, timeoutSeconds int) {
	containerId, err := c.findExistingContainer(ctx, containerName)
	if err != nil {
		logger.Debugf("Failed to find internal container %s: %v", containerName, err)
	} else {
		err = c.client.ContainerStop(ctx, containerId, container.StopOptions{Timeout: &timeoutSeconds})
		if err != nil {
			logger.Debugf("Failed to stop internal container %s: %v", containerName, err)
		}
	}
}

func (c *Client) deleteNetworks(ctx context.Context, containerName string) error {
	// Delete networks if there are no containers using them.
	toolHiveContainers, err := c.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "toolhive=true")),
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Delete associated internal network
	networkName := fmt.Sprintf("toolhive-%s-internal", containerName)
	if err := c.deleteNetwork(ctx, networkName); err != nil {
		// just log the error and continue
		logger.Warnf("failed to delete network %q: %v", networkName, err)
	}

	if len(toolHiveContainers) == 0 {
		// remove external network
		if err := c.deleteNetwork(ctx, "toolhive-external"); err != nil {
			// just log the error and continue
			logger.Warnf("failed to delete network %q: %v", "toolhive-external", err)
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

// inspectContainerByName finds a container by the workload name and inspects it.
func (c *Client) inspectContainerByName(ctx context.Context, workloadName string) (container.InspectResponse, error) {
	empty := container.InspectResponse{}

	// Since the Docker API expects a lookup by ID, do a search by name and label instead.
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "toolhive=true")
	filterArgs.Add("name", workloadName)

	containers, err := c.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return empty, NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}

	if len(containers) == 0 {
		return empty, NewContainerError(runtime.ErrWorkloadNotFound, workloadName, "no containers found")
	}
	// Docker does a prefix match on the name. If we find multiple containers,
	// we need to filter down to the exact name requested.
	var containerID string
	if len(containers) > 1 {
		// The name in the API has a leading slash, so we need to search for that.
		prefixedName := "/" + workloadName
		// The name in the API response is a list of names, so we need to check
		// if the prefixed name is in the list.
		// The extra names are used for docker network functionality which is
		// not relevant for us.
		idx := slices.IndexFunc(containers, func(c container.Summary) bool {
			return slices.Contains(c.Names, prefixedName)
		})
		if idx == -1 {
			return empty, NewContainerError(runtime.ErrWorkloadNotFound, workloadName, "no containers found with the exact name")
		}
		containerID = containers[idx].ID
	} else {
		containerID = containers[0].ID
	}

	return c.client.ContainerInspect(ctx, containerID)
}

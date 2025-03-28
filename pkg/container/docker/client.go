// Package docker provides Docker-specific implementation of container runtime,
// including creating, starting, stopping, and monitoring containers.
//
// The package is designed around idempotent operations that ensure consistent
// container state without requiring explicit restart operations. Key methods
// like CreateContainer and StartContainer are implemented to be idempotent,
// meaning they can be safely called multiple times with the same parameters
// and will produce the same result.
//
// This design eliminates the need for explicit restart operations:
//   - To "restart" a container, simply call CreateContainer (which verifies or creates
//     with the desired configuration) followed by StartContainer
//   - If configuration changes are needed, CreateContainer will return an error,
//     indicating that manual cleanup is required before applying new configuration
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/stacklok/vibetool/pkg/container/runtime"
	"github.com/stacklok/vibetool/pkg/permissions"
)

// Common socket paths for container runtimes
const (
	// PodmanSocketPath is the default Podman socket path
	PodmanSocketPath = "/var/run/podman/podman.sock"
	// PodmanXDGRuntimeSocketPath is the XDG runtime Podman socket path
	PodmanXDGRuntimeSocketPath = "podman/podman.sock"
	// DockerSocketPath is the default Docker socket path
	DockerSocketPath = "/var/run/docker.sock"
)

// Client implements the Runtime interface for container operations.
// It provides methods for managing containers using either Docker or Podman
// as the underlying container runtime.
type Client struct {
	runtimeType runtime.Type
	socketPath  string
	client      *client.Client
}

// NewClient creates a new container client by automatically detecting
// and selecting an available container runtime (Podman or Docker).
// It will prefer Podman if both runtimes are available.
func NewClient(ctx context.Context) (*Client, error) {
	// Try to find a container socket in various locations
	socketPath, runtimeType, err := findContainerSocket()
	if err != nil {
		return nil, err
	}

	return NewClientWithSocketPath(ctx, socketPath, runtimeType)
}

// NewClientWithSocketPath creates a new container client with a specific socket path.
// This allows for custom socket locations or explicit runtime selection.
//
// Parameters:
//   - ctx: Context for the operation
//   - socketPath: Path to the container runtime socket
//   - runtimeType: Type of container runtime (Docker or Podman)
//
// Returns:
//   - A new Client instance
//   - Error if the client creation fails or the runtime is not available
func NewClientWithSocketPath(ctx context.Context, socketPath string, runtimeType runtime.Type) (*Client, error) {
	// Create a custom HTTP client that uses the Unix socket
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	// Create Docker client with the custom HTTP client
	opts := []client.Opt{
		client.WithAPIVersionNegotiation(),
		client.WithHTTPClient(httpClient),
		client.WithHost("unix://" + socketPath),
	}

	dockerClient, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, NewContainerError(err, "", fmt.Sprintf("failed to create client: %v", err))
	}

	c := &Client{
		runtimeType: runtimeType,
		socketPath:  socketPath,
		client:      dockerClient,
	}

	// Verify that the container runtime is available
	if err := c.ping(ctx); err != nil {
		return nil, err
	}

	return c, nil
}

// ping checks if the container runtime is available
func (c *Client) ping(ctx context.Context) error {
	_, err := c.client.Ping(ctx)
	if err != nil {
		return NewContainerError(ErrRuntimeNotFound, "", fmt.Sprintf("failed to ping %s: %v", c.runtimeType, err))
	}
	return nil
}

// findContainerSocket finds a container socket path, preferring Podman over Docker
func findContainerSocket() (string, runtime.Type, error) {
	// Try Podman sockets first
	// Check standard Podman location
	if _, err := os.Stat(PodmanSocketPath); err == nil {
		return PodmanSocketPath, runtime.TypePodman, nil
	}

	// Check XDG_RUNTIME_DIR location for Podman
	if xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntimeDir != "" {
		xdgSocketPath := filepath.Join(xdgRuntimeDir, PodmanXDGRuntimeSocketPath)
		if _, err := os.Stat(xdgSocketPath); err == nil {
			return xdgSocketPath, runtime.TypePodman, nil
		}
	}

	// Check user-specific location for Podman
	if home := os.Getenv("HOME"); home != "" {
		userSocketPath := filepath.Join(home, ".local/share/containers/podman/machine/podman.sock")
		if _, err := os.Stat(userSocketPath); err == nil {
			return userSocketPath, runtime.TypePodman, nil
		}
	}

	// Try Docker socket as fallback
	if _, err := os.Stat(DockerSocketPath); err == nil {
		return DockerSocketPath, runtime.TypeDocker, nil
	}

	return "", "", ErrRuntimeNotFound
}

// findAndVerifyContainer checks if a container with the given name exists and verifies its configuration.
// Returns the container ID if found and verified, or an error if the container doesn't exist or has mismatched config.
func (c *Client) findAndVerifyContainer(
	ctx context.Context,
	name string,
	image string,
	command []string,
	envVars map[string]string,
	permissionProfile *permissions.Profile,
	transportType string,
	options *runtime.CreateContainerOptions,
) (string, error) {
	// List containers with exact name match
	containers, err := c.client.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("name", "^/?"+name+"$"), // Match exact name with optional leading slash
		),
	})
	if err != nil {
		return "", NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}

	// Check each matching container
	for _, existingContainer := range containers {
		for _, containerName := range existingContainer.Names {
			// Docker prefixes container names with '/', so handle both cases
			if containerName == "/"+name || containerName == name {
				// Verify the container configuration
				if err := c.verifyContainerConfig(ctx, existingContainer.ID, image, command, envVars,
					permissionProfile, transportType, options); err != nil {
					if err == ErrContainerAlreadyExists {
						return "", err // Return as is for configuration mismatch
					}
					return "", NewContainerError(err, existingContainer.ID,
						fmt.Sprintf("failed to verify container configuration: %v", err))
				}
				return existingContainer.ID, nil
			}
		}
	}

	return "", nil // Container not found
}

// CreateContainer creates a new container with the specified configuration.
// If a container with the same name already exists and has matching configuration,
// it returns the existing container's ID. If the configuration doesn't match,
// it returns an error.
//
// The operation is idempotent: repeated calls with the same configuration
// will return the same container ID. This design choice eliminates the need
// for explicit restart operations - to "restart" a container, simply ensure
// it exists with the desired configuration using this method, then call
// StartContainer.
//
// Parameters:
//   - ctx: Context for the operation
//   - image: Container image to use
//   - name: Name for the container
//   - command: Command to run in the container
//   - envVars: Environment variables to set
//   - labels: Labels to apply to the container
//   - permissionProfile: Security and permission settings
//   - transportType: Type of transport (stdio or sse)
//   - options: Additional container creation options
//
// Returns:
//   - Container ID on success
//   - Error if the operation fails or if an existing container has different configuration
func (c *Client) CreateContainer(
	ctx context.Context,
	image, name string,
	command []string,
	envVars, labels map[string]string,
	permissionProfile *permissions.Profile,
	transportType string,
	options *runtime.CreateContainerOptions,
) (string, error) {
	// Check if container exists and verify configuration
	if containerID, err := c.findAndVerifyContainer(ctx, name, image, command, envVars,
		permissionProfile, transportType, options); err != nil {
		return "", err
	} else if containerID != "" {
		return containerID, nil
	}

	// Get permission config from profile
	permissionConfig, err := c.getPermissionConfigFromProfile(permissionProfile, transportType)
	if err != nil {
		return "", fmt.Errorf("failed to get permission config: %w", err)
	}

	// Determine if we should attach stdio
	attachStdio := options == nil || options.AttachStdio

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
	}

	// Configure ports if options are provided
	if options != nil {
		// Setup exposed ports
		if err := setupExposedPorts(config, options.ExposedPorts); err != nil {
			return "", NewContainerError(err, "", err.Error())
		}

		// Setup port bindings
		if err := setupPortBindings(hostConfig, options.PortBindings); err != nil {
			return "", NewContainerError(err, "", err.Error())
		}
	}

	// Create the container
	resp, err := c.client.ContainerCreate(
		ctx,
		config,
		hostConfig,
		&network.NetworkingConfig{},
		nil,
		name,
	)
	if err != nil {
		// Handle race condition where container was created between our check and create
		if client.IsErrNotFound(err) || strings.Contains(err.Error(), "Conflict") {
			// Try to find and verify the container again
			if containerID, verifyErr := c.findAndVerifyContainer(ctx, name, image, command, envVars,
				permissionProfile, transportType, options); verifyErr == nil && containerID != "" {
				return containerID, nil
			}
			// If we get here, something unexpected happened
			return "", NewContainerError(err, "", fmt.Sprintf("container name conflict: %v", err))
		}
		return "", NewContainerError(err, "", fmt.Sprintf("failed to create container: %v", err))
	}

	return resp.ID, nil
}

// StartContainer starts a container with the given ID.
//
// The operation is idempotent: if the container is already running,
// it will return success without doing anything. This behavior,
// combined with CreateContainer's idempotency, provides a simple
// way to ensure a container is running with the desired configuration
// without needing explicit restart operations.
//
// To "restart" a container:
// 1. Call CreateContainer to ensure correct configuration
// 2. Call StartContainer to ensure the container is running
//
// If configuration changes are needed, CreateContainer will return
// an error, preventing inconsistent states.
//
// Parameters:
//   - ctx: Context for the operation
//   - containerID: ID of the container to start
//
// Returns:
//   - Error if the operation fails or if the container doesn't exist
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	// Check if container is already running
	info, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return NewContainerError(ErrContainerNotFound, containerID, "container not found")
		}
		return NewContainerError(err, containerID, fmt.Sprintf("failed to inspect container: %v", err))
	}

	// If container is already running, return success
	if info.State.Running {
		return nil
	}

	// Start the container
	err = c.client.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		// Check again in case the container was started between our check and start
		// This handles race conditions
		if strings.Contains(err.Error(), "already started") {
			return nil
		}
		return NewContainerError(err, containerID, fmt.Sprintf("failed to start container: %v", err))
	}
	return nil
}

// ListContainers lists containers
func (c *Client) ListContainers(ctx context.Context) ([]runtime.ContainerInfo, error) {
	// Create filter for vibetool containers
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "vibetool=true")

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
			ID:      c.ID,
			Name:    name,
			Image:   c.Image,
			Status:  c.Status,
			State:   c.State,
			Created: created,
			Labels:  c.Labels,
			Ports:   ports,
		})
	}

	return result, nil
}

// StopContainer stops a container
func (c *Client) StopContainer(ctx context.Context, containerID string) error {
	// Use a reasonable timeout
	timeoutSeconds := 30
	err := c.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeoutSeconds})
	if err != nil {
		return NewContainerError(err, containerID, fmt.Sprintf("failed to stop container: %v", err))
	}
	return nil
}

// RemoveContainer removes a container
func (c *Client) RemoveContainer(ctx context.Context, containerID string) error {
	err := c.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: true,
	})
	if err != nil {
		return NewContainerError(err, containerID, fmt.Sprintf("failed to remove container: %v", err))
	}
	return nil
}

// ContainerLogs gets container logs
func (c *Client) ContainerLogs(ctx context.Context, containerID string) (string, error) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	}

	// Get logs
	logs, err := c.client.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return "", NewContainerError(err, containerID, fmt.Sprintf("failed to get container logs: %v", err))
	}
	defer logs.Close()

	// Read logs
	logBytes, err := io.ReadAll(logs)
	if err != nil {
		return "", NewContainerError(err, containerID, fmt.Sprintf("failed to read container logs: %v", err))
	}

	return string(logBytes), nil
}

// IsContainerRunning checks if a container is running
func (c *Client) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	// Inspect container
	info, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		// Check if the error is because the container doesn't exist
		if client.IsErrNotFound(err) {
			return false, NewContainerError(ErrContainerNotFound, containerID, "container not found")
		}
		return false, NewContainerError(err, containerID, fmt.Sprintf("failed to inspect container: %v", err))
	}

	return info.State.Running, nil
}

// GetContainerInfo gets container information
func (c *Client) GetContainerInfo(ctx context.Context, containerID string) (runtime.ContainerInfo, error) {
	// Inspect container
	info, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		// Check if the error is because the container doesn't exist
		if client.IsErrNotFound(err) {
			return runtime.ContainerInfo{}, NewContainerError(ErrContainerNotFound, containerID, "container not found")
		}
		return runtime.ContainerInfo{}, NewContainerError(err, containerID, fmt.Sprintf("failed to inspect container: %v", err))
	}

	// Extract port mappings
	ports := make([]runtime.PortMapping, 0)
	for containerPort, bindings := range info.NetworkSettings.Ports {
		for _, binding := range bindings {
			hostPort := 0
			if _, err := fmt.Sscanf(binding.HostPort, "%d", &hostPort); err != nil {
				// If we can't parse the port, just use 0
				fmt.Printf("Warning: Failed to parse host port %s: %v\n", binding.HostPort, err)
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

	// Extract environment variables
	envVars := make([]string, 0)
	if info.Config != nil {
		envVars = append(envVars, info.Config.Env...)
	}

	// Create container info
	containerInfo := runtime.ContainerInfo{
		ID:      info.ID,
		Name:    strings.TrimPrefix(info.Name, "/"),
		Image:   info.Config.Image,
		Status:  info.State.Status,
		State:   info.State.Status,
		Created: created,
		Labels:  info.Config.Labels,
		Ports:   ports,
		Env:     envVars,
	}

	return containerInfo, nil
}

// GetContainerIP gets container IP address
func (c *Client) GetContainerIP(ctx context.Context, containerID string) (string, error) {
	// Inspect container
	info, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		// Check if the error is because the container doesn't exist
		if client.IsErrNotFound(err) {
			return "", NewContainerError(ErrContainerNotFound, containerID, "container not found")
		}
		return "", NewContainerError(err, containerID, fmt.Sprintf("failed to inspect container: %v", err))
	}

	// Get IP address from the default network
	for _, netInfo := range info.NetworkSettings.Networks {
		if netInfo.IPAddress != "" {
			return netInfo.IPAddress, nil
		}
	}

	return "", NewContainerError(fmt.Errorf("no IP address found"), containerID, "container has no IP address")
}

// readCloserWrapper wraps an io.Reader to implement io.ReadCloser
type readCloserWrapper struct {
	reader io.Reader
}

func (r *readCloserWrapper) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (*readCloserWrapper) Close() error {
	// No-op close for readers that don't need closing
	return nil
}

// AttachContainer attaches to a container
func (c *Client) AttachContainer(ctx context.Context, containerID string) (io.WriteCloser, io.ReadCloser, error) {
	// Check if container exists and is running
	running, err := c.IsContainerRunning(ctx, containerID)
	if err != nil {
		return nil, nil, err
	}
	if !running {
		return nil, nil, NewContainerError(ErrContainerNotRunning, containerID, "container is not running")
	}

	// Attach to container
	resp, err := c.client.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, nil, NewContainerError(ErrAttachFailed, containerID, fmt.Sprintf("failed to attach to container: %v", err))
	}

	// Wrap the reader in a ReadCloser
	readCloser := &readCloserWrapper{reader: resp.Reader}

	return resp.Conn, readCloser, nil
}

// ImageExists checks if an image exists locally
func (c *Client) ImageExists(ctx context.Context, imageName string) (bool, error) {
	// List images with the specified name
	filterArgs := filters.NewArgs()
	filterArgs.Add("reference", imageName)

	images, err := c.client.ImageList(ctx, dockerimage.ListOptions{
		Filters: filterArgs,
	})
	if err != nil {
		return false, NewContainerError(err, "", fmt.Sprintf("failed to list images: %v", err))
	}

	return len(images) > 0, nil
}

// parsePullOutput parses the Docker image pull output and formats it in a more readable way
func parsePullOutput(reader io.Reader, writer io.Writer) error {
	decoder := json.NewDecoder(reader)
	for {
		var pullStatus struct {
			Status         string          `json:"status"`
			ID             string          `json:"id,omitempty"`
			ProgressDetail json.RawMessage `json:"progressDetail,omitempty"`
			Progress       string          `json:"progress,omitempty"`
		}

		if err := decoder.Decode(&pullStatus); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode pull output: %w", err)
		}

		// Format the output based on the type of message
		if pullStatus.Progress != "" {
			// This is a progress update
			fmt.Fprintf(writer, "%s: %s %s\n", pullStatus.Status, pullStatus.ID, pullStatus.Progress)
		} else if pullStatus.ID != "" {
			// This is a layer-specific status update
			fmt.Fprintf(writer, "%s: %s\n", pullStatus.Status, pullStatus.ID)
		} else {
			// This is a general status update
			fmt.Fprintf(writer, "%s\n", pullStatus.Status)
		}
	}

	return nil
}

// PullImage pulls an image from a registry
func (c *Client) PullImage(ctx context.Context, imageName string) error {
	fmt.Printf("Pulling image: %s\n", imageName)

	// Pull the image
	reader, err := c.client.ImagePull(ctx, imageName, dockerimage.PullOptions{})
	if err != nil {
		return NewContainerError(err, "", fmt.Sprintf("failed to pull image: %v", err))
	}
	defer reader.Close()

	// Parse and filter the pull output
	if err := parsePullOutput(reader, os.Stdout); err != nil {
		return NewContainerError(err, "", fmt.Sprintf("failed to process pull output: %v", err))
	}

	return nil
}

// getPermissionConfigFromProfile converts a permission profile to a container permission config
// with transport-specific settings (internal function)
// addReadOnlyMounts adds read-only mounts to the permission config
func (*Client) addReadOnlyMounts(config *runtime.PermissionConfig, mounts []permissions.MountDeclaration) {
	for _, mountDecl := range mounts {
		source, target, err := mountDecl.Parse()
		if err != nil {
			// Skip invalid mounts
			fmt.Printf("Warning: Skipping invalid mount declaration: %s (%v)\n", mountDecl, err)
			continue
		}

		// Skip resource URIs for now (they need special handling)
		if strings.Contains(source, "://") {
			fmt.Printf("Warning: Resource URI mounts not yet supported: %s\n", source)
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
		})
	}
}

// addReadWriteMounts adds read-write mounts to the permission config
func (*Client) addReadWriteMounts(config *runtime.PermissionConfig, mounts []permissions.MountDeclaration) {
	for _, mountDecl := range mounts {
		source, target, err := mountDecl.Parse()
		if err != nil {
			// Skip invalid mounts
			fmt.Printf("Warning: Skipping invalid mount declaration: %s (%v)\n", mountDecl, err)
			continue
		}

		// Skip resource URIs for now (they need special handling)
		if strings.Contains(source, "://") {
			fmt.Printf("Warning: Resource URI mounts not yet supported: %s\n", source)
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
			})
		}
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
		fmt.Printf("Warning: Failed to get current working directory: %v\n", err)
		return "", false
	}

	// Convert relative path to absolute path
	absPath := filepath.Join(cwd, source)
	fmt.Printf("Converting relative path to absolute: %s -> %s\n", mountDecl, absPath)
	return absPath, true
}

// needsNetworkAccess determines if the container needs network access
func (*Client) needsNetworkAccess(profile *permissions.Profile, transportType string) bool {
	// SSE transport always needs network access
	if transportType == "sse" {
		return true
	}

	// Check if the profile has network settings that require network access
	if profile.Network != nil && profile.Network.Outbound != nil {
		outbound := profile.Network.Outbound

		// Any of these conditions require network access
		if outbound.InsecureAllowAll ||
			len(outbound.AllowTransport) > 0 ||
			len(outbound.AllowHost) > 0 ||
			len(outbound.AllowPort) > 0 {
			return true
		}
	}

	return false
}

// getPermissionConfigFromProfile converts a permission profile to a container permission config
func (c *Client) getPermissionConfigFromProfile(
	profile *permissions.Profile,
	transportType string,
) (*runtime.PermissionConfig, error) {
	// Start with a default permission config
	config := &runtime.PermissionConfig{
		Mounts:      []runtime.Mount{},
		NetworkMode: "none",
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{},
		SecurityOpt: []string{},
	}

	// Add mounts
	c.addReadOnlyMounts(config, profile.Read)
	c.addReadWriteMounts(config, profile.Write)

	// Determine network mode
	if c.needsNetworkAccess(profile, transportType) {
		config.NetworkMode = "bridge"
	}

	// Validate transport type
	if transportType != "sse" && transportType != "stdio" {
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}

	return config, nil
}

// verifyContainerConfig checks if an existing container's configuration matches the expected configuration
//
//nolint:gocyclo // This is a complex function, but it's not too bad
func (c *Client) verifyContainerConfig(ctx context.Context, containerID, image string, command []string,
	envVars map[string]string, permissionProfile *permissions.Profile,
	transportType string, options *runtime.CreateContainerOptions) error {

	containerInfo, err := c.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return NewContainerError(err, containerID, "failed to inspect container")
	}

	// Get permission config from profile for verification
	permissionConfig, err := c.getPermissionConfigFromProfile(permissionProfile, transportType)
	if err != nil {
		return fmt.Errorf("failed to get permission config: %w", err)
	}

	// Check image
	if !strings.HasPrefix(containerInfo.Image, "sha256:") && !strings.HasPrefix(containerInfo.Image, image) {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			fmt.Sprintf("container exists with different image: %s", containerInfo.Image))
	}

	// Check command
	if !compareStringSlices(containerInfo.Config.Cmd, command) {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			fmt.Sprintf("container exists with different command: %v", containerInfo.Config.Cmd))
	}

	// Check environment variables
	if !compareEnvVars(containerInfo.Config.Env, convertEnvVars(envVars)) {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			"container exists with different environment variables")
	}

	// Check network mode
	expectedNetworkMode := container.NetworkMode(permissionConfig.NetworkMode)
	if containerInfo.HostConfig.NetworkMode != expectedNetworkMode {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			fmt.Sprintf("container exists with different network mode: %s", containerInfo.HostConfig.NetworkMode))
	}

	// Check capabilities
	if !compareStringSlices(containerInfo.HostConfig.CapAdd, permissionConfig.CapAdd) ||
		!compareStringSlices(containerInfo.HostConfig.CapDrop, permissionConfig.CapDrop) {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			"container exists with different capabilities")
	}

	// Check security options
	if !compareStringSlices(containerInfo.HostConfig.SecurityOpt, permissionConfig.SecurityOpt) {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			"container exists with different security options")
	}

	// Check mounts
	if !compareMounts(containerInfo.Mounts, permissionConfig.Mounts) {
		return NewContainerError(ErrContainerAlreadyExists, containerID,
			"container exists with different mounts")
	}

	// Check port configuration if options are provided
	if options != nil {
		// Check exposed ports
		if !compareExposedPorts(containerInfo.Config.ExposedPorts, options.ExposedPorts) {
			return NewContainerError(ErrContainerAlreadyExists, containerID,
				"container exists with different exposed ports")
		}

		// Check port bindings
		if !comparePortBindings(containerInfo.HostConfig.PortBindings, options.PortBindings) {
			return NewContainerError(ErrContainerAlreadyExists, containerID,
				"container exists with different port bindings")
		}
	}

	return nil
}

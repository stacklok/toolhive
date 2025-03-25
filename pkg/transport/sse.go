package transport

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/networking"
	"github.com/stacklok/vibetool/pkg/permissions"
)

const (
	// LocalhostName is the standard hostname for localhost
	LocalhostName = "localhost"
)

// SSETransport implements the Transport interface using Server-Sent Events.
type SSETransport struct {
	host          string
	port          int
	targetPort    int
	containerID   string
	containerName string
	runtime       container.Runtime
	debug         bool
	middlewares   []Middleware

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Transparent proxy
	proxy Proxy

	// Shutdown channel
	shutdownCh chan struct{}

	// Container monitor
	monitor *container.Monitor
	errorCh <-chan error
}

// NewSSETransport creates a new SSE transport.
func NewSSETransport(
	host string,
	port int,
	targetPort int,
	runtime container.Runtime,
	debug bool,
	middlewares ...Middleware,
) *SSETransport {
	if host == "" {
		host = LocalhostName
	}

	return &SSETransport{
		host:        host,
		port:        port,
		middlewares: middlewares,
		targetPort:  targetPort,
		runtime:     runtime,
		debug:       debug,
		shutdownCh:  make(chan struct{}),
	}
}

// Mode returns the transport mode.
func (*SSETransport) Mode() TransportType {
	return TransportTypeSSE
}

// Port returns the port used by the transport.
func (t *SSETransport) Port() int {
	return t.port
}

// Setup prepares the transport for use.
func (t *SSETransport) Setup(ctx context.Context, runtime container.Runtime, containerName string, image string, cmdArgs []string,
	envVars, labels map[string]string, permissionProfile *permissions.Profile) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.runtime = runtime
	t.containerName = containerName

	// Add transport-specific environment variables
	envVars["MCP_TRANSPORT"] = "sse"

	// Use the target port for the container's environment variables
	envVars["MCP_PORT"] = fmt.Sprintf("%d", t.targetPort)
	envVars["FASTMCP_PORT"] = fmt.Sprintf("%d", t.targetPort)

	// Always use localhost for the host
	// In a Docker bridge network, the container IP is not directly accessible from the host
	envVars["MCP_HOST"] = LocalhostName

	// Get container permission config from the runtime
	containerPermConfig, err := runtime.GetPermissionConfigFromProfile(permissionProfile, "sse")
	if err != nil {
		return fmt.Errorf("failed to get permission configuration: %v", err)
	}

	// Create container options
	containerOptions := container.NewCreateContainerOptions()

	// For SSE transport, expose the target port in the container
	containerPortStr := fmt.Sprintf("%d/tcp", t.targetPort)
	containerOptions.ExposedPorts[containerPortStr] = struct{}{}

	// Create port bindings for localhost
	portBindings := []container.PortBinding{
		{
			HostIP:   "127.0.0.1", // IPv4 localhost
			HostPort: fmt.Sprintf("%d", t.targetPort),
		},
	}

	// Check if IPv6 is available and add IPv6 localhost binding
	if networking.IsIPv6Available() {
		portBindings = append(portBindings, container.PortBinding{
			HostIP:   "::1", // IPv6 localhost
			HostPort: fmt.Sprintf("%d", t.targetPort),
		})
	}

	// Set the port bindings
	containerOptions.PortBindings[containerPortStr] = portBindings

	fmt.Printf("Exposing container port %d\n", t.targetPort)

	// For SSE transport, we don't need to attach stdio
	containerOptions.AttachStdio = false

	// Create the container
	fmt.Printf("Creating container %s from image %s...\n", containerName, image)
	containerID, err := t.runtime.CreateContainer(
		ctx,
		image,
		containerName,
		cmdArgs,
		envVars,
		labels,
		containerPermConfig,
		containerOptions,
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	t.containerID = containerID
	fmt.Printf("Container created with ID: %s\n", containerID)

	return nil
}

// Start initializes the transport and begins processing messages.
// The transport is responsible for starting the container.
func (t *SSETransport) Start(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.containerID == "" {
		return ErrContainerIDNotSet
	}

	if t.containerName == "" {
		return ErrContainerNameNotSet
	}

	if t.runtime == nil {
		return fmt.Errorf("container runtime not set")
	}

	// Start the container
	fmt.Printf("Starting container %s...\n", t.containerName)
	if err := t.runtime.StartContainer(ctx, t.containerID); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	// Create and start the transparent proxy
	// The SSE transport forwards requests from the host port to the container's target port

	// In a Docker bridge network, we need to use localhost since the container port is mapped to the host
	// We ignore containerIP even if it's set, as it's not directly accessible from the host
	targetHost := LocalhostName

	// Check if target port is set
	if t.targetPort <= 0 {
		return fmt.Errorf("target port not set for SSE transport")
	}

	// Use the target port for the container
	containerPort := t.targetPort

	targetURI := fmt.Sprintf("http://%s:%d", targetHost, containerPort)
	fmt.Printf("Setting up transparent proxy to forward from host port %d to %s\n",
		t.port, targetURI)

	// Create the transparent proxy with middlewares
	t.proxy = NewTransparentProxy(t.port, t.containerName, targetURI, t.middlewares...)
	if err := t.proxy.Start(ctx); err != nil {
		return err
	}

	fmt.Printf("SSE transport started for container %s on port %d\n", t.containerName, t.port)

	// Create a container monitor
	monitorRuntime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container monitor: %v", err)
	}
	t.monitor = container.NewMonitor(monitorRuntime, t.containerID, t.containerName)

	// Start monitoring the container
	t.errorCh, err = t.monitor.StartMonitoring(ctx)
	if err != nil {
		return fmt.Errorf("failed to start container monitoring: %v", err)
	}

	// Start a goroutine to handle container exit
	go t.handleContainerExit(ctx)

	return nil
}

// Stop gracefully shuts down the transport and the container.
func (t *SSETransport) Stop(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Signal shutdown
	close(t.shutdownCh)

	// Stop the monitor if it's running
	if t.monitor != nil {
		t.monitor.StopMonitoring()
		t.monitor = nil
	}

	// Stop the transparent proxy
	if t.proxy != nil {
		if err := t.proxy.Stop(ctx); err != nil {
			fmt.Printf("Warning: Failed to stop proxy: %v\n", err)
		}
	}

	// Stop the container if runtime is available
	if t.runtime != nil && t.containerID != "" {
		if err := t.runtime.StopContainer(ctx, t.containerID); err != nil {
			return fmt.Errorf("failed to stop container: %w", err)
		}

		// Remove the container if debug mode is not enabled
		if !t.debug {
			fmt.Printf("Removing container %s...\n", t.containerName)
			if err := t.runtime.RemoveContainer(ctx, t.containerID); err != nil {
				fmt.Printf("Warning: Failed to remove container: %v\n", err)
			}
			fmt.Printf("Container %s removed\n", t.containerName)
		} else {
			fmt.Printf("Debug mode enabled, container %s not removed\n", t.containerName)
		}
	}

	return nil
}

// handleContainerExit handles container exit events.
func (t *SSETransport) handleContainerExit(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case err := <-t.errorCh:
		fmt.Printf("Container %s exited: %v\n", t.containerName, err)
		// Stop the transport when the container exits
		if stopErr := t.Stop(ctx); stopErr != nil {
			fmt.Printf("Error stopping transport after container exit: %v\n", stopErr)
		}
	}
}

// IsRunning checks if the transport is currently running.
func (t *SSETransport) IsRunning(_ context.Context) (bool, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Check if the shutdown channel is closed
	select {
	case <-t.shutdownCh:
		return false, nil
	default:
		return true, nil
	}
}

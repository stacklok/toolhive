package transport

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/transport/errors"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// LocalhostName is the standard hostname for localhost
	LocalhostName = "localhost"
	// LocalhostIPv4 is the standard IPv4 address for localhost
	LocalhostIPv4 = "127.0.0.1"
)

// SSETransport implements the Transport interface using Server-Sent Events.
type SSETransport struct {
	host          string
	port          int
	targetPort    int
	targetHost    string
	containerID   string
	containerName string
	runtime       rt.Runtime
	debug         bool
	middlewares   []types.Middleware

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Transparent proxy
	proxy types.Proxy

	// Shutdown channel
	shutdownCh chan struct{}

	// Container monitor
	monitor rt.Monitor
	errorCh <-chan error
}

// NewSSETransport creates a new SSE transport.
func NewSSETransport(
	host string,
	port int,
	targetPort int,
	runtime rt.Runtime,
	debug bool,
	targetHost string,
	middlewares ...types.Middleware,
) *SSETransport {
	if host == "" {
		host = LocalhostIPv4
	}

	// If targetHost is not specified, default to localhost
	if targetHost == "" {
		targetHost = LocalhostIPv4
	}

	return &SSETransport{
		host:        host,
		port:        port,
		middlewares: middlewares,
		targetPort:  targetPort,
		targetHost:  targetHost,
		runtime:     runtime,
		debug:       debug,
		shutdownCh:  make(chan struct{}),
	}
}

// Mode returns the transport mode.
func (*SSETransport) Mode() types.TransportType {
	return types.TransportTypeSSE
}

// Port returns the port used by the transport.
func (t *SSETransport) Port() int {
	return t.port
}

// Setup prepares the transport for use.
func (t *SSETransport) Setup(ctx context.Context, runtime rt.Runtime, containerName string, image string, cmdArgs []string,
	envVars, labels map[string]string, permissionProfile *permissions.Profile, k8sPodTemplatePatch string) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.runtime = runtime
	t.containerName = containerName

	// Add transport-specific environment variables
	envVars["MCP_TRANSPORT"] = "sse"

	// Use the target port for the container's environment variables
	envVars["MCP_PORT"] = fmt.Sprintf("%d", t.targetPort)
	envVars["FASTMCP_PORT"] = fmt.Sprintf("%d", t.targetPort)
	envVars["MCP_HOST"] = t.targetHost

	// Create workload options
	containerOptions := rt.NewDeployWorkloadOptions()
	containerOptions.K8sPodTemplatePatch = k8sPodTemplatePatch

	// For SSE transport, expose the target port in the container
	containerPortStr := fmt.Sprintf("%d/tcp", t.targetPort)
	containerOptions.ExposedPorts[containerPortStr] = struct{}{}

	// Create host port bindings (configurable through the --host flag)
	portBindings := []rt.PortBinding{
		{
			HostIP:   t.host,
			HostPort: fmt.Sprintf("%d", t.targetPort),
		},
	}

	// Check if IPv6 is available and add IPv6 localhost binding (commented out for now)
	//if networking.IsIPv6Available() {
	//	portBindings = append(portBindings, rt.PortBinding{
	//		HostIP:   "::1", // IPv6 localhost
	//		HostPort: fmt.Sprintf("%d", t.targetPort),
	//	})
	//}

	// Set the port bindings
	containerOptions.PortBindings[containerPortStr] = portBindings

	logger.Infof("Exposing container port %d", t.targetPort)

	// For SSE transport, we don't need to attach stdio
	containerOptions.AttachStdio = false

	// Create the container
	logger.Infof("Deploying workload %s from image %s...", containerName, image)
	containerID, err := t.runtime.DeployWorkload(
		ctx,
		image,
		containerName,
		cmdArgs,
		envVars,
		labels,
		permissionProfile,
		"sse",
		containerOptions,
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	t.containerID = containerID
	logger.Infof("Container created with ID: %s", containerID)

	return nil
}

// Start initializes the transport and begins processing messages.
// The transport is responsible for starting the container.
func (t *SSETransport) Start(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.containerID == "" {
		return errors.ErrContainerIDNotSet
	}

	if t.containerName == "" {
		return errors.ErrContainerNameNotSet
	}

	if t.runtime == nil {
		return fmt.Errorf("container runtime not set")
	}

	// Create and start the transparent proxy
	// The SSE transport forwards requests from the host port to the container's target port
	// In a Docker bridge network, we need to use the specified target host
	// We ignore containerIP even if it's set, as it's not directly accessible from the host
	targetHost := t.targetHost

	// Check if target port is set
	if t.targetPort <= 0 {
		return fmt.Errorf("target port not set for SSE transport")
	}

	// Use the target port for the container
	containerPort := t.targetPort

	targetURI := fmt.Sprintf("http://%s:%d", targetHost, containerPort)
	logger.Infof("Setting up transparent proxy to forward from host port %d to %s",
		t.port, targetURI)

	// Create the transparent proxy with middlewares
	t.proxy = transparent.NewTransparentProxy(t.host, t.port, t.containerName, targetURI, t.middlewares...)
	if err := t.proxy.Start(ctx); err != nil {
		return err
	}

	logger.Infof("SSE transport started for container %s on port %d", t.containerName, t.port)

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
			logger.Warnf("Warning: Failed to stop proxy: %v", err)
		}
	}

	// Stop the container if runtime is available
	if t.runtime != nil && t.containerID != "" {
		if err := t.runtime.StopWorkload(ctx, t.containerID); err != nil {
			return fmt.Errorf("failed to stop workload: %w", err)
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
		logger.Infof("Container %s exited: %v", t.containerName, err)
		// Stop the transport when the container exits
		if stopErr := t.Stop(ctx); stopErr != nil {
			logger.Errorf("Error stopping transport after container exit: %v", stopErr)
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

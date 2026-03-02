// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	transporterrors "github.com/stacklok/toolhive/pkg/transport/errors"
	"github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// LocalhostName is the standard hostname for localhost
	LocalhostName = "localhost"
	// LocalhostIPv4 is the standard IPv4 address for localhost
	LocalhostIPv4 = "127.0.0.1"
)

// HTTPTransport implements the Transport interface using Server-Sent/Streamable Events.
type HTTPTransport struct {
	transportType     types.TransportType
	host              string
	proxyPort         int
	targetPort        int
	targetHost        string
	containerName     string
	targetURI         string
	deployer          rt.Deployer
	debug             bool
	middlewares       []types.NamedMiddleware
	prometheusHandler http.Handler
	authInfoHandler   http.Handler
	prefixHandlers    map[string]http.Handler

	// endpointPrefix is an explicit prefix to prepend to SSE endpoint URLs
	endpointPrefix string

	// trustProxyHeaders indicates whether to trust X-Forwarded-* headers
	trustProxyHeaders bool

	// Remote MCP server support
	remoteURL string

	// tokenSource is the OAuth token source for remote authentication
	tokenSource oauth2.TokenSource

	// onHealthCheckFailed is called when a health check fails for remote servers
	onHealthCheckFailed types.HealthCheckFailedCallback

	// onUnauthorizedResponse is called when a 401 Unauthorized response is received
	onUnauthorizedResponse types.UnauthorizedResponseCallback

	// isMarkedUnauthorized tracks if we've already marked the workload as unauthenticated
	// This prevents repeated status updates on every 401 response
	isMarkedUnauthorized bool

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Transparent proxy
	proxy types.Proxy

	// Shutdown channel
	shutdownCh chan struct{}

	// Container monitor
	monitor        rt.Monitor
	monitorRuntime rt.Runtime // Stored for monitor reconnection on container restart
	errorCh        <-chan error

	// Container exit error (for determining if restart is needed)
	containerExitErr error
	exitErrMutex     sync.Mutex
}

// NewHTTPTransport creates a new HTTP transport.
func NewHTTPTransport(
	transportType types.TransportType,
	host string,
	proxyPort int,
	targetPort int,
	deployer rt.Deployer,
	debug bool,
	targetHost string,
	authInfoHandler http.Handler,
	prometheusHandler http.Handler,
	prefixHandlers map[string]http.Handler,
	endpointPrefix string,
	trustProxyHeaders bool,
	middlewares ...types.NamedMiddleware,
) *HTTPTransport {
	if host == "" {
		host = LocalhostIPv4
	}

	// If targetHost is not specified, default to localhost
	if targetHost == "" {
		targetHost = LocalhostIPv4
	}

	return &HTTPTransport{
		transportType:     transportType,
		host:              host,
		proxyPort:         proxyPort,
		middlewares:       middlewares,
		targetPort:        targetPort,
		targetHost:        targetHost,
		deployer:          deployer,
		debug:             debug,
		prometheusHandler: prometheusHandler,
		authInfoHandler:   authInfoHandler,
		prefixHandlers:    prefixHandlers,
		endpointPrefix:    endpointPrefix,
		trustProxyHeaders: trustProxyHeaders,
		shutdownCh:        make(chan struct{}),
	}
}

// SetRemoteURL sets the remote URL for the MCP server
func (t *HTTPTransport) SetRemoteURL(remoteURL string) {
	t.remoteURL = remoteURL
}

// SetTokenSource sets the OAuth token source for remote authentication
func (t *HTTPTransport) SetTokenSource(tokenSource oauth2.TokenSource) {
	t.tokenSource = tokenSource
}

// SetOnHealthCheckFailed sets the callback for health check failures
func (t *HTTPTransport) SetOnHealthCheckFailed(callback types.HealthCheckFailedCallback) {
	t.onHealthCheckFailed = callback
}

// SetOnUnauthorizedResponse sets the callback for 401 Unauthorized responses
// The callback is wrapped to check the unauthorized flag to prevent repeated status updates
func (t *HTTPTransport) SetOnUnauthorizedResponse(callback types.UnauthorizedResponseCallback) {
	if callback == nil {
		t.onUnauthorizedResponse = nil
		return
	}
	// Wrap the callback to check the flag before calling it
	t.onUnauthorizedResponse = func() {
		// Check if we've already marked as unauthenticated (skip if already marked)
		if t.checkAndMarkUnauthorized() {
			return // Already marked, skip update
		}
		// Call the original callback
		callback()
	}
}

// checkAndMarkUnauthorized checks if we've already marked as unauthenticated and marks it if not
// Returns true if we should skip the status update (already marked)
func (t *HTTPTransport) checkAndMarkUnauthorized() bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if t.isMarkedUnauthorized {
		return true // Already marked, skip update
	}
	t.isMarkedUnauthorized = true
	return false // Not marked yet, proceed with update
}

// createTokenInjectionMiddleware creates a middleware that injects the OAuth token into requests
func (t *HTTPTransport) createTokenInjectionMiddleware() types.MiddlewareFunction {
	return middleware.CreateTokenInjectionMiddleware(t.tokenSource)
}

// hasTokenExchangeMiddleware checks if any middleware in the slice is a token exchange middleware.
// When token exchange is configured, it handles its own Authorization header injection,
// so the oauth-token-injection middleware should be skipped to avoid overwriting the exchanged token.
func hasTokenExchangeMiddleware(middlewares []types.NamedMiddleware) bool {
	for _, mw := range middlewares {
		if mw.Name == tokenexchange.MiddlewareType {
			return true
		}
	}
	return false
}

// shouldEnableHealthCheck determines whether health checks should be enabled based on workload type.
// For local workloads, health checks are always enabled.
// For remote workloads, health checks are only enabled if explicitly opted in via the
// TOOLHIVE_REMOTE_HEALTHCHECKS environment variable (set to "true" or "1").
func shouldEnableHealthCheck(isRemote bool) bool {
	if !isRemote {
		// Always enable health checks for local workloads
		return true
	}
	// For remote workloads, only enable if explicitly opted in via environment variable
	envVal := os.Getenv("TOOLHIVE_REMOTE_HEALTHCHECKS")
	return strings.ToLower(envVal) == "true" || envVal == "1"
}

// Mode returns the transport mode.
func (t *HTTPTransport) Mode() types.TransportType {
	return t.transportType
}

// ProxyPort returns the proxy port used by the transport.
func (t *HTTPTransport) ProxyPort() int {
	return t.proxyPort
}

// setContainerName configures the transport with the container name.
// This is an unexported method used by the option pattern.
func (t *HTTPTransport) setContainerName(containerName string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.containerName = containerName
}

// setTargetURI configures the transport with the target URI for proxying.
// This is an unexported method used by the option pattern.
func (t *HTTPTransport) setTargetURI(targetURI string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.targetURI = targetURI
}

// Start initializes the transport and begins processing messages.
// The transport is responsible for starting the container.
func (t *HTTPTransport) Start(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.deployer == nil && t.remoteURL == "" {
		return fmt.Errorf("container deployer not set")
	}

	// Determine target URI
	var targetURI string

	if t.remoteURL != "" {
		// For remote MCP servers, construct target URI from remote URL
		remoteURL, err := url.Parse(t.remoteURL)
		if err != nil {
			return fmt.Errorf("failed to parse remote URL: %w", err)
		}
		targetURI = (&url.URL{
			Scheme: remoteURL.Scheme,
			Host:   remoteURL.Host,
		}).String()
		//nolint:gosec // G706: logging proxy port and remote URL from config
		slog.Debug("setting up transparent proxy to forward to remote URL",
			"port", t.proxyPort, "target", targetURI)
	} else {
		if t.containerName == "" {
			return transporterrors.ErrContainerNameNotSet
		}

		// For local containers, use the configured target URI
		if t.targetURI == "" {
			return fmt.Errorf("target URI not set for HTTP transport")
		}
		targetURI = t.targetURI
		//nolint:gosec // G706: logging proxy port and target URI from config
		slog.Debug("setting up transparent proxy to forward to target",
			"port", t.proxyPort, "target", targetURI)
	}

	// Create middlewares slice
	var middlewares []types.NamedMiddleware

	// Add the transport's existing middlewares
	middlewares = append(middlewares, t.middlewares...)

	isRemote := t.remoteURL != ""

	// Add OAuth token injection middleware for remote authentication if we have a token source.
	// Skip if token exchange is configured (it handles its own Authorization header injection).
	if isRemote && t.tokenSource != nil && !hasTokenExchangeMiddleware(t.middlewares) {
		tokenMiddleware := t.createTokenInjectionMiddleware()
		middlewares = append(middlewares, types.NamedMiddleware{
			Name:     "oauth-token-injection",
			Function: tokenMiddleware,
		})
	}

	// Determine whether to enable health checks based on workload type
	enableHealthCheck := shouldEnableHealthCheck(isRemote)

	// Create the transparent proxy
	t.proxy = transparent.NewTransparentProxy(
		t.host,
		t.proxyPort,
		targetURI,
		t.prometheusHandler,
		t.authInfoHandler,
		t.prefixHandlers,
		enableHealthCheck,
		isRemote,
		string(t.transportType),
		t.onHealthCheckFailed,
		t.onUnauthorizedResponse,
		t.endpointPrefix,
		t.trustProxyHeaders,
		middlewares...)
	if err := t.proxy.Start(ctx); err != nil {
		return err
	}

	//nolint:gosec // G706: logging container name and port from config
	slog.Debug("http transport started",
		"container", t.containerName, "port", t.proxyPort)

	// For remote MCP servers, we don't need container monitoring
	if isRemote {
		return nil
	}

	// Create a container monitor
	monitorRuntime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container monitor: %w", err)
	}
	t.monitorRuntime = monitorRuntime // Store for reconnection
	t.monitor = container.NewMonitor(monitorRuntime, t.containerName)

	// Start monitoring the container
	t.errorCh, err = t.monitor.StartMonitoring(ctx)
	if err != nil {
		return fmt.Errorf("failed to start container monitoring: %w", err)
	}

	// Start a goroutine to handle container exit
	go t.handleContainerExit(ctx)

	return nil
}

// Stop gracefully shuts down the transport and the container.
func (t *HTTPTransport) Stop(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Signal shutdown (guard against double-close if Stop is called
	// both from handleContainerExit and externally by the runner)
	select {
	case <-t.shutdownCh:
		// Already closed/stopping
		return nil
	default:
		close(t.shutdownCh)
	}

	// For remote MCP servers, we don't need container monitoring
	if t.remoteURL == "" {
		// Stop the monitor if it's running
		if t.monitor != nil {
			t.monitor.StopMonitoring()
			t.monitor = nil
		}

		// Stop the container if deployer is available
		if t.deployer != nil && t.containerName != "" {
			if err := t.deployer.StopWorkload(ctx, t.containerName); err != nil {
				return fmt.Errorf("failed to stop workload: %w", err)
			}
		}
	}

	// Stop the transparent proxy
	if t.proxy != nil {
		if err := t.proxy.Stop(ctx); err != nil {
			slog.Warn("failed to stop proxy", "error", err)
		}
	}

	return nil
}

// handleContainerExit handles container exit events.
// It loops to support reconnecting the monitor when a container is restarted
// by Docker (e.g., via restart policy) rather than truly exiting.
func (t *HTTPTransport) handleContainerExit(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.shutdownCh:
			return
		case err := <-t.errorCh:
			t.exitErrMutex.Lock()
			t.containerExitErr = err
			t.exitErrMutex.Unlock()

			if errors.Is(err, rt.ErrContainerRestarted) {
				//nolint:gosec // G706: logging container name from config
				slog.Debug("container was restarted by Docker, reconnecting monitor",
					"container", t.containerName)
				if reconnectErr := t.reconnectMonitor(ctx); reconnectErr != nil {
					//nolint:gosec // G706: logging container name from config
					slog.Error("failed to reconnect monitor, stopping transport",
						"container", t.containerName, "error", reconnectErr)
				} else {
					t.exitErrMutex.Lock()
					t.containerExitErr = nil
					t.exitErrMutex.Unlock()
					continue
				}
			}

			//nolint:gosec // G706: logging container name from config
			slog.Warn("container exited", "container", t.containerName, "error", err)

			// Check if container was removed (not just exited) using typed error
			if errors.Is(err, rt.ErrContainerRemoved) {
				//nolint:gosec // G706: logging container name from config
				slog.Debug("container was removed, stopping proxy and cleaning up",
					"container", t.containerName)
			} else {
				//nolint:gosec // G706: logging container name from config
				slog.Debug("container exited, will attempt automatic restart",
					"container", t.containerName)
			}

			// Stop the transport when the container exits/removed
			if stopErr := t.Stop(ctx); stopErr != nil {
				slog.Error("error stopping transport after container exit", "error", stopErr)
			}
			return
		}
	}
}

// reconnectMonitor stops the current monitor and starts a new one.
// This is used when a container is restarted by Docker -- the proxy keeps running
// but the monitor needs to track the new container start time.
func (t *HTTPTransport) reconnectMonitor(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Stop the old monitor (safe even if goroutine already returned)
	if t.monitor != nil {
		t.monitor.StopMonitoring()
	}

	// Create a new monitor that records the current (post-restart) start time
	t.monitor = container.NewMonitor(t.monitorRuntime, t.containerName)

	// Start monitoring — errorCh is reassigned here, which is safe because
	// handleContainerExit (the only reader) runs on the same goroutine and
	// will see the new channel when it re-enters the select after continue.
	var err error
	t.errorCh, err = t.monitor.StartMonitoring(ctx)
	if err != nil {
		return fmt.Errorf("failed to restart container monitoring: %w", err)
	}

	return nil
}

// ShouldRestart returns true if the container exited and should be restarted.
// Returns false if the container was removed (intentionally deleted) or
// restarted by Docker (already running, no ToolHive restart needed).
func (t *HTTPTransport) ShouldRestart() bool {
	t.exitErrMutex.Lock()
	defer t.exitErrMutex.Unlock()

	if t.containerExitErr == nil {
		return false // No exit error, normal shutdown
	}

	// Don't restart if container was removed or restarted by Docker (use typed error check)
	return !errors.Is(t.containerExitErr, rt.ErrContainerRemoved) &&
		!errors.Is(t.containerExitErr, rt.ErrContainerRestarted)
}

// IsRunning checks if the transport is currently running.
// It checks both the transport's shutdown channel and the proxy's running state.
// This ensures that if the proxy stops (e.g., due to health check failure),
// the transport is also reported as not running, allowing the runner to exit cleanly.
func (t *HTTPTransport) IsRunning() (bool, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Check if the shutdown channel is closed
	select {
	case <-t.shutdownCh:
		return false, nil
	default:
		// Also check if the proxy is still running (handles health check failure case)
		// When health checks fail, the proxy stops itself but the transport's
		// shutdownCh may not be closed, causing the runner to hang as a zombie process.
		proxyRunning := true
		var err error
		if t.proxy != nil {
			proxyRunning, err = t.proxy.IsRunning()
			if err != nil {
				return false, err
			}
		}
		return proxyRunning, nil
	}
}

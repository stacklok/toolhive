// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transport provides utilities for handling different transport modes
// for communication between the client and MCP server, including stdio transport
// with automatic re-attachment on Docker/container restarts.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/exp/jsonrpc2"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	transporterrors "github.com/stacklok/toolhive/pkg/transport/errors"
	"github.com/stacklok/toolhive/pkg/transport/proxy/httpsse"
	"github.com/stacklok/toolhive/pkg/transport/proxy/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// Retry configuration constants
	// defaultMaxRetries is the maximum number of re-attachment attempts after a connection loss.
	// Set to 10 to allow sufficient time for Docker/Rancher Desktop to restart (~5 minutes with backoff).
	defaultMaxRetries = 10

	// defaultInitialRetryDelay is the starting delay for exponential backoff.
	// Starts at 2 seconds to quickly recover from transient issues without overwhelming the system.
	defaultInitialRetryDelay = 2 * time.Second

	// defaultMaxRetryDelay caps the maximum delay between retry attempts.
	// Set to 30 seconds to balance between responsiveness and resource usage during extended outages.
	defaultMaxRetryDelay = 30 * time.Second

	// shutdownTimeout is the maximum time to wait for graceful shutdown operations.
	shutdownTimeout = 30 * time.Second
)

// StdioTransport implements the Transport interface using standard input/output.
// It acts as a proxy between the MCP client and the container's stdin/stdout.
type StdioTransport struct {
	host              string
	proxyPort         int
	containerName     string
	deployer          rt.Deployer
	debug             bool
	middlewares       []types.NamedMiddleware
	prometheusHandler http.Handler
	trustProxyHeaders bool

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Channels for communication
	shutdownCh chan struct{}
	errorCh    <-chan error

	// Proxy (SSE or Streamable HTTP)
	httpProxy types.Proxy
	proxyMode types.ProxyMode

	// Container I/O
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// Container monitor
	monitor rt.Monitor

	// Container exit error (for determining if restart is needed)
	containerExitErr error
	exitErrMutex     sync.Mutex

	// Retry configuration (for testing)
	retryConfig *retryConfig
}

// retryConfig holds configuration for retry behavior
type retryConfig struct {
	maxRetries   int
	initialDelay time.Duration
	maxDelay     time.Duration
}

// defaultRetryConfig returns the default retry configuration
func defaultRetryConfig() *retryConfig {
	return &retryConfig{
		maxRetries:   defaultMaxRetries,
		initialDelay: defaultInitialRetryDelay,
		maxDelay:     defaultMaxRetryDelay,
	}
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(
	host string,
	proxyPort int,
	deployer rt.Deployer,
	debug bool,
	trustProxyHeaders bool,
	prometheusHandler http.Handler,
	middlewares ...types.NamedMiddleware,
) *StdioTransport {
	return &StdioTransport{
		host:              host,
		proxyPort:         proxyPort,
		deployer:          deployer,
		debug:             debug,
		trustProxyHeaders: trustProxyHeaders,
		middlewares:       middlewares,
		prometheusHandler: prometheusHandler,
		shutdownCh:        make(chan struct{}),
		proxyMode:         types.ProxyModeSSE, // default to SSE for backward compatibility
		retryConfig:       defaultRetryConfig(),
	}
}

// SetProxyMode allows configuring the proxy mode (SSE or Streamable HTTP)
func (t *StdioTransport) SetProxyMode(mode types.ProxyMode) {
	t.proxyMode = mode
}

// Mode returns the transport mode.
func (*StdioTransport) Mode() types.TransportType {
	return types.TransportTypeStdio
}

// ProxyPort returns the proxy port used by the transport.
func (t *StdioTransport) ProxyPort() int {
	return t.proxyPort
}

// setContainerName configures the transport with the container name.
// This is an unexported method used by the option pattern.
func (t *StdioTransport) setContainerName(containerName string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.containerName = containerName
}

// setTargetURI configures the transport with the target URI for proxying.
// For stdio transport, this is a no-op as stdio doesn't use a target URI.
// This is an unexported method used by the option pattern.
func (*StdioTransport) setTargetURI(_ string) {
	// No-op for stdio transport
}

// Start initializes the transport and begins processing messages.
// The transport is responsible for attaching to the container.
func (t *StdioTransport) Start(ctx context.Context) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.containerName == "" {
		return transporterrors.ErrContainerNameNotSet
	}

	if t.deployer == nil {
		return fmt.Errorf("container deployer not set")
	}

	// Attach to the container
	var err error
	t.stdin, t.stdout, err = t.deployer.AttachToWorkload(ctx, t.containerName)
	if err != nil {
		return fmt.Errorf("failed to attach to container: %w", err)
	}

	// Create and start the correct proxy with middlewares
	switch t.proxyMode {
	case types.ProxyModeStreamableHTTP:
		t.httpProxy = streamable.NewHTTPProxy(t.host, t.proxyPort, t.prometheusHandler, t.middlewares...)
		if err := t.httpProxy.Start(ctx); err != nil {
			return err
		}
		slog.Debug("streamable HTTP proxy started, processing messages")
	case types.ProxyModeSSE:
		t.httpProxy = httpsse.NewHTTPSSEProxy(
			t.host,
			t.proxyPort,
			t.trustProxyHeaders,
			t.prometheusHandler,
			t.middlewares...,
		)
		if err := t.httpProxy.Start(ctx); err != nil {
			return err
		}
		slog.Debug("http SSE proxy started, processing messages")
	default:
		return fmt.Errorf("unsupported proxy mode: %v", t.proxyMode)
	}

	// Start processing messages in a goroutine
	go t.processMessages(ctx, t.stdin, t.stdout)

	// Create a container monitor
	monitorRuntime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container monitor: %w", err)
	}
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
func (t *StdioTransport) Stop(ctx context.Context) error {
	// First check if the transport is already stopped without locking
	// to avoid deadlocks if Stop is called from multiple goroutines
	select {
	case <-t.shutdownCh:
		// Channel is already closed, transport is already stopping or stopped
		// Just return without doing anything else
		return nil
	default:
		// Channel is still open, proceed with stopping
	}

	// Now lock the mutex for the actual stopping process
	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Check again after locking to handle race conditions
	select {
	case <-t.shutdownCh:
		// Channel was closed between our first check and acquiring the lock
		return nil
	default:
		// Channel is still open, close it to signal shutdown
		close(t.shutdownCh)
	}

	// Stop the monitor if it's running and we haven't already stopped it
	if t.monitor != nil {
		t.monitor.StopMonitoring()
		t.monitor = nil
	}

	// Stop the HTTP proxy
	if t.httpProxy != nil {
		if err := t.httpProxy.Stop(ctx); err != nil {
			slog.Warn("failed to stop HTTP proxy", "error", err)
		}
	}

	// Close stdin and stdout if they're open
	if t.stdin != nil {
		if err := t.stdin.Close(); err != nil {
			slog.Warn("failed to close stdin", "error", err)
		}
		t.stdin = nil
	}

	// Stop the container if deployer is available and we haven't already stopped it
	if t.deployer != nil && t.containerName != "" {
		// Check if the workload is still running before trying to stop it
		running, err := t.deployer.IsWorkloadRunning(ctx, t.containerName)
		if err != nil {
			// If there's an error checking the workload status, it might be gone already
			slog.Warn("failed to check workload status", "error", err)
		} else if running {
			// Only try to stop the workload if it's still running
			if err := t.deployer.StopWorkload(ctx, t.containerName); err != nil {
				slog.Warn("failed to stop workload", "error", err)
			}
		}
	}

	return nil
}

// IsRunning checks if the transport is currently running.
func (t *StdioTransport) IsRunning() (bool, error) {
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

// SetRemoteURL sets the remote URL for the MCP server.
// This is a no-op for stdio transport as it doesn't support remote servers.
func (*StdioTransport) SetRemoteURL(_ string) {
	// No-op: stdio transport doesn't support remote servers
}

// SetTokenSource sets the OAuth token source for remote authentication.
// This is a no-op for stdio transport as it doesn't support remote authentication.
func (*StdioTransport) SetTokenSource(_ oauth2.TokenSource) {
	// No-op: stdio transport doesn't support remote authentication
}

// SetOnHealthCheckFailed sets the callback for health check failures.
// This is a no-op for stdio transport as it doesn't support health checks.
func (*StdioTransport) SetOnHealthCheckFailed(_ types.HealthCheckFailedCallback) {
	// No-op: stdio transport doesn't support health checks
}

// SetOnUnauthorizedResponse sets the callback for 401 Unauthorized responses.
// This is a no-op for stdio transport as it doesn't handle HTTP responses.
func (*StdioTransport) SetOnUnauthorizedResponse(_ types.UnauthorizedResponseCallback) {
	// No-op: stdio transport doesn't handle HTTP responses
}

// isDockerSocketError checks if an error indicates Docker socket unavailability using typed error detection
func isDockerSocketError(err error) bool {
	if err == nil {
		return false
	}

	// Check for EOF errors
	if errors.Is(err, io.EOF) {
		return true
	}

	// Check for network-related errors
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		// Connection refused typically indicates Docker daemon is not running
		return true
	}

	// Fallback to string matching for errors that don't implement standard interfaces
	// This handles Docker SDK errors that may not wrap standard error types
	errStr := err.Error()
	return strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "Cannot connect to the Docker daemon")
}

// processMessages handles the message exchange between the client and container.
func (t *StdioTransport) processMessages(ctx context.Context, _ io.WriteCloser, stdout io.ReadCloser) {
	// Create a context that will be canceled when shutdown is signaled
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Monitor for shutdown signal
	go func() {
		select {
		case <-t.shutdownCh:
			cancel()
		case <-ctx.Done():
			// Context was canceled elsewhere
		}
	}()

	// Start a goroutine to read from stdout
	go t.processStdout(ctx, stdout)
	// Process incoming messages and send them to the container
	messageCh := t.httpProxy.GetMessageChannel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-messageCh:
			slog.Debug("processing incoming message and sending to container")
			// Use t.stdin instead of parameter so it uses the current stdin after re-attachment
			t.mutex.Lock()
			currentStdin := t.stdin
			t.mutex.Unlock()
			if err := t.sendMessageToContainer(ctx, currentStdin, msg); err != nil {
				slog.Error("error sending message to container", "error", err)
			}
			slog.Debug("message processed")
		}
	}
}

// attemptReattachment tries to re-attach to a container that has lost its stdout connection.
// Returns true if re-attachment was successful, false otherwise.
func (t *StdioTransport) attemptReattachment(ctx context.Context, stdout io.ReadCloser) bool {
	if t.deployer == nil || t.containerName == "" {
		return false
	}

	// Create an exponential backoff with the configured parameters
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = t.retryConfig.initialDelay
	expBackoff.MaxInterval = t.retryConfig.maxDelay
	// Reset to allow unlimited elapsed time - we control retries via MaxTries
	expBackoff.Reset()

	var attemptCount int
	maxRetries := t.retryConfig.maxRetries

	operation := func() (any, error) {
		attemptCount++

		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return nil, backoff.Permanent(ctx.Err())
		default:
		}

		running, checkErr := t.deployer.IsWorkloadRunning(ctx, t.containerName)
		if checkErr != nil {
			// Check if error is due to Docker being unavailable
			if isDockerSocketError(checkErr) {
				slog.Warn("docker socket unavailable, will retry",
					"attempt", attemptCount, "max_retries", maxRetries, "error", checkErr)
				return nil, checkErr // Retry
			}
			slog.Warn("error checking if container is running",
				"attempt", attemptCount, "max_retries", maxRetries, "error", checkErr)
			return nil, checkErr // Retry
		}

		if !running {
			slog.Info("container not running",
				"attempt", attemptCount, "max_retries", maxRetries)
			return nil, backoff.Permanent(fmt.Errorf("container not running"))
		}

		slog.Warn("container is still running after stdout EOF, attempting to re-attach")

		// Try to re-attach to the container
		newStdin, newStdout, attachErr := t.deployer.AttachToWorkload(ctx, t.containerName)
		if attachErr != nil {
			slog.Error("failed to re-attach to container",
				"attempt", attemptCount, "max_retries", maxRetries, "error", attachErr)
			return nil, attachErr // Retry
		}

		slog.Debug("successfully re-attached to container, restarting message processing")

		// Close old stdout and log any errors
		if closeErr := stdout.Close(); closeErr != nil {
			slog.Warn("error closing old stdout during re-attachment", "error", closeErr)
		}

		// Update stdio references with proper synchronization
		t.mutex.Lock()
		t.stdin = newStdin
		t.stdout = newStdout
		t.mutex.Unlock()

		// Start ONLY the stdout reader, not the full processMessages
		// The existing processMessages goroutine is still running and handling stdin
		go t.processStdout(ctx, newStdout)
		slog.Debug("restarted stdout processing with new pipe")
		return nil, nil // Success
	}

	// Execute the operation with retry
	// Safe conversion: maxRetries is constrained by defaultMaxRetries constant (10)
	_, err := backoff.Retry(ctx, operation,
		backoff.WithBackOff(expBackoff),
		backoff.WithMaxTries(uint(maxRetries)), // #nosec G115
		backoff.WithNotify(func(_ error, duration time.Duration) {
			slog.Info("retry attempt",
				"attempt", attemptCount+1, "max_retries", maxRetries, "after", duration)
		}),
	)

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("re-attachment cancelled or timed out", "error", err)
		} else {
			slog.Warn("failed to re-attach after all retry attempts")
		}
		return false
	}

	return true
}

// processStdout reads from the container's stdout and processes JSON-RPC messages.
func (t *StdioTransport) processStdout(ctx context.Context, stdout io.ReadCloser) {
	// Create a buffer for accumulating data
	var buffer bytes.Buffer

	// Create a buffer for reading
	readBuffer := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Read data from stdout
			n, err := stdout.Read(readBuffer)
			if err != nil {
				if err == io.EOF {
					slog.Warn("container stdout closed, checking if container is still running")

					// Try to re-attach to the container
					if t.attemptReattachment(ctx, stdout) {
						return
					}

					slog.Debug("container stdout closed, exiting read loop")
				} else {
					slog.Error("error reading from container stdout", "error", err)
				}
				return
			}

			if n > 0 {
				// Write the data to the buffer
				buffer.Write(readBuffer[:n])

				// Process the buffer
				t.processBuffer(ctx, &buffer)
			}
		}
	}
}

// processBuffer processes the accumulated data in the buffer.
func (t *StdioTransport) processBuffer(ctx context.Context, buffer *bytes.Buffer) {
	// Process complete lines
	for {
		line, err := buffer.ReadString('\n')
		if err == io.EOF {
			// No complete line found, put the data back in the buffer
			buffer.WriteString(line)
			break
		}

		// Verify if new line character is present as last character
		// If so, remove it
		if len(line) > 0 && line[len(line)-1] == '\n' {
			// Remove the trailing newline
			line = line[:len(line)-1]
		}
		t.parseAndForwardJSONRPC(ctx, line)
	}
}

// sanitizeJSONString extracts the first valid JSON object from a string
func sanitizeJSONString(input string) string {
	return sanitizeBinaryString(input)
}

// sanitizeBinaryString removes all non-JSON characters and whitespace from a string
func sanitizeBinaryString(input string) string {
	// Find the first opening brace
	startIdx := strings.Index(input, "{")
	if startIdx == -1 {
		return "" // No JSON object found
	}

	// Find the last closing brace
	endIdx := strings.LastIndex(input, "}")
	if endIdx == -1 || endIdx < startIdx {
		return "" // No valid JSON object found
	}

	// Extract just the JSON object, discarding everything else
	jsonObj := input[startIdx : endIdx+1]

	// Remove all whitespace, control characters, and replacement characters
	var buffer bytes.Buffer

	for _, r := range jsonObj {
		// Skip replacement character (U+FFFD) and non-printable characters
		if r != '\uFFFD' && (unicode.IsPrint(r) || isSpace(r)) {
			buffer.WriteRune(r)
		}
	}

	return buffer.String()
}

// isSpace reports whether r is a space character as defined by JSON.
// These are the valid space characters in JSON:
//   - ' ' (U+0020, SPACE)
//   - '\t' (U+0009, HORIZONTAL TAB)
//   - '\n' (U+000A, LINE FEED)
//   - '\r' (U+000D, CARRIAGE RETURN)
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// parseAndForwardJSONRPC parses a JSON-RPC message and forwards it.
func (t *StdioTransport) parseAndForwardJSONRPC(ctx context.Context, line string) {
	//nolint:gosec // G706: logging raw JSON-RPC data from container stdout
	slog.Debug("JSON-RPC raw", "line", line)
	jsonData := sanitizeJSONString(line)
	//nolint:gosec // G706: logging sanitized JSON data from container stdout
	slog.Debug("Sanitized JSON", "data", jsonData)

	if jsonData == "" || jsonData == "[]" {
		return
	}

	// Try to parse the JSON
	msg, err := jsonrpc2.DecodeMessage([]byte(jsonData))
	if err != nil {
		slog.Error("error parsing JSON-RPC message", "error", err)
		return
	}

	slog.Debug("received JSON-RPC message", "type", fmt.Sprintf("%T", msg))

	if err := t.httpProxy.ForwardResponseToClients(ctx, msg); err != nil {
		if t.proxyMode == types.ProxyModeStreamableHTTP {
			slog.Error("error forwarding to streamable-http client", "error", err)
		} else {
			slog.Error("error forwarding to SSE clients", "error", err)
		}
	}
}

// sendMessageToContainer sends a JSON-RPC message to the container.
func (*StdioTransport) sendMessageToContainer(_ context.Context, stdin io.Writer, msg jsonrpc2.Message) error {
	// Serialize the message
	data, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to encode JSON-RPC message: %w", err)
	}

	// Add newline
	data = append(data, '\n')

	// Write to stdin
	slog.Debug("writing to container stdin")
	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("failed to write to container stdin: %w", err)
	}
	slog.Debug("wrote to container stdin")

	return nil
}

// handleContainerExit handles container exit events.
func (t *StdioTransport) handleContainerExit(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case err, ok := <-t.errorCh:
		// Check if the channel is closed
		if !ok {
			slog.Debug("container monitor channel closed",
				"container", t.containerName)
			return
		}

		// Store the exit error so runner can check if restart is needed
		t.exitErrMutex.Lock()
		t.containerExitErr = err
		t.exitErrMutex.Unlock()

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

		// Check if the transport is already stopped before trying to stop it
		select {
		case <-t.shutdownCh:
			// Transport is already stopping or stopped
			slog.Debug("transport is already stopping or stopped",
				"container", t.containerName)
			return
		default:
			// Transport is still running, stop it
			// Create a context with timeout for stopping the transport
			stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()

			if stopErr := t.Stop(stopCtx); stopErr != nil {
				slog.Error("error stopping transport after container exit", "error", stopErr)
			}
		}
	}
}

// ShouldRestart returns true if the container exited and should be restarted.
// Returns false if the container was removed (intentionally deleted) or
// restarted by Docker (already running, no ToolHive restart needed).
func (t *StdioTransport) ShouldRestart() bool {
	t.exitErrMutex.Lock()
	defer t.exitErrMutex.Unlock()

	if t.containerExitErr == nil {
		return false // No exit error, normal shutdown
	}

	// Don't restart if container was removed or restarted by Docker (use typed error check)
	return !errors.Is(t.containerExitErr, rt.ErrContainerRemoved) &&
		!errors.Is(t.containerExitErr, rt.ErrContainerRestarted)
}

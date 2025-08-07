// Package types provides common types and interfaces for the transport package
// used in communication between the client and MCP server.
package types

import (
	"context"
	"encoding/json"
	"net/http"

	"golang.org/x/exp/jsonrpc2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/transport/errors"
)

// MiddlewareFunction is a function that wraps an http.Handler with additional functionality.
type MiddlewareFunction func(http.Handler) http.Handler

// Middleware defines a middleware interceptor and a close method.
type Middleware interface {
	// Handler returns the middleware function used by the proxy.
	Handler() MiddlewareFunction
	// Close cleans up any resources used by the middleware.
	Close() error
}

// MiddlewareConfig represents the configuration for a middleware.
// This is stored in the run config, and is used to instantiate the middleware.
type MiddlewareConfig struct {
	// Type is a string representing the middleware type.
	Type string `json:"type"`
	// Parameters is a JSON object containing the middleware parameters.
	// It is stored as a raw message to allow flexible parameter types.
	Parameters json.RawMessage `json:"parameters" swaggertype:"object"`
}

// NewMiddlewareConfig creates a new MiddlewareConfig with the given type and parameters.
// The parameters are marshaled to JSON and stored in the Parameters field.
func NewMiddlewareConfig(middlewareType string, parameters any) (*MiddlewareConfig, error) {
	// Marshal the parameters to JSON
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		return nil, err
	}

	return &MiddlewareConfig{
		Type:       middlewareType,
		Parameters: paramsJSON,
	}, nil
}

// MiddlewareFactory is a function that creates a Middleware instance based on the provided configuration.
type MiddlewareFactory func(config *MiddlewareConfig) (Middleware, error)

// Transport defines the interface for MCP transport implementations.
// It provides methods for handling communication between the client and server.
type Transport interface {
	// Mode returns the transport mode.
	Mode() TransportType

	// ProxyPort returns the port used by the transport.
	ProxyPort() int

	// Setup prepares the transport for use.
	// The runtime parameter provides access to container operations.
	// The permissionProfile is used to configure container permissions.
	// The k8sPodTemplatePatch is a JSON string to patch the Kubernetes pod template.
	Setup(ctx context.Context, runtime rt.Deployer, containerName string, image string, cmdArgs []string,
		envVars, labels map[string]string, permissionProfile *permissions.Profile, k8sPodTemplatePatch string,
		isolateNetwork bool, ignoreConfig *ignore.Config) error

	// Start initializes the transport and begins processing messages.
	// The transport is responsible for container operations like attaching to stdin/stdout if needed.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the transport.
	Stop(ctx context.Context) error

	// IsRunning checks if the transport is currently running.
	IsRunning(ctx context.Context) (bool, error)
}

// TransportType represents the type of transport to use.
//
//nolint:revive // Intentionally named TransportType despite package name
type TransportType string

const (
	// TransportTypeStdio represents the stdio transport.
	TransportTypeStdio TransportType = "stdio"

	// TransportTypeSSE represents the SSE transport.
	TransportTypeSSE TransportType = "sse"

	// TransportTypeStreamableHTTP represents the streamable HTTP transport.
	TransportTypeStreamableHTTP TransportType = "streamable-http"

	// TransportTypeInspector represents the transport mode for MCP Inspector.
	TransportTypeInspector TransportType = "inspector"
)

// String returns the string representation of the transport type.
func (t TransportType) String() string {
	return string(t)
}

// ParseTransportType parses a string into a transport type.
func ParseTransportType(s string) (TransportType, error) {
	switch s {
	case "stdio", "STDIO":
		return TransportTypeStdio, nil
	case "sse", "SSE":
		return TransportTypeSSE, nil
	case "streamable-http", "STREAMABLE-HTTP":
		return TransportTypeStreamableHTTP, nil
	case "inspector", "INSPECTOR":
		return TransportTypeInspector, nil
	default:
		return "", errors.ErrUnsupportedTransport
	}
}

// Proxy defines the interface for proxying messages between clients and destinations.
type Proxy interface {
	// Start starts the proxy.
	Start(ctx context.Context) error

	// Stop stops the proxy.
	Stop(ctx context.Context) error

	// GetMessageChannel returns the channel for messages to/from the destination.
	GetMessageChannel() chan jsonrpc2.Message

	// SendMessageToDestination sends a message to the destination.
	SendMessageToDestination(msg jsonrpc2.Message) error

	// ForwardResponseToClients forwards a response from the destination to clients.
	ForwardResponseToClients(ctx context.Context, msg jsonrpc2.Message) error
}

// Config contains configuration options for a transport.
type Config struct {
	// Type is the type of transport to use.
	Type TransportType

	// ProxyPort is the port to use for network transports (host port).
	ProxyPort int

	// TargetPort is the port that the container will expose (container port).
	// This is only applicable to SSE transport.
	TargetPort int

	// TargetHost is the host to forward traffic to.
	// This is only applicable to SSE transport.
	TargetHost string

	// Host is the host to use for network transports.
	Host string

	// Deployer is the container runtime to use.
	// This is used for container operations like creating, starting, and attaching.
	Deployer rt.Deployer

	// Debug indicates whether debug mode is enabled.
	// If debug mode is enabled, containers will not be removed when stopped.
	Debug bool

	// Middlewares is a list of middleware functions to apply to the transport.
	// These are applied in order, with the first middleware being the outermost wrapper.
	Middlewares []MiddlewareFunction

	// PrometheusHandler is an optional HTTP handler for Prometheus metrics endpoint.
	// If provided, it will be exposed at /metrics on the transport's HTTP server.
	PrometheusHandler http.Handler

	// ProxyMode is the proxy mode for stdio transport ("sse" or "streamable-http")
	ProxyMode ProxyMode
}

// ProxyMode represents the proxy mode for stdio transport.
type ProxyMode string

const (
	// ProxyModeSSE is the proxy mode for SSE.
	ProxyModeSSE ProxyMode = "sse"
	// ProxyModeStreamableHTTP is the proxy mode for streamable HTTP.
	ProxyModeStreamableHTTP ProxyMode = "streamable-http"
)

// IsValidProxyMode returns true if the given mode is a valid ProxyMode.
func IsValidProxyMode(mode string) bool {
	return mode == ProxyModeSSE.String() || mode == ProxyModeStreamableHTTP.String()
}

func (p ProxyMode) String() string {
	return string(p)
}

package transport

import (
	"context"

	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/permissions"
)

// Transport defines the interface for MCP transport implementations.
// It provides methods for handling communication between the client and server.
type Transport interface {
	// Mode returns the transport mode.
	Mode() TransportType

	// Port returns the port used by the transport.
	Port() int

	// Setup prepares the transport for use.
	// The runtime parameter provides access to container operations.
	// The permissionProfile is used to configure container permissions.
	Setup(ctx context.Context, runtime container.Runtime, containerName string, image string, cmdArgs []string,
		envVars, labels map[string]string, permissionProfile *permissions.Profile) error

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
	default:
		return "", ErrUnsupportedTransport
	}
}

// Config contains configuration options for a transport.
type Config struct {
	// Type is the type of transport to use.
	Type TransportType

	// Port is the port to use for network transports (host port).
	Port int

	// TargetPort is the port that the container will expose (container port).
	// This is only applicable to SSE transport.
	TargetPort int

	// Host is the host to use for network transports.
	Host string

	// Runtime is the container runtime to use.
	// This is used for container operations like creating, starting, and attaching.
	Runtime container.Runtime

	// Debug indicates whether debug mode is enabled.
	// If debug mode is enabled, containers will not be removed when stopped.
	Debug bool

	// Middlewares is a list of middleware functions to apply to the transport.
	// These are applied in order, with the first middleware being the outermost wrapper.
	Middlewares []Middleware
}

// Factory creates transports
type Factory struct{}

// NewFactory creates a new transport factory
func NewFactory() *Factory {
	return &Factory{}
}

// Create creates a transport based on the provided configuration
func (*Factory) Create(config Config) (Transport, error) {
	switch config.Type {
	case TransportTypeStdio:
		return NewStdioTransport(config.Port, config.Runtime, config.Debug, config.Middlewares...), nil
	case TransportTypeSSE:
		return NewSSETransport(config.Host, config.Port, config.TargetPort, config.Runtime, config.Debug, config.Middlewares...), nil
	default:
		return nil, ErrUnsupportedTransport
	}
}

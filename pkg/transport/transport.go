package transport

import (
	"context"
	"io"
)

// Transport defines the interface for MCP transport implementations.
// It provides methods for handling communication between the client and server.
type Transport interface {
	// Mode returns the transport mode.
	Mode() TransportType

	// Port returns the port used by the transport.
	Port() int

	// Setup prepares the transport for use with a specific container.
	Setup(ctx context.Context, containerID, containerName string, envVars map[string]string) error

	// Start initializes the transport and begins processing messages.
	// For STDIO transport, stdin and stdout are provided by the caller and are already attached to the container.
	Start(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) error

	// Stop gracefully shuts down the transport.
	Stop(ctx context.Context) error

	// IsRunning checks if the transport is currently running.
	IsRunning(ctx context.Context) (bool, error)

	// GetReader returns a reader for receiving messages from the transport.
	GetReader() io.Reader

	// GetWriter returns a writer for sending messages to the transport.
	GetWriter() io.Writer
}

// TransportType represents the type of transport to use.
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
}

// Factory creates transports
type Factory struct{}

// NewFactory creates a new transport factory
func NewFactory() *Factory {
	return &Factory{}
}

// Create creates a transport based on the provided configuration
func (f *Factory) Create(config Config) (Transport, error) {
	switch config.Type {
	case TransportTypeStdio:
		return NewStdioTransport(config.Port), nil
	case TransportTypeSSE:
		return NewSSETransport(config.Host, config.Port, config.TargetPort), nil
	default:
		return nil, ErrUnsupportedTransport
	}
}

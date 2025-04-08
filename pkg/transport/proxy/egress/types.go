// Package egress provides a proxy implementation for controlling outbound traffic
// from containers.
package egress

import (
	"context"
	"net/http"
)

// Proxy defines the interface for an egress proxy
type Proxy interface {
	// Start starts the egress proxy
	Start(ctx context.Context) error

	// Stop stops the egress proxy
	Stop(ctx context.Context) error

	// ServeHTTP handles HTTP requests (implements http.Handler)
	ServeHTTP(w http.ResponseWriter, r *http.Request)

	// GetPort returns the port the proxy is listening on
	GetPort() int

	// GetHost returns the host the proxy is bound to
	GetHost() string
}

// Config represents configuration for the egress proxy
type Config struct {
	// Port is the port to listen on (0 for auto-assign)
	Port int

	// Host is the host to bind to (empty for all interfaces)
	Host string

	// AllowedHosts is a list of hosts that are allowed for outbound connections
	AllowedHosts []string

	// AllowedPorts is a list of ports that are allowed for outbound connections
	AllowedPorts []int

	// AllowedTransports is a list of transport protocols that are allowed
	AllowedTransports []string

	// Name is a unique name for this proxy instance
	Name string
}

// NewConfig creates a new configuration with specified values
func NewConfig(name string, port int, host string) *Config {
	return &Config{
		Port: port,
		Host: host,
		Name: name,
	}
}

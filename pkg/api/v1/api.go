// Package v1 provides version 1 of the ToolHive API.
package v1

import (
	"context"
	"io"
)

// Client is the main entry point for the ToolHive API.
// It provides access to all the API interfaces.
type Client interface {
	// Server returns the ServerAPI for managing MCP servers.
	Server() ServerAPI
	// Secret returns the SecretAPI for managing secrets.
	Secret() SecretAPI
	// Config returns the ConfigAPI for managing configuration.
	Config() ConfigAPI
	// Version returns the VersionAPI for getting version information.
	Version() VersionAPI
	// Close releases any resources held by the client.
	Close() error
}

// ServerAPI provides methods for managing MCP servers.
type ServerAPI interface {
	// List returns a list of running MCP servers.
	List(ctx context.Context, opts *ListOptions) (*ServerList, error)
	// Get returns information about a specific running MCP server.
	Get(ctx context.Context, name string) (*Server, error)
	// Run runs an MCP server with the provided options.
	Run(ctx context.Context, serverOrImage string, opts *RunOptions) (*Server, error)
	// Stop stops a running MCP server.
	Stop(ctx context.Context, name string, opts *StopOptions) error
	// Remove removes a stopped MCP server.
	Remove(ctx context.Context, name string, opts *RemoveOptions) error
	// Restart restarts a running MCP server.
	Restart(ctx context.Context, name string, opts *RestartOptions) error
	// Logs gets logs from a running MCP server.
	Logs(ctx context.Context, name string, opts *LogsOptions) error
	// Proxy proxies to a running MCP server.
	Proxy(ctx context.Context, name string, opts *ProxyOptions) error
	// Search searches for MCP servers.
	Search(ctx context.Context, query string, opts *SearchOptions) (*ServerList, error)
}

// SecretAPI provides methods for managing secrets.
type SecretAPI interface {
	// Set sets a secret.
	Set(ctx context.Context, name string, opts *SecretSetOptions) error
	// Get gets a secret.
	Get(ctx context.Context, name string, opts *SecretGetOptions) (string, error)
	// Delete deletes a secret.
	Delete(ctx context.Context, name string, opts *SecretDeleteOptions) error
	// List lists secrets.
	List(ctx context.Context, opts *SecretListOptions) (*SecretList, error)
	// ResetKeyring resets the keyring.
	ResetKeyring(ctx context.Context, opts *SecretResetKeyringOptions) error
}

// ConfigAPI provides methods for managing configuration.
type ConfigAPI interface {
	// RegisterClient registers a client.
	RegisterClient(ctx context.Context, opts *ConfigRegisterClientOptions) error
	// RemoveClient removes a client.
	RemoveClient(ctx context.Context, opts *ConfigRemoveClientOptions) error
	// ListRegisteredClients lists registered clients.
	ListRegisteredClients(ctx context.Context, opts *ConfigListRegisteredClientsOptions) (*ClientList, error)
	// AutoDiscovery configures auto-discovery.
	AutoDiscovery(ctx context.Context, opts *ConfigAutoDiscoveryOptions) error
	// SecretsProvider configures the secrets provider.
	SecretsProvider(ctx context.Context, opts *ConfigSecretsProviderOptions) error
}

// VersionAPI provides methods for getting version information.
type VersionAPI interface {
	// Get returns version information.
	Get(ctx context.Context, opts *VersionOptions) (*VersionInfo, error)
}

// LogWriter is an interface for writing logs.
type LogWriter interface {
	// Write writes a log message.
	Write(p []byte) (n int, err error)
}

// LogReader is an interface for reading logs.
type LogReader interface {
	// Read reads a log message.
	Read(p []byte) (n int, err error)
}

// LogReadWriter is an interface for reading and writing logs.
type LogReadWriter interface {
	LogReader
	LogWriter
	io.Closer
}

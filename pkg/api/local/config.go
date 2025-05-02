package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/config"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/secrets"
)

// Config is the local implementation of the api.ConfigAPI interface.
type Config struct {
	// debug indicates whether debug mode is enabled
	debug bool
}

// NewConfig creates a new local ConfigAPI with the provided debug flag.
func NewConfig(debug bool) api.ConfigAPI {
	return &Config{
		debug: debug,
	}
}

// RegisterClient registers a client.
func (c *Config) RegisterClient(_ context.Context, opts *api.ConfigRegisterClientOptions) error {
	c.logDebug("Registering client: %s", opts.Name)

	// Validate the client type
	switch opts.Name {
	case "roo-code", "cursor", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf("invalid client type: %s (valid types: roo-code, cursor, vscode, vscode-insider)", opts.Name)
	}

	// Update the config
	err := config.UpdateConfig(func(cfg *config.Config) {
		// Check if client is already registered and skip.
		for _, registeredClient := range cfg.Clients.RegisteredClients {
			if registeredClient == opts.Name {
				c.logDebug("Client %s is already registered, skipping...", opts.Name)
				return
			}
		}

		// Add the client to the registered clients list
		cfg.Clients.RegisteredClients = append(cfg.Clients.RegisteredClients, opts.Name)
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// RemoveClient removes a client.
func (c *Config) RemoveClient(_ context.Context, opts *api.ConfigRemoveClientOptions) error {
	c.logDebug("Removing client: %s", opts.Name)

	// Validate the client type
	switch opts.Name {
	case "roo-code", "cursor", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf("invalid client type: %s (valid types: roo-code, cursor, vscode, vscode-insider)", opts.Name)
	}

	// Update the config
	err := config.UpdateConfig(func(cfg *config.Config) {
		// Find and remove the client from the registered clients list
		found := false
		for i, registeredClient := range cfg.Clients.RegisteredClients {
			if registeredClient == opts.Name {
				// Remove the client by appending the slice before and after the index
				cfg.Clients.RegisteredClients = append(cfg.Clients.RegisteredClients[:i], cfg.Clients.RegisteredClients[i+1:]...)
				found = true
				break
			}
		}
		if found {
			c.logDebug("Client %s removed from registered clients.", opts.Name)
		} else {
			c.logDebug("Client %s not found in registered clients.", opts.Name)
		}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// ListRegisteredClients lists registered clients.
func (c *Config) ListRegisteredClients(_ context.Context, _ *api.ConfigListRegisteredClientsOptions) ([]string, error) {
	c.logDebug("Listing registered clients")

	// Get the current config
	cfg := config.GetConfig()

	// Return the list of registered clients
	return cfg.Clients.RegisteredClients, nil
}

// AutoDiscovery configures auto-discovery.
func (c *Config) AutoDiscovery(_ context.Context, opts *api.ConfigAutoDiscoveryOptions) error {
	c.logDebug("Configuring auto-discovery: %v", opts.Enable)

	// Update the config
	err := config.UpdateConfig(func(cfg *config.Config) {
		cfg.Clients.AutoDiscovery = opts.Enable
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// SecretsProvider configures the secrets provider.
func (c *Config) SecretsProvider(_ context.Context, opts *api.ConfigSecretsProviderOptions) error {
	c.logDebug("Configuring secrets provider: %s", opts.Provider)

	// Validate the provider type
	switch opts.Provider {
	case string(secrets.EncryptedType):
		// Valid provider type
	default:
		return fmt.Errorf("invalid secrets provider type: %s (valid types: encrypted)", opts.Provider)
	}

	// Update the config
	err := config.UpdateConfig(func(cfg *config.Config) {
		cfg.Secrets.ProviderType = opts.Provider
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// logDebug logs a debug message if debug mode is enabled
func (c *Config) logDebug(format string, args ...interface{}) {
	if c.debug {
		logger.Log.Infof(format, args...)
	}
}

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/secrets"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage application configuration",
	Long:  "The config command provides subcommands to manage application configuration settings.",
}

var listRegisteredClientsCmd = &cobra.Command{
	Use:   "list-registered-clients",
	Short: "List all registered MCP clients",
	Long:  "List all clients that are registered for MCP server configuration.",
	RunE:  listRegisteredClientsCmdFunc,
}

var secretsProviderCmd = &cobra.Command{
	Use:   "secrets-provider [provider]",
	Short: "Set the secrets provider type",
	Long: `Set the secrets provider type for storing and retrieving secrets.
Valid providers are:
  - basic: Stores secrets in an unencrypted file (not recommended for production)
  - encrypted: Stores secrets in an encrypted file using AES-256-GCM`,
	Args: cobra.ExactArgs(1),
	RunE: secretsProviderCmdFunc,
}

var autoDiscoveryCmd = &cobra.Command{
	Use:   "auto-discovery [true|false]",
	Short: "Set whether to enable auto-discovery of MCP clients",
	Long: `Set whether to enable auto-discovery and configuration of MCP clients.
When enabled, ToolHive will automatically update client configuration files
with the URLs of running MCP servers.`,
	Args: cobra.ExactArgs(1),
	RunE: autoDiscoveryCmdFunc,
}

var registerClientCmd = &cobra.Command{
	Use:   "register-client [client]",
	Short: "Register a client for MCP server configuration",
	Long: `Register a client for MCP server configuration.
Valid clients are:
  - roo-code: The RooCode extension for VSCode
  - cursor: The Cursor editor
  - vscode-insider: The Visual Studio Code Insider editor`,
	Args: cobra.ExactArgs(1),
	RunE: registerClientCmdFunc,
}

var removeClientCmd = &cobra.Command{
	Use:   "remove-client [client]",
	Short: "Remove a client from MCP server configuration",
	Long: `Remove a client from MCP server configuration.
Valid clients are:
  - roo-code: The RooCode extension for VSCode
  - cursor: The Cursor editor
  - vscode-insider: The Visual Studio Code Insider editor`,
	Args: cobra.ExactArgs(1),
	RunE: removeClientCmdFunc,
}

func init() {
	// Add config command to root command
	rootCmd.AddCommand(configCmd)

	// Add subcommands to config command
	configCmd.AddCommand(secretsProviderCmd)
	configCmd.AddCommand(autoDiscoveryCmd)
	configCmd.AddCommand(registerClientCmd)
	configCmd.AddCommand(removeClientCmd)
	configCmd.AddCommand(listRegisteredClientsCmd)
}

func secretsProviderCmdFunc(_ *cobra.Command, args []string) error {
	provider := args[0]

	// Validate the provider type
	switch provider {
	case string(secrets.BasicType), string(secrets.EncryptedType):
		// Valid provider type
	default:
		return fmt.Errorf("invalid secrets provider type: %s (valid types: basic, encrypted)", provider)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Update the secrets provider type
	cfg.Secrets.ProviderType = provider

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Log.Info(fmt.Sprintf("Secrets provider type updated to: %s", provider))
	return nil
}

func autoDiscoveryCmdFunc(_ *cobra.Command, args []string) error {
	value := args[0]

	// Validate the boolean value
	var enabled bool
	switch value {
	case "true", "1", "yes":
		enabled = true
	case "false", "0", "no":
		enabled = false
	default:
		return fmt.Errorf("invalid boolean value: %s (valid values: true, false)", value)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Update the auto-discovery setting
	cfg.Clients.AutoDiscovery = enabled

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Log.Info(fmt.Sprintf("Auto-discovery of MCP clients %s", map[bool]string{true: "enabled", false: "disabled"}[enabled]))
	return nil
}

func registerClientCmdFunc(_ *cobra.Command, args []string) error {
	client := args[0]

	// Validate the client type
	switch client {
	case "roo-code", "cursor", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf("invalid client type: %s (valid types: roo-code, cursor, vscode, vscode-insider)", client)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Check if client is already registered
	for _, registeredClient := range cfg.Clients.RegisteredClients {
		if registeredClient == client {
			return fmt.Errorf("client %s is already registered", client)
		}
	}

	// Add the client to the registered clients list
	cfg.Clients.RegisteredClients = append(cfg.Clients.RegisteredClients, client)

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Log.Info(fmt.Sprintf("Successfully registered client: %s", client))
	return nil
}

func removeClientCmdFunc(_ *cobra.Command, args []string) error {
	client := args[0]

	// Validate the client type
	switch client {
	case "roo-code", "cursor", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf("invalid client type: %s (valid types: roo-code, cursor, vscode, vscode-insider)", client)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Find and remove the client from the registered clients list
	found := false
	for i, registeredClient := range cfg.Clients.RegisteredClients {
		if registeredClient == client {
			// Remove the client by appending the slice before and after the index
			cfg.Clients.RegisteredClients = append(cfg.Clients.RegisteredClients[:i], cfg.Clients.RegisteredClients[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("client %s is not registered", client)
	}

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Log.Info(fmt.Sprintf("Successfully removed client: %s", client))
	return nil
}

func listRegisteredClientsCmdFunc(_ *cobra.Command, _ []string) error {
	// Get the current config
	cfg := config.GetConfig()

	// Check if there are any registered clients
	if len(cfg.Clients.RegisteredClients) == 0 {
		logger.Log.Info("No clients are currently registered.")
		return nil
	}

	// Print the list of registered clients
	logger.Log.Info("Registered clients:")
	for _, client := range cfg.Clients.RegisteredClients {
		logger.Log.Info(fmt.Sprintf("  - %s", client))
	}

	return nil
}

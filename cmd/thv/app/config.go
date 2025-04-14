package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/client"
	"github.com/StacklokLabs/toolhive/pkg/config"
	"github.com/StacklokLabs/toolhive/pkg/container"
	rt "github.com/StacklokLabs/toolhive/pkg/container/runtime"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/secrets"
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
  - roo-code: Roo Code extension for VS Code
  - cursor: Cursor editor
  - vscode: Visual Studio Code
  - vscode-insider: Visual Studio Code Insiders edition`,
	Args: cobra.ExactArgs(1),
	RunE: registerClientCmdFunc,
}

var removeClientCmd = &cobra.Command{
	Use:   "remove-client [client]",
	Short: "Remove a client from MCP server configuration",
	Long: `Remove a client from MCP server configuration.
Valid clients are:
  - roo-code: Roo Code extension for VS Code
  - cursor: Cursor editor
  - vscode: Visual Studio Code
  - vscode-insider: Visual Studio Code Insiders edition`,
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
	case string(secrets.EncryptedType):
		// Valid provider type
	default:
		return fmt.Errorf("invalid secrets provider type: %s (valid types: encrypted)", provider)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Update the secrets provider type
	cfg.Secrets.ProviderType = provider

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Secrets provider type updated to: %s\n", provider)
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

	fmt.Printf("Auto-discovery of MCP clients %s\n", map[bool]string{true: "enabled", false: "disabled"}[enabled])

	// If auto-discovery is enabled, update all registered clients with currently running MCPs
	if enabled && len(cfg.Clients.RegisteredClients) > 0 {
		for _, clientName := range cfg.Clients.RegisteredClients {
			if err := addRunningMCPsToClient(clientName); err != nil {
				fmt.Printf("Warning: Failed to add running MCPs to client %s: %v\n", clientName, err)
			}
		}
	}

	return nil
}

func registerClientCmdFunc(_ *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cursor", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf("invalid client type: %s (valid types: roo-code, cursor, vscode, vscode-insider)", clientType)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Check if client is already registered
	for _, registeredClient := range cfg.Clients.RegisteredClients {
		if registeredClient == clientType {
			return fmt.Errorf("client %s is already registered", clientType)
		}
	}

	// Add the client to the registered clients list
	cfg.Clients.RegisteredClients = append(cfg.Clients.RegisteredClients, clientType)

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Successfully registered client: %s\n", clientType)

	// Add currently running MCPs to the newly registered client
	if err := addRunningMCPsToClient(clientType); err != nil {
		fmt.Printf("Warning: Failed to add running MCPs to client: %v\n", err)
	}

	return nil
}

func removeClientCmdFunc(_ *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cursor", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf("invalid client type: %s (valid types: roo-code, cursor, vscode, vscode-insider)", clientType)
	}

	// Get the current config
	cfg := config.GetConfig()

	// Find and remove the client from the registered clients list
	found := false
	for i, registeredClient := range cfg.Clients.RegisteredClients {
		if registeredClient == clientType {
			// Remove the client by appending the slice before and after the index
			cfg.Clients.RegisteredClients = append(cfg.Clients.RegisteredClients[:i], cfg.Clients.RegisteredClients[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("client %s is not registered", clientType)
	}

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Successfully removed client: %s\n", clientType)
	return nil
}

// addRunningMCPsToClient adds currently running MCP servers to the specified client's configuration
func addRunningMCPsToClient(clientName string) error {
	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// List containers
	containers, err := runtime.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive and running
	var runningContainers []rt.ContainerInfo
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) && c.State == "running" {
			runningContainers = append(runningContainers, c)
		}
	}

	if len(runningContainers) == 0 {
		// No running servers, nothing to do
		return nil
	}

	// Find the client configuration for the specified client
	clientConfigs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	// If no configs found, nothing to do
	if len(clientConfigs) == 0 {
		return nil
	}

	// For each running container, add it to the client configuration
	for _, c := range runningContainers {
		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get tool type from labels
		toolType := labels.GetToolType(c.Labels)

		// Only include containers with tool type "mcp"
		if toolType != "mcp" {
			continue
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			continue // Skip if we can't get the port
		}

		// Generate URL for the MCP server
		url := client.GenerateMCPServerURL("localhost", port, name)

		// Update each configuration file
		for _, clientConfig := range clientConfigs {
			// Update the MCP server configuration with locking
			if err := client.Upsert(clientConfig, name, url); err != nil {
				logger.Log.Warn(fmt.Sprintf("Warning: Failed to update MCP server configuration in %s: %v", clientConfig.Path, err))
				continue
			}

			fmt.Printf("Added MCP server %s to client %s\n", name, clientName)
		}
	}

	return nil
}

func listRegisteredClientsCmdFunc(_ *cobra.Command, _ []string) error {
	// Get the current config
	cfg := config.GetConfig()

	// Check if there are any registered clients
	if len(cfg.Clients.RegisteredClients) == 0 {
		fmt.Println("No clients are currently registered.")
		return nil
	}

	// Print the list of registered clients
	fmt.Println("Registered clients:")
	for _, clientName := range cfg.Clients.RegisteredClients {
		fmt.Printf("  - %s\n", clientName)
	}

	return nil
}

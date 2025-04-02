package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/secrets"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage application configuration",
	Long:  "The config command provides subcommands to manage application configuration settings.",
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
When enabled, Vibe Tool will automatically update client configuration files
with the URLs of running MCP servers.`,
	Args: cobra.ExactArgs(1),
	RunE: autoDiscoveryCmdFunc,
}

func init() {
	// Add config command to root command
	rootCmd.AddCommand(configCmd)

	// Add subcommands to config command
	configCmd.AddCommand(secretsProviderCmd)
	configCmd.AddCommand(autoDiscoveryCmd)
}

func secretsProviderCmdFunc(cmd *cobra.Command, args []string) error {
	provider := args[0]

	// Validate the provider type
	switch provider {
	case string(secrets.BasicType), string(secrets.EncryptedType):
		// Valid provider type
	default:
		return fmt.Errorf("invalid secrets provider type: %s (valid types: basic, encrypted)", provider)
	}

	// Get the current config
	cfg := GetConfig()

	// Update the secrets provider type
	cfg.Secrets.ProviderType = provider

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	cmd.Printf("Secrets provider type updated to: %s\n", provider)
	return nil
}

func autoDiscoveryCmdFunc(cmd *cobra.Command, args []string) error {
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
	cfg := GetConfig()

	// Update the auto-discovery setting
	cfg.Clients.AutoDiscovery = enabled

	// Save the updated config
	if err := cfg.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	cmd.Printf("Auto-discovery of MCP clients %s\n", map[bool]string{true: "enabled", false: "disabled"}[enabled])
	return nil
}

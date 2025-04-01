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

func init() {
	// Add config command to root command
	rootCmd.AddCommand(configCmd)

	// Add secrets-provider subcommand to config command
	configCmd.AddCommand(secretsProviderCmd)
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

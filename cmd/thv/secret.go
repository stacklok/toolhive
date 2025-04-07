package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/secrets"
)

func newSecretCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets",
		Long:  "The secret command provides subcommands to set, get, delete, and list secrets.",
	}

	cmd.AddCommand(
		newSecretSetCommand(),
		newSecretGetCommand(),
		newSecretDeleteCommand(),
		newSecretListCommand(),
		newSecretResetKeyringCommand(),
	)

	return cmd
}

func newSecretSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Set a secret",
		Long:  "Set a secret with the given name. The secret value will be read from the terminal.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			// Validate input
			if name == "" {
				logger.Log.Info("Validation Error: Secret name cannot be empty")
				return
			}

			// Prompt for the secret value
			logger.Log.Info("Enter secret value (input will be hidden): ")
			valueBytes, err := term.ReadPassword(int(syscall.Stdin))
			logger.Log.Info("") // Add a newline after the hidden input

			if err != nil {
				logger.Log.Error(fmt.Sprintf("Error reading secret from terminal: %v", err))
				return
			}

			value := string(valueBytes)
			if value == "" {
				logger.Log.Info("Validation Error: Secret value cannot be empty")
				return
			}

			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Error: %v", err))
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to create secrets manager: %v", err))
				return
			}

			err = manager.SetSecret(name, value)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to set secret %s: %v", name, err))
				return
			}
			logger.Log.Info(fmt.Sprintf("Secret %s set successfully", name))
		},
	}
}

func newSecretGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Get a secret",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			// Validate input
			if name == "" {
				logger.Log.Info("Validation Error Secret name cannot be empty")
				return
			}

			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Error: %v", err))
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to create secrets manager: %v", err))
				return
			}

			value, err := manager.GetSecret(name)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to get secret %s: %v", name, err))
				return
			}
			logger.Log.Info(fmt.Sprintf("Secret %s: %s", name, value))
		},
	}
}

func newSecretDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			// Validate input
			if name == "" {
				logger.Log.Info("Validation Error: Secret name cannot be empty")
				return
			}

			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Error: %v", err))
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to create secrets manager: %v", err))
				return
			}

			err = manager.DeleteSecret(name)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to delete secret %s: %v", name, err))
				return
			}
			logger.Log.Info(fmt.Sprintf("Secret %s deleted successfully", name))
		},
	}
}

func newSecretListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all available secrets",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Error: %v", err))
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to create secrets manager: %v", err))
				return
			}

			secretNames, err := manager.ListSecrets()
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to list secrets: %v", err))
				return
			}

			if len(secretNames) == 0 {
				logger.Log.Info("No secrets found")
				return
			}

			logger.Log.Info("Available secrets:")
			for _, name := range secretNames {
				logger.Log.Info(fmt.Sprintf("  - %s", name))
			}
		},
	}
}

func newSecretResetKeyringCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-keyring",
		Short: "Reset the keyring secret",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			if err := secrets.ResetKeyringSecret(); err != nil {
				logger.Log.Error(fmt.Sprintf("Failed to reset keyring secret: %v", err))
				return
			}
			logger.Log.Info("Successfully reset keyring secret")
		},
	}
}

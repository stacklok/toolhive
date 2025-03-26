package main

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/secrets"
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
	)

	return cmd
}

func newSecretSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a secret",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			name, value := args[0], args[1]

			// Validate input
			if name == "" {
				cmd.Println("Error: Secret name cannot be empty")
				return
			}

			manager, err := secrets.CreateDefaultSecretsManager()
			if err != nil {
				cmd.Printf("Failed to create secrets manager: %v\n", err)
				return
			}

			err = manager.SetSecret(name, value)
			if err != nil {
				cmd.Printf("Failed to set secret %s: %v\n", name, err)
				return
			}
			cmd.Printf("Secret %s set successfully\n", name)
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
				cmd.Println("Error: Secret name cannot be empty")
				return
			}

			manager, err := secrets.CreateDefaultSecretsManager()
			if err != nil {
				cmd.Printf("Failed to create secrets manager: %v\n", err)
				return
			}

			value, err := manager.GetSecret(name)
			if err != nil {
				cmd.Printf("Failed to get secret %s: %v\n", name, err)
				return
			}
			cmd.Printf("Secret %s: %s\n", name, value)
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
				cmd.Println("Error: Secret name cannot be empty")
				return
			}

			manager, err := secrets.CreateDefaultSecretsManager()
			if err != nil {
				cmd.Printf("Failed to create secrets manager: %v\n", err)
				return
			}

			err = manager.DeleteSecret(name)
			if err != nil {
				cmd.Printf("Failed to delete secret %s: %v\n", name, err)
				return
			}
			cmd.Printf("Secret %s deleted successfully\n", name)
		},
	}
}

func newSecretListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all available secrets",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			manager, err := secrets.CreateDefaultSecretsManager()
			if err != nil {
				cmd.Printf("Failed to create secrets manager: %v\n", err)
				return
			}

			secretNames, err := manager.ListSecrets()
			if err != nil {
				cmd.Printf("Failed to list secrets: %v\n", err)
				return
			}

			if len(secretNames) == 0 {
				cmd.Println("No secrets found")
				return
			}

			cmd.Println("Available secrets:")
			for _, name := range secretNames {
				cmd.Printf("  - %s\n", name)
			}
		},
	}
}

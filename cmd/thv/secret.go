package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/StacklokLabs/toolhive/pkg/secrets"
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
		Long: `Set a secret with the given name.

Input Methods:
		- Piped Input: If data is piped to the command, the secret value will be read from stdin.
		  Examples:
		    echo "my-secret-value" | thv secret set my-secret
		    cat secret-file.txt | thv secret set my-secret
		
		- Interactive Input: If no data is piped, you will be prompted to enter the secret value securely
		  (input will be hidden).
		  Example:
		    thv secret set my-secret
		    Enter secret value (input will be hidden): _

The secret will be stored securely using the configured secrets provider.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			// Validate input
			if name == "" {
				fmt.Println("Validation Error: Secret name cannot be empty")
				return
			}

			var value string
			var err error

			// Check if data is being piped to stdin
			stat, _ := os.Stdin.Stat()
			isPiped := (stat.Mode() & os.ModeCharDevice) == 0

			if isPiped {
				// Read from stdin (piped input)
				var valueBytes []byte
				valueBytes, err = io.ReadAll(os.Stdin)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading secret from stdin: %v\n", err)
					return
				}
				value = string(valueBytes)
				// Trim trailing newline if present
				value = strings.TrimSuffix(value, "\n")
			} else {
				// Interactive mode - prompt for the secret value
				fmt.Print("Enter secret value (input will be hidden): ")
				var valueBytes []byte
				valueBytes, err = term.ReadPassword(int(syscall.Stdin))
				fmt.Println("") // Add a newline after the hidden input

				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading secret from terminal: %v\n", err)
					return
				}
				value = string(valueBytes)
			}

			if value == "" {
				fmt.Println("Validation Error: Secret value cannot be empty")
				return
			}

			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			err = manager.SetSecret(name, value)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to set secret %s: %v\n", name, err)
				return
			}
			fmt.Printf("Secret %s set successfully\n", name)
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
				fmt.Println("Validation Error: Secret name cannot be empty")
				return
			}

			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			value, err := manager.GetSecret(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to get secret %s: %v\n", name, err)
				return
			}
			fmt.Printf("Secret %s: %s\n", name, value)
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
				fmt.Println("Validation Error: Secret name cannot be empty")
				return
			}

			providerType, err := GetSecretsProviderType(cmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			err = manager.DeleteSecret(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to delete secret %s: %v\n", name, err)
				return
			}
			fmt.Printf("Secret %s deleted successfully\n", name)
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
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			manager, err := secrets.CreateSecretManager(providerType)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			secretNames, err := manager.ListSecrets()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to list secrets: %v\n", err)
				return
			}

			if len(secretNames) == 0 {
				fmt.Println("No secrets found")
				return
			}

			fmt.Println("Available secrets:")
			for _, name := range secretNames {
				fmt.Printf("  - %s\n", name)
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
				fmt.Fprintf(os.Stderr, "Failed to reset keyring secret: %v\n", err)
				return
			}
			fmt.Println("Successfully reset keyring secret")
		},
	}
}

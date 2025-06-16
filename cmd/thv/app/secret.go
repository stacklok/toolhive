package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

func newSecretCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets",
		Long:  "The secret command provides subcommands to set, get, delete, and list secrets.",
	}

	cmd.AddCommand(
		newSecretSetupCommand(),
		newSecretSetCommand(),
		newSecretGetCommand(),
		newSecretDeleteCommand(),
		newSecretListCommand(),
		newSecretResetKeyringCommand(),
		newSecretProviderCommand(),
	)

	return cmd
}

func newSecretProviderCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "provider <name>",
		Short: "Configure the secrets provider directly",
		Long: `For most users, it is recommended to use "thv secret setup" instead.
Configure the secrets provider.
Valid secrets providers are:
  - encrypted: Full read-write secrets provider
  - 1password: Read-only secrets provider`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			provider := args[0]
			return SetSecretsProvider(secrets.ProviderType(provider))
		},
	}
}

func newSecretSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Set up secrets provider",
		Long: fmt.Sprintf(`Interactive setup for configuring a secrets provider.
This command will guide you through selecting and configuring
a secrets provider for storing and retrieving secrets.

Available providers:
  - %s: Stores secrets in an encrypted file using AES-256-GCM using the OS Keyring
  - %s: Read-only access to 1Password secrets (requires OP_SERVICE_ACCOUNT_TOKEN)
  - %s: Disables secrets functionality

You must run this command before using any other secrets functionality.`,
			string(secrets.EncryptedType), string(secrets.OnePasswordType), string(secrets.NoneType)), //nolint:gofmt,gci
		Args: cobra.NoArgs,
		RunE: runSecretsSetup,
	}
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
			ctx := cmd.Context()

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

			manager, err := getSecretsManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			// Check if the provider supports writing secrets
			if !manager.Capabilities().CanWrite {
				providerType, _ := config.GetConfig().Secrets.GetProviderType()
				fmt.Fprintf(os.Stderr, "Error: The %s secrets provider does not support setting secrets (read-only)\n", providerType)
				return
			}

			err = manager.SetSecret(ctx, name, value)
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
			ctx := cmd.Context()
			name := args[0]

			// Validate input
			if name == "" {
				fmt.Println("Validation Error: Secret name cannot be empty")
				return
			}

			manager, err := getSecretsManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			value, err := manager.GetSecret(ctx, name)
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
			ctx := cmd.Context()
			name := args[0]

			// Validate input
			if name == "" {
				fmt.Println("Validation Error: Secret name cannot be empty")
				return
			}

			manager, err := getSecretsManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			// Check if the provider supports deleting secrets
			if !manager.Capabilities().CanDelete {
				providerType, _ := config.GetConfig().Secrets.GetProviderType()
				fmt.Fprintf(os.Stderr, "Error: The %s secrets provider does not support deleting secrets\n", providerType)
				return
			}

			err = manager.DeleteSecret(ctx, name)
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
			ctx := cmd.Context()
			manager, err := getSecretsManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create secrets manager: %v\n", err)
				return
			}

			// Check if the provider supports listing secrets
			if !manager.Capabilities().CanList {
				providerType, _ := config.GetConfig().Secrets.GetProviderType()
				fmt.Fprintf(os.Stderr, "Error: The %s secrets provider does not support listing secrets\n", providerType)
				return
			}

			secrets, err := manager.ListSecrets(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to list secrets: %v\n", err)
				return
			}

			if len(secrets) == 0 {
				fmt.Println("No secrets found")
				return
			}

			fmt.Println("Available secrets:")
			for _, description := range secrets {
				fmt.Printf("  - %s", description.Key)
				// Add description if available.
				if description.Description != "" {
					fmt.Printf(" (%s)", description.Description)
				}
				fmt.Println()
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

func getSecretsManager() (secrets.Provider, error) {
	cfg := config.GetConfig()

	// Check if secrets setup has been completed
	if !cfg.Secrets.SetupCompleted {
		return nil, secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider type: %w", err)
	}

	manager, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return nil, fmt.Errorf("failed to create secrets manager: %w", err)
	}

	return manager, nil
}

func runSecretsSetup(_ *cobra.Command, _ []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf(`
ToolHive Secrets Setup
=====================

Please select a secrets provider:
  %s - Store secrets in an encrypted file (full read/write)
  %s - Use 1Password for secrets (read-only, requires service account)
  %s - Disable secrets functionality
`, string(secrets.EncryptedType), string(secrets.OnePasswordType), string(secrets.NoneType))

	var providerType secrets.ProviderType
	for {
		fmt.Printf("\nEnter provider (%s/%s/%s): ",
			string(secrets.EncryptedType), string(secrets.OnePasswordType), string(secrets.NoneType))
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}

		input = strings.TrimSpace(input)
		switch input {
		case string(secrets.EncryptedType):
			providerType = secrets.EncryptedType
		case string(secrets.OnePasswordType):
			providerType = secrets.OnePasswordType
		case string(secrets.NoneType):
			providerType = secrets.NoneType
		default:
			fmt.Printf("Invalid provider. Please enter '%s', '%s', or '%s'.\n",
				string(secrets.EncryptedType), string(secrets.OnePasswordType), string(secrets.NoneType))
			continue
		}
		break
	}

	fmt.Printf("\nYou selected: %s\n", providerType)

	// Show provider-specific setup instructions
	switch providerType {
	case secrets.EncryptedType:
		fmt.Println(`Setting up encrypted secrets provider...
You will need to provide a password to encrypt your secrets.
This password will be stored in your OS keyring if available.`)
	case secrets.OnePasswordType:
		fmt.Println(`Setting up 1Password secrets provider...

To use 1Password as your secrets provider, you need to:
1. Create a service account in your 1Password account
2. Generate a service account token
3. Set the OP_SERVICE_ACCOUNT_TOKEN environment variable

For more information, visit: https://developer.1password.com/docs/service-accounts/`)
	case secrets.NoneType:
		fmt.Println(`Setting up none secrets provider...
Secrets functionality will be disabled.
No secrets will be stored or retrieved.`)
	}

	// SetSecretsProvider will handle validation and configuration
	fmt.Println("Validating provider setup...")
	if err := SetSecretsProvider(providerType); err != nil {
		return fmt.Errorf("failed to configure secrets provider: %w", err)
	}

	fmt.Printf("\n✓ Secrets provider '%s' has been successfully configured!\n", providerType)

	// Show additional notes for specific providers
	if providerType == secrets.OnePasswordType {
		fmt.Println("Note: 1Password provider is read-only. You can retrieve secrets but not set new ones.")
	}

	return nil
}

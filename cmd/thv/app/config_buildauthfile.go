// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
)

var (
	unsetBuildAuthFileAll bool
	showAuthFileContent   bool
	authFileFromStdin     bool
)

var setBuildAuthFileCmd = &cobra.Command{
	Use:   "set-build-auth-file <name> [content]",
	Short: "Set an auth file for protocol builds",
	Long: `Set authentication file content that will be injected into the container
during protocol builds (npx://, uvx://, go://). This is useful for authenticating
to private package registries.

Supported file types:
  npmrc  - NPM configuration (~/.npmrc) for npm/npx registries
  netrc  - Netrc file (~/.netrc) for pip, Go, and other tools
  yarnrc - Yarn configuration (~/.yarnrc)

The file content is injected into the build stage only and is NOT included
in the final container image.

Examples:
  # Set npmrc for private npm registry
  thv config set-build-auth-file npmrc '//npm.corp.example.com/:_authToken=TOKEN'

  # Set netrc for pip/Go authentication
  thv config set-build-auth-file netrc 'machine github.com login git password TOKEN'

  # Read content from stdin (avoids exposing secrets in shell history)
  cat ~/.npmrc | thv config set-build-auth-file npmrc --stdin
  thv config set-build-auth-file npmrc --stdin < ~/.npmrc

Note: For multi-line content, use quotes, heredoc syntax, or --stdin.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: setBuildAuthFileCmdFunc,
}

var getBuildAuthFileCmd = &cobra.Command{
	Use:   "get-build-auth-file [name]",
	Short: "Get build auth file configuration",
	Long: `Display configured build auth files.
If a name is provided, shows only that specific file.
If no name is provided, shows all configured files.

By default, file contents are hidden to prevent credential exposure.
Use --show-content to display the actual content.

Examples:
  thv config get-build-auth-file                    # Show all files (content hidden)
  thv config get-build-auth-file npmrc              # Show specific file (content hidden)
  thv config get-build-auth-file npmrc --show-content  # Show with content`,
	Args: cobra.MaximumNArgs(1),
	RunE: getBuildAuthFileCmdFunc,
}

var unsetBuildAuthFileCmd = &cobra.Command{
	Use:   "unset-build-auth-file [name]",
	Short: "Remove build auth file(s)",
	Long: `Remove a specific build auth file or all files.

Examples:
  thv config unset-build-auth-file npmrc  # Remove specific file
  thv config unset-build-auth-file --all  # Remove all files`,
	Args: cobra.MaximumNArgs(1),
	RunE: unsetBuildAuthFileCmdFunc,
}

func init() {
	configCmd.AddCommand(setBuildAuthFileCmd)
	configCmd.AddCommand(getBuildAuthFileCmd)
	configCmd.AddCommand(unsetBuildAuthFileCmd)

	unsetBuildAuthFileCmd.Flags().BoolVar(
		&unsetBuildAuthFileAll,
		"all",
		false,
		"Remove all build auth files",
	)

	getBuildAuthFileCmd.Flags().BoolVar(
		&showAuthFileContent,
		"show-content",
		false,
		"Show the actual file content (contains credentials) (default false)",
	)

	setBuildAuthFileCmd.Flags().BoolVar(
		&authFileFromStdin,
		"stdin",
		false,
		"Read file content from stdin instead of command line argument (default false)",
	)
}

func setBuildAuthFileCmdFunc(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Validate the file name first
	if err := config.ValidateBuildAuthFileName(name); err != nil {
		return err
	}

	var content string
	if authFileFromStdin {
		// Read from stdin
		data, err := readFromStdin()
		if err != nil {
			return fmt.Errorf("failed to read from stdin: %w", err)
		}
		content = data
	} else {
		// Read from command line argument
		if len(args) < 2 {
			return fmt.Errorf("content argument required (or use --stdin to read from stdin)")
		}
		content = args[1]
	}

	// Get the secrets manager to store the content securely
	manager, err := getSecretsManager()
	if err != nil {
		return fmt.Errorf("failed to get secrets manager: %w (run 'thv secret setup' first)", err)
	}

	// Store the content in the secrets provider
	secretName := config.BuildAuthFileSecretName(name)
	ctx := cmd.Context()
	if err := manager.SetSecret(ctx, secretName, content); err != nil {
		return fmt.Errorf("failed to store auth file in secrets: %w", err)
	}

	// Mark the auth file as configured in the config (only a marker, no content)
	provider := config.NewDefaultProvider()
	if err := provider.MarkBuildAuthFileConfigured(name); err != nil {
		// Try to clean up the secret if marking fails
		_ = manager.DeleteSecret(ctx, secretName)
		return fmt.Errorf("failed to mark build auth file as configured: %w", err)
	}

	return nil
}

// readFromStdin reads all content from stdin.
func readFromStdin() (string, error) {
	// Check if stdin has data (is not a terminal)
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat stdin: %w", err)
	}

	// If stdin is a terminal with no piped data, return an error
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", fmt.Errorf("no input provided on stdin (pipe content or redirect from a file)")
	}

	reader := bufio.NewReader(os.Stdin)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	// Trim trailing newline that's often added by echo/cat
	content := strings.TrimSuffix(string(data), "\n")
	return content, nil
}

func getBuildAuthFileCmdFunc(cmd *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()
	ctx := cmd.Context()

	if len(args) == 1 {
		name := args[0]
		if !provider.IsBuildAuthFileConfigured(name) {
			fmt.Printf("Build auth file %s is not configured.\n", name)
			return nil
		}

		// Get content from secrets if requested
		if showAuthFileContent {
			manager, err := getSecretsManager()
			if err != nil {
				return fmt.Errorf("failed to get secrets manager: %w", err)
			}
			secretName := config.BuildAuthFileSecretName(name)
			content, err := manager.GetSecret(ctx, secretName)
			if err != nil {
				return fmt.Errorf("failed to retrieve auth file content: %w", err)
			}
			lines := strings.Count(content, "\n") + 1
			fmt.Printf("%s: %d line(s) -> %s\n", name, lines, config.SupportedAuthFiles[name])
			fmt.Printf("Content:\n%s\n", content)
		} else {
			fmt.Printf("%s: configured -> %s\n", name, config.SupportedAuthFiles[name])
		}
		return nil
	}

	configuredFiles := provider.GetConfiguredBuildAuthFiles()
	if len(configuredFiles) == 0 {
		fmt.Println("No build auth files are configured.")
		return nil
	}

	sort.Strings(configuredFiles)

	fmt.Println("Configured build auth files:")
	for _, name := range configuredFiles {
		if showAuthFileContent {
			manager, err := getSecretsManager()
			if err != nil {
				fmt.Printf("  %s: configured -> %s (unable to retrieve content: %v)\n",
					name, config.SupportedAuthFiles[name], err)
				continue
			}
			secretName := config.BuildAuthFileSecretName(name)
			content, err := manager.GetSecret(ctx, secretName)
			if err != nil {
				fmt.Printf("  %s: configured -> %s (unable to retrieve content: %v)\n",
					name, config.SupportedAuthFiles[name], err)
				continue
			}
			lines := strings.Count(content, "\n") + 1
			fmt.Printf("  %s: %d line(s) -> %s\n", name, lines, config.SupportedAuthFiles[name])
			fmt.Printf("  Content:\n%s\n", content)
		} else {
			fmt.Printf("  %s: configured -> %s\n", name, config.SupportedAuthFiles[name])
		}
	}
	return nil
}

func unsetBuildAuthFileCmdFunc(cmd *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()
	ctx := cmd.Context()

	if unsetBuildAuthFileAll {
		configuredFiles := provider.GetConfiguredBuildAuthFiles()
		if len(configuredFiles) == 0 {
			fmt.Println("No build auth files are configured.")
			return nil
		}

		// Try to get secrets manager to delete secrets (but don't fail if unavailable)
		manager, err := getSecretsManager()
		if err == nil {
			for _, name := range configuredFiles {
				secretName := config.BuildAuthFileSecretName(name)
				// Best effort - don't fail if secret doesn't exist
				_ = manager.DeleteSecret(ctx, secretName)
			}
		}

		if err := provider.UnsetAllBuildAuthFiles(); err != nil {
			return fmt.Errorf("failed to remove build auth files: %w", err)
		}

		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("please specify a file name or use --all")
	}

	name := args[0]
	if !provider.IsBuildAuthFileConfigured(name) {
		fmt.Printf("Build auth file %s is not configured.\n", name)
		return nil
	}

	// Try to delete the secret (but don't fail if secrets manager unavailable)
	manager, err := getSecretsManager()
	if err == nil {
		secretName := config.BuildAuthFileSecretName(name)
		_ = manager.DeleteSecret(ctx, secretName)
	}

	if err := provider.UnsetBuildAuthFile(name); err != nil {
		return fmt.Errorf("failed to remove build auth file: %w", err)
	}

	return nil
}

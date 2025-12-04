package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
)

var (
	unsetBuildAuthFileAll bool
	showAuthFileContent   bool
)

var setBuildAuthFileCmd = &cobra.Command{
	Use:   "set-build-auth-file <name> <content>",
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

Note: For multi-line content, use quotes or heredoc syntax in your shell.`,
	Args: cobra.ExactArgs(2),
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
		"Show the actual file content (contains credentials)",
	)
}

func setBuildAuthFileCmdFunc(_ *cobra.Command, args []string) error {
	name := args[0]
	content := args[1]

	provider := config.NewDefaultProvider()
	if err := provider.SetBuildAuthFile(name, content); err != nil {
		return fmt.Errorf("failed to set build auth file: %w", err)
	}

	fmt.Printf("Successfully set build auth file: %s\n", name)
	return nil
}

func getBuildAuthFileCmdFunc(_ *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()

	if len(args) == 1 {
		name := args[0]
		content, exists := provider.GetBuildAuthFile(name)
		if !exists {
			fmt.Printf("Build auth file %s is not configured.\n", name)
			return nil
		}
		lines := strings.Count(content, "\n") + 1
		fmt.Printf("%s: %d line(s) -> %s\n", name, lines, config.SupportedAuthFiles[name])
		if showAuthFileContent {
			fmt.Printf("Content:\n%s\n", content)
		}
		return nil
	}

	authFiles := provider.GetAllBuildAuthFiles()
	if len(authFiles) == 0 {
		fmt.Println("No build auth files are configured.")
		return nil
	}

	names := make([]string, 0, len(authFiles))
	for name := range authFiles {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Println("Configured build auth files:")
	for _, name := range names {
		content := authFiles[name]
		lines := strings.Count(content, "\n") + 1
		fmt.Printf("  %s: %d line(s) -> %s\n", name, lines, config.SupportedAuthFiles[name])
		if showAuthFileContent {
			fmt.Printf("  Content:\n%s\n", content)
		}
	}
	return nil
}

func unsetBuildAuthFileCmdFunc(_ *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()

	if unsetBuildAuthFileAll {
		authFiles := provider.GetAllBuildAuthFiles()
		if len(authFiles) == 0 {
			fmt.Println("No build auth files are configured.")
			return nil
		}

		if err := provider.UnsetAllBuildAuthFiles(); err != nil {
			return fmt.Errorf("failed to remove build auth files: %w", err)
		}

		fmt.Printf("Successfully removed %d build auth file(s).\n", len(authFiles))
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("please specify a file name or use --all")
	}

	name := args[0]
	_, exists := provider.GetBuildAuthFile(name)
	if !exists {
		fmt.Printf("Build auth file %s is not configured.\n", name)
		return nil
	}

	if err := provider.UnsetBuildAuthFile(name); err != nil {
		return fmt.Errorf("failed to remove build auth file: %w", err)
	}

	fmt.Printf("Successfully removed build auth file: %s\n", name)
	return nil
}

package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

var (
	unsetBuildEnvAll bool
	fromSecret       bool
	fromEnv          bool
)

var setBuildEnvCmd = &cobra.Command{
	Use:   "set-build-env <KEY> [value]",
	Short: "Set a build environment variable for protocol builds",
	Long: `Set a build environment variable that will be injected into Dockerfiles
during protocol builds (npx://, uvx://, go://). This is useful for configuring
custom package mirrors in corporate environments.

Environment variable names must:
- Start with an uppercase letter
- Contain only uppercase letters, numbers, and underscores
- Not be a reserved system variable (PATH, HOME, etc.)

You can set the value in three ways:
1. Directly: thv config set-build-env KEY value
2. From a ToolHive secret: thv config set-build-env KEY --from-secret secret-name
3. From shell environment: thv config set-build-env KEY --from-env

Common use cases:
- NPM_CONFIG_REGISTRY: Custom npm registry URL
- PIP_INDEX_URL: Custom PyPI index URL
- UV_DEFAULT_INDEX: Custom uv package index URL
- GOPROXY: Custom Go module proxy URL
- GOPRIVATE: Private Go module paths

Examples:
  thv config set-build-env NPM_CONFIG_REGISTRY https://npm.corp.example.com
  thv config set-build-env GITHUB_TOKEN --from-secret github-pat
  thv config set-build-env ARTIFACTORY_API_KEY --from-env`,
	Args: cobra.RangeArgs(1, 2),
	RunE: setBuildEnvCmdFunc,
}

var getBuildEnvCmd = &cobra.Command{
	Use:   "get-build-env [KEY]",
	Short: "Get build environment variables",
	Long: `Display configured build environment variables.
If a KEY is provided, shows only that specific variable.
If no KEY is provided, shows all configured variables.

Examples:
  thv config get-build-env                    # Show all variables
  thv config get-build-env NPM_CONFIG_REGISTRY  # Show specific variable`,
	Args: cobra.MaximumNArgs(1),
	RunE: getBuildEnvCmdFunc,
}

var unsetBuildEnvCmd = &cobra.Command{
	Use:   "unset-build-env [KEY]",
	Short: "Remove build environment variable(s)",
	Long: `Remove a specific build environment variable or all variables.

Examples:
  thv config unset-build-env NPM_CONFIG_REGISTRY  # Remove specific variable
  thv config unset-build-env --all                # Remove all variables`,
	Args: cobra.MaximumNArgs(1),
	RunE: unsetBuildEnvCmdFunc,
}

func init() {
	// Add build-env subcommands to config command
	configCmd.AddCommand(setBuildEnvCmd)
	configCmd.AddCommand(getBuildEnvCmd)
	configCmd.AddCommand(unsetBuildEnvCmd)

	// Add --from-secret and --from-env flags to set command
	setBuildEnvCmd.Flags().BoolVar(
		&fromSecret,
		"from-secret",
		false,
		"Read value from a ToolHive secret at build time (value argument becomes secret name)",
	)
	setBuildEnvCmd.Flags().BoolVar(
		&fromEnv,
		"from-env",
		false,
		"Read value from shell environment at build time",
	)

	// Make flags mutually exclusive
	setBuildEnvCmd.MarkFlagsMutuallyExclusive("from-secret", "from-env")

	// Add --all flag to unset command
	unsetBuildEnvCmd.Flags().BoolVar(
		&unsetBuildEnvAll,
		"all",
		false,
		"Remove all build environment variables",
	)
}

func validateSecretExists(ctx context.Context, secretName string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	// Check if secrets are set up
	if !cfg.Secrets.SetupCompleted {
		return secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return fmt.Errorf("failed to get secrets provider type: %w", err)
	}

	manager, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return fmt.Errorf("failed to create secrets provider: %w", err)
	}

	// Try to get the secret to validate it exists
	_, err = manager.GetSecret(ctx, secretName)
	if err != nil {
		return fmt.Errorf("secret '%s' not found or inaccessible: %w", secretName, err)
	}

	return nil
}

func setBuildEnvCmdFunc(cmd *cobra.Command, args []string) error {
	key := args[0]
	provider := config.NewDefaultProvider()

	// Handle --from-secret flag
	if fromSecret {
		if len(args) != 2 {
			return fmt.Errorf("secret name is required when using --from-secret")
		}
		secretName := args[1]

		// Validate that the secret exists
		ctx := cmd.Context()
		if err := validateSecretExists(ctx, secretName); err != nil {
			return fmt.Errorf("failed to validate secret: %w", err)
		}

		if err := provider.SetBuildEnvFromSecret(key, secretName); err != nil {
			return fmt.Errorf("failed to set build environment variable from secret: %w", err)
		}

		fmt.Printf("Successfully configured build environment variable %s to read from secret: %s\n", key, secretName)
		return nil
	}

	// Handle --from-env flag
	if fromEnv {
		if len(args) > 1 {
			return fmt.Errorf("value argument should not be provided when using --from-env")
		}

		if err := provider.SetBuildEnvFromShell(key); err != nil {
			return fmt.Errorf("failed to set build environment variable from shell: %w", err)
		}

		fmt.Printf("Successfully configured build environment variable %s to read from shell environment\n", key)
		return nil
	}

	// Handle literal value
	if len(args) != 2 {
		return fmt.Errorf("value is required when not using --from-secret or --from-env")
	}
	value := args[1]

	if err := provider.SetBuildEnv(key, value); err != nil {
		return fmt.Errorf("failed to set build environment variable: %w", err)
	}

	fmt.Printf("Successfully set build environment variable: %s\n", key)
	return nil
}

// buildEnvEntry represents a build environment variable with its source
type buildEnvEntry struct {
	key, value, source string
}

// getAllBuildEnvEntries collects all build env entries from all sources
func getAllBuildEnvEntries(provider config.Provider) []buildEnvEntry {
	var entries []buildEnvEntry
	for k, v := range provider.GetAllBuildEnv() {
		entries = append(entries, buildEnvEntry{k, v, "literal"})
	}
	for k, v := range provider.GetAllBuildEnvFromSecrets() {
		entries = append(entries, buildEnvEntry{k, v, "secret"})
	}
	for _, k := range provider.GetAllBuildEnvFromShell() {
		entries = append(entries, buildEnvEntry{k, "", "shell"})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries
}

func (e buildEnvEntry) String() string {
	switch e.source {
	case "secret":
		return fmt.Sprintf("%s=<from-secret:%s>", e.key, e.value)
	case "shell":
		return fmt.Sprintf("%s=<from-env>", e.key)
	default:
		return fmt.Sprintf("%s=%s", e.key, e.value)
	}
}

func getBuildEnvCmdFunc(_ *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()

	if len(args) == 1 {
		key := args[0]
		if value, exists := provider.GetBuildEnv(key); exists {
			fmt.Printf("%s=%s\n", key, value)
		} else if secretName, exists := provider.GetBuildEnvFromSecret(key); exists {
			fmt.Printf("%s=<from-secret:%s>\n", key, secretName)
		} else if provider.GetBuildEnvFromShell(key) {
			fmt.Printf("%s=<from-env>\n", key)
		} else {
			fmt.Printf("Build environment variable %s is not configured.\n", key)
		}
		return nil
	}

	entries := getAllBuildEnvEntries(provider)
	if len(entries) == 0 {
		fmt.Println("No build environment variables are configured.")
		return nil
	}

	fmt.Println("Configured build environment variables:")
	for _, e := range entries {
		fmt.Printf("  %s\n", e)
	}
	return nil
}

func unsetBuildEnvCmdFunc(_ *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()

	if unsetBuildEnvAll {
		entries := getAllBuildEnvEntries(provider)
		if len(entries) == 0 {
			fmt.Println("No build environment variables are configured.")
			return nil
		}
		for _, e := range entries {
			if err := unsetBuildEnvBySource(provider, e.key, e.source); err != nil {
				return err
			}
		}
		fmt.Printf("Successfully removed %d build environment variable(s).\n", len(entries))
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("please specify a KEY to remove or use --all to remove all variables")
	}

	key := args[0]
	if _, exists := provider.GetBuildEnv(key); exists {
		return unsetBuildEnvBySource(provider, key, "literal")
	}
	if _, exists := provider.GetBuildEnvFromSecret(key); exists {
		return unsetBuildEnvBySource(provider, key, "secret")
	}
	if provider.GetBuildEnvFromShell(key) {
		return unsetBuildEnvBySource(provider, key, "shell")
	}
	fmt.Printf("Build environment variable %s is not configured.\n", key)
	return nil
}

func unsetBuildEnvBySource(provider config.Provider, key, source string) error {
	var err error
	switch source {
	case "literal":
		err = provider.UnsetBuildEnv(key)
	case "secret":
		err = provider.UnsetBuildEnvFromSecret(key)
	case "shell":
		err = provider.UnsetBuildEnvFromShell(key)
	}
	if err != nil {
		return fmt.Errorf("failed to remove %s: %w", key, err)
	}
	fmt.Printf("Successfully removed build environment variable: %s\n", key)
	return nil
}

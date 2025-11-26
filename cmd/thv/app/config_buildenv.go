package app

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
)

var (
	unsetBuildEnvAll bool
)

var setBuildEnvCmd = &cobra.Command{
	Use:   "set-build-env <KEY> <value>",
	Short: "Set a build environment variable for protocol builds",
	Long: `Set a build environment variable that will be injected into Dockerfiles
during protocol builds (npx://, uvx://, go://). This is useful for configuring
custom package mirrors in corporate environments.

Environment variable names must:
- Start with an uppercase letter
- Contain only uppercase letters, numbers, and underscores
- Not be a reserved system variable (PATH, HOME, etc.)

Common use cases:
- NPM_CONFIG_REGISTRY: Custom npm registry URL
- PIP_INDEX_URL: Custom PyPI index URL
- UV_DEFAULT_INDEX: Custom uv package index URL
- GOPROXY: Custom Go module proxy URL
- GOPRIVATE: Private Go module paths

Examples:
  thv config set-build-env NPM_CONFIG_REGISTRY https://npm.corp.example.com
  thv config set-build-env PIP_INDEX_URL https://pypi.corp.example.com/simple
  thv config set-build-env GOPROXY https://goproxy.corp.example.com
  thv config set-build-env GOPRIVATE "github.com/myorg/*"`,
	Args: cobra.ExactArgs(2),
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

	// Add --all flag to unset command
	unsetBuildEnvCmd.Flags().BoolVar(
		&unsetBuildEnvAll,
		"all",
		false,
		"Remove all build environment variables",
	)
}

func setBuildEnvCmdFunc(_ *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]

	provider := config.NewDefaultProvider()
	if err := provider.SetBuildEnv(key, value); err != nil {
		return fmt.Errorf("failed to set build environment variable: %w", err)
	}

	fmt.Printf("Successfully set build environment variable: %s\n", key)
	return nil
}

func getBuildEnvCmdFunc(_ *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()

	if len(args) == 1 {
		// Get specific variable
		key := args[0]
		value, exists := provider.GetBuildEnv(key)
		if !exists {
			fmt.Printf("Build environment variable %s is not configured.\n", key)
			return nil
		}
		fmt.Printf("%s=%s\n", key, value)
		return nil
	}

	// Get all variables
	envVars := provider.GetAllBuildEnv()
	if len(envVars) == 0 {
		fmt.Println("No build environment variables are configured.")
		return nil
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("Configured build environment variables:")
	for _, k := range keys {
		fmt.Printf("  %s=%s\n", k, envVars[k])
	}
	return nil
}

func unsetBuildEnvCmdFunc(_ *cobra.Command, args []string) error {
	provider := config.NewDefaultProvider()

	if unsetBuildEnvAll {
		envVars := provider.GetAllBuildEnv()
		if len(envVars) == 0 {
			fmt.Println("No build environment variables are configured.")
			return nil
		}

		if err := provider.UnsetAllBuildEnv(); err != nil {
			return fmt.Errorf("failed to remove build environment variables: %w", err)
		}

		fmt.Printf("Successfully removed %d build environment variable(s).\n", len(envVars))
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("please specify a KEY to remove or use --all to remove all variables")
	}

	key := args[0]
	_, exists := provider.GetBuildEnv(key)
	if !exists {
		fmt.Printf("Build environment variable %s is not configured.\n", key)
		return nil
	}

	if err := provider.UnsetBuildEnv(key); err != nil {
		return fmt.Errorf("failed to remove build environment variable: %w", err)
	}

	fmt.Printf("Successfully removed build environment variable: %s\n", key)
	return nil
}

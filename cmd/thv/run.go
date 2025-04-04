package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
)

var runCmd = &cobra.Command{
	Use:   "run [flags] SERVER_OR_IMAGE [-- ARGS...]",
	Short: "Run an MCP server",
	Long: `Run an MCP server in a container with the specified server name or image and arguments.
If a server name is provided, it will first try to find it in the registry.
If found, it will use the registry defaults for transport, permissions, etc.
If not found, it will treat the argument as a Docker image and run it directly.
The container will be started with minimal permissions and the specified transport mode.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCmdFunc,
}

var (
	runTransport         string
	runName              string
	runPort              int
	runTargetPort        int
	runTargetHost        string
	runPermissionProfile string
	runEnv               []string
	runForeground        bool
	runVolumes           []string
	runSecrets           []string
	runAuthzConfig       string
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "stdio", "Transport mode (sse or stdio)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().IntVar(&runTargetPort, "target-port", 0, "Port for the container to expose (only applicable to SSE transport)")
	runCmd.Flags().StringVar(
		&runTargetHost,
		"target-host",
		"localhost",
		"Host to forward traffic to (only applicable to SSE transport)")
	runCmd.Flags().StringVar(
		&runPermissionProfile,
		"permission-profile",
		"stdio",
		"Permission profile to use (stdio, network, or path to JSON file)",
	)
	runCmd.Flags().StringArrayVarP(
		&runEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	runCmd.Flags().BoolVarP(&runForeground, "foreground", "f", false, "Run in foreground mode (block until container exits)")
	runCmd.Flags().StringArrayVarP(
		&runVolumes,
		"volume",
		"v",
		[]string{},
		"Mount a volume into the container (format: host-path:container-path[:ro])",
	)
	runCmd.Flags().StringArrayVar(
		&runSecrets,
		"secret",
		[]string{},
		"Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)",
	)
	runCmd.Flags().StringVar(
		&runAuthzConfig,
		"authz-config",
		"",
		"Path to the authorization configuration file",
	)

	// Add OIDC validation flags
	AddOIDCFlags(runCmd)
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	// Get the server name or image and command arguments
	serverOrImage := args[0]
	cmdArgs := args[1:]

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Get OIDC flag values
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Initialize a new RunConfig with values from command-line flags
	config := runner.NewRunConfigFromFlags(
		runtime,
		cmdArgs,
		runName,
		debugMode,
		runVolumes,
		runSecrets,
		runAuthzConfig,
		runPermissionProfile,
		runTargetHost,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
	)

	// Try to find the server in the registry
	server, err := registry.GetServer(serverOrImage)

	// Set the image based on whether we found a registry entry
	if err == nil {
		// Server found in registry
		logDebug(debugMode, "Found server '%s' in registry", serverOrImage)

		// Apply registry settings to config
		applyRegistrySettings(cmd, serverOrImage, server, config, debugMode)
	} else {
		// Server not found in registry, treat as direct image
		logDebug(debugMode, "Server '%s' not found in registry, treating as Docker image", serverOrImage)
		config.Image = serverOrImage
	}

	// Check if the image exists locally, and pull it if not
	imageExists, err := runtime.ImageExists(ctx, config.Image)
	if err != nil {
		return fmt.Errorf("failed to check if image exists: %v", err)
	}
	if !imageExists {
		fmt.Printf("Image %s not found locally, pulling...\n", config.Image)
		if err := runtime.PullImage(ctx, config.Image); err != nil {
			return fmt.Errorf("failed to pull image: %v", err)
		}
		fmt.Printf("Successfully pulled image: %s\n", config.Image)
	}

	// Configure the RunConfig with transport, ports, permissions, etc.
	if err := configureRunConfig(cmd, config, runTransport, runPort, runTargetPort, runTargetHost, runEnv); err != nil {
		return err
	}

	// Run the MCP server
	return RunMCPServer(ctx, cmd, config, runForeground)
}

// applyRegistrySettings applies settings from a registry server to the run config
func applyRegistrySettings(
	cmd *cobra.Command,
	serverName string,
	server *registry.Server,
	config *runner.RunConfig,
	debugMode bool,
) {
	// Use the image from the registry
	config.Image = server.Image

	// If name is not provided, use the server name from registry
	if config.Name == "" {
		config.Name = serverName
	}

	// Use registry transport if not overridden
	if !cmd.Flags().Changed("transport") {
		logDebug(debugMode, "Using registry transport: %s", server.Transport)
		// The actual transport setting will be handled by configureRunConfig
	} else {
		logDebug(debugMode, "Using provided transport: %s (overriding registry default: %s)",
			runTransport, server.Transport)
	}

	// Process environment variables from registry
	// This will be merged with command-line env vars in configureRunConfig
	envVarStrings := processEnvironmentVariables(server.EnvVars, runEnv, config.Secrets, debugMode)
	runEnv = envVarStrings

	// Create a temporary file for the permission profile if not explicitly provided
	if !cmd.Flags().Changed("permission-profile") {
		permProfilePath, err := createPermissionProfileFile(serverName, server.Permissions, debugMode)
		if err != nil {
			// Just log the error and continue with the default permission profile
			fmt.Printf("Warning: Failed to create permission profile file: %v\n", err)
		} else {
			// Update the permission profile path
			config.PermissionProfileNameOrPath = permProfilePath
		}
	}
}

// processEnvironmentVariables processes environment variables from the registry
func processEnvironmentVariables(
	registryEnvVars []*registry.EnvVar,
	cmdEnvVars []string,
	secrets []string,
	debugMode bool,
) []string {
	// Create a new slice with capacity for all env vars
	envVars := make([]string, 0, len(cmdEnvVars)+len(registryEnvVars))

	// Copy existing env vars
	envVars = append(envVars, cmdEnvVars...)

	// Add required environment variables from registry if not already provided
	for _, envVar := range registryEnvVars {
		// Check if the environment variable is already provided
		found := isEnvVarProvided(envVar.Name, envVars, secrets)

		if !found {
			if envVar.Required {
				// Ask the user for the required environment variable
				fmt.Printf("Required environment variable: %s (%s)\n", envVar.Name, envVar.Description)
				fmt.Printf("Enter value for %s: ", envVar.Name)
				var value string
				if _, err := fmt.Scanln(&value); err != nil {
					fmt.Printf("Warning: Failed to read input: %v\n", err)
				}

				if value != "" {
					envVars = append(envVars, fmt.Sprintf("%s=%s", envVar.Name, value))
				}
			} else if envVar.Default != "" {
				// Apply default value for non-required environment variables
				envVars = append(envVars, fmt.Sprintf("%s=%s", envVar.Name, envVar.Default))
				logDebug(debugMode, "Using default value for %s: %s", envVar.Name, envVar.Default)
			}
		}
	}

	return envVars
}

// isEnvVarProvided checks if an environment variable is already provided
func isEnvVarProvided(name string, envVars []string, secrets []string) bool {
	// Check if the environment variable is already provided in the command line
	for _, env := range envVars {
		if strings.HasPrefix(env, name+"=") {
			return true
		}
	}

	// Check if the environment variable is provided as a secret
	return findEnvironmentVariableFromSecrets(secrets, name)
}

// createPermissionProfileFile creates a temporary file with the permission profile
func createPermissionProfileFile(serverName string, permProfile *permissions.Profile, debugMode bool) (string, error) {
	tempFile, err := os.CreateTemp("", fmt.Sprintf("toolhive-%s-permissions-*.json", serverName))
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %v", err)
	}
	defer tempFile.Close()

	// Get the temporary file path
	permProfilePath := tempFile.Name()

	// Serialize the permission profile to JSON
	permProfileJSON, err := json.Marshal(permProfile)
	if err != nil {
		return "", fmt.Errorf("failed to serialize permission profile: %v", err)
	}

	// Write the permission profile to the temporary file
	if _, err := tempFile.Write(permProfileJSON); err != nil {
		return "", fmt.Errorf("failed to write permission profile to file: %v", err)
	}

	logDebug(debugMode, "Wrote permission profile to temporary file: %s", permProfilePath)

	return permProfilePath, nil
}

// logDebug logs a message if debug mode is enabled
func logDebug(debugMode bool, format string, args ...interface{}) {
	if debugMode {
		fmt.Printf(format+"\n", args...)
	}
}

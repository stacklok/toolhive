package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/verifier"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var runCmd = &cobra.Command{
	Use:   "run [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]",
	Short: "Run an MCP server",
	Long: `Run an MCP server with the specified name, image, or protocol scheme.

ToolHive supports three ways to run an MCP server:

1. From the registry:
   $ thv run server-name [-- args...]
   Looks up the server in the registry and uses its predefined settings
   (transport, permissions, environment variables, etc.)

2. From a container image:
   $ thv run ghcr.io/example/mcp-server:latest [-- args...]
   Runs the specified container image directly with the provided arguments

3. Using a protocol scheme:
   $ thv run uvx://package-name [-- args...]
   $ thv run npx://package-name [-- args...]
   $ thv run go://package-name [-- args...]
   $ thv run go://./local-path [-- args...]
   Automatically generates a container that runs the specified package
   using either uvx (Python with uv package manager), npx (Node.js),
   or go (Golang). For Go, you can also specify local paths starting
   with './' or '../' to build and run local Go projects.

The container will be started with the specified transport mode and
permission profile. Additional configuration can be provided via flags.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCmdFunc,
	// Ignore unknown flags to allow passing flags to the MCP server
	FParseErrWhitelist: cobra.FParseErrWhitelist{
		UnknownFlags: true,
	},
}

const (
	verifyImageWarn     = "warn"
	verifyImageEnabled  = "enabled"
	verifyImageDisabled = "disabled"
	verifyImageDefault  = verifyImageWarn
)

var (
	runTransport         string
	runName              string
	runHost              string
	runPort              int
	runTargetPort        int
	runTargetHost        string
	runPermissionProfile string
	runEnv               []string
	runForeground        bool
	runVolumes           []string
	runSecrets           []string
	runAuthzConfig       string
	runAuditConfig       string
	runEnableAudit       bool
	runK8sPodPatch       string
	runCACertPath        string
	runVerifyImage       string
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "stdio", "Transport mode (sse or stdio)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().StringVar(&runHost, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().IntVar(&runTargetPort, "target-port", 0, "Port for the container to expose (only applicable to SSE transport)")
	runCmd.Flags().StringVar(
		&runTargetHost,
		"target-host",
		transport.LocalhostIPv4,
		"Host to forward traffic to (only applicable to SSE transport)")
	runCmd.Flags().StringVar(
		&runPermissionProfile,
		"permission-profile",
		permissions.ProfileNetwork,
		"Permission profile to use (none, network, or path to JSON file)",
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
	runCmd.Flags().StringVar(
		&runAuditConfig,
		"audit-config",
		"",
		"Path to the audit configuration file",
	)
	runCmd.Flags().BoolVar(
		&runEnableAudit,
		"enable-audit",
		false,
		"Enable audit logging with default configuration",
	)
	runCmd.Flags().StringVar(
		&runK8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	runCmd.Flags().StringVar(
		&runCACertPath,
		"ca-cert",
		"",
		"Path to a custom CA certificate file to use for container builds",
	)
	runCmd.Flags().StringVar(
		&runVerifyImage,
		"image-verification",
		verifyImageDefault,
		fmt.Sprintf("Set image verification mode (%s, %s, %s)", verifyImageWarn, verifyImageEnabled, verifyImageDisabled),
	)

	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}

	// Add OIDC validation flags
	AddOIDCFlags(runCmd)
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(runHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", runHost)
	}
	runHost = validatedHost

	// Get the server name or image
	serverOrImage := args[0]

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Infof("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Get OIDC flag values
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Initialize a new RunConfig with values from command-line flags
	runConfig := runner.NewRunConfigFromFlags(
		rt,
		cmdArgs,
		runName,
		runHost,
		debugMode,
		runVolumes,
		runSecrets,
		runAuthzConfig,
		runAuditConfig,
		runEnableAudit,
		runPermissionProfile,
		runTargetHost,
		oidcIssuer,
		oidcAudience,
		oidcJwksURL,
		oidcClientID,
	)

	// Set the Kubernetes pod template patch if provided
	if runK8sPodPatch != "" {
		runConfig.K8sPodTemplatePatch = runK8sPodPatch
	}

	var server *registry.Server

	// Check if the serverOrImage is a protocol scheme, e.g., uvx://, npx://, or go://
	if runner.IsImageProtocolScheme(serverOrImage) {
		logger.Debugf("Detected protocol scheme: %s", serverOrImage)
		// Process the protocol scheme and build the image
		caCertPath := resolveCACertPath(runCACertPath)
		generatedImage, err := runner.HandleProtocolScheme(ctx, rt, serverOrImage, caCertPath)
		if err != nil {
			return fmt.Errorf("failed to process protocol scheme: %v", err)
		}
		// Update the image in the runConfig with the generated image
		logger.Debugf("Using built image: %s instead of %s", generatedImage, serverOrImage)
		runConfig.Image = generatedImage
	} else {
		logger.Debugf("No protocol scheme detected, using image: %s", serverOrImage)
		// Try to find the server in the registry
		server, err = registry.GetServer(serverOrImage)
		if err != nil {
			// Server isn't found in registry, treat as direct image
			logger.Debugf("Server '%s' not found in registry: %v", serverOrImage, err)
			runConfig.Image = serverOrImage
		} else {
			// Server found in registry
			logger.Debugf("Found server '%s' in registry: %v", serverOrImage, server)
			// Apply registry settings to runConfig
			applyRegistrySettings(ctx, cmd, serverOrImage, server, runConfig, debugMode)
		}
	}

	// Verify the image against the expected provenance info (if applicable)
	if err := verifyImage(ctx, runConfig.Image, rt, server, runVerifyImage); err != nil {
		return err
	}

	// Pull the image if necessary
	if err := pullImage(ctx, runConfig.Image, rt); err != nil {
		return fmt.Errorf("failed to retrieve or pull image: %v", err)
	}

	// Configure the RunConfig with transport, ports, permissions, etc.
	if err := configureRunConfig(runConfig, runTransport, runPort, runTargetPort, runEnv); err != nil {
		return err
	}

	// Run the MCP server
	return RunMCPServer(ctx, runConfig, runForeground)
}

// pullImage pulls an image from a remote registry if it has the "latest" tag
// or if it doesn't exist locally. If the image is a local image, it will not be pulled.
// If the image has the latest tag, it will be pulled to ensure we have the most recent version.
// however, if there is a failure in pulling the "latest" tag, it will check if the image exists locally
// as it is possible that the image was locally built.
func pullImage(ctx context.Context, image string, rt runtime.Runtime) error {
	// Check if the image has the "latest" tag
	isLatestTag := hasLatestTag(image)

	if isLatestTag {
		// For "latest" tag, try to pull first
		logger.Infof("Image %s has 'latest' tag, pulling to ensure we have the most recent version...", image)
		err := rt.PullImage(ctx, image)
		if err != nil {
			// Pull failed, check if it exists locally
			logger.Infof("Pull failed, checking if image exists locally: %s", image)
			imageExists, checkErr := rt.ImageExists(ctx, image)
			if checkErr != nil {
				return fmt.Errorf("failed to check if image exists: %v", checkErr)
			}

			if imageExists {
				logger.Debugf("Using existing local image: %s", image)
			} else {
				return fmt.Errorf("failed to pull image from remote registry and image doesn't exist locally. %v", err)
			}
		} else {
			logger.Infof("Successfully pulled image: %s", image)
		}
	} else {
		// For non-latest tags, check locally first
		logger.Debugf("Checking if image exists locally: %s", image)
		imageExists, err := rt.ImageExists(ctx, image)
		logger.Debugf("ImageExists locally: %t", imageExists)
		if err != nil {
			return fmt.Errorf("failed to check if image exists locally: %v", err)
		}

		if imageExists {
			logger.Debugf("Using existing local image: %s", image)
		} else {
			// Image doesn't exist locally, try to pull
			logger.Infof("Image %s not found locally, pulling...", image)
			if err := rt.PullImage(ctx, image); err != nil {
				return fmt.Errorf("failed to pull image: %v", err)
			}
			logger.Infof("Successfully pulled image: %s", image)
		}
	}

	return nil
}

// verifyImage verifies the image using the specified verification setting (warn, enabled, or disabled)
func verifyImage(ctx context.Context, image string, rt runtime.Runtime, server *registry.Server, verifySetting string) error {
	switch verifySetting {
	case verifyImageDisabled:
		logger.Warn("Image verification is disabled")
	case verifyImageWarn, verifyImageEnabled:
		isSafe, err := rt.VerifyImage(ctx, server, image)
		if err != nil {
			// This happens if we have no provenance entry in the registry for this server.
			// Not finding provenance info in the registry is not a fatal error if the setting is "warn".
			if errors.Is(err, verifier.ErrProvenanceServerInformationNotSet) && verifySetting == verifyImageWarn {
				logger.Warnf("⚠️  MCP server %s has no provenance information set, skipping image verification", image)
				return nil
			}
			return fmt.Errorf("❌ image verification failed: %v", err)
		}
		if !isSafe {
			if verifySetting == verifyImageWarn {
				logger.Warnf("❌ MCP server %s failed image verification", image)
			} else {
				return fmt.Errorf("❌ MCP server %s failed image verification", image)
			}
		} else {
			logger.Infof("✅ MCP server %s is verified successfully", image)
		}
	default:
		return fmt.Errorf("invalid value for --image-verification: %s", verifySetting)
	}
	return nil
}

// applyRegistrySettings applies settings from a registry server to the run config
func applyRegistrySettings(
	ctx context.Context,
	cmd *cobra.Command,
	serverName string,
	server *registry.Server,
	runConfig *runner.RunConfig,
	debugMode bool,
) {
	// Use the image from the registry
	runConfig.Image = server.Image

	// If name is not provided, use the server name from registry
	if runConfig.Name == "" {
		runConfig.Name = serverName
	}

	// Use registry transport if not overridden
	if !cmd.Flags().Changed("transport") {
		logDebug(debugMode, "Using registry transport: %s", server.Transport)
		runTransport = server.Transport
	} else {
		logDebug(debugMode, "Using provided transport: %s (overriding registry default: %s)",
			runTransport, server.Transport)
	}

	// Use registry target port if not overridden and transport is SSE
	if !cmd.Flags().Changed("target-port") && server.Transport == "sse" && server.TargetPort > 0 {
		logDebug(debugMode, "Using registry target port: %d", server.TargetPort)
		runTargetPort = server.TargetPort
	}

	// Prepend registry args to command-line args if available
	if len(server.Args) > 0 {
		logDebug(debugMode, "Prepending registry args: %v", server.Args)
		runConfig.CmdArgs = append(server.Args, runConfig.CmdArgs...)
	}

	// Process environment variables from registry
	// This will be merged with command-line env vars in configureRunConfig
	envVarStrings, secretStrings := processEnvironmentVariables(ctx, server.EnvVars, runEnv, runConfig.Secrets, debugMode)
	runEnv = envVarStrings
	runConfig.Secrets = secretStrings

	// Create a temporary file for the permission profile if not explicitly provided
	if !cmd.Flags().Changed("permission-profile") {
		permProfilePath, err := workloads.CreatePermissionProfileFile(serverName, server.Permissions)
		if err != nil {
			// Just log the error and continue with the default permission profile
			logger.Warnf("Warning: Failed to create permission profile file: %v", err)
		} else {
			// Update the permission profile path
			runConfig.PermissionProfileNameOrPath = permProfilePath
		}
	}
}

// processEnvironmentVariables processes environment variables from the registry
// Returns two lists: regular environment variables and secrets
func processEnvironmentVariables(
	ctx context.Context,
	registryEnvVars []*registry.EnvVar,
	cmdEnvVars []string,
	secretsConfig []string,
	debugMode bool,
) ([]string, []string) {
	// Create a new slice with capacity for all env vars
	envVars := make([]string, 0, len(cmdEnvVars)+len(registryEnvVars))
	secretsList := make([]string, 0, len(secretsConfig))

	// Copy existing env vars and secrets
	envVars = append(envVars, cmdEnvVars...)
	secretsList = append(secretsList, secretsConfig...)

	// Initialize secrets manager if needed
	secretsManager := initializeSecretsManagerIfNeeded(registryEnvVars)

	// Process each environment variable from the registry
	for _, envVar := range registryEnvVars {
		if isEnvVarProvided(envVar.Name, envVars, secretsList) {
			continue
		}

		if envVar.Required {
			value, err := promptForEnvironmentVariable(envVar)
			if err != nil {
				logger.Warnf("Warning: Failed to read input for %s: %v", envVar.Name, err)
				continue
			}
			if value != "" {
				addEnvironmentVariable(ctx, envVar, value, secretsManager, &envVars, &secretsList, debugMode)
			}
		} else if envVar.Default != "" {
			addEnvironmentVariable(ctx, envVar, envVar.Default, secretsManager, &envVars, &secretsList, debugMode)
		}
	}

	return envVars, secretsList
}

// initializeSecretsManagerIfNeeded initializes the secrets manager if there are secret environment variables
func initializeSecretsManagerIfNeeded(registryEnvVars []*registry.EnvVar) secrets.Provider {
	// Check if we have any secret environment variables
	hasSecrets := false
	for _, envVar := range registryEnvVars {
		if envVar.Secret {
			hasSecrets = true
			break
		}
	}

	if !hasSecrets {
		return nil
	}

	secretsManager, err := getSecretsManager()
	if err != nil {
		logger.Warnf("Warning: Failed to initialize secrets manager: %v", err)
		logger.Warnf("Secret environment variables will be stored as regular environment variables")
		return nil
	}

	return secretsManager
}

// promptForEnvironmentVariable prompts the user for an environment variable value
func promptForEnvironmentVariable(envVar *registry.EnvVar) (string, error) {
	var byteValue []byte
	var err error
	if envVar.Secret {
		logger.Infof("Required secret environment variable: %s (%s)", envVar.Name, envVar.Description)
		fmt.Printf("Enter value for %s (input will be hidden): ", envVar.Name)
		byteValue, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println() // Move to the next line after hidden input
	} else {
		logger.Infof("Required environment variable: %s (%s)", envVar.Name, envVar.Description)
		fmt.Printf("Enter value for %s: ", envVar.Name)
		// For non-secret input, we can use a simple fmt.Scanln or bufio.Scanner
		var input string
		_, err = fmt.Scanln(&input)
		if err != nil {
			return "", fmt.Errorf("failed to read input for %s: %v", envVar.Name, err)
		}
		byteValue = []byte(input)
	}

	if err != nil {
		return "", fmt.Errorf("failed to read input for %s: %v", envVar.Name, err)
	}

	return strings.TrimSpace(string(byteValue)), nil
}

// addEnvironmentVariable adds an environment variable or secret to the appropriate list
func addEnvironmentVariable(
	ctx context.Context,
	envVar *registry.EnvVar,
	value string,
	secretsManager secrets.Provider,
	envVars *[]string,
	secretsList *[]string,
	debugMode bool,
) {
	if envVar.Secret && secretsManager != nil {
		addAsSecret(ctx, envVar, value, secretsManager, secretsList, envVars, debugMode)
	} else {
		addAsEnvironmentVariable(envVar, value, envVars, debugMode)
	}
}

// addAsSecret stores the value as a secret and adds a secret reference
func addAsSecret(
	ctx context.Context,
	envVar *registry.EnvVar,
	value string,
	secretsManager secrets.Provider,
	secretsList *[]string,
	envVars *[]string,
	debugMode bool,
) {
	var secretName string
	if envVar.Required {
		secretName = fmt.Sprintf("registry-user-%s", strings.ToLower(envVar.Name))
	} else {
		secretName = fmt.Sprintf("registry-default-%s", strings.ToLower(envVar.Name))
	}

	if err := secretsManager.SetSecret(ctx, secretName, value); err != nil {
		logger.Warnf("Warning: Failed to store secret %s: %v", secretName, err)
		logger.Warnf("Falling back to environment variable for %s", envVar.Name)
		*envVars = append(*envVars, fmt.Sprintf("%s=%s", envVar.Name, value))
		logDebug(debugMode, "Added environment variable (secret fallback): %s", envVar.Name)
	} else {
		// Create secret reference for RunConfig
		secretEntry := fmt.Sprintf("%s,target=%s", secretName, envVar.Name)
		*secretsList = append(*secretsList, secretEntry)
		if envVar.Required {
			logDebug(debugMode, "Created secret for %s: %s", envVar.Name, secretName)
		} else {
			logDebug(debugMode, "Created secret with default value for %s: %s", envVar.Name, secretName)
		}
	}
}

// addAsEnvironmentVariable adds the value as a regular environment variable
func addAsEnvironmentVariable(
	envVar *registry.EnvVar,
	value string,
	envVars *[]string,
	debugMode bool,
) {
	*envVars = append(*envVars, fmt.Sprintf("%s=%s", envVar.Name, value))

	if envVar.Secret {
		if envVar.Required {
			logDebug(debugMode, "Added secret as environment variable (no secrets manager): %s", envVar.Name)
		} else {
			logDebug(debugMode, "Added default secret as environment variable (no secrets manager): %s", envVar.Name)
		}
	} else {
		if envVar.Required {
			logDebug(debugMode, "Added environment variable: %s", envVar.Name)
		} else {
			logDebug(debugMode, "Using default value for %s: %s", envVar.Name, value)
		}
	}
}

// isEnvVarProvided checks if an environment variable is already provided
func isEnvVarProvided(name string, envVars []string, secretsConfig []string) bool {
	// Check if the environment variable is already provided in the command line
	for _, env := range envVars {
		if strings.HasPrefix(env, name+"=") {
			return true
		}
	}

	// Check if the environment variable is provided as a secret
	return findEnvironmentVariableFromSecrets(secretsConfig, name)
}

// hasLatestTag checks if the given image reference has the "latest" tag or no tag (which defaults to "latest")
func hasLatestTag(imageRef string) bool {
	ref, err := nameref.ParseReference(imageRef)
	if err != nil {
		// If we can't parse the reference, assume it's not "latest"
		logger.Warnf("Warning: Failed to parse image reference: %v", err)
		return false
	}

	// Check if the reference is a tag
	if taggedRef, ok := ref.(nameref.Tag); ok {
		// Check if the tag is "latest"
		return taggedRef.TagStr() == "latest"
	}

	// If the reference is not a tag (e.g., it's a digest), it's not "latest"
	// If no tag was specified, it defaults to "latest"
	_, isDigest := ref.(nameref.Digest)
	return !isDigest
}

// logDebug logs a message if debug mode is enabled
func logDebug(debugMode bool, format string, args ...interface{}) {
	if debugMode {
		logger.Infof(format+"", args...)
	}
}

// parseCommandArguments processes command-line arguments to find everything after the -- separator
// which are the arguments to be passed to the MCP server
func parseCommandArguments(args []string) []string {
	var cmdArgs []string
	for i, arg := range args {
		if arg == "--" && i < len(args)-1 {
			// Found the separator, take everything after it
			cmdArgs = args[i+1:]
			break
		}
	}
	return cmdArgs
}

// ValidateAndNormaliseHostFlag validates and normalizes the host flag resolving it to an IP address if hostname is provided
func ValidateAndNormaliseHostFlag(host string) (string, error) {
	// Check if the host is a valid IP address
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.To4() == nil {
			return "", fmt.Errorf("IPv6 addresses are not supported: %s", host)
		}
		return host, nil
	}

	// If not an IP address, resolve the hostname to an IP address
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("invalid host: %s", host)
	}

	// Use the first IPv4 address found
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil {
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("could not resolve host: %s", host)
}

// resolveCACertPath determines the CA certificate path to use, prioritizing command-line flag over configuration
func resolveCACertPath(flagValue string) string {
	// If command-line flag is provided, use it (highest priority)
	if flagValue != "" {
		return flagValue
	}

	// Otherwise, check configuration
	cfg := config.GetConfig()
	if cfg.CACertificatePath != "" {
		logger.Debugf("Using configured CA certificate: %s", cfg.CACertificatePath)
		return cfg.CACertificatePath
	}

	// No CA certificate configured
	return ""
}

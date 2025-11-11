package app

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	regtypes "github.com/stacklok/toolhive/pkg/registry/types"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var runCmd *cobra.Command
var runFlags proxyRunFlags

// NewRunCmd creates a new run command for testing
func NewRunCmd() *cobra.Command {
	return &cobra.Command{
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

	The container will be started with the specified transport mode and
	permission profile. Additional configuration can be provided via flags.`,
		Args: cobra.MinimumNArgs(1),
		RunE: runCmdFunc,
		// Ignore unknown flags to allow passing flags to the MCP server
		FParseErrWhitelist: cobra.FParseErrWhitelist{
			UnknownFlags: true,
		},
	}
}

type proxyRunFlags struct {
	runK8sPodPatch string
}

func addRunFlags(runCmd *cobra.Command, runFlags *proxyRunFlags) {
	runCmd.Flags().StringVar(
		&runFlags.runK8sPodPatch,
		"k8s-pod-patch",
		"",
		"JSON string to patch the Kubernetes pod template (only applicable when using Kubernetes runtime)",
	)
	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}
}

func init() {
	runCmd = NewRunCmd()
	addRunFlags(runCmd, &runFlags)
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Common setup for both execution paths
	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Select an env var validation strategy depending on how the CLI is run:
	// If we have called the CLI directly, we use the CLIEnvVarValidator.
	// If we are running in detached mode, or the CLI is wrapped by the K8s operator,
	// we use the DetachedEnvVarValidator.
	envVarValidator := &runner.DetachedEnvVarValidator{}

	var imageMetadata *regtypes.ImageMetadata

	// Get the name of the MCP server to run.
	// This may be a server name from the registry, a container image, or a protocol scheme.
	mcpServerImage := args[0]

	// Always try to load runconfig.json from filesystem first
	fileBasedConfig, err := tryLoadConfigFromFile()
	if err != nil {
		logger.Debugf("No configuration file found or failed to load: %v", err)
		// Continue without configuration file - will use flags instead
	}
	logger.Infof("Auto-discovered and loaded configuration from runconfig.json file")
	// Use simplified approach: when config file exists, use it directly and only apply essential flags
	return runWithFileBasedConfig(ctx, cmd, mcpServerImage, fileBasedConfig, rt, debugMode, envVarValidator, imageMetadata)
}

// Standard configuration file paths for runconfig.json
// These paths match the volume mount paths used by the Kubernetes operator
const (
	kubernetesRunConfigPath = "/etc/runconfig/runconfig.json" // Primary path for K8s ConfigMap volume mounts
	systemRunConfigPath     = "/etc/toolhive/runconfig.json"  // System-wide configuration path
	localRunConfigPath      = "./runconfig.json"              // Local directory fallback
)

// tryLoadConfigFromFile attempts to load runconfig.json from standard file locations
func tryLoadConfigFromFile() (*runner.RunConfig, error) {
	// Standard locations where runconfig.json might be mounted or placed
	configPaths := []string{
		kubernetesRunConfigPath,
		systemRunConfigPath,
		localRunConfigPath,
	}

	for _, path := range configPaths {
		if _, err := os.Stat(path); err != nil {
			continue // File doesn't exist, try next location
		}

		logger.Debugf("Found configuration file at %s", path)

		// Security: Only read from predefined safe paths to avoid path traversal
		file, err := os.Open(path) // #nosec G304 - path is from predefined safe list
		if err != nil {
			return nil, fmt.Errorf("found config file at %s but failed to open: %w", path, err)
		}
		defer file.Close()

		// Use existing runner.ReadJSON function for consistency
		runConfig, err := runner.ReadJSON(file)
		if err != nil {
			return nil, fmt.Errorf("found config file at %s but failed to parse JSON: %w", path, err)
		}

		logger.Infof("Successfully loaded configuration from %s", path)
		return runConfig, nil
	}

	// No configuration file found
	return nil, fmt.Errorf("configuration file required but no configuration file was found")
}

// runWithFileBasedConfig handles execution when a runconfig.json file is found.
// Uses config from file exactly as-is, ignoring all CLI configuration flags.
// Only uses essential non-configuration inputs: image, command args, and --k8s-pod-patch.
func runWithFileBasedConfig(
	ctx context.Context,
	cmd *cobra.Command,
	mcpServerImage string,
	config *runner.RunConfig,
	rt runtime.Runtime,
	debugMode bool,
	envVarValidator runner.EnvVarValidator,
	imageMetadata *regtypes.ImageMetadata,
) error {
	// Use the file config directly with minimal essential overrides
	config.Image = mcpServerImage
	config.Deployer = rt
	config.Debug = debugMode

	// Apply --k8s-pod-patch flag if provided (essential for K8s operation)
	if cmd.Flags().Changed("k8s-pod-patch") && runFlags.runK8sPodPatch != "" {
		config.K8sPodTemplatePatch = runFlags.runK8sPodPatch
	}

	// Validate environment variables using the provided validator
	if envVarValidator != nil {
		validatedEnvVars, err := envVarValidator.Validate(ctx, imageMetadata, config, config.EnvVars)
		if err != nil {
			return fmt.Errorf("failed to validate environment variables: %v", err)
		}
		config.EnvVars = validatedEnvVars
	}

	// Process environment files from EnvFileDir if specified (e.g., for Vault secrets)
	if config.EnvFileDir != "" {
		updatedConfig, err := config.WithEnvFilesFromDirectory(config.EnvFileDir)
		if err != nil {
			return fmt.Errorf("failed to process environment files from directory %s: %v", config.EnvFileDir, err)
		}
		config = updatedConfig
	}

	// Apply image metadata overrides if needed (similar to what the builder does)
	if imageMetadata != nil && config.Name == "" {
		config.Name = imageMetadata.Name
	}

	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}
	return workloadManager.RunWorkload(ctx, config)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var runCmd = &cobra.Command{
	Use:   "run [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]",
	Short: "Run an MCP server",
	Long: `Run an MCP server with the specified name, image, or protocol scheme.

ToolHive supports five ways to run an MCP server:

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

4. From an exported configuration:

	   $ thv run --from-config <path>

   Runs an MCP server using a previously exported configuration file.

5. Remote MCP server:

	   $ thv run <URL> [--name <name>]

   Runs a remote MCP server as a workload, proxying requests to the specified URL.
   This allows remote MCP servers to be managed like local workloads with full
   support for client configuration, tool filtering, import/export, etc.

#### Dynamic client registration

When no client credentials are provided, ToolHive automatically registers an OAuth client
with the authorization server using RFC 7591 dynamic client registration:

- No need to pre-configure client ID and secret
- Automatically discovers registration endpoint via OIDC
- Supports PKCE flow for enhanced security

The container will be started with the specified transport mode and
permission profile. Additional configuration can be provided via flags.

#### Network Configuration

You can specify the network mode for the container using the --network flag:

- Host networking: $ thv run --network host <image>
- Custom network: $ thv run --network my-network <image>
- Default (bridge): $ thv run <image>

The --network flag accepts any Docker-compatible network mode.

Examples:
  # Run a server from the registry
  thv run filesystem

  # Run a server with custom arguments and toolsets
  thv run github -- --toolsets repos

  # Run from a container image
  thv run ghcr.io/github/github-mcp-server

  # Run using a protocol scheme (Python with uv)
  thv run uvx://mcp-server-git

  # Run using npx (Node.js)
  thv run npx://@modelcontextprotocol/server-everything

  # Run a server in a specific group
  thv run filesystem --group production

# Run a remote GitHub MCP server with authentication
thv run github-remote --remote-auth \
  --remote-auth-client-id <oauth-client-id> \
  --remote-auth-client-secret <oauth-client-secret>`,
	Args: func(cmd *cobra.Command, args []string) error {
		// If --from-config is provided, no args are required
		if runFlags.FromConfig != "" {
			return nil
		}
		// Otherwise, require at least 1 argument
		return cobra.MinimumNArgs(1)(cmd, args)
	},
	RunE: runCmdFunc,
	// Ignore unknown flags to allow passing flags to the MCP server
	FParseErrWhitelist: cobra.FParseErrWhitelist{
		UnknownFlags: true,
	},
}

var runFlags RunFlags

func init() {
	// Add run flags
	AddRunFlags(runCmd, &runFlags)

	runCmd.PreRunE = validateRunFlags

	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}

	// Add OIDC validation flags
	AddOIDCFlags(runCmd)
}

func cleanupAndWait(workloadManager workloads.Manager, name string) {
	// Use Background context for cleanup operations. This function is called after the
	// workload has exited, and we need a fresh context with its own timeout to ensure
	// cleanup completes successfully regardless of the parent context state.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()

	complete, err := workloadManager.DeleteWorkloads(cleanupCtx, []string{name})
	if err != nil {
		logger.Warnf("Failed to delete workload %q: %v", name, err)
	} else if complete != nil {
		if err := complete(); err != nil {
			logger.Warnf("DeleteWorkloads error for %q: %v", name, err)
		}
	}
}

// nolint:gocyclo // This function is complex by design
func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Check if we should load configuration from a file
	if runFlags.FromConfig != "" {
		return runFromConfigFile(ctx)
	}

	// Get the name of the MCP server to run.
	// This may be a server name from the registry, a container image, a protocol scheme, or a remote URL.
	var serverOrImage string
	if len(args) > 0 {
		serverOrImage = args[0]
	}

	// Check if the server name is actually a URL (remote server)
	if serverOrImage != "" && networking.IsURL(serverOrImage) {
		runFlags.RemoteURL = serverOrImage
		// If no name is given, generate a name from the URL
		if runFlags.Name == "" {
			name, err := deriveRemoteName(serverOrImage)
			if err != nil {
				return err
			}
			runFlags.Name = name
		}
	}

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Debugf("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	return runSingleServer(ctx, &runFlags, serverOrImage, cmdArgs, debugMode, cmd, "")
}

// runSingleServer handles the core logic for running a single MCP server
func runSingleServer(ctx context.Context, runFlags *RunFlags, serverOrImage string, cmdArgs []string, debugMode bool, cmd *cobra.Command, groupName string) error { //nolint:lll
	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %w", err)
	}
	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	if runFlags.Name == "" {
		runFlags.Name = getworkloadDefaultName(ctx, serverOrImage)
		logger.Debugf("No workload name specified, using generated name: %s", runFlags.Name)
	}
	exists, err := workloadManager.DoesWorkloadExist(ctx, runFlags.Name)
	if err != nil {
		return fmt.Errorf("failed to check if workload exists: %w", err)
	}
	if exists {
		return fmt.Errorf("workload with name '%s' already exists", runFlags.Name)
	}
	err = validateGroup(ctx, workloadManager, serverOrImage)
	if err != nil {
		return err
	}

	// Build the run configuration
	runnerConfig, err := BuildRunnerConfig(ctx, runFlags, serverOrImage, cmdArgs, debugMode, cmd, groupName)
	if err != nil {
		return err
	}

	// Always save the run config to disk before starting (both foreground and detached modes)
	// NOTE: Save before secrets processing to avoid storing secrets in the state store
	if err := runnerConfig.SaveState(ctx); err != nil {
		return fmt.Errorf("failed to save run configuration: %w", err)
	}

	if runFlags.Foreground {
		return runForeground(ctx, workloadManager, runnerConfig)
	}

	return workloadManager.RunWorkloadDetached(ctx, runnerConfig)
}

// deriveRemoteName extracts a name from a remote URL
func deriveRemoteName(remoteURL string) (string, error) {
	parsedURL, err := url.Parse(remoteURL)
	if err != nil {
		return "", fmt.Errorf("invalid remote URL: %w", err)
	}

	// Use the hostname as the base name
	hostname := parsedURL.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("could not extract hostname from URL: %s", remoteURL)
	}

	// Remove common TLDs and use the main domain name
	parts := strings.Split(hostname, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2], nil
	}

	return hostname, nil
}

// getworkloadDefaultName generates a default workload name based on the serverOrImage input
// This function reuses the existing system's naming logic to ensure consistency
func getworkloadDefaultName(_ context.Context, serverOrImage string) string {
	// If it's a protocol scheme (uvx://, npx://, go://)
	if runner.IsImageProtocolScheme(serverOrImage) {
		// Extract package name from protocol scheme using the existing parseProtocolScheme logic
		_, packageName, err := runner.ParseProtocolScheme(serverOrImage)
		if err != nil {
			return ""
		}

		// Use the existing packageNameToImageName function from the runner package
		return runner.PackageNameToImageName(packageName)
	}

	// If it's a URL (remote server)
	if networking.IsURL(serverOrImage) {
		name, err := deriveRemoteName(serverOrImage)
		if err != nil {
			return ""
		}
		return name
	}

	// Check if it's a server name from registry (including reverse-DNS names with slashes)
	if !strings.Contains(serverOrImage, "://") && !strings.Contains(serverOrImage, ":") {
		// Check if this is a registry server name by attempting to look it up
		provider, err := registry.GetDefaultProvider()
		if err == nil {
			_, err := provider.GetServer(serverOrImage)
			if err == nil {
				// It's a valid registry server name - sanitize for container/filesystem use
				// Replace dots and slashes with dashes to create a valid workload name
				sanitized := strings.ReplaceAll(serverOrImage, ".", "-")
				sanitized = strings.ReplaceAll(sanitized, "/", "-")
				return sanitized
			}
		}
	}

	// For container images, use the existing container.GetOrGenerateContainerName logic
	// We pass empty string as containerName to force generation, and extract the baseName
	_, baseName := container.GetOrGenerateContainerName("", serverOrImage)
	return baseName
}

func runForeground(ctx context.Context, workloadManager workloads.Manager, runnerConfig *runner.RunConfig) error {

	errCh := make(chan error, 1)
	go func() {
		errCh <- workloadManager.RunWorkload(ctx, runnerConfig)
	}()

	// workloadManager.RunWorkload will block until the context is cancelled
	// or an unrecoverable error is returned. In either case, it will stop the server.
	// We wait until workloadManager.RunWorkload exits before deleting the workload,
	// so stopping and deleting don't race.
	//
	// There's room for improvement in the factoring here.
	// Shutdown and cancellation logic is unnecessarily spread across two goroutines.
	err := <-errCh
	if !process.IsDetached() {
		logger.Errorf("RunWorkload Exited. Error: %v, stopping server %q", err, runnerConfig.BaseName)
		cleanupAndWait(workloadManager, runnerConfig.BaseName)
	}
	return err

}

func validateGroup(ctx context.Context, workloadsManager workloads.Manager, serverOrImage string) error {
	workloadName := runFlags.Name
	if workloadName == "" {
		// For protocol schemes without an explicit name, skip group validation.
		// Protocol schemes (like npx://@scope/package) contain characters that are invalid
		// for filesystem operations. The actual workload name will be generated during
		// the build process (in BuildRunnerConfig) where it gets properly sanitized.
		// Since the workload doesn't exist yet with the protocol URL as its name,
		// and we can't check for conflicts without the final sanitized name,
		// we defer group validation to when the workload is actually created.
		if runner.IsImageProtocolScheme(serverOrImage) {
			return nil
		}
		workloadName = serverOrImage
	}

	// Create group manager
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	// Check if the workload is already in a group
	workload, err := workloadsManager.GetWorkload(ctx, workloadName)
	if err != nil {
		// If the workload does not exist, we can proceed to create it
		if !errors.Is(err, runtime.ErrWorkloadNotFound) {
			return fmt.Errorf("failed to get workload: %w", err)
		}
	} else if workload.Group != "" && workload.Group != runFlags.Group {
		return fmt.Errorf("workload '%s' is already in group '%s'", workloadName, workload.Group)
	}

	if runFlags.Group != "" {
		// Validate that the group specified exists
		exists, err := groupManager.Exists(ctx, runFlags.Group)
		if err != nil {
			return fmt.Errorf("failed to check if group exists: %w", err)
		}
		if !exists {
			return fmt.Errorf("group '%s' does not exist", runFlags.Group)
		}
	}
	return nil
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

// runFromConfigFile loads a run configuration from a file and executes it
func runFromConfigFile(ctx context.Context) error {
	// Open and read the configuration file
	configFile, err := os.Open(runFlags.FromConfig)
	if err != nil {
		return fmt.Errorf("failed to open configuration file '%s': %w", runFlags.FromConfig, err)
	}
	defer func() {
		// Non-fatal: file cleanup failure after reading
		_ = configFile.Close()
	}()

	// Deserialize the configuration
	runConfig, err := runner.ReadJSON(configFile)
	if err != nil {
		return fmt.Errorf("failed to parse configuration file: %w", err)
	}

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %w", err)
	}

	// Set the runtime in the config
	runConfig.Deployer = rt

	// Create workload manager
	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Save the run config to disk in the usual directory (before running)
	// This ensures that imported configs are persisted like normal runs
	if err := runConfig.SaveState(ctx); err != nil {
		return fmt.Errorf("failed to save run configuration: %w", err)
	}

	// Run the workload based on foreground flag
	if runFlags.Foreground {
		err = workloadManager.RunWorkload(ctx, runConfig)
	} else {
		err = workloadManager.RunWorkloadDetached(ctx, runConfig)
	}
	if err != nil {
		return err
	}

	return nil
}

// validateRunFlags validates run command flags
func validateRunFlags(cmd *cobra.Command, args []string) error {
	// Validate group flag
	if err := validateGroupFlag()(cmd, args); err != nil {
		return err
	}

	// Validate --remote-auth-resource flag (RFC 8707)
	if resourceFlag := cmd.Flags().Lookup("remote-auth-resource"); resourceFlag != nil && resourceFlag.Changed {
		resource := resourceFlag.Value.String()
		if resource != "" {
			if err := validation.ValidateResourceURI(resource); err != nil {
				return fmt.Errorf("invalid --remote-auth-resource: %w", err)
			}
		}
	}

	// Validate --from-config flag usage
	fromConfigFlag := cmd.Flags().Lookup("from-config")
	if fromConfigFlag != nil && fromConfigFlag.Value.String() != "" {
		// When --from-config is used, only execution-related flags are allowed
		// Execution-related flags control HOW to run (foreground vs detached)
		// Configuration flags control WHAT to run and should not be mixed with --from-config
		allowedFlags := map[string]bool{
			"from-config": true,
			"foreground":  true,
			"debug":       true, // Debug is also an execution flag
		}

		var conflictingFlags []string
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			// Skip allowed flags and only check flags that were changed
			if !allowedFlags[flag.Name] && flag.Changed {
				conflictingFlags = append(conflictingFlags, "--"+flag.Name)
			}
		})

		if len(conflictingFlags) > 0 {
			return fmt.Errorf("--from-config cannot be used with other configuration flags: %v", conflictingFlags)
		}
	}

	// Show deprecation warning if --proxy-mode is explicitly set to SSE
	proxyModeFlag := cmd.Flags().Lookup("proxy-mode")
	if proxyModeFlag != nil && proxyModeFlag.Changed && proxyModeFlag.Value.String() == "sse" {
		logger.Warn("The 'sse' proxy mode is deprecated and will be removed in a future release. " +
			"Please migrate to 'streamable-http' (the new default).")
	}

	return nil
}

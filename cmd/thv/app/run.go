package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
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

	   $ thv run --remote <URL> [--name <name>]

   Runs a remote MCP server as a workload, proxying requests to the specified URL.
   This allows remote MCP servers to be managed like local workloads with full
   support for client configuration, tool filtering, import/export, etc.

The container will be started with the specified transport mode and
permission profile. Additional configuration can be provided via flags.`,
	Args: func(cmd *cobra.Command, args []string) error {
		// If --from-config is provided, no args are required
		if runFlags.FromConfig != "" {
			return nil
		}
		// If --remote is provided, no args are required
		if runFlags.RemoteURL != "" {
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

	//runCmd.PreRunE = validateGroupFlag()

	// This is used for the K8s operator which wraps the run command, but shouldn't be visible to users.
	if err := runCmd.Flags().MarkHidden("k8s-pod-patch"); err != nil {
		logger.Warnf("Error hiding flag: %v", err)
	}

	// Add OIDC validation flags
	AddOIDCFlags(runCmd)
}

func cleanupAndWait(workloadManager workloads.Manager, name string, cancel context.CancelFunc, errCh <-chan error) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()

	group, err := workloadManager.DeleteWorkloads(cleanupCtx, []string{name})
	if err != nil {
		logger.Warnf("Failed to delete workload %q: %v", name, err)
	} else if group != nil {
		if err := group.Wait(); err != nil {
			logger.Warnf("DeleteWorkloads group error for %q: %v", name, err)
		}
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		logger.Warnf("Timeout waiting for workload to stop")
	}
}

func runCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Check if we should load configuration from a file
	if runFlags.FromConfig != "" {
		return runFromConfigFile(ctx)
	}

	// Get the name of the MCP server to run.
	// This may be a server name from the registry, a container image, or a protocol scheme.
	// When using --from-config or --remote, no args are required
	var serverOrImage string
	if len(args) > 0 {
		serverOrImage = args[0]
	}

	// If --remote is provided but no name is given, generate a name from the URL
	if runFlags.RemoteURL != "" && runFlags.Name == "" {
		// Extract a name from the remote URL
		parsedURL, err := url.Parse(runFlags.RemoteURL)
		if err != nil {
			return fmt.Errorf("invalid remote URL: %v", err)
		}
		// Use the hostname as the base name
		hostname := parsedURL.Hostname()
		if hostname == "" {
			hostname = "remote"
		}
		runFlags.Name = fmt.Sprintf("%s-remote", hostname)
	}

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Print the processed command arguments for debugging
	logger.Debugf("Processed cmdArgs: %v", cmdArgs)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}
	workloadManager := workloads.NewManagerFromRuntime(rt)

	err = validateGroup(ctx, workloadManager, serverOrImage)
	if err != nil {
		return err
	}

	// Build the run configuration
	runnerConfig, err := BuildRunnerConfig(ctx, &runFlags, serverOrImage, cmdArgs, debugMode, cmd)
	if err != nil {
		return err
	}

	// Always save the run config to disk before starting (both foreground and detached modes)
	// NOTE: Save before secrets processing to avoid storing secrets in the state store
	if err := runnerConfig.SaveState(ctx); err != nil {
		return fmt.Errorf("failed to save run configuration: %v", err)
	}

	if runFlags.Foreground {
		return runForeground(ctx, workloadManager, runnerConfig)
	}

	return workloadManager.RunWorkloadDetached(ctx, runnerConfig)
}

func runForeground(ctx context.Context, workloadManager workloads.Manager, runnerConfig *runner.RunConfig) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	errCh := make(chan error, 1)
	go func() {
		errCh <- workloadManager.RunWorkload(ctx, runnerConfig)
	}()

	select {
	case sig := <-sigCh:
		if !process.IsDetached() {
			logger.Infof("Received signal: %v, stopping server %q", sig, runnerConfig.BaseName)
			cleanupAndWait(workloadManager, runnerConfig.BaseName, cancel, errCh)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func validateGroup(ctx context.Context, workloadsManager workloads.Manager, serverOrImage string) error {
	workloadName := runFlags.Name
	if workloadName == "" {
		workloadName = serverOrImage
	}

	// Create group manager
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %v", err)
	}

	// Check if the workload is already in a group
	workload, err := workloadsManager.GetWorkload(ctx, workloadName)
	if err != nil {
		// If the workload does not exist, we can proceed to create it
		if !errors.Is(err, runtime.ErrWorkloadNotFound) {
			return fmt.Errorf("failed to get workload: %v", err)
		}
	} else if workload.Group != "" && workload.Group != runFlags.Group {
		return fmt.Errorf("workload '%s' is already in group '%s'", workloadName, workload.Group)
	}

	if runFlags.Group != "" {
		// Validate that the group specified exists
		exists, err := groupManager.Exists(ctx, runFlags.Group)
		if err != nil {
			return fmt.Errorf("failed to check if group exists: %v", err)
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
	defer configFile.Close()

	// Deserialize the configuration
	runConfig, err := runner.ReadJSON(configFile)
	if err != nil {
		return fmt.Errorf("failed to parse configuration file: %w", err)
	}

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Set the runtime in the config
	runConfig.Deployer = rt

	// Create workload manager
	workloadManager := workloads.NewManagerFromRuntime(rt)

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

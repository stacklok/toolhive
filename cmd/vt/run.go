package main

import (
	"context"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run [flags] IMAGE [-- ARGS...]",
	Short: "Run an MCP server",
	Long: `Run an MCP server in a container with the specified image and arguments.
The container will be started with minimal permissions and the specified transport mode.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCmdFunc,
}

var (
	runTransport         string
	runName              string
	runPort              int
	runTargetPort        int
	runPermissionProfile string
	runEnv               []string
	runNoClientConfig    bool
	runForeground        bool
	runVolumes           []string
	runSecrets           []string
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "stdio", "Transport mode (sse or stdio)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().IntVar(&runTargetPort, "target-port", 0, "Port for the container to expose (only applicable to SSE transport)")
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
	runCmd.Flags().BoolVar(
		&runNoClientConfig,
		"no-client-config",
		false,
		"Do not update client configuration files with the MCP server URL",
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

	// Add OIDC validation flags
	AddOIDCFlags(runCmd)
}

//nolint:gocyclo // This function is complex but manageable
func runCmdFunc(cmd *cobra.Command, args []string) error {
	// Get the image and command arguments
	image := args[0]
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

	// Create run options
	options := RunOptions{
		Image:             image,
		CmdArgs:           cmdArgs,
		Transport:         runTransport,
		Name:              runName,
		Port:              runPort,
		TargetPort:        runTargetPort,
		PermissionProfile: runPermissionProfile,
		EnvVars:           runEnv,
		NoClientConfig:    runNoClientConfig,
		Foreground:        runForeground,
		OIDCIssuer:        oidcIssuer,
		OIDCAudience:      oidcAudience,
		OIDCJwksURL:       oidcJwksURL,
		OIDCClientID:      oidcClientID,
		Debug:             debugMode,
		Volumes:           runVolumes,
		Secrets:           runSecrets,
	}

	// Run the MCP server
	return RunMCPServer(ctx, cmd, options)
}

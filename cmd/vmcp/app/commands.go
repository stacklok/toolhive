// Package app provides the entry point for the vmcp command-line application.
package app

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/logger"
)

var rootCmd = &cobra.Command{
	Use:               "vmcp",
	DisableAutoGenTag: true,
	Short:             "Virtual MCP Server - Aggregate and proxy multiple MCP servers",
	Long: `Virtual MCP Server (vmcp) is a proxy that aggregates multiple MCP (Model Context Protocol) servers
into a single unified interface. It provides:

- Tool aggregation from multiple MCP servers
- Resource aggregation from multiple sources
- Prompt aggregation and routing
- Authentication and authorization middleware
- Audit logging and telemetry
- Per-backend middleware configuration

vmcp reuses ToolHive's security and middleware infrastructure to provide a secure,
observable, and controlled way to expose multiple MCP servers through a single endpoint.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			logger.Errorf("Error displaying help: %v", err)
		}
	},
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		logger.Initialize()
	},
}

// NewRootCmd creates a new root command for the vmcp CLI.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		logger.Errorf("Error binding debug flag: %v", err)
	}

	rootCmd.PersistentFlags().StringP("config", "c", "", "Path to vMCP configuration file")
	err = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	if err != nil {
		logger.Errorf("Error binding config flag: %v", err)
	}

	// Add subcommands
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newValidateCmd())

	// Silence printing the usage on error
	rootCmd.SilenceUsage = true

	return rootCmd
}

// newServeCmd creates the serve command for starting the vMCP server
func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Virtual MCP Server",
		Long: `Start the Virtual MCP Server to aggregate and proxy multiple MCP servers.

The server will read the configuration file specified by --config flag and start
listening for MCP client connections. It will aggregate tools, resources, and prompts
from all configured backend MCP servers.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			configPath := viper.GetString("config")
			if configPath == "" {
				return fmt.Errorf("no configuration file specified, use --config flag")
			}

			logger.Infof("Loading configuration from: %s", configPath)
			// TODO: Load configuration and start server
			// This will be implemented in a future PR when pkg/vmcp is added

			return fmt.Errorf("serve command not yet implemented")
		},
	}
}

// newVersionCmd creates the version command
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Display version information for vmcp",
		Run: func(_ *cobra.Command, _ []string) {
			// Version information will be injected at build time
			logger.Infof("vmcp version: %s", getVersion())
		},
	}
}

// newValidateCmd creates the validate command for checking configuration
func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration file",
		Long: `Validate the vMCP configuration file for syntax and semantic errors.

This command checks:
- YAML/JSON syntax validity
- Required fields presence
- Middleware configuration correctness
- Backend configuration validity`,
		RunE: func(_ *cobra.Command, _ []string) error {
			configPath := viper.GetString("config")
			if configPath == "" {
				return fmt.Errorf("no configuration file specified, use --config flag")
			}

			logger.Infof("Validating configuration: %s", configPath)
			// TODO: Validate configuration
			// This will be implemented in a future PR when pkg/vmcp is added

			return fmt.Errorf("validate command not yet implemented")
		},
	}
}

// getVersion returns the version string (will be set at build time)
func getVersion() string {
	// This will be replaced with actual version info using ldflags
	return "dev"
}

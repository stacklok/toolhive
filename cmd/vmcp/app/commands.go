// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package app provides the entry point for the vmcp command-line application.
package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive-core/logging"
	"github.com/stacklok/toolhive/pkg/versions"
	vmcpcli "github.com/stacklok/toolhive/pkg/vmcp/cli"
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
			slog.Error(fmt.Sprintf("Error displaying help: %v", err))
		}
	},
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		// Re-initialize logger now that cobra has parsed flags and viper has
		// the correct value for "debug". The logger installed in main() runs
		// before flag parsing, so the --debug flag is not yet visible there.
		var opts []logging.Option
		if viper.GetBool("debug") {
			opts = append(opts, logging.WithLevel(slog.LevelDebug))
		}
		slog.SetDefault(logging.New(opts...))
		return nil
	},
}

// NewRootCmd creates a new root command for the vmcp CLI.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		slog.Error(fmt.Sprintf("Error binding debug flag: %v", err))
	}

	rootCmd.PersistentFlags().StringP("config", "c", "", "Path to vMCP configuration file")
	err = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	if err != nil {
		slog.Error(fmt.Sprintf("Error binding config flag: %v", err))
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
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Virtual MCP Server",
		Long: `Start the Virtual MCP Server to aggregate and proxy multiple MCP servers.

The server will read the configuration file specified by --config flag and start
listening for MCP client connections. It will aggregate tools, resources, and prompts
from all configured backend MCP servers.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			configPath := viper.GetString("config")
			if configPath == "" {
				return fmt.Errorf("no configuration file specified, use --config flag")
			}

			host, _ := cmd.Flags().GetString("host")
			port, _ := cmd.Flags().GetInt("port")
			enableAudit, _ := cmd.Flags().GetBool("enable-audit")
			sessionTTL, _ := cmd.Flags().GetDuration("session-ttl")

			return vmcpcli.Serve(cmd.Context(), vmcpcli.ServeConfig{
				ConfigPath:  configPath,
				Host:        host,
				Port:        port,
				EnableAudit: enableAudit,
				SessionTTL:  sessionTTL,
			})
		},
	}

	// Add serve-specific flags
	cmd.Flags().String("host", "127.0.0.1", "Host address to bind to")
	cmd.Flags().Int("port", 4483, "Port to listen on")
	cmd.Flags().Bool("enable-audit", false, "Enable audit logging with default configuration")
	cmd.Flags().Duration("session-ttl", time.Duration(0),
		"Session inactivity timeout (e.g., 30m, 2h); zero uses the default (30m)")

	return cmd
}

// newVersionCmd creates the version command
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Display version information for vmcp",
		Run: func(_ *cobra.Command, _ []string) {
			slog.Info(fmt.Sprintf("vmcp version: %s", versions.Version))
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			configPath := viper.GetString("config")
			if configPath == "" {
				return fmt.Errorf("no configuration file specified, use --config flag")
			}
			return vmcpcli.Validate(cmd.Context(), vmcpcli.ValidateConfig{
				ConfigPath: configPath,
			})
		},
	}
}

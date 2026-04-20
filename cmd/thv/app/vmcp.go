// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	vmcpcli "github.com/stacklok/toolhive/pkg/vmcp/cli"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// newVMCPCommand returns the top-level "vmcp" Cobra command with subcommands attached.
func newVMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vmcp",
		Short: "Run and manage a Virtual MCP Server locally",
		Long: `The vmcp command provides subcommands to run and validate a Virtual MCP
Server (vMCP) locally without Kubernetes. A vMCP aggregates multiple MCP
servers from a ToolHive group into a single unified endpoint.`,
	}
	cmd.AddCommand(newVMCPServeCommand())
	cmd.AddCommand(newVMCPValidateCommand())
	cmd.AddCommand(newVMCPInitCommand())
	return cmd
}

// newVMCPServeCommand returns the "vmcp serve" subcommand.
func newVMCPServeCommand() *cobra.Command {
	var (
		configPath  string
		host        string
		port        int
		enableAudit bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Virtual MCP Server",
		Long: `Start the Virtual MCP Server to aggregate and proxy multiple MCP servers.

The server reads the configuration file specified by --config and starts
listening for MCP client connections, aggregating tools, resources, and
prompts from all configured backend MCP servers.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return vmcpcli.Serve(cmd.Context(), vmcpcli.ServeConfig{
				ConfigPath:  configPath,
				Host:        host,
				Port:        port,
				EnableAudit: enableAudit,
			})
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to vMCP configuration file (required)")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Host address to bind to")
	cmd.Flags().IntVar(&port, "port", 4483, "Port to listen on")
	cmd.Flags().BoolVar(&enableAudit, "enable-audit", false, "Enable audit logging with default configuration")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

// newVMCPInitCommand returns the "vmcp init" subcommand.
func newVMCPInitCommand() *cobra.Command {
	var (
		groupName  string
		outputPath string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a starter vMCP configuration file",
		Long: `Discover running workloads in a ToolHive group and generate a starter
vMCP YAML configuration file pre-populated with one backend entry per
accessible workload.

The generated file can be reviewed and customized, then passed to
'thv vmcp validate --config' to check it and 'thv vmcp serve --config'
to start the aggregated server.

If neither --output nor --config is provided, the generated YAML is written to stdout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := workloads.NewManager(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to create workload manager: %w", err)
			}
			return vmcpcli.Init(cmd.Context(), vmcpcli.InitConfig{
				GroupName:  groupName,
				OutputPath: outputPath,
				Discoverer: workloads.NewDiscovererAdapter(manager),
			})
		},
	}
	cmd.Flags().StringVarP(&groupName, "group", "g", "", "ToolHive group name to discover workloads from (required)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path for the generated config (default: stdout)")
	cmd.Flags().StringVarP(&outputPath, "config", "c", "", "Output file path for the generated config; alias for --output")
	_ = cmd.MarkFlagRequired("group")
	return cmd
}

// newVMCPValidateCommand returns the "vmcp validate" subcommand.
func newVMCPValidateCommand() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a vMCP configuration file",
		Long: `Validate the vMCP configuration file for syntax and semantic errors.

This command checks YAML syntax, required field presence, middleware
configuration correctness, and backend configuration validity. Exits 0
for valid configurations, non-zero with a descriptive error otherwise.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return vmcpcli.Validate(cmd.Context(), vmcpcli.ValidateConfig{
				ConfigPath: configPath,
			})
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to vMCP configuration file (required)")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

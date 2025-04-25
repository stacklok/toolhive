// Package app provides the entry point for the toolhive command-line application.
package app

import (
	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/logger"
)

var rootCmd = &cobra.Command{
	Use:               "thv",
	DisableAutoGenTag: true,
	Short:             "ToolHive (thv) is a lightweight, secure, and fast manager for MCP servers",
	Long: `ToolHive (thv) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
It is written in Go and has extensive test coverage—including input validation—to ensure reliability and security.

Under the hood, ToolHive acts as a very thin client for the Docker/Podman Unix socket API.
This design choice allows it to remain both efficient and lightweight while still providing powerful,
container-based isolation for running MCP servers.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			logger.Log.Errorf("Error displaying help: %v", err)
		}
	},
}

// NewRootCmd creates a new root command for the ToolHive CLI.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(logsCommand())
	rootCmd.AddCommand(newSecretCommand())

	return rootCmd
}

// IsCompletionCommand checks if the command being run is the completion command
func IsCompletionCommand(args []string) bool {
	if len(args) > 1 {
		return args[1] == "completion"
	}
	return false
}

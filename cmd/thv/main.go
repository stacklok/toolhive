package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
)

var rootCmd = &cobra.Command{
	Use:   "thv",
	Short: "ToolHive (thv) is a lightweight, secure, and fast manager for MCP servers",
	Long: `ToolHive (thv) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
It is written in Go and has extensive test coverage—including input validation—to ensure reliability and security.

Under the hood, ToolHive acts as a very thin client for the Docker/Podman Unix socket API.
This design choice allows it to remain both efficient and lightweight while still providing powerful,
container-based isolation for running MCP servers.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			logger.Log.Error(fmt.Sprintf("Error displaying help: %v", err))
		}
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the version of ToolHive",
	Run: func(_ *cobra.Command, _ []string) {
		logger.Log.Info("ToolHive v0.1.0")
	},
}

func init() {
	// Initialize the logger system
	logger.Initialize()

	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(newLogsCommand())
	rootCmd.AddCommand(newSecretCommand())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		logger.Log.Error("%v, %v", os.Stderr, err)
		os.Exit(1)
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package app provides the entry point for the toolhive command-line application.
package app

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/desktop"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/updates"
)

var rootCmd = &cobra.Command{
	Use:               "thv",
	DisableAutoGenTag: true,
	Short:             "ToolHive (thv) is a lightweight, secure, and fast manager for MCP servers",
	Long: `ToolHive (thv) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
It is written in Go and has extensive test coverage—including input validation—to ensure reliability and security.

Under the hood, ToolHive acts as a very thin client for the Docker/Podman/Colima Unix socket API.
This design choice allows it to remain both efficient and lightweight while still providing powerful,
container-based isolation for running MCP servers.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			logger.Errorf("Error displaying help: %v", err)
		}
	},
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		logger.Initialize()

		// Check for desktop app conflict
		return desktop.ValidateDesktopAlignment()
	},
}

// NewRootCmd creates a new root command for the ToolHive CLI.
func NewRootCmd(enableUpdates bool) *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		logger.Errorf("Error binding debug flag: %v", err)
	}

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(newExportCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(logsCommand())
	rootCmd.AddCommand(newSecretCommand())
	rootCmd.AddCommand(inspectorCommand())
	rootCmd.AddCommand(newMCPCommand())
	rootCmd.AddCommand(groupCmd)
	rootCmd.AddCommand(statusCmd)

	// Silence printing the usage on error
	rootCmd.SilenceUsage = true

	if enableUpdates {
		checkForUpdates()
	}

	return rootCmd
}

// IsCompletionCommand checks if the command being run is the completion command
func IsCompletionCommand(args []string) bool {
	if len(args) > 1 {
		return args[1] == "completion"
	}
	return false
}

// IsInformationalCommand checks if the command being run is an informational command that doesn't need container runtime
func IsInformationalCommand(args []string) bool {
	if len(args) < 2 {
		return true // Help is shown when no subcommand is provided
	}

	command := args[1]

	// Commands that are entirely informational and don't need container runtime
	informationalCommands := map[string]bool{
		"version":    true,
		"search":     true,
		"completion": true,
		"registry":   true,
		"mcp":        true,
	}

	return informationalCommands[command]
}

func checkForUpdates() {
	if updates.ShouldSkipUpdateChecks() {
		return
	}

	versionClient := updates.NewVersionClient()
	updateChecker, err := updates.NewUpdateChecker(versionClient)
	// treat update-related errors as non-fatal
	if err != nil {
		logger.Warnf("unable to create update client: %s", err)
		return
	}

	err = updateChecker.CheckLatestVersion()
	if err != nil {
		logger.Warnf("could not check for updates: %s", err)
	}
}

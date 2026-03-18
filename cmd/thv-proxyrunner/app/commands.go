// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package app provides the entry point for the toolhive command-line application.
package app

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:               "thv-proxyrunner",
	DisableAutoGenTag: true,
	Short:             "ToolHive (thv) is a lightweight, secure, and fast manager for MCP servers",
	Long: `ToolHive (thv) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
It is written in Go and has extensive test coverage—including input validation—to ensure reliability and security.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			slog.Error(fmt.Sprintf("Error displaying help: %v", err))
		}
	},
}

// NewRootCmd creates a new root command for the ToolHive CLI.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		slog.Error(fmt.Sprintf("Error binding debug flag: %v", err))
	}

	// Bind TOOLHIVE_DEBUG environment variable to viper debug config
	// This allows setting debug mode via environment variable
	err = viper.BindEnv("debug", "TOOLHIVE_DEBUG")
	if err != nil {
		slog.Error(fmt.Sprintf("Error binding TOOLHIVE_DEBUG env var: %v", err))
	}

	// Add subcommands
	rootCmd.AddCommand(runCmd)

	return rootCmd
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package app provides the entry point for the toolhive command-line application.
package app

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive-core/logging"
	"github.com/stacklok/toolhive/pkg/desktop"
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
			slog.Error(fmt.Sprintf("Error displaying help: %v", err))
		}
	},
	PersistentPreRunE: rootPersistentPreRunE,
}

// rootPersistentPreRunE re-initializes the logger now that cobra has parsed
// flags and viper has the correct value for "debug", then checks for a desktop
// app conflict. NewRootCmd wraps it to also run the update check, so that the
// update banner only appears after flag parsing and these checks succeed.
func rootPersistentPreRunE(_ *cobra.Command, _ []string) error {
	var opts []logging.Option
	if viper.GetBool("debug") {
		opts = append(opts, logging.WithLevel(slog.LevelDebug))
	}
	slog.SetDefault(logging.New(opts...))

	// Check for desktop app conflict
	return desktop.ValidateDesktopAlignment()
}

// NewRootCmd creates a new root command for the ToolHive CLI.
func NewRootCmd(enableUpdates bool) *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		slog.Error(fmt.Sprintf("Error binding debug flag: %v", err))
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
	rootCmd.AddCommand(newVMCPCommand())
	rootCmd.AddCommand(newLLMCommand())
	rootCmd.AddCommand(groupCmd)
	rootCmd.AddCommand(skillCmd)
	rootCmd.AddCommand(aiPluginCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(upgradeCmd)

	// Silence printing the usage on error
	rootCmd.SilenceUsage = true

	// Run the update check from PersistentPreRunE rather than before Execute so
	// the banner only appears once the command's flags have been parsed and the
	// persistent pre-run checks have passed. Otherwise the banner prints on top
	// of flag-parsing errors (e.g. an unknown flag), adding noise to an already
	// confusing error. Assigning (not wrapping) keeps NewRootCmd idempotent.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := rootPersistentPreRunE(cmd, args); err != nil {
			return err
		}
		if enableUpdates {
			checkForUpdates()
		}
		return nil
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

	// Commands that don't need container runtime or startup migrations.
	// "vmcp" is safe here: telemetry/secret-scope migrations only affect thv run state,
	// and EnsureDefaultGroupExists is called inside pkg/vmcp/cli/Serve when dynamic
	// backend discovery is used (i.e. when no static backends are configured).
	// "secret" is safe here: secrets management is pure config/credential I/O and
	// does not interact with container runtimes.
	informationalCommands := map[string]bool{
		"version":    true,
		"search":     true,
		"completion": true,
		"registry":   true,
		"mcp":        true,
		"secret":     true,
		"skill":      true,
		"ai-plugin":  true,
		"vmcp":       true,
		"llm":        true,
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
		slog.Warn(fmt.Sprintf("unable to create update client: %s", err))
		return
	}

	err = updateChecker.CheckLatestVersion()
	if err != nil {
		slog.Warn(fmt.Sprintf("could not check for updates: %s", err))
	}
}

// Package app provides the entry point for the toolhive command-line application.
package app

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/updates"
)

// NewRootCmd creates a new root command for the ToolHive CLI.
func NewRootCmd(enableUpdates bool) *cobra.Command {
	rootCmd := &cobra.Command{
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
				logger.Errorf("Error displaying help: %v", err)
			}
		},
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			logger.Initialize()
		},
	}

	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	rootCmd.PersistentFlags().String("config", "", "Path to config file (default: ~/.config/toolhive/config.yaml)")

	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		logger.Errorf("Error binding debug flag: %v", err)
	}

	err = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	if err != nil {
		logger.Errorf("Error binding config flag: %v", err)
	}

	// Add subcommands
	initClientCmds(rootCmd)
	initRegistryCmds(rootCmd)
	initConfigCmds(rootCmd)
	initSearchCmds(rootCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(logsCommand())
	rootCmd.AddCommand(newSecretCommand())
	rootCmd.AddCommand(inspectorCommand())
	rootCmd.AddCommand(newMCPCommand())

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

func checkForUpdates() {
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

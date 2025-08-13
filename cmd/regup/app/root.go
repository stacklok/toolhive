// Package app provides the entry point for the regup command-line application.
package app

import (
	"github.com/spf13/cobra"

	log "github.com/stacklok/toolhive/pkg/logger"
)

var logger = log.NewLogger()

var rootCmd = &cobra.Command{
	Use:               "regup",
	DisableAutoGenTag: true,
	Short:             "Update MCP server registry entries with latest information",
	Long: `regup is a utility for updating MCP server registry entries with the latest information.
It identifies the oldest entries in the registry and updates them with the latest GitHub stars and pulls data.
This tool is designed to be run as a GitHub action to keep the registry up-to-date.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no flags are provided, run the update command
		if err := updateCmd.RunE(cmd, nil); err != nil {
			cmd.PrintErrf("Error: %v\n", err)
		}
	},
}

// NewRootCmd creates a new root command for the regup CLI.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")

	// Add subcommands
	rootCmd.AddCommand(updateCmd)

	return rootCmd
}

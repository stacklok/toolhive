// Package app provides the entry point for the regup command-line application.
package app

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:               "regup",
	DisableAutoGenTag: true,
	Short:             "[DEPRECATED] Update MCP server registry entries with latest information",
	Long: `regup is a utility for updating MCP server registry entries with the latest information.
It identifies the oldest entries in the registry and updates them with the latest GitHub stars and pulls data.

⚠️  DEPRECATED: This tool is deprecated. The registry is now maintained in the separate
toolhive-registry repository (https://github.com/stacklok/toolhive-registry) and
automatically synced via GitHub Actions. This command is kept for local development
and manual operations only.`,
	Run: func(cmd *cobra.Command, _ []string) {
		cmd.PrintErrln("⚠️  WARNING: regup is deprecated. The registry is now maintained in")
		cmd.PrintErrln("   the toolhive-registry repository and synced automatically.")
		cmd.PrintErrln("   See: https://github.com/stacklok/toolhive-registry")
		cmd.PrintErrln("")

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

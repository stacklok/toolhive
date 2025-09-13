// Package app provides the entry point for the ToolHive Registry API application.
package app

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/versions"
)

var rootCmd = &cobra.Command{
	Use:               "thv-registry-api",
	DisableAutoGenTag: true,
	Short:             "ToolHive Registry API server",
	Long: `ToolHive Registry API server provides REST endpoints for accessing MCP server registry
data in Kubernetes.`,
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

// NewRootCmd creates a new root command for the Registry API.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		logger.Errorf("Error binding debug flag: %v", err)
	}

	// Add subcommands
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)

	return rootCmd
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(_ *cobra.Command, _ []string) {
		info := versions.GetVersionInfo()
		logger.Infof("thv-registry-api version %s (commit: %s, built: %s, go: %s, platform: %s)",
			info.Version, info.Commit, info.BuildDate, info.GoVersion, info.Platform)
	},
}

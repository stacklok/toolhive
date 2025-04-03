package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/config"
)

var rootCmd = &cobra.Command{
	Use:   "vt",
	Short: "Vibe Tool (vt) is a lightweight, secure, and fast manager for MCP servers",
	Long: `Vibe Tool (vt) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
It is written in Go and has extensive test coverage—including input validation—to ensure reliability and security.

Under the hood, Vibe Tool acts as a very thin client for the Docker/Podman Unix socket API.
This design choice allows it to remain both efficient and lightweight while still providing powerful,
container-based isolation for running MCP servers.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			fmt.Printf("Error displaying help: %v\n", err)
		}
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the version of Vibe Tool",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println("Vibe Tool v0.1.0")
	},
}

// Singleton value - should only be written to by the init function.
var appConfig *config.Config

// GetConfig returns the application configuration.
// This can only be called after it is initialized in the init function.
func GetConfig() *config.Config {
	if appConfig == nil {
		panic("configuration is not initialized")
	}
	return appConfig
}

func init() {
	// Initialize the application configuration.
	var err error
	appConfig, err = config.LoadOrCreateConfig()
	if err != nil {
		fmt.Printf("error loading configuration: %v\n", err)
		os.Exit(1)
	}

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
	rootCmd.AddCommand(newSecretCommand())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

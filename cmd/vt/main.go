package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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

func init() {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	rootCmd.PersistentFlags().String("secrets-provider", "basic", "Secrets provider to use (basic)")

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(newSecretCommand())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

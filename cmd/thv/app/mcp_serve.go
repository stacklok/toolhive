package app

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	mcpserver "github.com/stacklok/toolhive/pkg/mcp/server"
)

var (
	mcpServePort string
	mcpServeHost string
)

// newMCPServeCommand creates the 'mcp serve' subcommand
func newMCPServeCommand() *cobra.Command {
	// Check for MCP_PORT environment variable
	defaultPort := mcpserver.DefaultMCPPort
	if envPort := os.Getenv("MCP_PORT"); envPort != "" {
		defaultPort = envPort
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "🧪 EXPERIMENTAL: Start an MCP server to control ToolHive",
		Long: `🧪 EXPERIMENTAL: Start an MCP (Model Context Protocol) server that allows external clients to control ToolHive.
The server provides tools to search the registry, run MCP servers, and remove servers.
The server runs in privileged mode and can access the Docker socket directly.

The port can be configured via the --port flag or the MCP_PORT environment variable.`,
		RunE: mcpServeCmdFunc,
	}

	// Add flags
	cmd.Flags().StringVar(&mcpServePort, "port", defaultPort, "Port to listen on (can also be set via MCP_PORT env var)")
	cmd.Flags().StringVar(&mcpServeHost, "host", "localhost", "Host to listen on")

	return cmd
}

// mcpServeCmdFunc is the main function for the MCP serve command
func mcpServeCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Create MCP server configuration
	config := &mcpserver.Config{
		Host: mcpServeHost,
		Port: mcpServePort,
	}

	// Create the MCP server
	server, err := mcpserver.New(ctx, config)
	if err != nil {
		return err
	}

	// Start server in goroutine
	go func() {
		if err := server.Start(); err != nil {
			cancel()
		}
	}()

	// Wait for shutdown signal
	<-sigChan

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	return server.Shutdown(shutdownCtx)
}

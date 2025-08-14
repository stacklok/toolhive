package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	s "github.com/stacklok/toolhive/pkg/api"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	mcpserver "github.com/stacklok/toolhive/pkg/mcp/server"
)

var (
	host            string
	port            int
	enableDocs      bool
	socketPath      string
	enableMCPServer bool
	mcpServerPort   string
	mcpServerHost   string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the ToolHive API server",
	Long:  `Starts the ToolHive API server and listen for HTTP requests.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Ensure server is shutdown gracefully on Ctrl+C.
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer cancel()

		// Get debug mode flag
		debugMode, _ := cmd.Flags().GetBool("debug")

		// If socket path is provided, use it; otherwise use host:port
		address := fmt.Sprintf("%s:%d", host, port)
		isUnixSocket := false
		if socketPath != "" {
			address = socketPath
			isUnixSocket = true
		}

		// Get OIDC configuration if enabled
		var oidcConfig *auth.TokenValidatorConfig
		if IsOIDCEnabled(cmd) {
			// Get OIDC flag values
			issuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
			audience := GetStringFlagOrEmpty(cmd, "oidc-audience")
			jwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
			introspectionURL := GetStringFlagOrEmpty(cmd, "oidc-introspection-url")
			clientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")
			clientSecret := GetStringFlagOrEmpty(cmd, "oidc-client-secret")

			oidcConfig = &auth.TokenValidatorConfig{
				Issuer:           issuer,
				Audience:         audience,
				JWKSURL:          jwksURL,
				IntrospectionURL: introspectionURL,
				ClientID:         clientID,
				ClientSecret:     clientSecret,
			}
		}

		// Optionally start MCP server if experimental flag is enabled
		if enableMCPServer {
			logger.Info("EXPERIMENTAL: Starting embedded MCP server")

			// Create MCP server configuration
			mcpConfig := &mcpserver.Config{
				Host: mcpServerHost,
				Port: mcpServerPort,
			}

			// Create and start the MCP server in a goroutine
			mcpServer, err := mcpserver.New(ctx, mcpConfig)
			if err != nil {
				return fmt.Errorf("failed to create MCP server: %w", err)
			}

			go func() {
				if err := mcpServer.Start(); err != nil {
					logger.Errorf("MCP server error: %v", err)
				}
			}()

			// Ensure MCP server is shut down on context cancellation
			go func() {
				<-ctx.Done()
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutdownCancel()
				if err := mcpServer.Shutdown(shutdownCtx); err != nil {
					logger.Errorf("Failed to shutdown MCP server: %v", err)
				}
			}()
		}

		return s.Serve(ctx, address, isUnixSocket, debugMode, enableDocs, oidcConfig)
	},
}

func init() {
	serveCmd.Flags().StringVar(&host, "host", "127.0.0.1", "Host address to bind the server to")
	serveCmd.Flags().IntVar(&port, "port", 8080, "Port to bind the server to")
	serveCmd.Flags().BoolVar(&enableDocs, "openapi", false,
		"Enable OpenAPI documentation endpoints (/api/openapi.json and /api/doc)")
	serveCmd.Flags().StringVar(&socketPath, "socket", "", "UNIX socket path to bind the "+
		"server to (overrides host and port if provided)")

	// Add experimental MCP server flags
	serveCmd.Flags().BoolVar(&enableMCPServer, "experimental-mcp", false,
		"EXPERIMENTAL: Enable embedded MCP server for controlling ToolHive")
	serveCmd.Flags().StringVar(&mcpServerPort, "experimental-mcp-port", mcpserver.DefaultMCPPort,
		"EXPERIMENTAL: Port for the embedded MCP server")
	serveCmd.Flags().StringVar(&mcpServerHost, "experimental-mcp-host", "localhost",
		"EXPERIMENTAL: Host for the embedded MCP server")

	// Add OIDC validation flags
	AddOIDCFlags(serveCmd)
}

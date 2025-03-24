package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/auth"
	"github.com/stacklok/vibetool/pkg/networking"
	"github.com/stacklok/vibetool/pkg/transport"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy [flags] SERVER_NAME",
	Short: "Spawn a transparent proxy for an MCP server",
	Long: `Spawn a transparent proxy that will redirect to an MCP server endpoint.
This command creates a standalone proxy without starting a container.`,
	Args: cobra.ExactArgs(1),
	RunE: proxyCmdFunc,
}

var (
	proxyPort      int
	proxyTargetURI string

	// OIDC validation flags
	proxyOIDCIssuer   string
	proxyOIDCAudience string
	proxyOIDCJWKSURL  string
	proxyOIDCClientID string
)

func init() {
	proxyCmd.Flags().IntVar(&proxyPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	proxyCmd.Flags().StringVar(
		&proxyTargetURI,
		"target-uri",
		"",
		"URI for the target MCP server (e.g., http://localhost:8080) (required)",
	)

	// OIDC validation flags
	proxyCmd.Flags().StringVar(&proxyOIDCIssuer, "oidc-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com)")
	proxyCmd.Flags().StringVar(&proxyOIDCAudience, "oidc-audience", "", "Expected audience for the token")
	proxyCmd.Flags().StringVar(&proxyOIDCJWKSURL, "oidc-jwks-url", "", "URL to fetch the JWKS from")
	proxyCmd.Flags().StringVar(&proxyOIDCClientID, "oidc-client-id", "", "OIDC client ID")

	// Mark target-uri as required
	if err := proxyCmd.MarkFlagRequired("target-uri"); err != nil {
		fmt.Printf("Warning: Failed to mark flag as required: %v\n", err)
	}
}

func proxyCmdFunc(_ *cobra.Command, args []string) error {
	// Get the server name
	serverName := args[0]

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	fmt.Printf("Using host port: %d\n", port)

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create middlewares slice
	var middlewares []transport.Middleware

	// Create JWT validator if OIDC flags are provided
	if proxyOIDCJWKSURL != "" {
		fmt.Println("OIDC validation enabled")

		// Create JWT validator
		jwtValidator, err := auth.NewJWTValidator(ctx, auth.JWTValidatorConfig{
			Issuer:   proxyOIDCIssuer,
			Audience: proxyOIDCAudience,
			JWKSURL:  proxyOIDCJWKSURL,
			ClientID: proxyOIDCClientID,
		})
		if err != nil {
			return fmt.Errorf("failed to create JWT validator: %v", err)
		}

		// Add JWT validation middleware
		middlewares = append(middlewares, jwtValidator.Middleware)
	} else {
		fmt.Println("OIDC validation disabled")
	}

	// Create the transparent proxy
	fmt.Printf("Setting up transparent proxy to forward from host port %d to %s\n",
		port, proxyTargetURI)

	// Create the transparent proxy with middlewares
	proxy := transport.NewTransparentProxy(port, serverName, proxyTargetURI, middlewares...)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	fmt.Printf("Transparent proxy started for server %s on port %d -> %s\n",
		serverName, port, proxyTargetURI)
	fmt.Println("Press Ctrl+C to stop")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigCh
	fmt.Printf("Received signal %s, stopping proxy...\n", sig)

	// Stop the proxy
	if err := proxy.Stop(ctx); err != nil {
		fmt.Printf("Warning: Failed to stop proxy: %v\n", err)
	}

	fmt.Printf("Proxy for server %s stopped\n", serverName)
	return nil
}

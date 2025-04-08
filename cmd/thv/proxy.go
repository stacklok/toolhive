package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/auth"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/networking"
	"github.com/StacklokLabs/toolhive/pkg/transport/proxy/transparent"
	"github.com/StacklokLabs/toolhive/pkg/transport/types"
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
)

func init() {
	proxyCmd.Flags().IntVar(&proxyPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	proxyCmd.Flags().StringVar(
		&proxyTargetURI,
		"target-uri",
		"",
		"URI for the target MCP server (e.g., http://localhost:8080) (required)",
	)

	// Add OIDC validation flags
	AddOIDCFlags(proxyCmd)

	// Mark target-uri as required
	if err := proxyCmd.MarkFlagRequired("target-uri"); err != nil {
		logger.Log.Warn(fmt.Sprintf("Warning: Failed to mark flag as required: %v", err))
	}
}

func proxyCmdFunc(cmd *cobra.Command, args []string) error {
	// Get the server name
	serverName := args[0]

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	logger.Log.Info(fmt.Sprintf("Using host port: %d", port))

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create middlewares slice
	var middlewares []types.Middleware

	// Create JWT validator if OIDC flags are provided
	if IsOIDCEnabled(cmd) {
		logger.Log.Info("OIDC validation enabled")

		// Get OIDC flag values
		issuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
		audience := GetStringFlagOrEmpty(cmd, "oidc-audience")
		jwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
		clientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

		// Create JWT validator
		jwtValidator, err := auth.NewJWTValidator(ctx, auth.JWTValidatorConfig{
			Issuer:   issuer,
			Audience: audience,
			JWKSURL:  jwksURL,
			ClientID: clientID,
		})
		if err != nil {
			return fmt.Errorf("failed to create JWT validator: %v", err)
		}

		// Add JWT validation middleware
		middlewares = append(middlewares, jwtValidator.Middleware)
	} else {
		logger.Log.Info("OIDC validation disabled")
	}

	// Create the transparent proxy
	logger.Log.Info(fmt.Sprintf("Setting up transparent proxy to forward from host port %d to %s",
		port, proxyTargetURI))

	// Create the transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(port, serverName, proxyTargetURI, middlewares...)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	logger.Log.Info(fmt.Sprintf("Transparent proxy started for server %s on port %d -> %s",
		serverName, port, proxyTargetURI))
	logger.Log.Info("Press Ctrl+C to stop")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigCh
	logger.Log.Info(fmt.Sprintf("Received signal %s, stopping proxy...", sig))

	// Stop the proxy
	if err := proxy.Stop(ctx); err != nil {
		logger.Log.Warn(fmt.Sprintf("Warning: Failed to stop proxy: %v", err))
	}

	logger.Log.Info(fmt.Sprintf("Proxy for server %s stopped", serverName))
	return nil
}

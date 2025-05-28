package app

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
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
	proxyHost      string
	proxyPort      int
	proxyTargetURI string
)

func init() {
	proxyCmd.Flags().StringVar(&proxyHost, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
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
		logger.Warnf("Warning: Failed to mark flag as required: %v", err)
	}
}

func proxyCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get the server name
	serverName := args[0]

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(proxyHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", proxyHost)
	}
	proxyHost = validatedHost

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	logger.Infof("Using host port: %d", port)

	// Create middlewares slice
	var middlewares []types.Middleware

	// Get OIDC configuration if enabled
	var oidcConfig *auth.JWTValidatorConfig
	if IsOIDCEnabled(cmd) {
		// Get OIDC flag values
		issuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
		audience := GetStringFlagOrEmpty(cmd, "oidc-audience")
		jwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
		clientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

		oidcConfig = &auth.JWTValidatorConfig{
			Issuer:   issuer,
			Audience: audience,
			JWKSURL:  jwksURL,
			ClientID: clientID,
		}
	}

	// Get authentication middleware
	authMiddleware, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %v", err)
	}
	middlewares = append(middlewares, authMiddleware)

	// Create the transparent proxy
	logger.Infof("Setting up transparent proxy to forward from host port %d to %s",
		port, proxyTargetURI)

	// Create the transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(proxyHost, port, serverName, proxyTargetURI, middlewares...)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	logger.Infof("Transparent proxy started for server %s on port %d -> %s",
		serverName, port, proxyTargetURI)
	logger.Info("Press Ctrl+C to stop")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigCh
	logger.Infof("Received signal %s, stopping proxy...", sig)

	// Stop the proxy
	if err := proxy.Stop(ctx); err != nil {
		logger.Warnf("Warning: Failed to stop proxy: %v", err)
	}

	logger.Infof("Proxy for server %s stopped", serverName)
	return nil
}

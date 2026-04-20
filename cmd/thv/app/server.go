// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	s "github.com/stacklok/toolhive/pkg/api"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpserver "github.com/stacklok/toolhive/pkg/mcp/server"
	sentrypkg "github.com/stacklok/toolhive/pkg/sentry"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

var (
	host                   string
	port                   int
	enableDocs             bool
	socketPath             string
	enableMCPServer        bool
	mcpServerPort          string
	mcpServerHost          string
	sentryDSN              string
	sentryEnvironment      string
	sentryTracesSampleRate float64
)

// ApplyServerExtensions is an optional hook called with the ServerBuilder just
// before the server is created. Enterprise builds use this to inject middleware
// and mount additional routes without modifying this file.
var ApplyServerExtensions func(*s.ServerBuilder)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the ToolHive API server",
	Long:  `Starts the ToolHive API server and listen for HTTP requests.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Ensure server is shutdown gracefully on Ctrl+C or SIGTERM.
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		// Get debug mode flag
		debugMode, _ := cmd.Flags().GetBool("debug")

		// Resolve Sentry DSN from flag then env var to avoid exposing secrets in
		// process listings (ps aux / /proc/<pid>/cmdline).
		dsn := sentryDSN
		if dsn == "" {
			dsn = os.Getenv("SENTRY_DSN")
		}
		env := sentryEnvironment
		if env == "" {
			env = os.Getenv("SENTRY_ENVIRONMENT")
		}

		// Initialize Sentry for error reporting and panic capture.
		// Must happen before telemetry.NewServeProvider so the Sentry span
		// processor is registered in time to be picked up by NewProvider.
		sentryCfg := sentrypkg.Config{
			DSN:              dsn,
			Environment:      env,
			TracesSampleRate: sentryTracesSampleRate,
			Debug:            debugMode,
		}
		if err := sentrypkg.Init(sentryCfg); err != nil {
			return fmt.Errorf("failed to initialize sentry: %w", err)
		}

		// Initialize OTEL provider from global config (thv config otel set-endpoint).
		// If Sentry is also initialized, the Sentry span processor is wired in so spans
		// are exported to both the configured OTLP backend and Sentry simultaneously.
		otelProvider, otelEnabled, err := telemetry.NewServeProvider(ctx)
		if err != nil {
			return err
		}

		// Shutdown ordering is intentionally LIFO via defer:
		//   1. OTEL provider shuts down first — flushes the Sentry span processor
		//      (which calls hub.Flush internally) before the Sentry client is closed.
		//   2. Sentry client closes second — safe because the span processor has
		//      already flushed by the time sentrypkg.Close() runs.
		// Using defer instead of a goroutine makes the ordering deterministic.
		if otelProvider != nil {
			defer func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				if err := otelProvider.Shutdown(shutdownCtx); err != nil {
					slog.Warn("telemetry shutdown error", "error", err)
				}
			}()
		}
		defer sentrypkg.Close()

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
			fmt.Println("EXPERIMENTAL: Starting embedded MCP server")

			mcpConfig := &mcpserver.Config{
				Host: mcpServerHost,
				Port: mcpServerPort,
			}

			go func() {
				mcpServer, err := mcpserver.New(ctx, mcpConfig)
				if err != nil {
					slog.Error("Failed to create MCP server, continuing without it", "error", err)
					return
				}

				go func() {
					<-ctx.Done()
					shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer shutdownCancel()
					if err := mcpServer.Shutdown(shutdownCtx); err != nil {
						slog.Error("Failed to shutdown MCP server", "error", err)
					}
				}()

				if err := mcpServer.Start(); err != nil {
					slog.Error("MCP server error", "error", err)
				}
			}()
		}

		// Use ServerBuilder directly to set otelEnabled without adding it as a
		// positional parameter on the Serve() convenience function.
		nonce, err := s.GenerateNonce()
		if err != nil {
			return err
		}
		builder := s.NewServerBuilder().
			WithAddress(address).
			WithUnixSocket(isUnixSocket).
			WithDebugMode(debugMode).
			WithDocs(enableDocs).
			WithNonce(nonce).
			WithOIDCConfig(oidcConfig).
			WithOtelEnabled(otelEnabled)

		if ApplyServerExtensions != nil {
			ApplyServerExtensions(builder)
		}

		server, err := s.NewServer(ctx, builder)
		if err != nil {
			return err
		}
		return server.Start(ctx)
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

	// Add Sentry flags. The DSN and environment also fall back to the SENTRY_DSN
	// and SENTRY_ENVIRONMENT environment variables respectively, which is the
	// preferred way to supply credentials (avoids exposing the DSN in ps output).
	serveCmd.Flags().StringVar(&sentryDSN, "sentry-dsn", "",
		"Sentry DSN for error tracking and distributed tracing (falls back to SENTRY_DSN env var)")
	serveCmd.Flags().StringVar(&sentryEnvironment, "sentry-environment", "",
		"Sentry environment name, e.g. production or development (falls back to SENTRY_ENVIRONMENT env var)")
	serveCmd.Flags().Float64Var(&sentryTracesSampleRate, "sentry-traces-sample-rate", 1.0,
		"Sentry traces sample rate (0.0-1.0) for performance monitoring")

	// Add OIDC validation flags
	AddOIDCFlags(serveCmd)
}

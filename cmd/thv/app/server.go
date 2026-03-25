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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	s "github.com/stacklok/toolhive/pkg/api"
	"github.com/stacklok/toolhive/pkg/auth"
	cfg "github.com/stacklok/toolhive/pkg/config"
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

		// Initialize Sentry for error reporting and panic capture.
		sentryCfg := sentrypkg.Config{
			DSN:              sentryDSN,
			Environment:      sentryEnvironment,
			TracesSampleRate: sentryTracesSampleRate,
			Debug:            debugMode,
		}
		if err := sentrypkg.Init(sentryCfg); err != nil {
			return fmt.Errorf("failed to initialize sentry: %w", err)
		}
		defer sentrypkg.Close()

		// Initialize OTEL provider from global config (thv config otel set-endpoint).
		// If Sentry is also initialized, the Sentry span processor is wired in so spans
		// are exported to both the configured OTLP backend and Sentry simultaneously.
		otelEnabled, err := initServeOTEL(ctx, debugMode)
		if err != nil {
			return err
		}

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
					slog.Error(fmt.Sprintf("MCP server error: %v", err))
				}
			}()

			// Ensure MCP server is shut down on context cancellation
			go func() {
				<-ctx.Done()
				// Use Background context for MCP server shutdown. The parent context is already
				// cancelled at this point, so we need a fresh context with its own timeout to
				// ensure the shutdown operation completes successfully.
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutdownCancel()
				if err := mcpServer.Shutdown(shutdownCtx); err != nil {
					slog.Error(fmt.Sprintf("Failed to shutdown MCP server: %v", err))
				}
			}()
		}

		return s.Serve(ctx, address, isUnixSocket, debugMode, enableDocs, oidcConfig, otelEnabled)
	},
}

// initServeOTEL initialises the OTEL provider for thv serve using the global config
// (set via `thv config otel set-endpoint`). No new CLI flags are introduced; serve reuses
// the same OTEL config as thv run. If Sentry is also initialised, the Sentry span processor
// is registered so spans are exported to both the configured OTLP backend and Sentry.
// Returns true when OTEL HTTP middleware should be enabled on the API server.
func initServeOTEL(ctx context.Context, _ bool) (bool, error) {
	configProvider := cfg.NewDefaultProvider()
	appConfig := configProvider.GetConfig()

	otelCfg := appConfig.OTEL
	hasSentryProcessor := sentrypkg.SpanProcessor() != nil
	if otelCfg.Endpoint == "" && !otelCfg.EnablePrometheusMetricsPath && !hasSentryProcessor {
		return false, nil
	}

	telemetryCfg := telemetry.Config{
		ServiceName:                 "thv-api",
		Endpoint:                    otelCfg.Endpoint,
		TracingEnabled:              otelCfg.TracingEnabled,
		MetricsEnabled:              otelCfg.MetricsEnabled,
		Insecure:                    otelCfg.Insecure,
		EnablePrometheusMetricsPath: otelCfg.EnablePrometheusMetricsPath,
		EnvironmentVariables:        otelCfg.EnvVars,
	}
	if otelCfg.SamplingRate != 0.0 {
		telemetryCfg.SetSamplingRateFromFloat(otelCfg.SamplingRate)
	}
	if telemetryCfg.SamplingRate == "" {
		telemetryCfg.SamplingRate = "0.05"
	}

	// Sentry-only mode: no OTLP endpoint but the Sentry span processor is active.
	// Force tracing on with 100% OTEL sampling so every span reaches the Sentry processor.
	// Sentry's own TracesSampleRate flag is then the sole sampling gate.
	if otelCfg.Endpoint == "" && hasSentryProcessor {
		telemetryCfg.TracingEnabled = true
		telemetryCfg.SamplingRate = "1.0"
	}

	var extraProcessors []sdktrace.SpanProcessor
	if sp := sentrypkg.SpanProcessor(); sp != nil {
		extraProcessors = append(extraProcessors, sp)
		slog.Debug("sentry span processor registered with OTEL")
	}

	provider, err := telemetry.NewProvider(ctx, telemetryCfg, extraProcessors...)
	if err != nil {
		return false, fmt.Errorf("failed to initialize telemetry: %w", err)
	}

	// Provider shutdown is deferred via the serve context — provider.Shutdown is tied to the
	// context cancel so we don't need an explicit defer here; the caller's defer cancel() is enough.
	// However, we register a goroutine to flush on context cancellation.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			slog.Warn("telemetry shutdown error", "error", err)
		}
	}()

	slog.Debug("OTEL provider initialized for thv serve",
		"endpoint", otelCfg.Endpoint,
		"tracing", otelCfg.TracingEnabled,
		"metrics", otelCfg.MetricsEnabled)

	return true, nil
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

	// Add Sentry flags
	serveCmd.Flags().StringVar(&sentryDSN, "sentry-dsn", "",
		"Sentry DSN for error tracking and distributed tracing (env: SENTRY_DSN)")
	serveCmd.Flags().StringVar(&sentryEnvironment, "sentry-environment", "",
		"Sentry environment name, e.g. production or development (env: SENTRY_ENVIRONMENT)")
	serveCmd.Flags().Float64Var(&sentryTracesSampleRate, "sentry-traces-sample-rate", 1.0,
		"Sentry traces sample rate (0.0-1.0) for performance monitoring (env: SENTRY_TRACES_SAMPLE_RATE)")

	// Add OIDC validation flags
	AddOIDCFlags(serveCmd)
}

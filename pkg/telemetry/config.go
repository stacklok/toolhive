// Package telemetry provides OpenTelemetry instrumentation for ToolHive MCP server proxies.
package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/telemetry/providers"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/versions"
)

// Config holds the configuration for OpenTelemetry instrumentation.
type Config struct {
	// Endpoint is the OTLP endpoint URL
	Endpoint string

	// ServiceName is the service name for telemetry
	ServiceName string

	// ServiceVersion is the service version for telemetry
	ServiceVersion string

	// SamplingRate is the trace sampling rate (0.0-1.0)
	SamplingRate float64

	// Headers contains authentication headers for the OTLP endpoint
	Headers map[string]string

	// Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint
	Insecure bool

	// EnablePrometheusMetricsPath controls whether to expose Prometheus-style /metrics endpoint
	// The metrics are served on the main transport port at /metrics
	// This is separate from OTLP metrics which are sent to the Endpoint
	EnablePrometheusMetricsPath bool

	// EnvironmentVariables is a list of environment variable names that should be
	// included in telemetry spans as attributes. Only variables in this list will
	// be read from the host machine and included in spans for observability.
	// Example: []string{"NODE_ENV", "DEPLOYMENT_ENV", "SERVICE_VERSION"}
	EnvironmentVariables []string
}

// DefaultConfig returns a default telemetry configuration.
func DefaultConfig() Config {
	versionInfo := versions.GetVersionInfo()
	return Config{
		ServiceName:                 "toolhive-mcp-proxy",
		ServiceVersion:              versionInfo.Version,
		SamplingRate:                0.1, // 10% sampling by default
		Headers:                     make(map[string]string),
		Insecure:                    false,
		EnablePrometheusMetricsPath: false,      // No metrics endpoint by default
		EnvironmentVariables:        []string{}, // No environment variables by default
	}
}

// Provider encapsulates OpenTelemetry providers and configuration.
type Provider struct {
	config            Config
	tracerProvider    trace.TracerProvider
	meterProvider     metric.MeterProvider
	prometheusHandler http.Handler
	shutdown          func(context.Context) error
}

// NewProvider creates a new OpenTelemetry provider with the given configuration.
func NewProvider(ctx context.Context, config Config) (*Provider, error) {
	// Use the new factory pattern
	builderConfig := providers.Config{
		ServiceName:                 config.ServiceName,
		ServiceVersion:              config.ServiceVersion,
		OTLPEndpoint:                config.Endpoint,
		Headers:                     config.Headers,
		Insecure:                    config.Insecure,
		SamplingRate:                config.SamplingRate,
		EnablePrometheusMetricsPath: config.EnablePrometheusMetricsPath,
	}

	builder := providers.NewBuilder(builderConfig)
	composite, err := builder.Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build telemetry providers: %w", err)
	}

	// Set global providers
	setGlobalProviders(composite.TracerProvider(), composite.MeterProvider())

	return &Provider{
		config:            config,
		tracerProvider:    composite.TracerProvider(),
		meterProvider:     composite.MeterProvider(),
		prometheusHandler: composite.PrometheusHandler(),
		shutdown:          composite.Shutdown,
	}, nil
}

// setGlobalProviders sets the global OpenTelemetry providers.
func setGlobalProviders(tracerProvider trace.TracerProvider, meterProvider metric.MeterProvider) {
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// Middleware returns an HTTP middleware that instruments requests with OpenTelemetry.
// serverName is the name of the MCP server (e.g., "github", "fetch")
// transport is the backend transport type ("stdio" or "sse")
func (p *Provider) Middleware(serverName, transport string) types.MiddlewareFunction {
	return NewHTTPMiddleware(p.config, p.tracerProvider, p.meterProvider, serverName, transport)
}

// Shutdown gracefully shuts down the telemetry provider.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.shutdown != nil {
		return p.shutdown(ctx)
	}
	return nil
}

// TracerProvider returns the configured tracer provider.
func (p *Provider) TracerProvider() trace.TracerProvider {
	return p.tracerProvider
}

// MeterProvider returns the configured meter provider.
func (p *Provider) MeterProvider() metric.MeterProvider {
	return p.meterProvider
}

// PrometheusHandler returns the Prometheus metrics handler if configured.
// Returns nil if no metrics port is configured.
func (p *Provider) PrometheusHandler() http.Handler {
	return p.prometheusHandler
}

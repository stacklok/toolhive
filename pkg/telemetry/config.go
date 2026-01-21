// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package telemetry provides OpenTelemetry instrumentation for ToolHive MCP server proxies.
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/telemetry/providers"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/versions"
)

// Config holds the configuration for OpenTelemetry instrumentation.
// +kubebuilder:object:generate=true
// +gendoc
type Config struct {
	// Endpoint is the OTLP endpoint URL
	// +optional
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`

	// ServiceName is the service name for telemetry.
	// When omitted, defaults to the server name (e.g., VirtualMCPServer name).
	// +optional
	ServiceName string `json:"serviceName,omitempty" yaml:"serviceName,omitempty"`

	// ServiceVersion is the service version for telemetry.
	// When omitted, defaults to the ToolHive version.
	// +optional
	ServiceVersion string `json:"serviceVersion,omitempty" yaml:"serviceVersion,omitempty"`

	// TracingEnabled controls whether distributed tracing is enabled.
	// When false, no tracer provider is created even if an endpoint is configured.
	// +kubebuilder:default=false
	// +optional
	TracingEnabled bool `json:"tracingEnabled,omitempty" yaml:"tracingEnabled,omitempty"`

	// MetricsEnabled controls whether OTLP metrics are enabled.
	// When false, OTLP metrics are not sent even if an endpoint is configured.
	// This is independent of EnablePrometheusMetricsPath.
	// +kubebuilder:default=false
	// +optional
	MetricsEnabled bool `json:"metricsEnabled,omitempty" yaml:"metricsEnabled,omitempty"`

	// SamplingRate is the trace sampling rate (0.0-1.0) as a string.
	// Only used when TracingEnabled is true.
	// Example: "0.05" for 5% sampling.
	// +kubebuilder:default="0.05"
	// +optional
	SamplingRate string `json:"samplingRate,omitempty" yaml:"samplingRate,omitempty"`

	// Headers contains authentication headers for the OTLP endpoint.
	// +optional
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`

	// Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint.
	// +kubebuilder:default=false
	// +optional
	Insecure bool `json:"insecure,omitempty" yaml:"insecure,omitempty"`

	// EnablePrometheusMetricsPath controls whether to expose Prometheus-style /metrics endpoint.
	// The metrics are served on the main transport port at /metrics.
	// This is separate from OTLP metrics which are sent to the Endpoint.
	// +kubebuilder:default=false
	// +optional
	EnablePrometheusMetricsPath bool `json:"enablePrometheusMetricsPath,omitempty" yaml:"enablePrometheusMetricsPath,omitempty"`

	// EnvironmentVariables is a list of environment variable names that should be
	// included in telemetry spans as attributes. Only variables in this list will
	// be read from the host machine and included in spans for observability.
	// Example: ["NODE_ENV", "DEPLOYMENT_ENV", "SERVICE_VERSION"]
	// +optional
	EnvironmentVariables []string `json:"environmentVariables,omitempty" yaml:"environmentVariables,omitempty"`

	// CustomAttributes contains custom resource attributes to be added to all telemetry signals.
	// These are parsed from CLI flags (--otel-custom-attributes) or environment variables
	// (OTEL_RESOURCE_ATTRIBUTES) as key=value pairs.
	// +optional
	CustomAttributes map[string]string `json:"customAttributes,omitempty" yaml:"customAttributes,omitempty"`
}

// GetSamplingRateFloat parses the SamplingRate string and returns it as float64.
// Returns 0.0 if the string is empty or cannot be parsed.
func (c *Config) GetSamplingRateFloat() float64 {
	if c.SamplingRate == "" {
		return 0.0
	}
	rate, err := strconv.ParseFloat(c.SamplingRate, 64)
	if err != nil {
		return 0.0
	}
	return rate
}

// SetSamplingRateFromFloat sets the SamplingRate from a float64 value.
func (c *Config) SetSamplingRateFromFloat(rate float64) {
	c.SamplingRate = strconv.FormatFloat(rate, 'f', -1, 64)
}

// DefaultConfig returns a default telemetry configuration.
func DefaultConfig() Config {
	versionInfo := versions.GetVersionInfo()
	return Config{
		ServiceName:                 "toolhive-mcp-proxy",
		ServiceVersion:              versionInfo.Version,
		TracingEnabled:              true,   // Enable tracing by default if endpoint is configured
		MetricsEnabled:              true,   // Enable metrics by default if endpoint is configured
		SamplingRate:                "0.05", // 5% sampling by default
		Headers:                     make(map[string]string),
		Insecure:                    false,
		EnablePrometheusMetricsPath: false,      // No metrics endpoint by default
		EnvironmentVariables:        []string{}, // No environment variables by default
	}
}

// MaybeMakeConfig creates a new telemetry configuration from the given values.
// It may return nil if no telemetry is configured.
func MaybeMakeConfig(
	otelEndpoint string,
	otelEnablePrometheusMetricsPath bool,
	otelTracingEnabled bool,
	otelMetricsEnabled bool,
	otelServiceName string,
	otelSamplingRate float64,
	otelHeaders []string,
	otelInsecure bool,
	otelEnvironmentVariables []string,
) *Config {
	if otelEndpoint == "" && !otelEnablePrometheusMetricsPath {
		return nil
	}
	// Parse headers from key=value format
	headers := make(map[string]string)
	for _, header := range otelHeaders {
		parts := strings.SplitN(header, "=", 2)
		if len(parts) == 2 {
			headers[parts[0]] = parts[1]
		}
	}

	// Use provided service name or default
	serviceName := otelServiceName
	if serviceName == "" {
		serviceName = DefaultConfig().ServiceName
	}

	// Process environment variables - split comma-separated values
	var processedEnvVars []string
	for _, envVarEntry := range otelEnvironmentVariables {
		// Split by comma and trim whitespace
		envVars := strings.Split(envVarEntry, ",")
		for _, envVar := range envVars {
			trimmed := strings.TrimSpace(envVar)
			if trimmed != "" {
				processedEnvVars = append(processedEnvVars, trimmed)
			}
		}
	}
	return &Config{
		Endpoint:                    otelEndpoint,
		ServiceName:                 serviceName,
		ServiceVersion:              DefaultConfig().ServiceVersion,
		TracingEnabled:              otelTracingEnabled,
		MetricsEnabled:              otelMetricsEnabled,
		SamplingRate:                strconv.FormatFloat(otelSamplingRate, 'f', -1, 64),
		Headers:                     headers,
		Insecure:                    otelInsecure,
		EnablePrometheusMetricsPath: otelEnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
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
	// Validate configuration
	if err := validateOtelConfig(config); err != nil {
		return nil, err
	}

	telemetryOptions := []providers.ProviderOption{
		providers.WithServiceName(config.ServiceName),
		providers.WithServiceVersion(config.ServiceVersion),
		providers.WithOTLPEndpoint(config.Endpoint),
		providers.WithHeaders(config.Headers),
		providers.WithInsecure(config.Insecure),
		providers.WithTracingEnabled(config.TracingEnabled),
		providers.WithMetricsEnabled(config.MetricsEnabled),
		providers.WithSamplingRate(config.GetSamplingRateFloat()),
		providers.WithEnablePrometheusMetricsPath(config.EnablePrometheusMetricsPath),
		providers.WithCustomAttributes(config.CustomAttributes),
	}

	telemetryProviders, err := providers.NewCompositeProvider(ctx, telemetryOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to build telemetry providers: %w", err)
	}

	return setGlobalProvidersAndReturn(telemetryProviders, config)
}

// setGlobalProvidersAndReturn sets the global providers for OTEL and returns the providers
func setGlobalProvidersAndReturn(telemetryProviders *providers.CompositeProvider, config Config) (*Provider, error) {
	tracingProvider := telemetryProviders.TracerProvider()
	meterProvider := telemetryProviders.MeterProvider()

	// set the global providers for OTEL
	otel.SetTracerProvider(tracingProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Provider{
		config:            config,
		tracerProvider:    tracingProvider,
		meterProvider:     meterProvider,
		prometheusHandler: telemetryProviders.PrometheusHandler(),
		shutdown:          telemetryProviders.Shutdown,
	}, nil
}

// Middleware returns an HTTP middleware that instruments requests with OpenTelemetry.
// serverName is the name of the MCP server (e.g., "github", "fetch")
// transport is the backend transport type ("stdio", "sse", or "streamable-http").
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

// validateOtelConfig validates the otel configuration
func validateOtelConfig(config Config) error {
	// If OTLP endpoint is configured but both tracing and metrics are disabled, that's an error
	if config.Endpoint != "" && !config.TracingEnabled && !config.MetricsEnabled {
		return fmt.Errorf("OTLP endpoint is configured but both tracing and metrics are disabled; " +
			"either enable tracing or metrics, or remove the endpoint")
	}
	return nil
}

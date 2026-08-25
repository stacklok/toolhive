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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive-core/telemetry/providers"
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
	TracingEnabled bool `json:"tracingEnabled" yaml:"tracingEnabled"`

	// MetricsEnabled controls whether OTLP metrics are enabled.
	// When false, OTLP metrics are not sent even if an endpoint is configured.
	// This is independent of EnablePrometheusMetricsPath.
	// +kubebuilder:default=false
	// +optional
	MetricsEnabled bool `json:"metricsEnabled" yaml:"metricsEnabled"`

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
	// The metrics are served at /metrics on a dedicated diagnostics port rather than on the
	// main transport port, so the endpoint can be restricted by port and is not routed
	// alongside application traffic. The endpoint is unauthenticated either way.
	// See PrometheusPort and pkg/diagnostics.
	// This is separate from OTLP metrics which are sent to the Endpoint.
	// +kubebuilder:default=false
	// +optional
	EnablePrometheusMetricsPath bool `json:"enablePrometheusMetricsPath,omitempty" yaml:"enablePrometheusMetricsPath,omitempty"`

	// PrometheusPort is the port the Prometheus /metrics endpoint is served on when
	// EnablePrometheusMetricsPath is true. It is deliberately not the main transport port,
	// so that access can be restricted with a NetworkPolicy: NetworkPolicy matches on port,
	// not on HTTP path, so a shared port makes "allow MCP traffic, deny metrics scraping"
	// impossible to express. The endpoint itself is unauthenticated, so restricting who can
	// reach this port is how it is protected.
	//
	// Zero selects the default diagnostics port (9464, the OpenTelemetry specification's
	// Prometheus exporter default). If that port is taken the listener falls back to an
	// available one and logs the resolved address. Do not route this port publicly.
	// +optional
	PrometheusPort int `json:"prometheusPort,omitempty" yaml:"prometheusPort,omitempty"`

	// MetricsOnTransportPort controls whether /metrics is ALSO served on the main
	// transport port, in addition to the diagnostics port. It exists to give
	// deployments a migration window: while true, an existing scrape configuration
	// aimed at the transport port keeps working, and a new one aimed at
	// PrometheusPort works too, so a scraper can be moved and verified before the
	// old location goes away. See https://github.com/stacklok/toolhive/issues/6384 for
	// the removal timeline.
	//
	// Deliberately a pointer with NO kubebuilder default. Nil means "unset", and is
	// resolved against DefaultMetricsOnTransportPort at the point of use rather than
	// written into config. A plain bool with a default marker would be materialised
	// into the persisted RunConfig and into CRD objects at admission, so changing
	// the default later would not move any workload that already exists — the flip
	// would silently do nothing.
	//
	// +optional
	MetricsOnTransportPort *bool `json:"metricsOnTransportPort,omitempty" yaml:"metricsOnTransportPort,omitempty"`

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

	// UseLegacyAttributes controls whether legacy (pre-MCP OTEL semconv) attribute names
	// are emitted alongside the new standard attribute names. When true, spans include both
	// old and new attribute names for backward compatibility with existing dashboards.
	// Currently defaults to true; this will change to false in a future release.
	// +kubebuilder:default=true
	// +optional
	UseLegacyAttributes bool `json:"useLegacyAttributes" yaml:"useLegacyAttributes"`

	// CACertPath is the file path to a CA certificate bundle for the OTLP endpoint.
	// When set, the OTLP exporters use this CA to verify the collector's TLS certificate
	// instead of relying solely on the system CA pool.
	// +optional
	CACertPath string `json:"caCertPath,omitempty" yaml:"caCertPath,omitempty"`
}

// Ensure Config implements fmt.Stringer and fmt.GoStringer
var _ fmt.Stringer = Config{}
var _ fmt.GoStringer = Config{}

// GoString returns the same redacted representation as String().
// This prevents credential leakage via the %#v format verb, which calls GoString() instead of String().
func (c Config) GoString() string {
	return c.String()
}

// String returns a human-readable representation of the Config with sensitive header values redacted.
func (c Config) String() string {
	// Redact header values to prevent credential leakage
	redactedHeaders := make(map[string]string, len(c.Headers))
	for key := range c.Headers {
		redactedHeaders[key] = "[REDACTED]"
	}

	return fmt.Sprintf("Config{Endpoint: %q, ServiceName: %q, ServiceVersion: %q, TracingEnabled: %t, "+
		"MetricsEnabled: %t, SamplingRate: %q, Headers: %v, Insecure: %t, "+
		"EnablePrometheusMetricsPath: %t, PrometheusPort: %d, MetricsOnTransportPort: %t, "+
		"EnvironmentVariables: %v, CustomAttributes: %v, "+
		"UseLegacyAttributes: %t, CACertPath: %q}",
		c.Endpoint, c.ServiceName, c.ServiceVersion, c.TracingEnabled,
		c.MetricsEnabled, c.SamplingRate, redactedHeaders, c.Insecure,
		c.EnablePrometheusMetricsPath, c.PrometheusPort, c.ServeMetricsOnTransportPort(),
		c.EnvironmentVariables, c.CustomAttributes,
		c.UseLegacyAttributes, c.CACertPath)
}

// DefaultMetricsOnTransportPort is the value used when Config.MetricsOnTransportPort
// is unset.
//
// True during the deprecation window: /metrics is served on both the transport port
// and the diagnostics port, so no existing scrape configuration breaks. Flipping this
// to false is the entire cutover — it stops serving the transport-port copy and leaves
// only the diagnostics port. Deployments that set MetricsOnTransportPort explicitly
// are unaffected by the flip, by design.
//
// Flipping it is not the whole job: #6384 carries the timeline and the cleanup this
// field, its CLI flag, and its CRD entry all need afterwards.
const DefaultMetricsOnTransportPort = true

// ServeMetricsOnTransportPort reports whether /metrics should also be served on the
// main transport port. It resolves the unset case at the point of use, which is what
// makes changing DefaultMetricsOnTransportPort take effect for workloads that already
// exist. A nil receiver reports the default so callers need no nil check.
func (c *Config) ServeMetricsOnTransportPort() bool {
	if c == nil || c.MetricsOnTransportPort == nil {
		return DefaultMetricsOnTransportPort
	}
	return *c.MetricsOnTransportPort
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

// DefaultServiceNamePrefix is prepended to the workload name when deriving the
// OTel service name automatically (e.g. "thv-fetch", "thv-github").
const DefaultServiceNamePrefix = "thv-"

// DefaultConfig returns a default telemetry configuration.
func DefaultConfig() Config {
	return Config{
		ServiceName:                 "",     // empty — resolved at runtime from the workload name
		ServiceVersion:              "",     // resolved at runtime in NewProvider()
		TracingEnabled:              true,   // Enable tracing by default if endpoint is configured
		MetricsEnabled:              true,   // Enable metrics by default if endpoint is configured
		SamplingRate:                "0.05", // 5% sampling by default
		Headers:                     make(map[string]string),
		Insecure:                    false,
		EnablePrometheusMetricsPath: false,      // No metrics endpoint by default
		EnvironmentVariables:        []string{}, // No environment variables by default
		UseLegacyAttributes:         true,       // Dual emission for backward compat
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
	otelUseLegacyAttributes bool,
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
		ServiceName:                 otelServiceName,
		ServiceVersion:              "", // resolved at runtime in NewProvider()
		TracingEnabled:              otelTracingEnabled,
		MetricsEnabled:              otelMetricsEnabled,
		SamplingRate:                strconv.FormatFloat(otelSamplingRate, 'f', -1, 64),
		Headers:                     headers,
		Insecure:                    otelInsecure,
		EnablePrometheusMetricsPath: otelEnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
		UseLegacyAttributes:         otelUseLegacyAttributes,
	}
}

// ResolveServiceName sets the telemetry service name on the config if it has
// not been explicitly provided. When empty, it derives the name from the
// workload/server name with the "thv-" prefix (e.g. "thv-fetch").
func ResolveServiceName(config *Config, serverName string) {
	if config == nil || config.ServiceName != "" {
		return
	}
	if serverName != "" {
		config.ServiceName = DefaultServiceNamePrefix + serverName
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
// Optional extra span processors (e.g. a Sentry bridge) can be registered via extraProcessors.
func NewProvider(ctx context.Context, config Config, extraProcessors ...sdktrace.SpanProcessor) (*Provider, error) {
	// Validate configuration
	if err := validateOtelConfig(config); err != nil {
		return nil, err
	}

	// Always use the current binary version so that restarts and exports
	// report the version actually running, not the version that originally
	// created the config. See https://github.com/stacklok/toolhive/issues/2296
	serviceVersion := config.ServiceVersion
	if serviceVersion == "" {
		serviceVersion = versions.GetVersionInfo().Version
	}

	telemetryOptions := []providers.ProviderOption{
		providers.WithServiceName(config.ServiceName),
		providers.WithServiceVersion(serviceVersion),
		providers.WithOTLPEndpoint(config.Endpoint),
		providers.WithHeaders(config.Headers),
		providers.WithInsecure(config.Insecure),
		providers.WithCACertPath(config.CACertPath),
		providers.WithTracingEnabled(config.TracingEnabled),
		providers.WithMetricsEnabled(config.MetricsEnabled),
		providers.WithSamplingRate(config.GetSamplingRateFloat()),
		providers.WithEnablePrometheusMetricsPath(config.EnablePrometheusMetricsPath),
		providers.WithCustomAttributes(config.CustomAttributes),
	}

	// Merge globally registered processors (self-registered by integrations such
	// as a Sentry bridge) with any explicitly passed ones.
	allProcessors := append(registeredSpanProcessors(), extraProcessors...)
	if len(allProcessors) > 0 {
		telemetryOptions = append(telemetryOptions, providers.WithExtraSpanProcessors(allProcessors...))
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

// PrometheusPort returns the configured diagnostics port for the Prometheus
// /metrics endpoint. Zero means unset; see diagnostics.ResolvePort.
func (p *Provider) PrometheusPort() int {
	return p.config.PrometheusPort
}

// ServeMetricsOnTransportPort reports whether this provider's configuration also
// wants /metrics on the main transport port. Resolves the unset case against the
// current default; see Config.ServeMetricsOnTransportPort.
func (p *Provider) ServeMetricsOnTransportPort() bool {
	return p.config.ServeMetricsOnTransportPort()
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

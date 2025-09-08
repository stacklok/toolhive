// Package providers contains telemetry provider implementations and builder logic
package providers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Config holds the telemetry configuration for all providers.
// It contains service information, OTLP settings, and Prometheus configuration.
type Config struct {
	// Service information
	ServiceName    string // ServiceName identifies the service for telemetry data
	ServiceVersion string // ServiceVersion identifies the service version for telemetry data

	// OTLP configuration
	OTLPEndpoint   string            // OTLPEndpoint is the OTLP collector endpoint (e.g., "localhost:4318")
	Headers        map[string]string // Headers are additional headers to send with OTLP requests
	Insecure       bool              // Insecure enables insecure transport (no TLS) for OTLP
	TracingEnabled bool              // TracingEnabled controls whether tracing is enabled for OTLP
	MetricsEnabled bool              // MetricsEnabled controls whether metrics are enabled for OTLP
	SamplingRate   float64           // SamplingRate controls trace sampling (0.0 to 1.0)

	// Prometheus configuration
	EnablePrometheusMetricsPath bool // EnablePrometheusMetricsPath enables Prometheus /metrics endpoint
}

// ProviderOption is an option type used to configure the telemetry providers
type ProviderOption func(*Config) error

// WithServiceName sets the service name
func WithServiceName(serviceName string) ProviderOption {
	return func(config *Config) error {
		if serviceName == "" {
			return fmt.Errorf("service cannot be empty")
		}
		config.ServiceName = serviceName
		return nil
	}
}

// WithServiceVersion sets the service version
func WithServiceVersion(serviceVersion string) ProviderOption {
	return func(config *Config) error {
		if serviceVersion == "" {
			return fmt.Errorf("service cannot be empty")
		}
		config.ServiceVersion = serviceVersion
		return nil
	}
}

// WithOTLPEndpoint sets the OTLP endpoint
func WithOTLPEndpoint(endpoint string) ProviderOption {
	return func(config *Config) error {
		config.OTLPEndpoint = endpoint
		return nil
	}
}

// WithHeaders sets the headers
func WithHeaders(headers map[string]string) ProviderOption {
	return func(config *Config) error {
		config.Headers = headers
		return nil
	}
}

// WithInsecure sets the insecure flag
func WithInsecure(insecure bool) ProviderOption {
	return func(config *Config) error {
		config.Insecure = insecure
		return nil
	}
}

// WithTracingEnabled sets the tracing enabled flag
func WithTracingEnabled(tracingEnabled bool) ProviderOption {
	return func(config *Config) error {
		config.TracingEnabled = tracingEnabled
		return nil
	}
}

// WithMetricsEnabled sets the metrics enabled flag
func WithMetricsEnabled(metricsEnabled bool) ProviderOption {
	return func(config *Config) error {
		config.MetricsEnabled = metricsEnabled
		return nil
	}
}

// WithSamplingRate sets the sampling rate
func WithSamplingRate(samplingRate float64) ProviderOption {
	return func(config *Config) error {
		config.SamplingRate = samplingRate
		return nil
	}
}

// WithEnablePrometheusMetricsPath sets the enable prometheus metrics path flag
func WithEnablePrometheusMetricsPath(enablePrometheusMetricsPath bool) ProviderOption {
	return func(config *Config) error {
		config.EnablePrometheusMetricsPath = enablePrometheusMetricsPath
		return nil
	}
}

// CompositeProvider combines telemetry providers into a single interface.
// It manages tracer providers, meter providers, Prometheus handlers, and cleanup.
type CompositeProvider struct {
	tracerProvider    trace.TracerProvider          // tracerProvider provides distributed tracing
	meterProvider     metric.MeterProvider          // meterProvider provides metrics collection
	prometheusHandler http.Handler                  // prometheusHandler serves Prometheus metrics
	shutdownFuncs     []func(context.Context) error // shutdownFuncs clean up resources on shutdown
}

// NewCompositeProvider creates the appropriate providers based on provided options
func NewCompositeProvider(
	ctx context.Context,
	options ...ProviderOption,
) (*CompositeProvider, error) {
	config := Config{}
	for _, option := range options {
		if err := option(&config); err != nil {
			return nil, err
		}
	}

	// Create resource for all providers
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceVersion(config.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource with service name '%s' and version '%s': %w",
			config.ServiceName, config.ServiceVersion, err)
	}

	// Use strategy selector to determine provider strategies
	selector := &StrategySelector{config: config}

	// Early return for no-op case
	if selector.IsFullyNoOp() {
		logger.Infof("No telemetry configured, using no-op providers")
		return createNoOpProvider(), nil
	}

	// Build composite provider using strategies
	return buildProviders(ctx, config, selector, res)
}

func createNoOpProvider() *CompositeProvider {
	return &CompositeProvider{
		tracerProvider:    tracenoop.NewTracerProvider(),
		meterProvider:     noop.NewMeterProvider(),
		prometheusHandler: nil,
		shutdownFuncs:     []func(context.Context) error{},
	}
}

// buildProviders creates a composite provider using the selected strategies
func buildProviders(
	ctx context.Context,
	config Config,
	selector *StrategySelector,
	res *resource.Resource,
) (*CompositeProvider, error) {
	composite := &CompositeProvider{
		shutdownFuncs: []func(context.Context) error{},
	}

	if err := createMetricsProvider(ctx, config, composite, selector, res); err != nil {
		return nil, err
	}

	if err := createTracingProvider(ctx, config, composite, selector, res); err != nil {
		return nil, err
	}

	logger.Infof("Telemetry providers created successfully")
	return composite, nil
}

// createMetricsProvider creates the metrics provider for the composite provider
func createMetricsProvider(
	ctx context.Context,
	config Config,
	composite *CompositeProvider,
	selector *StrategySelector,
	res *resource.Resource,
) error {
	// Create meter provider using selected strategy
	meterStrategy := selector.SelectMeterStrategy()
	meterResult, err := meterStrategy.CreateMeterProvider(ctx, config, res)
	if err != nil {
		return fmt.Errorf(
			"failed to create meter provider with config (endpoint: %s, metrics enabled: %t, prometheus enabled: %t): %w",
			config.OTLPEndpoint,
			config.MetricsEnabled,
			config.EnablePrometheusMetricsPath,
			err)
	}

	composite.meterProvider = meterResult.MeterProvider
	composite.prometheusHandler = meterResult.PrometheusHandler

	if meterResult.ShutdownFunc != nil {
		composite.shutdownFuncs = append(composite.shutdownFuncs, meterResult.ShutdownFunc)
	}

	return nil
}

// createTracingProvider creates the tracing provider for the composite provider
func createTracingProvider(
	ctx context.Context,
	config Config,
	composite *CompositeProvider,
	selector *StrategySelector,
	res *resource.Resource,
) error {
	// Create tracer provider using selected strategy
	tracerStrategy := selector.SelectTracerStrategy()
	tracerProvider, tracerShutdown, err := tracerStrategy.CreateTracerProvider(ctx, config, res)
	if err != nil {
		return fmt.Errorf("failed to create tracer provider with config (endpoint: %s, tracing enabled: %t): %w",
			config.OTLPEndpoint,
			config.TracingEnabled,
			err)
	}

	composite.tracerProvider = tracerProvider

	if tracerShutdown != nil {
		composite.shutdownFuncs = append(composite.shutdownFuncs, tracerShutdown)
	}

	return nil
}

// TracerProvider returns the tracer provider
func (p *CompositeProvider) TracerProvider() trace.TracerProvider {
	return p.tracerProvider
}

// MeterProvider returns the primary meter provider
func (p *CompositeProvider) MeterProvider() metric.MeterProvider {
	return p.meterProvider
}

// PrometheusHandler returns the Prometheus metrics handler if configured
func (p *CompositeProvider) PrometheusHandler() http.Handler {
	return p.prometheusHandler
}

// Shutdown gracefully shuts down all providers
func (p *CompositeProvider) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var errs []error
	for i, shutdown := range p.shutdownFuncs {
		if err := shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("provider %d shutdown failed: %w", i, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown failed with %d errors: %v", len(errs), errs)
	}
	return nil
}

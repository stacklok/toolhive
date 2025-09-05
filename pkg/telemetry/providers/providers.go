// Package providers contains telemetry provider implementations and assembler logic
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

// Assembler assembles telemetry providers based on configuration.
// It validates configuration, creates resources, and assembles composite providers.
type Assembler struct {
	config   Config             // config holds the telemetry configuration
	resource *resource.Resource // resource holds the OpenTelemetry resource information
}

// CompositeProvider combines telemetry providers into a single interface.
// It manages tracer providers, meter providers, Prometheus handlers, and cleanup.
type CompositeProvider struct {
	tracerProvider    trace.TracerProvider          // tracerProvider provides distributed tracing
	meterProvider     metric.MeterProvider          // meterProvider provides metrics collection
	prometheusHandler http.Handler                  // prometheusHandler serves Prometheus metrics
	shutdownFuncs     []func(context.Context) error // shutdownFuncs clean up resources on shutdown
}

// WithConfig creates a new provider assembler with the given configuration
func WithConfig(config Config) *Assembler {
	return &Assembler{
		config: config,
	}
}

// Assemble creates the appropriate providers based on configuration
func (b *Assembler) Assemble(ctx context.Context) (*CompositeProvider, error) {

	// Create resource for all providers
	err := b.createResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Use strategy selector to determine provider strategies
	selector := &StrategySelector{config: b.config}

	// Early return for no-op case
	if selector.IsFullyNoOp() {
		logger.Infof("No telemetry configured, using no-op providers")
		return b.createNoOpProvider(), nil
	}

	// Assemble composite provider using strategies
	return b.assembleProviders(ctx, selector, b.resource)
}

// createResource creates an OpenTelemetry resource with service information
func (b *Assembler) createResource(ctx context.Context) error {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(b.config.ServiceName),
			semconv.ServiceVersion(b.config.ServiceVersion),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create resource with service name '%s' and version '%s': %w",
			b.config.ServiceName, b.config.ServiceVersion, err)
	}

	b.resource = res
	return nil
}

// createNoOpProvider returns a composite provider with all no-op implementations
func (*Assembler) createNoOpProvider() *CompositeProvider {
	return &CompositeProvider{
		tracerProvider:    tracenoop.NewTracerProvider(),
		meterProvider:     noop.NewMeterProvider(),
		prometheusHandler: nil,
		shutdownFuncs:     []func(context.Context) error{},
	}
}

// assembleProviders creates a composite provider using the selected strategies
func (b *Assembler) assembleProviders(
	ctx context.Context,
	selector *StrategySelector,
	res *resource.Resource,
) (*CompositeProvider, error) {
	composite := &CompositeProvider{
		shutdownFuncs: []func(context.Context) error{},
	}

	if err := b.createMetricsProvider(ctx, composite, selector, res); err != nil {
		return nil, err
	}

	if err := b.createTracingProvider(ctx, composite, selector, res); err != nil {
		return nil, err
	}

	logger.Infof("Telemetry providers created successfully")
	return composite, nil
}

// createMetricsProvider creates the metrics provider for the composite provider
func (b *Assembler) createMetricsProvider(
	ctx context.Context,
	composite *CompositeProvider,
	selector *StrategySelector,
	res *resource.Resource,
) error {
	// Create meter provider using selected strategy
	meterStrategy := selector.SelectMeterStrategy()
	meterResult, err := meterStrategy.CreateMeterProvider(ctx, b.config, res)
	if err != nil {
		return fmt.Errorf(
			"failed to create meter provider with config (endpoint: %s, metrics enabled: %t, prometheus enabled: %t): %w",
			b.config.OTLPEndpoint,
			b.config.MetricsEnabled,
			b.config.EnablePrometheusMetricsPath,
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
func (b *Assembler) createTracingProvider(
	ctx context.Context,
	composite *CompositeProvider,
	selector *StrategySelector,
	res *resource.Resource,
) error {
	// Create tracer provider using selected strategy
	tracerStrategy := selector.SelectTracerStrategy()
	tracerProvider, tracerShutdown, err := tracerStrategy.CreateTracerProvider(ctx, b.config, res)
	if err != nil {
		return fmt.Errorf("failed to create tracer provider with config (endpoint: %s, tracing enabled: %t): %w",
			b.config.OTLPEndpoint,
			b.config.TracingEnabled,
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

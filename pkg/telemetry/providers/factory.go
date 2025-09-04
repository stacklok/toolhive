// Package providers contains telemetry provider implementations and factory logic
package providers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/telemetry/providers/otlp"
	"github.com/stacklok/toolhive/pkg/telemetry/providers/prometheus"
)

// Config holds the telemetry configuration
type Config struct {
	// Service information
	ServiceName    string
	ServiceVersion string

	// OTLP configuration
	OTLPEndpoint   string
	Headers        map[string]string
	Insecure       bool
	TracingEnabled bool    // TracingEnabled controls whether tracing is enabled for OTLP
	MetricsEnabled bool    // MetricsEnabled controls whether metrics are enabled for OTLP
	SamplingRate   float64 // SamplingRate controls trace sampling (0.0 to 1.0)

	// Prometheus configuration
	EnablePrometheusMetricsPath bool
}

// Builder builds telemetry providers based on configuration
type Builder struct {
	config   Config
	resource *resource.Resource
}

// NewBuilder creates a new provider builder
func NewBuilder(config Config) *Builder {
	return &Builder{
		config: config,
	}
}

// Build creates the appropriate providers based on configuration
func (b *Builder) Build(ctx context.Context) (*CompositeProvider, error) {
	// Create resource once for reuse
	res, err := b.createResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}
	b.resource = res

	// Determine what providers to create
	hasOTLP := b.config.OTLPEndpoint != ""
	hasPrometheus := b.config.EnablePrometheusMetricsPath

	// If neither is configured, return no-op provider
	if !hasOTLP && !hasPrometheus {
		logger.Infof("No telemetry endpoints configured, using no-op providers")
		return b.createNoOpProvider(), nil
	}

	// Create composite provider
	composite := &CompositeProvider{
		shutdownFuncs: []func(context.Context) error{},
	}

	// Build a single meter provider with multiple readers if needed
	meterProvider, prometheusHandler, err := b.createUnifiedMeterProvider(ctx, hasOTLP, hasPrometheus)
	if err != nil {
		return nil, fmt.Errorf("failed to create meter provider: %w", err)
	}
	composite.meterProvider = meterProvider
	composite.prometheusHandler = prometheusHandler

	// Add shutdown for meter provider if it's an SDK provider
	if sdkProvider, ok := meterProvider.(*sdkmetric.MeterProvider); ok {
		composite.shutdownFuncs = append(composite.shutdownFuncs, sdkProvider.Shutdown)
	}

	// Create tracer provider (only OTLP supports tracing)
	if hasOTLP {
		logger.Infof("Creating OTLP tracer provider for endpoint: %s", b.config.OTLPEndpoint)
		tracerProvider, err := b.createOTLPTracerProvider(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create tracer provider: %w", err)
		}
		composite.tracerProvider = tracerProvider

		// Add shutdown for tracer provider
		if sdkProvider, ok := tracerProvider.(*sdktrace.TracerProvider); ok {
			composite.shutdownFuncs = append(composite.shutdownFuncs, sdkProvider.Shutdown)
		}
	} else {
		composite.tracerProvider = tracenoop.NewTracerProvider()
	}

	logger.Infof("Telemetry providers created successfully")
	return composite, nil
}

func (b *Builder) createResource(ctx context.Context) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(b.config.ServiceName),
			semconv.ServiceVersion(b.config.ServiceVersion),
		),
	)
}

func (b *Builder) createUnifiedMeterProvider(
	ctx context.Context,
	hasOTLP, hasPrometheus bool,
) (metric.MeterProvider, http.Handler, error) {
	var readers []sdkmetric.Reader
	var prometheusHandler http.Handler

	// Add OTLP reader if configured
	if hasOTLP {
		logger.Infof("Adding OTLP metrics reader for endpoint: %s", b.config.OTLPEndpoint)
		otlpConfig := otlp.Config{
			Endpoint:     b.config.OTLPEndpoint,
			Headers:      b.config.Headers,
			Insecure:     b.config.Insecure,
			SamplingRate: b.config.SamplingRate,
		}
		reader, err := otlp.NewMetricReader(ctx, otlpConfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create OTLP metric reader: %w", err)
		}
		readers = append(readers, reader)
	}

	// Add Prometheus reader if configured
	if hasPrometheus {
		logger.Infof("Adding Prometheus metrics reader")
		promConfig := prometheus.Config{
			EnableMetricsPath:     true,
			IncludeRuntimeMetrics: true,
		}
		reader, handler, err := prometheus.NewReader(promConfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create Prometheus reader: %w", err)
		}
		readers = append(readers, reader)
		prometheusHandler = handler
	}

	// Create meter provider with all readers
	if len(readers) > 0 {
		opts := []sdkmetric.Option{sdkmetric.WithResource(b.resource)}
		for _, reader := range readers {
			opts = append(opts, sdkmetric.WithReader(reader))
		}
		return sdkmetric.NewMeterProvider(opts...), prometheusHandler, nil
	}

	return noop.NewMeterProvider(), nil, nil
}

func (b *Builder) createOTLPTracerProvider(ctx context.Context) (trace.TracerProvider, error) {
	otlpConfig := otlp.Config{
		Endpoint:     b.config.OTLPEndpoint,
		Headers:      b.config.Headers,
		Insecure:     b.config.Insecure,
		SamplingRate: b.config.SamplingRate,
	}
	return otlp.NewTracerProvider(ctx, otlpConfig, b.resource)
}

func (*Builder) createNoOpProvider() *CompositeProvider {
	return &CompositeProvider{
		tracerProvider:    tracenoop.NewTracerProvider(),
		meterProvider:     noop.NewMeterProvider(),
		prometheusHandler: nil,
		shutdownFuncs:     []func(context.Context) error{},
	}
}

// CompositeProvider combines telemetry providers
type CompositeProvider struct {
	tracerProvider    trace.TracerProvider
	meterProvider     metric.MeterProvider
	prometheusHandler http.Handler
	shutdownFuncs     []func(context.Context) error
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
	for _, shutdown := range p.shutdownFuncs {
		if err := shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

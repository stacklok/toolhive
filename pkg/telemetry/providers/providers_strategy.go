package providers

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/telemetry/providers/otlp"
	"github.com/stacklok/toolhive/pkg/telemetry/providers/prometheus"
)

// TracerStrategy defines the interface for creating tracer providers.
// Implementations create trace providers based on configuration and resource information.
type TracerStrategy interface {
	// CreateTracerProvider creates a tracer provider with optional shutdown function
	CreateTracerProvider(ctx context.Context, config Config, res *resource.Resource) (
		trace.TracerProvider, func(context.Context) error, error)
}

// NoOpTracerStrategy creates a no-op tracer provider that discards all trace data.
// It's used when tracing is disabled or no OTLP endpoint is configured.
type NoOpTracerStrategy struct{}

// CreateTracerProvider creates a no-op tracer provider
func (*NoOpTracerStrategy) CreateTracerProvider(
	_ context.Context,
	_ Config,
	_ *resource.Resource,
) (trace.TracerProvider, func(context.Context) error, error) {
	logger.Debugf("Creating no-op tracer provider")
	return tracenoop.NewTracerProvider(), nil, nil
}

// OTLPTracerStrategy creates an OTLP tracer provider that sends traces to an OTLP collector.
// It supports sampling configuration, custom headers, and secure/insecure transport.
type OTLPTracerStrategy struct{}

// CreateTracerProvider creates an OTLP tracer provider with the configured endpoint and sampling rate
func (*OTLPTracerStrategy) CreateTracerProvider(
	ctx context.Context,
	config Config,
	res *resource.Resource,
) (trace.TracerProvider, func(context.Context) error, error) {
	logger.Infof("Creating OTLP tracer provider for endpoint: %s with sampling rate: %.2f",
		config.OTLPEndpoint, config.SamplingRate)

	otlpConfig := otlp.Config{
		Endpoint:     config.OTLPEndpoint,
		Headers:      config.Headers,
		Insecure:     config.Insecure,
		SamplingRate: config.SamplingRate,
	}

	provider, shutdown, err := otlp.NewTracerProviderWithShutdown(ctx, otlpConfig, res)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OTLP tracer provider for endpoint %s: %w", config.OTLPEndpoint, err)
	}
	return provider, shutdown, nil
}

// MeterResult contains the result of creating a meter provider
type MeterResult struct {
	MeterProvider     metric.MeterProvider
	PrometheusHandler http.Handler
	ShutdownFunc      func(context.Context) error
}

// MeterStrategy defines the interface for creating meter providers
type MeterStrategy interface {
	CreateMeterProvider(ctx context.Context, config Config, res *resource.Resource) (*MeterResult, error)
}

// NoOpMeterStrategy creates a no-op meter provider that discards all metric data.
// It's used when both OTLP and Prometheus metrics are disabled.
type NoOpMeterStrategy struct{}

// CreateMeterProvider creates a no-op meter provider
func (*NoOpMeterStrategy) CreateMeterProvider(
	_ context.Context,
	_ Config,
	_ *resource.Resource,
) (*MeterResult, error) {
	logger.Debugf("Creating no-op meter provider")
	return &MeterResult{
		MeterProvider:     noop.NewMeterProvider(),
		PrometheusHandler: nil,
		ShutdownFunc:      nil,
	}, nil
}

// UnifiedMeterStrategy creates a meter provider with multiple readers (OTLP and/or Prometheus).
// It can combine OTLP metrics export and Prometheus scraping in a single provider.
type UnifiedMeterStrategy struct {
	EnableOTLP       bool // EnableOTLP controls whether to add an OTLP metrics reader
	EnablePrometheus bool // EnablePrometheus controls whether to add a Prometheus reader
}

// CreateMeterProvider creates a unified meter provider with OTLP and/or Prometheus readers
func (s *UnifiedMeterStrategy) CreateMeterProvider(
	ctx context.Context,
	config Config,
	res *resource.Resource,
) (*MeterResult, error) {
	var readers []sdkmetric.Reader
	var prometheusHandler http.Handler

	// Add OTLP reader if enabled
	if s.EnableOTLP {
		logger.Infof("Adding OTLP metrics reader for endpoint: %s", config.OTLPEndpoint)

		otlpConfig := otlp.Config{
			Endpoint:     config.OTLPEndpoint,
			Headers:      config.Headers,
			Insecure:     config.Insecure,
			SamplingRate: config.SamplingRate,
		}

		reader, err := otlp.NewMetricReader(ctx, otlpConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP metric reader for endpoint %s: %w", config.OTLPEndpoint, err)
		}
		readers = append(readers, reader)
	}

	// Add Prometheus reader if enabled
	if s.EnablePrometheus {
		logger.Infof("Adding Prometheus metrics reader")
		promConfig := prometheus.Config{
			EnableMetricsPath:     true,
			IncludeRuntimeMetrics: true,
		}
		reader, handler, err := prometheus.NewReader(promConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create Prometheus metric reader: %w", err)
		}
		readers = append(readers, reader)
		prometheusHandler = handler
	}

	// Create meter provider with all readers
	if len(readers) == 0 {
		return &MeterResult{
			MeterProvider:     noop.NewMeterProvider(),
			PrometheusHandler: nil,
			ShutdownFunc:      nil,
		}, nil
	}

	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	for _, reader := range readers {
		opts = append(opts, sdkmetric.WithReader(reader))
	}

	provider := sdkmetric.NewMeterProvider(opts...)
	return &MeterResult{
		MeterProvider:     provider,
		PrometheusHandler: prometheusHandler,
		ShutdownFunc:      provider.Shutdown,
	}, nil
}

// StrategySelector determines which strategies to use based on configuration.
// It analyzes the configuration to select appropriate tracer and meter strategies.
type StrategySelector struct {
	config Config // config holds the telemetry configuration to analyze
}

// NewStrategySelector creates a new strategy selector with the given configuration.
// The selector will analyze the config to determine appropriate strategies.
func NewStrategySelector(config Config) *StrategySelector {
	return &StrategySelector{config: config}
}

// SelectTracerStrategy determines the appropriate tracer strategy based on configuration.
func (s *StrategySelector) SelectTracerStrategy() TracerStrategy {
	hasEndpoint := s.config.OTLPEndpoint != ""
	tracingEnabled := s.config.TracingEnabled

	if hasEndpoint && tracingEnabled {
		return &OTLPTracerStrategy{}
	}

	// Log informational message when endpoint is configured but tracing is disabled
	if hasEndpoint && !tracingEnabled {
		logger.Infof("OTLP endpoint configured but tracing is disabled")
	}

	return &NoOpTracerStrategy{}
}

// SelectMeterStrategy determines the appropriate meter strategy based on configuration.
func (s *StrategySelector) SelectMeterStrategy() MeterStrategy {
	wantsOTLPMetrics := s.hasOTLPMetrics()
	wantsPrometheus := s.config.EnablePrometheusMetricsPath

	// Return no-op if no metrics are enabled
	if !wantsOTLPMetrics && !wantsPrometheus {
		return &NoOpMeterStrategy{}
	}

	// Return unified strategy with appropriate readers enabled
	return &UnifiedMeterStrategy{
		EnableOTLP:       wantsOTLPMetrics,
		EnablePrometheus: wantsPrometheus,
	}
}

// IsFullyNoOp returns true if both tracer and meter would be no-op.
func (s *StrategySelector) IsFullyNoOp() bool {
	return !s.hasOTLPMetrics() && !s.hasOTLPTracing() && !s.hasPrometheus()
}

// hasOTLPMetrics returns true if OTLP metrics are wanted.
func (s *StrategySelector) hasOTLPMetrics() bool {
	return s.config.OTLPEndpoint != "" && s.config.MetricsEnabled
}

// hasOTLPTracing returns true if OTLP tracing is wanted.
func (s *StrategySelector) hasOTLPTracing() bool {
	return s.config.OTLPEndpoint != "" && s.config.TracingEnabled
}

// hasPrometheus returns true if Prometheus metrics are wanted.
func (s *StrategySelector) hasPrometheus() bool {
	return s.config.EnablePrometheusMetricsPath
}

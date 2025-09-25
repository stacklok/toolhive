// Package analytics provides usage analytics telemetry functionality for ToolHive.
// This package implements privacy-first usage analytics by collecting anonymous
// tool call metrics and sending them to Stacklok's analytics collector.
package analytics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	metricsdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Provider creates a metrics provider specifically for anonymous usage analytics.
// It sends only minimal, anonymous metrics to Stacklok's analytics collector.
type Provider struct {
	meterProvider metric.MeterProvider
	shutdownFunc  func(context.Context) error
}

// New creates a new analytics provider with the specified endpoint.
// Returns a no-op provider if endpoint is empty or creation fails.
func New(ctx context.Context, endpoint string, res *resource.Resource) (*Provider, error) {
	if endpoint == "" {
		logger.Infof("Analytics endpoint not configured, using no-op analytics provider")
		return &Provider{
			meterProvider: noop.NewMeterProvider(),
			shutdownFunc:  func(context.Context) error { return nil },
		}, nil
	}

	// Create OTLP HTTP exporter for analytics
	exporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		// Analytics endpoint should always use HTTPS - no WithInsecure() call needed
		// No authentication headers needed - anonymous analytics
	)
	if err != nil {
		logger.Warnf("Failed to create analytics exporter, falling back to no-op: %v", err)
		return &Provider{
			meterProvider: noop.NewMeterProvider(),
			shutdownFunc:  func(context.Context) error { return nil },
		}, nil
	}

	// Create meter provider with periodic reader
	meterProvider := metricsdk.NewMeterProvider(
		metricsdk.WithResource(res),
		metricsdk.WithReader(
			metricsdk.NewPeriodicReader(
				exporter,
				metricsdk.WithInterval(30*time.Second), // Send analytics every 30 seconds
			),
		),
	)

	return &Provider{
		meterProvider: meterProvider,
		shutdownFunc:  meterProvider.Shutdown,
	}, nil
}

// MeterProvider returns the analytics meter provider
func (p *Provider) MeterProvider() metric.MeterProvider {
	return p.meterProvider
}

// Shutdown gracefully shuts down the analytics provider
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.shutdownFunc != nil {
		return p.shutdownFunc(ctx)
	}
	return nil
}

// CreateAnalyticsMeterProvider creates a meter provider specifically for usage analytics
func CreateAnalyticsMeterProvider(
	ctx context.Context, endpoint string, res *resource.Resource,
) (metric.MeterProvider, func(context.Context) error, error) {
	provider, err := New(ctx, endpoint, res)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create analytics provider: %w", err)
	}

	return provider.MeterProvider(), provider.Shutdown, nil
}

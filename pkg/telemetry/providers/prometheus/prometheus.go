// Package prometheus provides Prometheus metric exporter implementation
package prometheus

import (
	"fmt"
	"net/http"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Config holds Prometheus-specific configuration
type Config struct {
	// EnableMetricsPath controls whether to expose Prometheus-style /metrics endpoint
	EnableMetricsPath bool
	// IncludeRuntimeMetrics adds Go runtime metrics to the registry
	IncludeRuntimeMetrics bool
}

// NewReader creates a Prometheus metric reader and HTTP handler for use in a unified meter provider
func NewReader(config Config) (sdkmetric.Reader, http.Handler, error) {
	if !config.EnableMetricsPath {
		return nil, nil, fmt.Errorf("prometheus provider requires EnableMetricsPath to be true")
	}

	// Create a dedicated registry
	registry := promclient.NewRegistry()

	// Add runtime metrics if requested
	if config.IncludeRuntimeMetrics {
		registry.MustRegister(collectors.NewGoCollector())
		registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	}

	// Create the Prometheus exporter (which is also a Reader)
	exporter, err := prometheus.New(prometheus.WithRegisterer(registry))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create prometheus exporter: %w", err)
	}

	// Create HTTP handler
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      nil,
	})

	return exporter, handler, nil
}

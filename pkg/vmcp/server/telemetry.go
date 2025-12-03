package server

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

const (
	instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"
)

// MonitorBackends decorate the backend client so it records telemetry on each method call.
// It also emits a gauge for the number of backends discovered once, since the number of backends is static.
func MonitorBackends(ctx context.Context, meterProvider metric.MeterProvider, backends []vmcp.Backend, backendClient vmcp.BackendClient) (vmcp.BackendClient, error) {
	meter := meterProvider.Meter(instrumentationName)

	backendCount, err := meter.Int64Gauge(
		"toolhive_vmcp_backends_discovered",
		metric.WithDescription("Number of backends discovered"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend count gauge: %w", err)
	}
	backendCount.Record(ctx, int64(len(backends)))

	requestsTotal, err := meter.Int64Counter("toolhive_vmcp_requests_total", metric.WithDescription("Total number of requests per backend"))
	if err != nil {
		return nil, fmt.Errorf("failed to create requests total counter: %w", err)
	}
	errorsTotal, err := meter.Int64Counter("toolhive_vmcp_errors_total", metric.WithDescription("Total number of errors per backend"))
	if err != nil {
		return nil, fmt.Errorf("failed to create errors total counter: %w", err)
	}
	requestsDuration, err := meter.Float64Histogram("toolhive_vmcp_requests_duration", metric.WithDescription("Duration of requests in seconds per backend"))
	if err != nil {
		return nil, fmt.Errorf("failed to create requests duration histogram: %w", err)
	}

	return telemetryBackendClient{
		backendClient:    backendClient,
		requestsTotal:    requestsTotal,
		errorsTotal:      errorsTotal,
		requestsDuration: requestsDuration,
	}, nil

}

type telemetryBackendClient struct {
	backendClient vmcp.BackendClient

	requestsTotal    metric.Int64Counter
	errorsTotal      metric.Int64Counter
	requestsDuration metric.Float64Histogram
}

var _ vmcp.BackendClient = telemetryBackendClient{}

// record updates the telemetry metrics for each method on the BackendClient interface.
// It returns a function that should be deferred to record the duration and error.
func (t telemetryBackendClient) record(ctx context.Context, target *vmcp.BackendTarget, action string, err *error) func() {
	attrs := metric.WithAttributes(
		attribute.String("target.workload_id", target.WorkloadID),
		attribute.String("target.workload_name", target.WorkloadName),
		attribute.String("target.base_url", target.BaseURL),
		attribute.String("target.transport_type", target.TransportType),
		attribute.String("action", action),
	)
	start := time.Now()
	t.requestsTotal.Add(ctx, 1, attrs)

	return func() {
		duration := time.Since(start)
		t.requestsDuration.Record(ctx, duration.Seconds(), attrs)
		if err != nil {
			t.errorsTotal.Add(ctx, 1, attrs)
		}
	}
}

func (t telemetryBackendClient) CallTool(ctx context.Context, target *vmcp.BackendTarget, toolName string, arguments map[string]any) (_ map[string]any, retErr error) {
	defer t.record(ctx, target, "call_tool", &retErr)()
	return t.backendClient.CallTool(ctx, target, toolName, arguments)
}

func (t telemetryBackendClient) ReadResource(ctx context.Context, target *vmcp.BackendTarget, uri string) (_ []byte, retErr error) {
	defer t.record(ctx, target, "read_resource", &retErr)()
	return t.backendClient.ReadResource(ctx, target, uri)
}

func (t telemetryBackendClient) GetPrompt(ctx context.Context, target *vmcp.BackendTarget, name string, arguments map[string]any) (_ string, retErr error) {
	defer t.record(ctx, target, "get_prompt", &retErr)()
	return t.backendClient.GetPrompt(ctx, target, name, arguments)
}

func (t telemetryBackendClient) ListCapabilities(ctx context.Context, target *vmcp.BackendTarget) (_ *vmcp.CapabilityList, retErr error) {
	defer t.record(ctx, target, "list_capabilities", &retErr)()
	return t.backendClient.ListCapabilities(ctx, target)
}

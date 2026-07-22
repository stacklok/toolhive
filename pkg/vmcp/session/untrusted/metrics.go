// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instrumentationName matches the vMCP-wide OTel instrumentation scope used by
// the other vMCP meters (pkg/vmcp/internal/backendtelemetry,
// pkg/vmcp/server/sessionmanager).
const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// untrustedMetrics holds the untrusted-mode instruments. Labels are kept
// low-cardinality per ADR D11: the MCPServer name and the admission result —
// never a user identifier.
type untrustedMetrics struct {
	// backendPods gauges live untrusted backend pods per MCPServer, refreshed
	// by the reaper from the authoritative pod LIST each tick.
	backendPods metric.Int64Gauge
	// admissions counts pod provisioning admission decisions by result.
	admissions metric.Int64Counter
}

// newUntrustedMetrics registers the untrusted-mode instruments on the vMCP
// meter. Registration failures are startup-fatal (fail loudly): a silently
// uninstrumented untrusted mode hides the DoS controls' effectiveness.
func newUntrustedMetrics(meterProvider metric.MeterProvider) (*untrustedMetrics, error) {
	meter := meterProvider.Meter(instrumentationName)

	backendPods, err := meter.Int64Gauge(
		"untrusted_backend_pods",
		metric.WithDescription("Number of live untrusted per-session backend pods"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create untrusted_backend_pods gauge: %w", err)
	}
	admissions, err := meter.Int64Counter(
		"untrusted_pod_admissions_total",
		metric.WithDescription("Total untrusted pod provisioning admission decisions"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create untrusted_pod_admissions_total counter: %w", err)
	}
	return &untrustedMetrics{backendPods: backendPods, admissions: admissions}, nil
}

// recordAdmission records one admission decision. result is a small fixed
// vocabulary ("admitted", "quota_exceeded", "error") — never a user or pod.
func (m *untrustedMetrics) recordAdmission(ctx context.Context, result string) {
	if m == nil {
		return
	}
	m.admissions.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// recordPodCounts records the per-MCPServer live pod gauge from the reaper's
// authoritative pod list. mcpserverName is low-cardinality (one per untrusted
// MCPServer CR).
func (m *untrustedMetrics) recordPodCounts(ctx context.Context, mcpserverName string, count int) {
	if m == nil {
		return
	}
	m.backendPods.Record(ctx, int64(count), metric.WithAttributes(attribute.String("mcpserver", mcpserverName)))
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instrumentationName matches the vMCP-wide OTel instrumentation scope used by
// the other broker-adjacent meters (pkg/vmcp/session/untrusted).
const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// Metric label vocabularies (ADR-0001 D11 — low cardinality: workload,
// provider, and a small fixed result/location/where vocabulary only; NEVER a
// user identifier, pod name, request-id, or token material).
const (
	metricAttrMCPServer = "mcpserver"
	metricAttrProvider  = "provider"
	metricAttrResult    = "result"
	metricAttrWhere     = "where"

	// metricResultOK: response passed the scanner untouched.
	metricResultOK = "ok"
	// metricResultLeak: response suppressed — injected credential echoed back.
	metricResultLeak = "leak"
	// metricResultUnknown: response carried no known request-id (direct hit,
	// scanner restart, TTL eviction) and passed under the fail-open default.
	metricResultUnknown = "unknown_request"
)

// BrokerMetrics holds the egress-broker instruments. All instruments are
// optional: a nil *brokerMetrics (or nil individual instruments) is a silent
// no-op so the broker runs uninstrumented when no MeterProvider is wired.
type BrokerMetrics struct {
	scans  metric.Int64Counter
	skips  metric.Int64Counter
	inject metric.Int64Counter
	denies metric.Int64Counter
}

// NewBrokerMetrics registers the broker instruments. Registration failures
// are startup-fatal (fail loudly): a silently uninstrumented credential broker
// hides the leak detector — the one signal paged on (ADR D11).
func NewBrokerMetrics(meterProvider metric.MeterProvider) (*BrokerMetrics, error) {
	if meterProvider == nil {
		return nil, fmt.Errorf("egressbroker: meter provider must not be nil")
	}
	meter := meterProvider.Meter(instrumentationName)

	scans, err := meter.Int64Counter(
		"egress_broker_response_scan_total",
		metric.WithDescription("Response-side credential scans by outcome (ADR D6c)"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create egress_broker_response_scan_total counter: %w", err)
	}
	skips, err := meter.Int64Counter(
		"egress_broker_scan_skipped_total",
		metric.WithDescription("Response scans skipped (body over cap; headers still scanned)"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create egress_broker_scan_skipped_total counter: %w", err)
	}
	inject, err := meter.Int64Counter(
		"egress_broker_injections_total",
		metric.WithDescription("Successful credential injections"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create egress_broker_injections_total counter: %w", err)
	}
	denies, err := meter.Int64Counter(
		"egress_broker_denials_total",
		metric.WithDescription("Injection denials by reason"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create egress_broker_denials_total counter: %w", err)
	}
	return &BrokerMetrics{scans: scans, skips: skips, inject: inject, denies: denies}, nil
}

// RecordScan records one scan outcome. result is one of the metricResult*
// constants; where is only meaningful for leaks (empty otherwise).
func (m *BrokerMetrics) RecordScan(ctx context.Context, mcpserver, provider, result, where string) {
	if m == nil || m.scans == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(metricAttrMCPServer, mcpserver),
		attribute.String(metricAttrProvider, provider),
		attribute.String(metricAttrResult, result),
	}
	if where != "" {
		attrs = append(attrs, attribute.String(metricAttrWhere, where))
	}
	m.scans.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordScanSkipped records a body-over-cap skip (headers were still scanned).
func (m *BrokerMetrics) RecordScanSkipped(ctx context.Context, mcpserver, provider string) {
	if m == nil || m.skips == nil {
		return
	}
	m.skips.Add(ctx, 1, metric.WithAttributes(
		attribute.String(metricAttrMCPServer, mcpserver),
		attribute.String(metricAttrProvider, provider),
	))
}

// RecordInjection records one successful injection.
func (m *BrokerMetrics) RecordInjection(ctx context.Context, mcpserver, provider string) {
	if m == nil || m.inject == nil {
		return
	}
	m.inject.Add(ctx, 1, metric.WithAttributes(
		attribute.String(metricAttrMCPServer, mcpserver),
		attribute.String(metricAttrProvider, provider),
	))
}

// RecordDenial records one injection denial. reason must be a DenyReason
// vocabulary value (validated at the call site).
func (m *BrokerMetrics) RecordDenial(ctx context.Context, mcpserver, provider string, reason DenyReason) {
	if m == nil || m.denies == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String(metricAttrResult, string(reason))}
	if mcpserver != "" {
		attrs = append(attrs, attribute.String(metricAttrMCPServer, mcpserver))
	}
	if provider != "" {
		attrs = append(attrs, attribute.String(metricAttrProvider, provider))
	}
	m.denies.Add(ctx, 1, metric.WithAttributes(attrs...))
}

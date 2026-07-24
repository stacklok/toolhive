// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"maps"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Compile-time assertion that MetaCarrier implements propagation.TextMapCarrier
var _ propagation.TextMapCarrier = (*MetaCarrier)(nil)

// MetaCarrier implements propagation.TextMapCarrier for MCP _meta fields.
// This enables W3C Trace Context propagation through MCP request params._meta,
// as recommended by the MCP OpenTelemetry specification.
//
// The carrier wraps a map[string]interface{} (the _meta field from MCP params)
// and allows the OpenTelemetry propagator to inject/extract traceparent and
// tracestate headers into/from the map.
type MetaCarrier struct {
	meta map[string]interface{}
}

// NewMetaCarrier creates a new MetaCarrier wrapping the given meta map.
// If meta is nil, a new empty map is created.
func NewMetaCarrier(meta map[string]interface{}) *MetaCarrier {
	if meta == nil {
		meta = make(map[string]interface{})
	}
	return &MetaCarrier{meta: meta}
}

// Get returns the value associated with the passed key.
func (c *MetaCarrier) Get(key string) string {
	if v, ok := c.meta[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Set stores the key-value pair.
func (c *MetaCarrier) Set(key string, value string) {
	c.meta[key] = value
}

// Keys lists the keys stored in this carrier.
func (c *MetaCarrier) Keys() []string {
	keys := make([]string, 0, len(c.meta))
	for k := range c.meta {
		keys = append(keys, k)
	}
	return keys
}

// Meta returns the underlying meta map. Use this after injection to retrieve
// the enriched map containing trace context fields.
func (c *MetaCarrier) Meta() map[string]interface{} {
	return c.meta
}

// InjectMetaTraceContext injects the current trace context from ctx directly into
// the given meta map using W3C Trace Context format (traceparent, tracestate)
// plus W3C Baggage, subject to the globally configured OTEL propagator.
//
// This function operates directly on the meta map contents. Use this when you
// already have the _meta map and want to inject trace context fields into it.
func InjectMetaTraceContext(ctx context.Context, meta map[string]interface{}) {
	if meta == nil {
		return
	}
	carrier := NewMetaCarrier(meta)
	otel.GetTextMapPropagator().Inject(ctx, carrier)
}

// MetaWithTraceContext returns a COPY of meta enriched with the current W3C
// trace context (traceparent/tracestate/baggage) from ctx, per SEP-414.
// The caller's map is never mutated (copy-before-mutate). Returns nil when the
// result is empty (no caller meta and no active trace context) so callers can
// omit _meta entirely for 2025-11-25 peers that don't expect it.
//
// Overwrite semantics: injecting the CURRENT span's context overwrites any
// inbound traceparent already present in the cloned caller map. This is the
// correct behavior for an intermediary — the outbound request must be a child
// of the span active here, not of whatever the caller inherited upstream.
//
// SECURITY: the injected trace context AND baggage cross the trust boundary into
// backend requests, and backends may be untrusted. Callers must never place
// secrets or PII in baggage, since it is serialized verbatim into the outbound
// _meta. Baggage is intentionally NOT stripped: SEP-414 explicitly reserves it.
func MetaWithTraceContext(ctx context.Context, meta map[string]interface{}) map[string]interface{} {
	enriched := maps.Clone(meta) // nil-safe: returns nil for nil input
	if enriched == nil {
		enriched = make(map[string]interface{})
	}
	InjectMetaTraceContext(ctx, enriched)
	if len(enriched) == 0 {
		return nil
	}
	return enriched
}

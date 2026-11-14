// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"maps"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// DefaultRegisteredSamplingRate is the rate used when a registered integration
// did not specify one, preserving the "sample everything" behaviour that
// processor-only mode had before rates were configurable.
const DefaultRegisteredSamplingRate = 1.0

var (
	globalProcessors    []sdktrace.SpanProcessor
	globalResourceAttrs map[string]string
	globalSamplingRate  *float64
	globalProcessorsMu  sync.Mutex
)

// RegisterSpanProcessor registers an extra OTEL span processor to be included
// in any provider created via NewProvider. This allows optional integrations
// (e.g. a Sentry bridge, Datadog exporter) to self-register during their own
// Init without coupling to the caller that creates the OTEL provider.
//
// Registration must happen before NewProvider is called; processors registered
// after provider creation will not be included in the already-created provider.
//
// Duplicate registrations of the same processor pointer are silently ignored
// to prevent OnStart/OnEnd from firing twice on a single span when Init is
// called more than once (e.g. during tests or config reload).
func RegisterSpanProcessor(p sdktrace.SpanProcessor) {
	if p == nil {
		return
	}
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	for _, existing := range globalProcessors {
		if existing == p {
			return
		}
	}
	globalProcessors = append(globalProcessors, p)
}

// RegisterResourceAttributes merges attributes into the OTEL resource of any
// provider created via NewProvider. Integrations whose exporter bypasses their
// own SDK — such as the Sentry OTLP exporter, which never passes spans through
// the Sentry client — use this to attach the grouping keys their backend needs.
//
// As with RegisterSpanProcessor, registration must happen before NewProvider is
// called. Repeated registrations of the same key overwrite earlier values.
//
// Note that resource attributes apply to the whole provider, so they are also
// exported to any configured OTLP collector, not just to the integration that
// registered them.
func RegisterResourceAttributes(attrs map[string]string) {
	if len(attrs) == 0 {
		return
	}
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	if globalResourceAttrs == nil {
		globalResourceAttrs = make(map[string]string, len(attrs))
	}
	maps.Copy(globalResourceAttrs, attrs)
}

// RegisterSamplingRate records the trace sampling rate an integration wants
// applied to the spans it receives. In processor-only mode (no OTLP endpoint)
// NewServeProvider hands this to the SDK sampler, so unsampled spans are never
// constructed at all.
//
// The SDK sampler is shared by the whole provider, so this rate is not
// per-processor: when an OTLP endpoint is also configured its own sampling rate
// wins and this value is ignored. Repeated registrations overwrite the previous
// value; only one integration is expected to register a rate.
func RegisterSamplingRate(rate float64) {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	globalSamplingRate = &rate
}

// RegisteredSamplingRate returns the rate registered via RegisterSamplingRate,
// or DefaultRegisteredSamplingRate when no integration registered one.
func RegisteredSamplingRate() float64 {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	if globalSamplingRate == nil {
		return DefaultRegisteredSamplingRate
	}
	return *globalSamplingRate
}

// HasRegisteredSpanProcessors returns true if any extra span processors have
// been registered. Callers can use this to decide whether to initialise an
// OTEL provider even when no OTLP endpoint is configured.
func HasRegisteredSpanProcessors() bool {
	return RegisteredSpanProcessorCount() > 0
}

// RegisteredSpanProcessorCount returns how many extra span processors are
// currently registered.
func RegisteredSpanProcessorCount() int {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	return len(globalProcessors)
}

// RegisteredResourceAttributes returns a copy of every resource attribute
// registered via RegisterResourceAttributes, or nil when there are none.
func RegisteredResourceAttributes() map[string]string {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	if len(globalResourceAttrs) == 0 {
		return nil
	}
	return maps.Clone(globalResourceAttrs)
}

// ResetSpanProcessorsForTesting clears all registered span processors, resource
// attributes and the sampling rate. For use in tests only.
func ResetSpanProcessorsForTesting() {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	globalProcessors = nil
	globalResourceAttrs = nil
	globalSamplingRate = nil
}

// registeredSpanProcessors returns a snapshot of all registered processors.
func registeredSpanProcessors() []sdktrace.SpanProcessor {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	if len(globalProcessors) == 0 {
		return nil
	}
	result := make([]sdktrace.SpanProcessor, len(globalProcessors))
	copy(result, globalProcessors)
	return result
}

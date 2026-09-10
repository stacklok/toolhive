// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sentry provides Sentry error tracking and distributed tracing for the ToolHive API server.
package sentry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
	sentryotel "github.com/getsentry/sentry-go/otel"
	sentryotlp "github.com/getsentry/sentry-go/otel/otlp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/versions"
)

const flushTimeout = 2 * time.Second

const (
	// environmentKey and releaseKey are the attribute names Sentry itself uses
	// to carry Environment and Release on OTLP payloads (see sentry-go's
	// log.go/metrics.go), so exported spans must use the same names to be
	// grouped alongside Issues.
	environmentKey = "sentry.environment"
	releaseKey     = "sentry.release"
	// instanceIDKey carries the anonymous instance ID on both Sentry events
	// (as a scope tag) and exported spans (as a resource attribute), so Issues
	// and Traces can be correlated with toolhive-studio by the same value.
	instanceIDKey = "custom.instance_id"
)

// initialized tracks whether Sentry was successfully initialized.
var initialized atomic.Bool

// spanProcessor is the single Sentry OTLP span processor for this process,
// created on the first Init and reused by every subsequent one. Guarded by
// spanProcessorMu.
var (
	spanProcessorMu sync.Mutex
	spanProcessor   sdktrace.SpanProcessor
)

// Config holds the configuration for Sentry integration.
type Config struct {
	// DSN is the Sentry Data Source Name. When empty, Sentry is disabled.
	DSN string
	// Environment identifies the deployment environment (e.g. "production", "development").
	Environment string
	// TracesSampleRate controls the percentage of transactions captured for
	// performance monitoring (0.0–1.0).
	TracesSampleRate float64
	// Debug enables Sentry SDK debug logging.
	Debug bool
}

// Init initializes the Sentry SDK with the given configuration.
// If the DSN is empty, initialization is skipped and all Sentry operations become no-ops.
func Init(cfg Config) error {
	if cfg.DSN == "" {
		slog.Debug("sentry disabled (no DSN configured)")
		return nil
	}

	vi := versions.GetVersionInfo()
	// Reused verbatim as a span resource attribute below so Issues and Traces
	// report the same release string.
	release := fmt.Sprintf("toolhive@%s", vi.Version)

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.DSN,
		Environment:      cfg.Environment,
		Release:          release,
		TracesSampleRate: cfg.TracesSampleRate,
		Debug:            cfg.Debug,
		EnableTracing:    true,
		AttachStacktrace: true,
		SendDefaultPII:   false,
		Integrations: func(integrations []sentry.Integration) []sentry.Integration {
			return append(integrations, sentryotel.NewOtelIntegration())
		},
	})
	if err != nil {
		return fmt.Errorf("sentry init: %w", err)
	}

	if err := registerTraceExporter(cfg); err != nil {
		return err
	}
	initialized.Store(true)
	slog.Debug("sentry initialized", "environment", cfg.Environment)

	// Tag every event and transaction with the anonymous instance ID so that
	// Sentry events from the API server can be correlated with those from
	// toolhive-studio. Note: toolhive-studio currently uses "custom.user_id"
	// for the same value; these should be aligned to "custom.instance_id" in
	// both repos in a follow-up to avoid misleading PII detection heuristics.
	instanceID := ""
	if id, err := updates.TryGetAnonymousID(); err == nil && id != "" {
		instanceID = id
		sentry.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetTag(instanceIDKey, id)
		})
		slog.Debug("sentry anonymous instance ID tagged", "id", id)
	}

	// Spans are exported straight to Sentry's OTLP endpoint and never pass
	// through the Sentry client, so neither ClientOptions nor the scope
	// configured above reach them. Environment, release and instance ID have to
	// travel as OTEL resource attributes instead, or Traces would lose the
	// grouping that Issues keep and the two would disagree.
	telemetry.RegisterResourceAttributes(resourceAttributes(cfg.Environment, release, instanceID))

	return nil
}

// registerTraceExporter registers the Sentry OTLP span processor with the global
// OTEL registry, creating it on first use.
//
// The processor is cached because the registry deduplicates by pointer identity:
// sdktrace.NewBatchSpanProcessor allocates a fresh processor on every call, so
// without this a second Init (config reload, or a test that does not reset the
// registry) would register a second processor and double-export every span while
// leaking the first exporter's goroutine.
//
// Caching means a second Init keeps the first call's DSN and sample rate. thv
// serve calls Init exactly once per process, and the registry already only
// feeds processors to providers created after registration, so re-initialising
// with different values is not supported either way.
func registerTraceExporter(cfg Config) error {
	spanProcessorMu.Lock()
	defer spanProcessorMu.Unlock()

	if spanProcessor == nil {
		exporter, err := sentryotlp.NewTraceExporter(context.Background(), cfg.DSN)
		if err != nil {
			return fmt.Errorf("create Sentry trace exporter: %w", err)
		}
		spanProcessor = newSamplingSpanProcessor(
			sdktrace.NewBatchSpanProcessor(exporter),
			cfg.TracesSampleRate,
		)
	}

	telemetry.RegisterSpanProcessor(spanProcessor)
	slog.Debug("sentry trace exporter registered with OTEL registry",
		"traces_sample_rate", cfg.TracesSampleRate)
	return nil
}

// resourceAttributes returns the OTEL resource attributes Sentry needs to group
// OTLP-ingested traces the same way it groups Issues. Empty values are omitted
// so they do not show up as blank attributes on other OTLP backends.
func resourceAttributes(environment, release, instanceID string) map[string]string {
	attrs := make(map[string]string, 3)
	for key, value := range map[string]string{
		environmentKey: environment,
		releaseKey:     release,
		instanceIDKey:  instanceID,
	} {
		if value != "" {
			attrs[key] = value
		}
	}
	return attrs
}

// Close flushes buffered Sentry events and shuts down the SDK.
// Safe to call even when Sentry was not initialized.
func Close() {
	if !initialized.Load() {
		return
	}
	sentry.Flush(flushTimeout)
	initialized.Store(false)
	slog.Debug("sentry flushed and closed")
}

// Enabled reports whether the Sentry SDK was successfully initialized.
func Enabled() bool {
	return initialized.Load()
}

// CaptureException reports an error to Sentry using the hub from the request context.
// Falls back to the current hub if no hub is attached to the context.
// No-op when Sentry is not initialized.
//
// The API server's error handler calls this alongside span.RecordError so that
// 5xx errors appear as both OTEL span errors (distributed tracing) and
// standalone Sentry Issues (error tracking). The Sentry OTEL integration links
// those issues to the active OTEL trace; explicit hub calls are required to
// create Issues.
func CaptureException(r *http.Request, err error) {
	if !initialized.Load() || err == nil {
		return
	}
	hub := sentry.GetHubFromContext(r.Context())
	if hub == nil {
		hub = sentry.CurrentHub().Clone()
	}
	client := hub.Client()
	if client == nil {
		return
	}
	event := client.EventFromException(err, sentry.LevelError)
	hub.CaptureEventWithHint(event, &sentry.EventHint{
		OriginalException: err,
		Context:           r.Context(),
	})
}

// RecoverPanic reports a recovered panic value to Sentry.
// No-op when Sentry is not initialized.
func RecoverPanic(r *http.Request, recovered interface{}) {
	if !initialized.Load() || recovered == nil {
		return
	}
	hub := sentry.GetHubFromContext(r.Context())
	if hub == nil {
		hub = sentry.CurrentHub().Clone()
	}
	hub.RecoverWithContext(r.Context(), recovered)
	hub.Flush(flushTimeout)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sentry provides Sentry error tracking and distributed tracing for the ToolHive API server.
package sentry

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
	sentryotel "github.com/getsentry/sentry-go/otel"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/versions"
)

const flushTimeout = 2 * time.Second

// initialized tracks whether Sentry was successfully initialized.
var initialized atomic.Bool

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

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.DSN,
		Environment:      cfg.Environment,
		Release:          fmt.Sprintf("toolhive@%s", vi.Version),
		TracesSampleRate: cfg.TracesSampleRate,
		Debug:            cfg.Debug,
		EnableTracing:    true,
		AttachStacktrace: true,
		SendDefaultPII:   false,
	})
	if err != nil {
		return fmt.Errorf("sentry init: %w", err)
	}

	initialized.Store(true)
	slog.Debug("sentry initialized", "environment", cfg.Environment)

	// Tag every event and transaction with the anonymous instance ID so that
	// Sentry events from the API server can be correlated with those from
	// toolhive-studio. Note: toolhive-studio currently uses "custom.user_id"
	// for the same value; these should be aligned to "custom.instance_id" in
	// both repos in a follow-up to avoid misleading PII detection heuristics.
	if id, err := updates.TryGetAnonymousID(); err == nil && id != "" {
		sentry.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetTag("custom.instance_id", id)
		})
		slog.Debug("sentry anonymous instance ID tagged", "id", id)
	}

	// Self-register the Sentry span processor with the global OTEL registry so
	// that any telemetry.NewProvider call automatically includes it. This decouples
	// the OTEL provider setup from Sentry-specific code.
	telemetry.RegisterSpanProcessor(sentryotel.NewSentrySpanProcessor())
	slog.Debug("sentry span processor registered with OTEL registry")

	return nil
}

// Close flushes buffered Sentry events and shuts down the SDK.
// Safe to call even when Sentry was not initialized.
func Close() {
	if !initialized.Load() {
		return
	}
	sentry.Flush(flushTimeout)
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
// standalone Sentry Issues (error tracking). The Sentry span processor only
// creates transactions; explicit hub calls are required for Issues.
func CaptureException(r *http.Request, err error) {
	if !initialized.Load() || err == nil {
		return
	}
	hub := sentry.GetHubFromContext(r.Context())
	if hub == nil {
		hub = sentry.CurrentHub().Clone()
	}
	hub.CaptureException(err)
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

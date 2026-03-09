// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

const (
	// DefaultKeepaliveInterval is the default interval between keepalive probes.
	DefaultKeepaliveInterval = 5 * time.Minute

	// DefaultKeepaliveMaxFailures is the number of consecutive probe failures
	// before keepalive is disabled for a backend (circuit breaker threshold).
	DefaultKeepaliveMaxFailures = 3

	// DefaultKeepaliveProbeAfter is how long to wait before re-probing a
	// backend after the circuit breaker disables its keepalive.
	DefaultKeepaliveProbeAfter = 30 * time.Minute

	// pingTimeout is the per-ping deadline. Short to avoid tying up goroutines.
	pingTimeout = 10 * time.Second

	// keepaliveInstrumentationScope is the OTel meter name for keepalive metrics.
	keepaliveInstrumentationScope = "github.com/stacklok/toolhive/pkg/vmcp/session/keepalive"
)

// KeepaliveConfig holds server-level keepalive settings applied to every
// backend in a session. Per-backend method overrides live in BackendTarget.
type KeepaliveConfig struct {
	// DefaultMethod is the keepalive method used when a backend's
	// BackendTarget.KeepaliveMethod is empty. Defaults to KeepaliveMethodNone,
	// so keepalive is disabled unless explicitly configured.
	DefaultMethod vmcp.KeepaliveMethod

	// Interval is the time between successive keepalive probes.
	// Defaults to DefaultKeepaliveInterval when zero.
	Interval time.Duration

	// MaxFailures is the consecutive-failure threshold for the circuit breaker.
	// After this many failures keepalive is disabled for the backend and a
	// warning is logged. Defaults to DefaultKeepaliveMaxFailures when zero.
	MaxFailures int

	// ProbeAfter is how long to wait before re-enabling keepalive after the
	// circuit breaker fires. Defaults to DefaultKeepaliveProbeAfter when zero.
	ProbeAfter time.Duration
}

func (c *KeepaliveConfig) intervalOrDefault() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return DefaultKeepaliveInterval
}

func (c *KeepaliveConfig) maxFailuresOrDefault() int {
	if c.MaxFailures > 0 {
		return c.MaxFailures
	}
	return DefaultKeepaliveMaxFailures
}

func (c *KeepaliveConfig) probeAfterOrDefault() time.Duration {
	if c.ProbeAfter > 0 {
		return c.ProbeAfter
	}
	return DefaultKeepaliveProbeAfter
}

// keepaliveMetrics holds the OTel instruments for the keepalive subsystem.
type keepaliveMetrics struct {
	attempts    metric.Int64Counter
	successes   metric.Int64Counter
	failures    metric.Int64Counter
	latency     metric.Float64Histogram
	autoDisable metric.Int64Counter
}

func newKeepaliveMetrics(mp metric.MeterProvider) (*keepaliveMetrics, error) {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	m := mp.Meter(keepaliveInstrumentationScope)

	attempts, err := m.Int64Counter("vmcp_keepalive_attempt_total",
		metric.WithDescription("Total keepalive probes sent, by backend"))
	if err != nil {
		return nil, fmt.Errorf("keepalive attempts counter: %w", err)
	}
	successes, err := m.Int64Counter("vmcp_keepalive_success_total",
		metric.WithDescription("Keepalive probes that succeeded, by backend"))
	if err != nil {
		return nil, fmt.Errorf("keepalive successes counter: %w", err)
	}
	failures, err := m.Int64Counter("vmcp_keepalive_failure_total",
		metric.WithDescription("Keepalive probes that failed, by backend and reason"))
	if err != nil {
		return nil, fmt.Errorf("keepalive failures counter: %w", err)
	}
	latency, err := m.Float64Histogram("vmcp_keepalive_latency_seconds",
		metric.WithDescription("Duration of successful keepalive probes, in seconds"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("keepalive latency histogram: %w", err)
	}
	autoDisable, err := m.Int64Counter("vmcp_keepalive_auto_disabled_total",
		metric.WithDescription("Number of times keepalive was automatically disabled for a backend"))
	if err != nil {
		return nil, fmt.Errorf("keepalive auto-disable counter: %w", err)
	}
	return &keepaliveMetrics{
		attempts:    attempts,
		successes:   successes,
		failures:    failures,
		latency:     latency,
		autoDisable: autoDisable,
	}, nil
}

// circuitBreakerState tracks per-backend keepalive health.
type circuitBreakerState struct {
	consecutiveFailures int
	disabled            bool
	disabledAt          time.Time
}

// backendKeepalive manages keepalive probes for a single backend connection.
type backendKeepalive struct {
	backendID string
	conn      backend.Session
	target    *vmcp.BackendTarget
	cfg       KeepaliveConfig
	metrics   *keepaliveMetrics

	mu    sync.Mutex
	state circuitBreakerState
}

// probe executes one keepalive probe according to the configured method.
// It returns (success bool, reason string). reason is non-empty on failure.
func (b *backendKeepalive) probe(ctx context.Context) (bool, string) {
	method := b.target.KeepaliveMethod
	if method == "" {
		method = b.cfg.DefaultMethod
	}
	if method == "" {
		method = vmcp.KeepaliveMethodNone
	}

	pCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	switch method {
	case vmcp.KeepaliveMethodNone:
		return true, "" // keepalive disabled for this backend
	case vmcp.KeepaliveMethodTool:
		toolName := b.target.KeepaliveToolName
		if toolName == "" {
			return false, "keepalive_tool_name_not_configured"
		}
		_, err := b.conn.CallTool(pCtx, toolName, nil, nil)
		if err != nil {
			return false, "tool_call_failed"
		}
		return true, ""
	case vmcp.KeepaliveMethodPing:
		fallthrough
	default: // unknown values also fall back to ping
		if err := b.conn.Ping(pCtx); err != nil {
			return false, "ping_failed"
		}
		return true, ""
	}
}

// tick runs a single keepalive iteration: probe and update circuit breaker state.
func (b *backendKeepalive) tick(ctx context.Context) {
	b.mu.Lock()
	state := b.state
	cfg := b.cfg
	b.mu.Unlock()

	// Circuit breaker: if disabled, check whether the probe window has elapsed.
	if state.disabled {
		if time.Since(state.disabledAt) < cfg.probeAfterOrDefault() {
			return // still within the quiet window; skip this tick
		}
		slog.Info("keepalive: re-enabling probe after quiet window",
			"backend_id", b.backendID,
			"quiet_window", cfg.probeAfterOrDefault())
		b.mu.Lock()
		b.state.disabled = false
		b.state.consecutiveFailures = 0
		b.mu.Unlock()
	}

	attrs := attribute.NewSet(attribute.String("backend_id", b.backendID))
	b.metrics.attempts.Add(ctx, 1, metric.WithAttributeSet(attrs))

	start := time.Now()
	ok, reason := b.probe(ctx)
	elapsed := time.Since(start)

	if ok {
		b.metrics.successes.Add(ctx, 1, metric.WithAttributeSet(attrs))
		b.metrics.latency.Record(ctx, elapsed.Seconds(), metric.WithAttributeSet(attrs))
		b.mu.Lock()
		b.state.consecutiveFailures = 0
		b.mu.Unlock()
		slog.Debug("keepalive: probe succeeded", "backend_id", b.backendID)
		return
	}

	// Probe failed.
	failAttrs := attribute.NewSet(
		attribute.String("backend_id", b.backendID),
		attribute.String("reason", reason),
	)
	b.metrics.failures.Add(ctx, 1, metric.WithAttributeSet(failAttrs))
	slog.Warn("keepalive: probe failed",
		"backend_id", b.backendID, "reason", reason,
		"consecutive_failures", state.consecutiveFailures+1)

	b.mu.Lock()
	b.state.consecutiveFailures++
	if b.state.consecutiveFailures >= cfg.maxFailuresOrDefault() {
		b.state.disabled = true
		b.state.disabledAt = time.Now()
		b.mu.Unlock()
		b.metrics.autoDisable.Add(ctx, 1, metric.WithAttributeSet(failAttrs))
		slog.Warn("keepalive: circuit breaker tripped, disabling keepalive for backend",
			"backend_id", b.backendID,
			"consecutive_failures", cfg.maxFailuresOrDefault(),
			"probe_after", cfg.probeAfterOrDefault())
	} else {
		b.mu.Unlock()
	}
}

// run is the keepalive goroutine body. It adds per-backend jitter to the
// initial sleep to spread load across sessions, then probes on each tick.
func (b *backendKeepalive) run(ctx context.Context) {
	interval := b.cfg.intervalOrDefault()

	// Jitter: random initial delay in [0, interval/4) to spread probes.
	// Skip when the upper bound is less than 1 to avoid rand.Int64N(0) panic.
	if jitterBound := int64(interval / 4); jitterBound > 0 {
		jitter := time.Duration(rand.Int64N(jitterBound)) //nolint:gosec // non-crypto jitter for probe spread
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.tick(ctx)
		}
	}
}

// KeepaliveManager starts and stops per-backend keepalive goroutines for a
// single MultiSession. It is created by the session factory and embedded in
// defaultMultiSession.
type KeepaliveManager struct {
	backends []*backendKeepalive
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// newKeepaliveManager creates a KeepaliveManager but does not start goroutines.
// Call Start() after construction.
func newKeepaliveManager(
	connections map[string]backend.Session,
	targets map[string]*vmcp.BackendTarget, // workloadID → target
	cfg KeepaliveConfig,
	metrics *keepaliveMetrics,
) *KeepaliveManager {
	km := &KeepaliveManager{}
	for id, conn := range connections {
		target, ok := targets[id]
		if !ok {
			continue
		}
		// Skip backends that explicitly opt out of keepalive.
		if target.KeepaliveMethod == vmcp.KeepaliveMethodNone {
			slog.Debug("keepalive: disabled for backend", "backend_id", id)
			continue
		}
		km.backends = append(km.backends, &backendKeepalive{
			backendID: id,
			conn:      conn,
			target:    target,
			cfg:       cfg,
			metrics:   metrics,
		})
	}
	return km
}

// Start launches background goroutines. ctx should be the session lifetime context.
func (km *KeepaliveManager) Start(ctx context.Context) {
	ctx, km.cancel = context.WithCancel(ctx)
	for _, b := range km.backends {
		km.wg.Add(1)
		go func(bk *backendKeepalive) {
			defer km.wg.Done()
			bk.run(ctx)
		}(b)
	}
}

// Stop cancels all keepalive goroutines and waits for them to exit.
func (km *KeepaliveManager) Stop() {
	if km.cancel != nil {
		km.cancel()
	}
	km.wg.Wait()
}

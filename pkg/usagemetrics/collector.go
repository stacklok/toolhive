// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/updates"
)

const (
	flushInterval = 15 * time.Minute

	// EnvVarUsageMetricsEnabled is the environment variable to disable usage metrics
	EnvVarUsageMetricsEnabled = "TOOLHIVE_USAGE_METRICS_ENABLED"
)

// ShouldEnableMetrics returns true if metrics collection should be enabled
// Checks: CI detection > Config disable > Environment variable > Default (true)
// If either config or env var explicitly disables metrics, they stay disabled
func ShouldEnableMetrics(configDisabled bool) bool {
	return shouldEnableMetrics(configDisabled, updates.ShouldSkipUpdateChecks, os.Getenv)
}

// shouldEnableMetrics is an internal testable version that accepts dependencies
func shouldEnableMetrics(configDisabled bool, isCI func() bool, getEnv func(string) string) bool {
	// 1. Skip metrics in CI environments (automatic)
	if isCI() {
		return false
	}

	// 2. Check config for explicit disable
	if configDisabled {
		return false
	}

	// 3. Check environment variable for explicit opt-out
	if envValue := getEnv(EnvVarUsageMetricsEnabled); envValue == "false" {
		return false
	}

	// 4. Default: enabled
	return true
}

// CollectorOption configures a Collector.
type CollectorOption func(*Collector)

// WithProfile sets the function that describes the server being reported on.
// It is called on every flush, so it must be cheap and safe for concurrent use.
func WithProfile(profile func() Profile) CollectorOption {
	return func(c *Collector) {
		c.profile = profile
	}
}

// WithComponent sets the component reported in the User-Agent (for example
// "vmcp"). By default the component is "local" or "operator", depending on
// whether the process runs in Kubernetes.
func WithComponent(component string) CollectorOption {
	return func(c *Collector) {
		c.client.component = component
	}
}

// WithEndpoint sets the URL reports are sent to, instead of the Stacklok
// update service. Tests use it to point the collector at a local server.
func WithEndpoint(endpoint string) CollectorOption {
	return func(c *Collector) {
		c.client.endpoint = endpoint
	}
}

// NewCollector creates a new metrics collector
func NewCollector(opts ...CollectorOption) (*Collector, error) {
	client := NewClient("")
	collector := &Collector{
		instanceID:    uuid.NewString(), // Generate new instance ID for this process
		currentDate:   getCurrentDateUTC(),
		client:        client,
		callsByClient: make(map[string]int64),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		flushCh:       make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(collector)
	}

	return collector, nil
}

// Start begins the background goroutine for periodic flush
// If already started, this is a no-op to prevent goroutine leaks
func (c *Collector) Start() {
	if c.started.Swap(true) {
		return // Already started
	}
	go c.flushLoop()
}

// IncrementToolCall records a tool call from an unidentified client.
func (c *Collector) IncrementToolCall() {
	c.IncrementToolCallFrom(ClientUnknown)
}

// IncrementToolCallFrom records a tool call made by the given client. The client
// name is normalized, so callers may pass the raw clientInfo name.
func (c *Collector) IncrementToolCallFrom(client string) {
	client = NormalizeClientName(client)
	c.countsMu.Lock()
	defer c.countsMu.Unlock()
	c.counter++
	c.callsByClient[client]++
}

// Flush sends the current metrics to the API
// Checks for midnight boundary crossing and handles daily reset
func (c *Collector) Flush() error {
	currentDate := getCurrentDateUTC()

	//nolint:gosec // G706: instance ID and date from internal state
	slog.Debug("usage metrics flush triggered",
		"date", currentDate, "instance_id", c.instanceID)

	// Check if we crossed midnight UTC
	if c.currentDate != currentDate {
		// Midnight crossed! We need to flush the previous day's count
		// IMPORTANT: We send a fake timestamp (previous day at 23:59:00Z) to ensure
		// the backend attributes these calls to the correct day. The count includes
		// calls from the entire previous day plus ~0-15 minutes from the current day
		// (depending on when the flush runs), but we accept this small misattribution
		// to avoid backend complexity.

		//nolint:gosec // G706: date values from internal state
		slog.Debug("date changed, flushing previous day's count",
			"previous_date", c.currentDate, "current_date", currentDate)

		// Take the previous day's per-client split and reset both counters
		// together, so the split always adds up to the count.
		prevCount, byClient := c.resetCounts()

		if prevCount > 0 {
			// Create fake timestamp for end of previous day
			previousDayTimestamp := c.currentDate + "T23:59:00Z"

			record := c.newRecord(prevCount, byClient, previousDayTimestamp)

			//nolint:gosec // G706: date from internal state
			slog.Debug("flushing tool calls for previous day at midnight boundary",
				"count", prevCount, "date", c.currentDate)

			if err := c.client.SendMetrics(c.instanceID, record); err != nil {
				slog.Debug("failed to send metrics for previous day",
					"error", err)
				// Continue anyway - we'll reset for the new day
			}
		}

		// Start the new day
		c.currentDate = currentDate

		return nil
	}

	// Normal flush - not a midnight boundary
	// Send even if count is 0 to provide presence signal

	// Send with real current timestamp
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339) // ISO 8601 format

	count, byClient := c.snapshotCounts()
	record := c.newRecord(count, byClient, timestamp)

	//nolint:gosec // G706: timestamp from time.Now()
	slog.Debug("flushing tool calls", "count", count, "timestamp", timestamp)

	if err := c.client.SendMetrics(c.instanceID, record); err != nil {
		slog.Debug("failed to send metrics", "error", err)
		// Don't return error - we'll retry on next flush
		return nil
	}

	return nil
}

// Shutdown stops background goroutines and sends a final report if any tool
// calls were counted. With nothing to report it sends nothing: an idle process
// already announced itself through the periodic flush, and a process that is
// restarted repeatedly would otherwise add one empty report per restart.
func (c *Collector) Shutdown(ctx context.Context) {
	if c.shutdown.Load() {
		return
	}

	slog.Debug("shutting down usage metrics collector")
	c.shutdown.Store(true)

	// Signal stop to background goroutines (if started)
	if c.started.Load() {
		close(c.stopCh)
	}

	// Perform final flush, unless there is nothing to report
	if c.GetCurrentCount() > 0 {
		if err := c.Flush(); err != nil {
			slog.Warn("failed to flush metrics on shutdown", "error", err)
		}
	}

	// Wait for goroutines to finish with timeout (only if started)
	if c.started.Load() {
		select {
		case <-c.doneCh:
			slog.Debug("collector stopped cleanly")
		case <-ctx.Done():
			slog.Warn("collector shutdown timed out", "error", ctx.Err())
		}
	}
}

// GetCurrentCount returns the current count (for testing/debugging)
func (c *Collector) GetCurrentCount() int64 {
	c.countsMu.Lock()
	defer c.countsMu.Unlock()
	return c.counter
}

// snapshotCounts returns the current count and a copy of the per-client split.
func (c *Collector) snapshotCounts() (int64, map[string]int64) {
	c.countsMu.Lock()
	defer c.countsMu.Unlock()
	return c.counter, maps.Clone(c.callsByClient)
}

// resetCounts returns the current count and per-client split, and resets both.
func (c *Collector) resetCounts() (int64, map[string]int64) {
	c.countsMu.Lock()
	defer c.countsMu.Unlock()
	count, byClient := c.counter, c.callsByClient
	c.counter, c.callsByClient = 0, make(map[string]int64)
	return count, byClient
}

// newRecord builds the payload for one report, adding the server profile.
func (c *Collector) newRecord(count int64, byClient map[string]int64, timestamp string) MetricRecord {
	record := MetricRecord{
		Count:         count,
		Timestamp:     timestamp,
		CallsByClient: byClient,
	}
	if c.profile != nil {
		record.Profile = c.profile().normalize()
	}
	return record
}

// flushLoop periodically flushes metrics
func (c *Collector) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	defer close(c.doneCh)

	for {
		select {
		case <-ticker.C:
			if err := c.Flush(); err != nil {
				slog.Warn("periodic flush failed", "error", err)
			}
		case <-c.flushCh:
			if err := c.Flush(); err != nil {
				slog.Warn("manual flush failed", "error", err)
			}
		case <-c.stopCh:
			return
		}
	}
}

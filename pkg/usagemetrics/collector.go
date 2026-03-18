// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"log/slog"
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

// NewCollector creates a new metrics collector
func NewCollector() (*Collector, error) {
	client := NewClient("")
	collector := &Collector{
		instanceID:  uuid.NewString(), // Generate new instance ID for this process
		currentDate: getCurrentDateUTC(),
		client:      client,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		flushCh:     make(chan struct{}, 1),
	}

	// Counter starts at 0
	collector.counter.Store(0)

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

// IncrementToolCall increments the tool call counter atomically
func (c *Collector) IncrementToolCall() {
	c.counter.Add(1)
}

// Flush sends the current metrics to the API
// Checks for midnight boundary crossing and handles daily reset
func (c *Collector) Flush() error {
	currentDate := getCurrentDateUTC()
	count := c.counter.Load()

	//nolint:gosec // G706: instance ID and date from internal state
	slog.Debug("usage metrics flush triggered",
		"count", count, "date", currentDate, "instance_id", c.instanceID)

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

		if count > 0 {
			// Create fake timestamp for end of previous day
			previousDayTimestamp := c.currentDate + "T23:59:00Z"

			record := MetricRecord{
				Count:     count,
				Timestamp: previousDayTimestamp,
			}

			//nolint:gosec // G706: date from internal state
			slog.Debug("flushing tool calls for previous day at midnight boundary",
				"count", count, "date", c.currentDate)

			if err := c.client.SendMetrics(c.instanceID, record); err != nil {
				slog.Debug("failed to send metrics for previous day",
					"error", err)
				// Continue anyway - we'll reset for the new day
			}
		}

		// Reset for the new day
		c.currentDate = currentDate
		c.counter.Store(0)

		return nil
	}

	// Normal flush - not a midnight boundary
	// Send even if count is 0 to provide presence signal

	// Send with real current timestamp
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339) // ISO 8601 format

	record := MetricRecord{
		Count:     count,
		Timestamp: timestamp,
	}

	//nolint:gosec // G706: timestamp from time.Now()
	slog.Debug("flushing tool calls", "count", count, "timestamp", timestamp)

	if err := c.client.SendMetrics(c.instanceID, record); err != nil {
		slog.Debug("failed to send metrics", "error", err)
		// Don't return error - we'll retry on next flush
		return nil
	}

	return nil
}

// Shutdown performs final flush and stops background goroutines
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

	// Perform final flush
	if err := c.Flush(); err != nil {
		slog.Warn("failed to flush metrics on shutdown", "error", err)
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
	return c.counter.Load()
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

package usagemetrics

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/logger"
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

	logger.Debugf("Usage metrics flush triggered: count=%d, date=%s, instance_id=%s",
		count, currentDate, c.instanceID)

	// Check if we crossed midnight UTC
	if c.currentDate != currentDate {
		// Midnight crossed! We need to flush the previous day's count
		// IMPORTANT: We send a fake timestamp (previous day at 23:59:00Z) to ensure
		// the backend attributes these calls to the correct day. The count includes
		// calls from the entire previous day plus ~0-15 minutes from the current day
		// (depending on when the flush runs), but we accept this small misattribution
		// to avoid backend complexity.

		logger.Debugf("Date changed from %s to %s, flushing previous day's count", c.currentDate, currentDate)

		if count > 0 {
			// Create fake timestamp for end of previous day
			previousDayTimestamp := c.currentDate + "T23:59:00Z"

			record := MetricRecord{
				Count:     count,
				Timestamp: previousDayTimestamp,
			}

			logger.Debugf("Flushing %d tool calls for previous day %s (midnight boundary)", count, c.currentDate)

			if err := c.client.SendMetrics(c.instanceID, record); err != nil {
				logger.Debugf("Failed to send metrics for previous day: %v", err)
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

	logger.Debugf("Flushing %d tool calls at %s", count, timestamp)

	if err := c.client.SendMetrics(c.instanceID, record); err != nil {
		logger.Debugf("Failed to send metrics: %v", err)
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

	logger.Debugf("Shutting down usage metrics collector")
	c.shutdown.Store(true)

	// Signal stop to background goroutines (if started)
	if c.started.Load() {
		close(c.stopCh)
	}

	// Perform final flush
	if err := c.Flush(); err != nil {
		logger.Warnf("Failed to flush metrics on shutdown: %v", err)
	}

	// Wait for goroutines to finish with timeout (only if started)
	if c.started.Load() {
		select {
		case <-c.doneCh:
			logger.Debugf("Collector stopped cleanly")
		case <-ctx.Done():
			logger.Warnf("Collector shutdown timed out: %v", ctx.Err())
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
				logger.Warnf("Periodic flush failed: %v", err)
			}
		case <-c.flushCh:
			if err := c.Flush(); err != nil {
				logger.Warnf("Manual flush failed: %v", err)
			}
		case <-c.stopCh:
			return
		}
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package usagemetrics provides internal usage metrics for tracking usage and adoption.
package usagemetrics

import (
	"sync/atomic"
	"time"
)

// MetricRecord is the payload sent to the metrics API
type MetricRecord struct {
	Count     int64  `json:"count"`
	Timestamp string `json:"timestamp"` // ISO 8601 format in UTC (e.g., "2025-01-01T23:50:00Z")
}

// Collector manages tool call counting and reporting
type Collector struct {
	// Unique identifier for this proxy instance (UUID)
	instanceID string
	// Atomic counter for thread-safe increments
	counter atomic.Int64
	// Current date in YYYY-MM-DD format (UTC)
	currentDate string
	// HTTP client
	client *Client
	// Lifecycle management
	stopCh   chan struct{}
	doneCh   chan struct{}
	flushCh  chan struct{}
	started  atomic.Bool
	shutdown atomic.Bool
}

// getCurrentDateUTC returns the current date in YYYY-MM-DD format (UTC)
func getCurrentDateUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package usage is a throwaway proof-of-concept "usage telemetry" reporter for
// the ToolHive Kubernetes operator. It phones home product-usage metrics (an
// anonymous installation ID, the operator version, the Kubernetes server
// version, and the number of MCPServer resources) from clusters that Stacklok
// does not operate.
//
// This package is intentionally self-contained so the PoC can be deleted in a
// single commit. It is SEPARATE from the in-cluster OpenTelemetry pipeline and
// from the pkg/operator/telemetry update-check service; it neither reuses nor
// modifies either of those.
//
// The reporter defaults to DISABLED. It only runs when USAGE_CLICKHOUSE_URL is
// set. When enabled it runs leader-only (so HA replicas do not double-emit) and
// every error is logged but never fatal.
package usage

import (
	"fmt"
	"os"
	"time"
)

const (
	// envClickHouseURL is the env var holding the ClickHouse HTTP endpoint to
	// POST usage events to. When unset/empty the reporter is disabled.
	envClickHouseURL = "USAGE_CLICKHOUSE_URL"
	// envReportInterval is the env var holding the reporting interval as a Go
	// duration string (e.g. "5m"). Falls back to defaultReportInterval.
	envReportInterval = "USAGE_REPORT_INTERVAL"

	// defaultReportInterval is the interval used when USAGE_REPORT_INTERVAL is
	// unset or unparsable.
	defaultReportInterval = 5 * time.Minute
)

// Config holds the runtime configuration for the usage reporter. It is
// populated from the environment via ConfigFromEnv.
type Config struct {
	// ClickHouseURL is the ClickHouse HTTP endpoint to POST usage events to.
	// Empty means the reporter is disabled.
	ClickHouseURL string
	// ReportInterval is how often a snapshot is collected and reported.
	ReportInterval time.Duration
}

// ConfigFromEnv builds a Config from environment variables. It never fails: an
// unset URL yields a disabled config, and an unparsable interval falls back to
// the default (with the parse error returned to the caller for logging). The
// caller should treat a non-nil error as advisory, not fatal.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		ClickHouseURL:  os.Getenv(envClickHouseURL),
		ReportInterval: defaultReportInterval,
	}

	raw, found := os.LookupEnv(envReportInterval)
	if !found || raw == "" {
		return cfg, nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return cfg, fmt.Errorf("invalid %s %q, falling back to %s: %w",
			envReportInterval, raw, defaultReportInterval, err)
	}
	if d <= 0 {
		return cfg, fmt.Errorf("%s must be positive, got %q, falling back to %s",
			envReportInterval, raw, defaultReportInterval)
	}
	cfg.ReportInterval = d
	return cfg, nil
}

// Enabled reports whether the usage reporter should run. It is disabled unless
// a ClickHouse URL is configured.
func (c Config) Enabled() bool {
	return c.ClickHouseURL != ""
}

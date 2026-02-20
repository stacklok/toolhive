// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"log/slog"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
)

// middlewareTelemetryMigrationOnce ensures the middleware telemetry migration only runs once
var middlewareTelemetryMigrationOnce sync.Once

// CheckAndPerformMiddlewareTelemetryMigration checks if middleware telemetry migration is needed and performs it.
// This migration ensures middleware-based telemetry configs are properly migrated.
// It handles any additional middleware telemetry configuration migrations beyond the samplingRate conversion.
// It repeats performTelemetryConfigMigration, because an earlier iteration did not migrate middleware telemetry configs.
func CheckAndPerformMiddlewareTelemetryMigration() {
	middlewareTelemetryMigrationOnce.Do(func() {
		// Check if migration was already performed
		appConfig := config.NewDefaultProvider().GetConfig()
		if appConfig.MiddlewareTelemetryMigration {
			slog.Debug("telemetry config migration already completed, skipping")
			return
		}

		if err := performTelemetryConfigMigration(); err != nil {
			slog.Error("failed to perform middleware telemetry migration", "error", err)
			return
		}

		// Mark migration as completed
		if err := config.UpdateConfig(func(c *config.Config) {
			c.MiddlewareTelemetryMigration = true
		}); err != nil {
			slog.Error("failed to update config after middleware telemetry migration", "error", err)
		}
	})
}

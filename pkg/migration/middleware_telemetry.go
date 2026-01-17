package migration

import (
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

// middlewareTelemetryMigrationOnce ensures the middleware telemetry migration only runs once
var middlewareTelemetryMigrationOnce sync.Once

// CheckAndPerformMiddlewareTelemetryMigration checks if middleware telemetry migration is needed and performs it.
// This migration ensures middleware-based telemetry configs are properly migrated.
// It handles any additional middleware telemetry configuration migrations beyond the samplingRate conversion.
// It repeats performTelemetryConfigMigration, because an earlier iteration did not migrate middleware telemetry configs.
func CheckAndPerformMiddlewareTelemetryMigration() {
	middlewareTelemetryMigrationOnce.Do(func() {
		if err := performTelemetryConfigMigration(); err != nil {
			logger.Errorf("Failed to perform middleware telemetry migration: %v", err)
			return
		}
	})
}

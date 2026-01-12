package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/state"
)

// telemetryMigrationOnce ensures the telemetry migration only runs once
var telemetryMigrationOnce sync.Once

// CheckAndPerformTelemetryConfigMigration checks if telemetry config migration is needed and performs it.
// This migration converts telemetry_config.samplingRate from float64 to string in run configs.
func CheckAndPerformTelemetryConfigMigration() {
	telemetryMigrationOnce.Do(func() {
		if err := performTelemetryConfigMigration(); err != nil {
			logger.Errorf("Failed to perform telemetry config migration: %v", err)
			return
		}
	})
}

// performTelemetryConfigMigration migrates all run configs with float64 samplingRate to string
func performTelemetryConfigMigration() error {
	// Check if migration was already performed
	appConfig := config.NewDefaultProvider().GetConfig()
	if appConfig.TelemetryConfigMigration {
		logger.Debugf("Telemetry config migration already completed, skipping")
		return nil
	}

	ctx := context.Background()

	// Get all run config names
	store, err := state.NewRunConfigStore(state.DefaultAppName)
	if err != nil {
		return fmt.Errorf("failed to create state store: %w", err)
	}

	configNames, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list run configs: %w", err)
	}

	migratedCount := 0
	for _, name := range configNames {
		migrated, err := migrateTelemetryConfigForWorkload(ctx, store, name)
		if err != nil {
			logger.Warnf("Failed to migrate telemetry config for workload %s: %v", name, err)
			continue
		}
		if migrated {
			migratedCount++
		}
	}

	if migratedCount > 0 {
		logger.Infof("Successfully migrated telemetry config for %d workload(s)", migratedCount)
	}

	// Mark migration as completed
	err = config.UpdateConfig(func(c *config.Config) {
		c.TelemetryConfigMigration = true
	})
	if err != nil {
		return fmt.Errorf("failed to update config after migration: %w", err)
	}

	return nil
}

// migrateTelemetryConfigForWorkload migrates a single workload's telemetry config
// Returns true if the workload was migrated, false if no migration was needed
func migrateTelemetryConfigForWorkload(ctx context.Context, store state.Store, name string) (bool, error) {
	// Read the raw JSON
	reader, err := store.GetReader(ctx, name)
	if err != nil {
		return false, fmt.Errorf("failed to get reader for %s: %w", name, err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			logger.Warnf("Failed to close reader for %s: %v", name, closeErr)
		}
	}()

	data, err := io.ReadAll(reader)
	if err != nil {
		return false, fmt.Errorf("failed to read config for %s: %w", name, err)
	}

	// Parse as generic map to check the type of samplingRate
	var rawConfig map[string]interface{}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		return false, fmt.Errorf("failed to parse config for %s: %w", name, err)
	}

	// Check if telemetry_config exists and has a numeric samplingRate
	telemetryConfig, ok := rawConfig["telemetry_config"].(map[string]interface{})
	if !ok {
		// No telemetry config, nothing to migrate
		return false, nil
	}

	samplingRate, exists := telemetryConfig["samplingRate"]
	if !exists {
		// No samplingRate field, nothing to migrate
		return false, nil
	}

	// Check if it's already a string
	if _, isString := samplingRate.(string); isString {
		// Already a string, nothing to migrate
		return false, nil
	}

	// Check if it's a number that needs to be converted
	var samplingRateFloat float64
	switch v := samplingRate.(type) {
	case float64:
		samplingRateFloat = v
	case int:
		samplingRateFloat = float64(v)
	case int64:
		samplingRateFloat = float64(v)
	default:
		// Unknown type, skip
		logger.Warnf("Unknown samplingRate type for %s: %T", name, samplingRate)
		return false, nil
	}

	// Convert to string
	telemetryConfig["samplingRate"] = strconv.FormatFloat(samplingRateFloat, 'f', -1, 64)

	// Write back the migrated config
	migratedData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to marshal migrated config for %s: %w", name, err)
	}

	writer, err := store.GetWriter(ctx, name)
	if err != nil {
		return false, fmt.Errorf("failed to get writer for %s: %w", name, err)
	}
	defer func() {
		if closeErr := writer.Close(); closeErr != nil {
			logger.Warnf("Failed to close writer for %s: %v", name, closeErr)
		}
	}()

	if _, err := writer.Write(migratedData); err != nil {
		return false, fmt.Errorf("failed to write migrated config for %s: %w", name, err)
	}

	logger.Debugf("Migrated telemetry config samplingRate from float to string for workload %s", name)
	return true, nil
}

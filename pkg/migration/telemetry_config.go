// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
// It handles both deprecated top-level telemetry_config and middleware-based telemetry configs.
func CheckAndPerformTelemetryConfigMigration() {
	telemetryMigrationOnce.Do(func() {
		// Check if migration was already performed
		appConfig := config.NewDefaultProvider().GetConfig()
		if appConfig.TelemetryConfigMigration {
			logger.Debugf("Telemetry config migration already completed, skipping")
			return
		}

		if err := performTelemetryConfigMigration(); err != nil {
			logger.Errorf("Failed to perform telemetry config migration: %v", err)
			return
		}

		// Mark migration as completed
		if err := config.UpdateConfig(func(c *config.Config) {
			c.TelemetryConfigMigration = true
		}); err != nil {
			logger.Errorf("Failed to update config after telemetry config migration: %v", err)
		}
	})
}

// performTelemetryConfigMigration migrates all run configs with float64 samplingRate to string.
// It handles both deprecated top-level telemetry_config and middleware-based telemetry configs.
func performTelemetryConfigMigration() error {
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

	return nil
}

// migrateSamplingRate converts a samplingRate value from numeric types to string.
// Returns true if conversion was performed, false if already string or missing.
func migrateSamplingRate(telemetryConfig map[string]interface{}) (bool, error) {
	// Check if samplingRate exists
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

	// Convert numeric types to string
	var samplingRateStr string
	switch v := samplingRate.(type) {
	case float64:
		samplingRateStr = strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		samplingRateStr = strconv.Itoa(v)
	case int64:
		samplingRateStr = strconv.FormatInt(v, 10)
	case json.Number:
		samplingRateStr = v.String()
	default:
		return false, fmt.Errorf("unsupported samplingRate type: %T", samplingRate)
	}

	// Update the samplingRate to string
	telemetryConfig["samplingRate"] = samplingRateStr
	return true, nil
}

// migrateTelemetryConfigJSON migrates a run config's telemetry_config.samplingRate from float64 to string.
// This function handles both:
//   - Deprecated top-level telemetry_config field
//   - Middleware-based telemetry configs in middleware_configs array
//
// This is a pure function that takes input JSON and returns migrated JSON without side effects.
//
// Returns:
//   - (nil, nil) if no migration needed (samplingRate missing or already string)
//   - (data, nil) if migration was performed successfully
//   - (nil, error) if the input is invalid or migration would cause data loss
//
// The function preserves all existing fields and only modifies samplingRate if it's a numeric type.
// nolint:gocyclo // this function is complex because we have multiple locations to migrate.
func migrateTelemetryConfigJSON(inputJSON []byte) ([]byte, error) {
	if len(inputJSON) == 0 {
		return nil, fmt.Errorf("empty input JSON")
	}

	// Parse as generic map to preserve all fields
	var rawConfig map[string]interface{}
	if err := json.Unmarshal(inputJSON, &rawConfig); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	migrated := false

	// Migrate deprecated top-level telemetry_config
	telemetryConfigRaw, exists := rawConfig["telemetry_config"]
	if exists {
		telemetryConfig, ok := telemetryConfigRaw.(map[string]interface{})
		if ok {
			didMigrate, err := migrateSamplingRate(telemetryConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to migrate top-level telemetry_config: %w", err)
			}
			if didMigrate {
				migrated = true
			}
		}
	}

	// Migrate middleware-based telemetry configs
	middlewareConfigsRaw, exists := rawConfig["middleware_configs"]
	if exists {
		middlewareConfigs, ok := middlewareConfigsRaw.([]interface{})
		if ok {
			for i, middlewareRaw := range middlewareConfigs {
				middleware, ok := middlewareRaw.(map[string]interface{})
				if !ok {
					continue
				}

				// Check if this is a telemetry middleware
				middlewareType, exists := middleware["type"]
				if !exists {
					continue
				}

				typeStr, ok := middlewareType.(string)
				if !ok || typeStr != "telemetry" {
					continue
				}

				// Get parameters.config
				parametersRaw, exists := middleware["parameters"]
				if !exists {
					continue
				}

				parameters, ok := parametersRaw.(map[string]interface{})
				if !ok {
					continue
				}

				configRaw, exists := parameters["config"]
				if !exists {
					continue
				}

				cfg, ok := configRaw.(map[string]interface{})
				if !ok {
					continue
				}

				// Migrate the samplingRate in this middleware config
				didMigrate, err := migrateSamplingRate(cfg)
				if err != nil {
					return nil, fmt.Errorf("failed to migrate telemetry middleware config at index %d: %w", i, err)
				}
				if didMigrate {
					migrated = true
				}
			}
		}
	}

	// If nothing was migrated, return nil
	if !migrated {
		return nil, nil
	}

	// Marshal back to JSON, preserving formatting
	migratedData, err := json.MarshalIndent(rawConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal migrated config: %w", err)
	}

	// Verify the migration didn't lose data by checking round-trip
	var verifyConfig map[string]interface{}
	if err := json.Unmarshal(migratedData, &verifyConfig); err != nil {
		return nil, fmt.Errorf("migration verification failed: %w", err)
	}

	// Verify key counts match (basic data loss check)
	if len(verifyConfig) != len(rawConfig) {
		return nil, fmt.Errorf("migration would cause data loss: field count mismatch")
	}

	return migratedData, nil
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

	// Use the pure helper to perform the migration
	migratedData, err := migrateTelemetryConfigJSON(data)
	if err != nil {
		return false, fmt.Errorf("failed to migrate config for %s: %w", name, err)
	}

	if migratedData == nil {
		// No migration needed
		return false, nil
	}

	// Atomically write the migrated config
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

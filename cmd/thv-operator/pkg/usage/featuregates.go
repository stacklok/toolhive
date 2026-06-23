// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usage

import (
	"os"
	"strconv"
)

// Feature-gate env vars. These are the operator's existing feature-flag
// environment variables — the single source of truth for whether a gate is on.
// Keep this list small and stable; each entry maps an env var to a snake_case
// snapshot key.
const (
	// envEnableStorageVersionMigrator gates the StorageVersionMigrator controller.
	// Mirrors cmd/thv-operator/app's TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR.
	envEnableStorageVersionMigrator = "TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR"
	// envEnableExperimentalFeatures gates experimental operator features. This is
	// the env var the operator-deploy task and the operator chart inject.
	envEnableExperimentalFeatures = "ENABLE_EXPERIMENTAL_FEATURES"
)

// Snapshot feature-gate keys. Stable, snake_case keys reported in the
// feature_gates map. Renaming a key changes the data emitted into the ClickHouse
// Map(String, UInt8) column, so treat these as part of the data contract.
const (
	gateStorageVersionMigrator = "storage_version_migrator"
	gateExperimentalFeatures   = "experimental_features"
)

// collectFeatureGates reports the on/off state of the operator's feature gates
// as a map of stable, snake_case keys to 1 (on) or 0 (off). It reads each gate
// from its existing operator feature-flag env var so there is a single source
// of truth. It is best-effort: an unset, empty, or unparsable value is treated
// as off (0). Env vars do not change at runtime, so this is intended to be
// called ONCE at reporter construction and the result reused for every tick.
func collectFeatureGates() map[string]uint8 {
	return map[string]uint8{
		gateStorageVersionMigrator: boolEnvAsGate(envEnableStorageVersionMigrator),
		gateExperimentalFeatures:   boolEnvAsGate(envEnableExperimentalFeatures),
	}
}

// boolEnvAsGate reads a boolean feature-flag env var and returns 1 when it is
// set to a truthy value, 0 otherwise. Missing, empty, or unparsable values
// yield 0 — the gate is reported as off rather than failing.
func boolEnvAsGate(envVar string) uint8 {
	value, found := os.LookupEnv(envVar)
	if !found {
		return 0
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil || !enabled {
		return 0
	}
	return 1
}

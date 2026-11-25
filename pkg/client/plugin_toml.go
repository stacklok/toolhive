// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

// ReadTOMLConfig reads and parses a TOML config file into a generic map.
// Exported wrapper around the unexported readTOMLConfig so the plugin adapters
// (which live in a separate package) can reuse the same parse path without
// duplicating code.
func ReadTOMLConfig(path string) (map[string]any, error) {
	return readTOMLConfig(path)
}

// WriteTOMLConfig marshals and writes the config map to a TOML file atomically.
// Exported wrapper around the unexported writeTOMLConfig.
func WriteTOMLConfig(path string, config map[string]any) error {
	return writeTOMLConfig(path, config)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package telemetry provides OpenTelemetry instrumentation for ToolHive MCP server proxies.
package telemetry

import (
	"fmt"
	"strings"
)

// ParseCustomAttributes parses a comma-separated list of key=value pairs into a map.
// Example input: "server_type=production,region=us-east-1,team=platform"
// Returns a map[string]string that can be converted to resource attributes.
func ParseCustomAttributes(input string) (map[string]string, error) {
	if input == "" {
		return map[string]string{}, nil
	}

	attributes := make(map[string]string)
	pairs := strings.Split(input, ",")

	for _, pair := range pairs {
		trimmedPair := strings.TrimSpace(pair)
		if trimmedPair == "" {
			continue
		}

		parts := strings.SplitN(trimmedPair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid attribute format '%s': expected key=value", trimmedPair)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("empty attribute key in '%s'", trimmedPair)
		}

		// Store as key-value pair in map
		attributes[key] = value
	}

	return attributes, nil
}

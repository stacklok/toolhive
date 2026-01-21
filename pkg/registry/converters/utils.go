// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package converters provides utility functions for conversion between upstream and toolhive formats.
package converters

import (
	"strings"
)

// interfaceSliceToStringSlice converts []interface{} to []string
func interfaceSliceToStringSlice(input []interface{}) []string {
	result := make([]string, 0, len(input))
	for _, item := range input {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

// ExtractServerName extracts the simple server name from a reverse-DNS format name
// Example: "io.github.stacklok/fetch" -> "fetch"
func ExtractServerName(reverseDNSName string) string {
	parts := strings.Split(reverseDNSName, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return reverseDNSName
}

// BuildReverseDNSName builds a reverse-DNS format name from a simple name
// Example: "fetch" -> "io.github.stacklok/fetch"
func BuildReverseDNSName(simpleName string) string {
	if strings.Contains(simpleName, "/") {
		return simpleName // Already in reverse-DNS format
	}
	return "io.github.stacklok/" + simpleName
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"encoding/json"
	"fmt"
	"text/template"
)

// FuncMap returns the standard template functions available in composite tools.
// These functions are used both at validation time and runtime to ensure
// templates using json, quote, or fromJson are valid.
//
// Smell: It is odd to expose this low-level detail of which functions are available to templating to other packages.
// Ideally, this would be encapsulated and the higher-level functions like validation would be implemented within this package.
// However, validation is currently implemented against the config types and not the composer types.
// We should make this function internal and consolidate all validation capabilities here once we've
// replaced the composer types for the config types. They are semantically the same and the config types represent the
// documented API types.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"json":     jsonEncode,
		"quote":    quote,
		"fromJson": fromJson,
	}
}

// jsonEncode is a template function that encodes a value as JSON.
func jsonEncode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to encode JSON: %w", err)
	}
	return string(b), nil
}

// quote is a template function that quotes a string.
func quote(s string) string {
	return fmt.Sprintf("%q", s)
}

// fromJson is a template function that parses a JSON string into a value.
// It is useful when the underlying MCP server does not support structured content.
func fromJson(s string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return v, nil
}

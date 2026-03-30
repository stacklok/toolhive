// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"strings"
)

// mergeEnv builds a combined environment from a base set and override entries.
// Both base and overrides are slices of "KEY=value" strings (the standard Go
// convention used by os.Environ and OCI image configs).  Overrides replace
// base entries with the same key.  The result preserves the order of the base
// entries (with overridden values updated in place) followed by any new keys
// from overrides.
func mergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}

	// Index base entries by key for O(1) lookup.
	idx := make(map[string]int, len(base))
	result := make([]string, len(base))
	copy(result, base)
	for i, entry := range result {
		if k, _, ok := strings.Cut(entry, "="); ok {
			idx[k] = i
		}
	}

	for _, entry := range overrides {
		k, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if i, exists := idx[k]; exists {
			result[i] = entry
		} else {
			idx[k] = len(result)
			result = append(result, entry)
		}
	}
	return result
}

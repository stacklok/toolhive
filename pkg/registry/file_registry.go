// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"os"
)

// NewFileRegistry constructs a Registry backed by an upstream-format JSON
// file on disk. The file is read once at construction; subsequent file
// changes require restart (config-mutability is restart-only in v1).
//
// name is the registry identifier reported by Registry.Name(). It must be
// non-empty.
func NewFileRegistry(name, path string) (Registry, error) {
	if path == "" {
		return nil, fmt.Errorf("file registry %q: path must not be empty", name)
	}
	// #nosec G304 -- the path is supplied by the user via configuration; reading
	// arbitrary local files is the entire purpose of a file-source registry.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("file registry %q: read %s: %w", name, path, err)
	}
	entries, err := parseEntries(data)
	if err != nil {
		return nil, fmt.Errorf("file registry %q: %w", name, err)
	}
	return NewStore(name, entries)
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

// readJSONFile reads and parses the JSONC file at path into a map.
// If allowMissing is true and the file does not exist, an empty map is returned
// with no error (suitable for patchJSONFile which creates the file if absent).
// If allowMissing is false and the file does not exist, a nil map and nil error
// are returned as a signal to callers that should be no-ops on missing files.
func readJSONFile(path string, allowMissing bool) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is a known tool config file location
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return make(map[string]any), nil
		}
		return nil, nil //nolint:nilnil // nil map signals "file absent, caller should no-op"
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	m := make(map[string]any)
	if len(data) > 0 {
		standardized, err := hujson.Standardize(data)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if err := json.Unmarshal(standardized, &m); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	return m, nil
}

// patchJSONFile reads the JSON(C) object at path (or starts from an empty
// object if the file does not exist), applies patchFn to the in-memory map,
// and writes the result back to path atomically via a sibling temp file +
// rename. JSONC input (comments, trailing commas) is accepted on read; the
// file is always written back as standard JSON so comments are not preserved.
func patchJSONFile(path string, patchFn func(m map[string]any)) error {
	m, err := readJSONFile(path, true)
	if err != nil {
		return err
	}

	patchFn(m)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	return writeJSONAtomic(path, m)
}

// revertFlatJSONFile reads the JSON(C) object at path and removes the given
// keys as literal top-level keys (dots are part of the key name, not path
// separators). Use this for tools like VS Code and Cursor whose settings use
// "a.b.c" as a single flat key rather than a nested object path.
// If the file does not exist the function is a no-op.
func revertFlatJSONFile(path string, keys ...string) error {
	m, err := readJSONFile(path, false)
	if err != nil {
		return err
	}
	if m == nil {
		return nil // file was absent
	}
	for _, k := range keys {
		delete(m, k)
	}
	return writeJSONAtomic(path, m)
}

// revertJSONFile reads the JSON(C) object at path and removes the given
// dot-path keys (e.g. "env.ANTHROPIC_BASE_URL"), then writes the result back
// atomically. If the file does not exist the function is a no-op.
func revertJSONFile(path string, keys ...string) error {
	m, err := readJSONFile(path, false)
	if err != nil {
		return err
	}
	if m == nil {
		return nil // file was absent
	}
	for _, k := range keys {
		deleteNestedKey(m, k)
	}
	return writeJSONAtomic(path, m)
}

// setNestedKey sets a dot-delimited key path in m, creating intermediate maps
// as needed. For example, setNestedKey(m, "env.ANTHROPIC_BASE_URL", "https://…")
// produces m["env"]["ANTHROPIC_BASE_URL"] = "https://…".
func setNestedKey(m map[string]any, key string, value any) {
	head, tail, found := strings.Cut(key, ".")
	if !found {
		m[key] = value
		return
	}
	sub, ok := m[head].(map[string]any)
	if !ok {
		sub = make(map[string]any)
	}
	setNestedKey(sub, tail, value)
	m[head] = sub
}

// deleteNestedKey removes a dot-delimited key path from m.
// If any intermediate map is missing the function is a no-op.
func deleteNestedKey(m map[string]any, key string) {
	head, tail, found := strings.Cut(key, ".")
	if !found {
		delete(m, key)
		return
	}
	sub, ok := m[head].(map[string]any)
	if !ok {
		return
	}
	deleteNestedKey(sub, tail)
	// Remove empty intermediate maps left behind after deletion.
	if len(sub) == 0 {
		delete(m, head)
	}
}

// writeJSONAtomic marshals m as indented JSON and writes it to path atomically
// by first writing a sibling temp file and then renaming it.
func writeJSONAtomic(path string, m map[string]any) error {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding JSON for %s: %w", path, err)
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".thv-patch-*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming temp file to %s: %w", path, err)
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ManifestPath is the required manifest file path for a plugin directory,
// matching the Claude Plugin manifest format.
const ManifestPath = ".claude-plugin/plugin.json"

// MaxManifestSize limits the plugin.json size to prevent JSON parsing attacks
// (e.g. billion laughs). Mirrors the packager's maxManifestSize.
const MaxManifestSize = 64 * 1024

// MaxComponentsPerGroup caps the number of entries allowed in a single
// component array (commands, agents, skills, hooks). Mirrors the skills
// parser's MaxDependencies bound and bounds the cost of ValidatePluginDir,
// which walks each bundled skill path. A 64KB manifest can otherwise pack
// thousands of "./x" entries cheaply.
const MaxComponentsPerGroup = 100

// ErrInvalidManifest indicates that the plugin manifest is malformed, missing,
// or fails toolhive-side strictness checks.
var ErrInvalidManifest = errors.New("invalid plugin manifest")

// Author represents the author field of a plugin manifest.
type Author struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// PluginManifest represents the toolhive-readable fields of
// .claude-plugin/plugin.json. Unknown fields are preserved in Raw so the
// manifest can be round-tripped without loss.
type PluginManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Author      Author `json:"author,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Repository  string `json:"repository,omitempty"`
	License     string `json:"license,omitempty"`
	// Keywords MUST be a JSON array (a string is a hard error — see strictStringSlice).
	Keywords strictStringSlice `json:"keywords"`
	// Component directories: each entry must be a relative path starting with "./".
	Commands   []string        `json:"commands,omitempty"`
	Agents     []string        `json:"agents,omitempty"`
	Skills     []string        `json:"skills,omitempty"`
	Hooks      []string        `json:"hooks,omitempty"`
	McpServers json.RawMessage `json:"mcpServers,omitempty"`
	LspServers json.RawMessage `json:"lspServers,omitempty"`
	// Raw is the full original document, preserved for round-tripping unknown fields.
	Raw json.RawMessage `json:"-"`
}

// strictStringSlice is a []string that rejects a JSON string value. Plugins
// require keywords to be a JSON array (unlike skills, which accept a
// space-delimited string fallback); a string here is a hard error.
type strictStringSlice []string

// UnmarshalJSON implements json.Unmarshaler. A JSON string is rejected;
// a JSON array is decoded into the slice; any other type is an error.
func (s *strictStringSlice) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*s = nil
		return nil
	}
	if trimmed[0] == '"' {
		return fmt.Errorf("%w: keywords must be an array, got string", ErrInvalidManifest)
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("%w: keywords must be an array: %w", ErrInvalidManifest, err)
	}
	*s = arr
	return nil
}

// ParsePluginManifest reads and parses .claude-plugin/plugin.json from
// pluginDir. It performs toolhive-specific pre-build strictness checks only;
// the packager re-walks the directory and re-validates at build time.
//
// Strictness (exit gate):
//   - keywords MUST be a JSON array; a string is a hard error.
//   - component paths (commands/agents/skills/hooks) must be relative,
//     start with "./", and contain no ".." traversal segments.
//
// The full document is preserved in .Raw (unknown fields preserved).
func ParsePluginManifest(pluginDir string) (*PluginManifest, error) {
	manifestPath := filepath.Join(pluginDir, ManifestPath)

	// Lstat before reading: reject a symlinked manifest (TOCTOU guard) and
	// avoid allocating an oversized file before the size check rejects it.
	fi, err := os.Lstat(manifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s not found in plugin directory: %w", ManifestPath, ErrInvalidManifest)
		}
		return nil, fmt.Errorf("checking %s: %w", ManifestPath, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file: %w", ManifestPath, ErrInvalidManifest)
	}
	if fi.Size() > MaxManifestSize {
		return nil, fmt.Errorf("manifest size %d exceeds maximum of %d bytes: %w",
			fi.Size(), MaxManifestSize, ErrInvalidManifest)
	}

	content, err := os.ReadFile(manifestPath) //#nosec G304 -- path joined from validated pluginDir; symlink rejected above
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ManifestPath, err)
	}

	return parseManifestBytes(content)
}

// ParsePluginManifestFromBytes parses manifest bytes into a PluginManifest,
// applying the same strictness checks as ParsePluginManifest. Split out for
// callers (e.g. the git install flow) that already hold the manifest bytes
// rather than a directory on disk.
func ParsePluginManifestFromBytes(content []byte) (*PluginManifest, error) {
	return parseManifestBytes(content)
}

// parseManifestBytes parses manifest bytes into a PluginManifest, applying the
// strictness checks. Split from ParsePluginManifest for testability.
func parseManifestBytes(content []byte) (*PluginManifest, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return nil, fmt.Errorf("%w: manifest is empty", ErrInvalidManifest)
	}
	if len(content) > MaxManifestSize {
		return nil, fmt.Errorf("manifest exceeds maximum size of %d bytes: %w", MaxManifestSize, ErrInvalidManifest)
	}

	// Decode into a json.Decoder withDisallowUnknownFields? No — we preserve
	// unknown fields in .Raw, so we use standard unmarshal which ignores them.
	var m PluginManifest
	if err := json.Unmarshal(content, &m); err != nil {
		return nil, fmt.Errorf("%w: parsing manifest JSON: %w", ErrInvalidManifest, err)
	}

	// Preserve the full document for round-tripping.
	m.Raw = make([]byte, len(content))
	copy(m.Raw, content)

	// Validate component paths after unmarshaling. Cap each group at
	// MaxComponentsPerGroup to bound the cost of downstream validation
	// (ValidatePluginDir walks each bundled skill path).
	for _, group := range [][]string{m.Commands, m.Agents, m.Skills, m.Hooks} {
		if len(group) > MaxComponentsPerGroup {
			return nil, fmt.Errorf("%w: component group exceeds maximum of %d entries", ErrInvalidManifest, MaxComponentsPerGroup)
		}
		for _, p := range group {
			if err := validateComponentPath(p); err != nil {
				return nil, err
			}
		}
	}

	return &m, nil
}

// validateComponentPath rejects empty, non-"./"-prefixed, absolute, or
// ".."-containing component paths. Component directories declared in the
// manifest must be relative paths scoped under the plugin directory to
// prevent path traversal during packaging/extraction.
func validateComponentPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: component path is empty", ErrInvalidManifest)
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("%w: component path %q must be relative (got absolute path)", ErrInvalidManifest, p)
	}
	if !strings.HasPrefix(p, "./") {
		return fmt.Errorf("%w: component path %q must be relative and start with ./", ErrInvalidManifest, p)
	}
	for _, segment := range strings.Split(filepath.ToSlash(p), "/") {
		if segment == ".." {
			return fmt.Errorf("%w: component path %q must not contain '..' traversal", ErrInvalidManifest, p)
		}
	}
	return nil
}

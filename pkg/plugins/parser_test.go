// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeManifest writes a .claude-plugin/plugin.json with the given content
// under a fresh plugin directory named after the supplied name, and returns
// the plugin directory path.
func writeManifest(t *testing.T, name, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ManifestPath), []byte(manifest), 0o600))
	return pluginDir
}

func TestParsePluginManifest_Valid(t *testing.T) {
	t.Parallel()
	manifest := `{
		"name": "good-plugin",
		"version": "1.2.3",
		"description": "a test plugin",
		"author": {"name": "tester", "email": "t@example.com"},
		"license": "MIT",
		"keywords": ["alpha", "beta"],
		"commands": ["./commands/foo.md"],
		"skills": ["./skills/bar"],
		"customField": 42
	}`
	dir := writeManifest(t, "good-plugin", manifest)

	m, err := ParsePluginManifest(dir)
	require.NoError(t, err)
	assert.Equal(t, "good-plugin", m.Name)
	assert.Equal(t, "1.2.3", m.Version)
	assert.Equal(t, "a test plugin", m.Description)
	assert.Equal(t, "tester", m.Author.Name)
	assert.Equal(t, "MIT", m.License)
	assert.Equal(t, []string{"alpha", "beta"}, []string(m.Keywords))
	assert.Contains(t, string(m.Raw), "customField")
	assert.Contains(t, string(m.Raw), `"name": "good-plugin"`)
}

func TestParsePluginManifest_Missing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := ParsePluginManifest(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
}

func TestParsePluginManifest_KeywordsStringRejected(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"x","keywords":"foo"}`
	dir := writeManifest(t, "x", manifest)

	_, err := ParsePluginManifest(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "keywords must be an array, got string")
}

func TestParsePluginManifest_KeywordsArrayAccepted(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"x","keywords":["a","b"]}`
	dir := writeManifest(t, "x", manifest)

	m, err := ParsePluginManifest(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, []string(m.Keywords))
}

func TestParsePluginManifest_KeywordsNull(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"x","keywords":null}`
	dir := writeManifest(t, "x", manifest)

	m, err := ParsePluginManifest(dir)
	require.NoError(t, err)
	assert.Nil(t, m.Keywords)
}

func TestParsePluginManifest_ComponentPathTraversal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		manifest  string
		errSubstr string
	}{
		{"parent traversal", `{"name":"x","skills":["../escape"]}`, "must be relative and start with ./"},
		{"absolute path", `{"name":"x","skills":["/abs/path"]}`, "must be relative (got absolute path)"},
		{"no ./ prefix", `{"name":"x","skills":["nope"]}`, "must be relative and start with ./"},
		{"commands traversal", `{"name":"x","commands":["./ok","../bad"]}`, "must be relative and start with ./"},
		{"hooks traversal", `{"name":"x","hooks":["./h","/etc/passwd"]}`, "must be relative (got absolute path)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := writeManifest(t, "x", tt.manifest)
			_, err := ParsePluginManifest(dir)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidManifest)
			assert.Contains(t, err.Error(), tt.errSubstr)
		})
	}
}

func TestParsePluginManifest_ComponentPathEmpty(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"x","skills":[""]}`
	dir := writeManifest(t, "x", manifest)
	_, err := ParsePluginManifest(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "component path is empty")
}

func TestParsePluginManifest_ComponentPathValid(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"x","skills":["./nested/skill"],"commands":["./cmd.md"]}`
	dir := writeManifest(t, "x", manifest)

	m, err := ParsePluginManifest(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"./nested/skill"}, m.Skills)
	assert.Equal(t, []string{"./cmd.md"}, m.Commands)
}

func TestParsePluginManifest_UnknownFieldsPreserved(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"x","customField":42,"nested":{"a":1}}`
	dir := writeManifest(t, "x", manifest)

	m, err := ParsePluginManifest(dir)
	require.NoError(t, err)
	assert.Contains(t, string(m.Raw), "customField")
	assert.Contains(t, string(m.Raw), "nested")
}

func TestValidatePluginName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", "good-name", false},
		{"valid numeric", "plugin-123", false},
		{"invalid uppercase", "Bad_Name", true},
		{"invalid spaces", "bad name", true},
		{"invalid underscores", "bad_name", true},
		{"invalid consecutive hyphens", "bad--name", true},
		{"invalid single char", "a", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePluginName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParsePluginManifest_NameKebabCase(t *testing.T) {
	t.Parallel()
	// parseManifestBytes does not validate the name (that is ValidatePluginDir's
	// job); it only enforces manifest structure. So a bad name parses here.
	manifest := `{"name":"Bad_Name"}`
	dir := writeManifest(t, "Bad_Name", manifest)

	m, err := ParsePluginManifest(dir)
	require.NoError(t, err)
	assert.Equal(t, "Bad_Name", m.Name)
}

func TestParsePluginManifest_SymlinkRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o750))
	target := filepath.Join(dir, "real-manifest.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"name":"x"}`), 0o600))
	linkPath := filepath.Join(dir, ManifestPath)
	require.NoError(t, os.Symlink(target, linkPath))

	_, err := ParsePluginManifest(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "regular file")
}

func TestParsePluginManifest_OversizedRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o750))
	// A manifest one byte larger than the limit.
	oversized := make([]byte, MaxManifestSize+1)
	for i := range oversized {
		oversized[i] = ' '
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ManifestPath), oversized, 0o600))

	_, err := ParsePluginManifest(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

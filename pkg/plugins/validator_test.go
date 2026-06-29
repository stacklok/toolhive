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

// writePluginDir creates a plugin directory at dir/name with a manifest and
// optional bundled content. manifestBody is written to .claude-plugin/plugin.json.
// name is the directory name and is kept distinct from the manifest's "name"
// field so mismatch tests can construct dir-name ≠ manifest-name fixtures.
//
//nolint:unparam // name is always "my-plugin" in current tests but is required for mismatch fixtures.
func writePluginDir(t *testing.T, name, manifestBody string) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ManifestPath), []byte(manifestBody), 0o600))
	return pluginDir
}

func TestValidatePluginDir_ValidWithBundledSkill(t *testing.T) {
	t.Parallel()
	manifest := `{
		"name": "my-plugin",
		"version": "1.0.0",
		"description": "test",
		"keywords": ["a"],
		"skills": ["./skills/foo"]
	}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	// Bundled skill: ./skills/foo with SKILL.md frontmatter name == "foo".
	skillDir := filepath.Join(pluginDir, "skills", "foo")
	require.NoError(t, os.MkdirAll(skillDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: foo\ndescription: a bundled skill\n---\n# Foo\n"), 0o600))

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.True(t, result.Valid, "errors: %v", result.Errors)
}

func TestValidatePluginDir_MissingManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// empty dir, no .claude-plugin/plugin.json
	result, err := ValidatePluginDir(dir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "plugin.json")
}

func TestValidatePluginDir_NameMismatch(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"other","description":"x"}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "must match directory name")
}

func TestValidatePluginDir_BundledSkillValidationReuse(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"my-plugin","description":"x","skills":["./skills/bad"]}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	// Bundled skill missing the required name in frontmatter — the skills
	// validator must surface its message.
	skillDir := filepath.Join(pluginDir, "skills", "bad")
	require.NoError(t, os.MkdirAll(skillDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\ndescription: no name here\n---\n# Bad\n"), 0o600))

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "bundled skill")
	assert.Contains(t, joinErrors(result.Errors), "name is required")
}

func TestValidatePluginDir_SymlinkRejected(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"my-plugin","description":"x"}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	// Create a symlink inside the plugin dir — CheckFilesystem must reject it.
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(pluginDir, "link.md")))

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "symlink")
}

func TestValidatePluginDir_TraversalInManifest(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"my-plugin","description":"x","skills":["../escape"]}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "must be relative and start with ./")
}

func TestValidatePluginDir_RelativePath(t *testing.T) {
	t.Parallel()
	_, err := ValidatePluginDir("relative/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestValidatePluginDir_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := ValidatePluginDir("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// joinErrors concatenates error strings for substring assertions.
func joinErrors(errs []string) string {
	out := ""
	for _, e := range errs {
		out += e + "\n"
	}
	return out
}

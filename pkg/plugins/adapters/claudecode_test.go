// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// resolvedTempDir returns a temp directory path with symlinks resolved, so that
// paths under it pass plugin extraction's "refusing to extract through symlinks"
// check (e.g. on macOS, t.TempDir() may return /var/folders/... where /var is a
// symlink to /private/var).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return resolved
}

// makePluginLayer builds a tar.gz layer from the given file entries, suitable
// for passing as MaterializeRequest.LayerData. Mirrors the fixture helper in
// pkg/skills/installer_test.go.
func makePluginLayer(t *testing.T, files []ociskills.FileEntry) []byte {
	t.Helper()
	data, err := ociskills.CompressTar(files, ociskills.DefaultTarOptions(), ociskills.DefaultGzipOptions())
	require.NoError(t, err)
	return data
}

// newTestClientManager builds a ClientManager whose home directory is tempHome,
// so plugin paths resolve under the test's temp tree. Uses the production
// client integrations (which include ClaudeCode and Codex with full plugin
// metadata) rooted at tempHome — no process-wide HOME mutation, so tests can
// run in parallel.
func newTestClientManager(t *testing.T, tempHome string) *client.ClientManager {
	t.Helper()
	return client.NewTestClientManagerWithHome(tempHome)
}

// userSettingsPath returns <tempHome>/.claude/settings.json.
func userSettingsPath(tempHome string) string {
	return filepath.Join(tempHome, ".claude", "settings.json")
}

// readSettings reads and parses the JSON settings file at path.
func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m), "parsing %s", path)
	return m
}

// requireMarketplaceEntry asserts settings.json has extraKnownMarketplaces.toolhive
// with a "directory" source pointing at the plugins parent directory (the
// directory containing pluginDir). Callers pass the per-plugin directory; the
// expected marketplace path is derived as filepath.Dir(pluginDir).
func requireMarketplaceEntry(t *testing.T, settings map[string]any, pluginDir string) {
	t.Helper()
	marketplaces, ok := settings["extraKnownMarketplaces"].(map[string]any)
	require.True(t, ok, "extraKnownMarketplaces present")
	tv, ok := marketplaces["toolhive"].(map[string]any)
	require.True(t, ok, "toolhive marketplace entry present")
	src, ok := tv["source"].(map[string]any)
	require.True(t, ok, "marketplace source object present")
	assert.Equal(t, "directory", src["source"], "marketplace source type")
	assert.Equal(t, filepath.Dir(pluginDir), src["path"], "marketplace source path")
}

// readClaudeMarketplaceManifest parses the shared marketplace.json at the
// plugins root (<pluginsRoot>/.claude-plugin/marketplace.json).
func readClaudeMarketplaceManifest(t *testing.T, pluginsRoot string) claudeMarketplace {
	t.Helper()
	path := filepath.Join(pluginsRoot, ".claude-plugin", "marketplace.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err, "marketplace.json should exist at %s", path)
	var mp claudeMarketplace
	require.NoError(t, json.Unmarshal(b, &mp))
	return mp
}

// requireMarketplacePlugin asserts the manifest lists a plugin with the given
// name and a "./<name>" source.
func requireMarketplacePlugin(t *testing.T, mp claudeMarketplace, name string) {
	t.Helper()
	assert.Equal(t, "toolhive", mp.Name, "marketplace name")
	assert.NotEmpty(t, mp.Owner.Name, "marketplace owner name")
	for _, p := range mp.Plugins {
		if p.Name == name {
			assert.Equal(t, "./"+name, p.Source, "plugin source is a relative ./<name> path")
			return
		}
	}
	t.Fatalf("plugin %q not found in marketplace manifest %+v", name, mp.Plugins)
}

func TestClaudeCodeAdapter_SupportedComponents(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	got := a.SupportedComponents()
	assert.Equal(t, []plugins.ComponentType{
		plugins.ComponentCommands,
		plugins.ComponentAgents,
		plugins.ComponentSkills,
		plugins.ComponentHooks,
	}, got)
}

func TestClaudeCodeAdapter_MaterializeWritesFiles(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/greet.md", Content: []byte("# greet"), Mode: 0644},
		{Path: "skills/useful/SKILL.md", Content: []byte("# useful"), Mode: 0644},
	})

	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "my-plugin",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1, "skills": 1},
	})
	require.NoError(t, err)

	// Install path is ~/.claude/plugins/my-plugin.
	wantDir := filepath.Join(tempHome, ".claude", "plugins", "my-plugin")
	assert.Equal(t, wantDir, res.InstallPath)

	_, err = os.Stat(filepath.Join(wantDir, "commands", "greet.md"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(wantDir, "skills", "useful", "SKILL.md"))
	require.NoError(t, err)
	assert.False(t, res.ProjectScopeDegraded)

	// The shared marketplace.json at the plugins root must list the plugin with
	// a "./<name>" source and the required top-level name/owner fields.
	pluginsRoot := filepath.Dir(wantDir)
	_, err = os.Stat(filepath.Join(wantDir, ".claude-plugin", "marketplace.json"))
	assert.True(t, os.IsNotExist(err), "no per-plugin marketplace.json should exist")
	mp := readClaudeMarketplaceManifest(t, pluginsRoot)
	requireMarketplacePlugin(t, mp, "my-plugin")

	// settings.json must register the plugin as enabled under toolhive.
	settings := readSettings(t, userSettingsPath(tempHome))
	enabled, ok := settings["enabledPlugins"].(map[string]any)
	require.True(t, ok, "enabledPlugins present")
	assert.Equal(t, true, enabled["my-plugin@toolhive"], "plugin enabled")
	requireMarketplaceEntry(t, settings, wantDir)
}

func TestClaudeCodeAdapter_ExecutableBitPreserved(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	// Layer includes a hook script committed at 0755 — the exec bit must
	// survive extraction to disk (not just in the in-memory FileEntry).
	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "hooks/preinstall.sh", Content: []byte("#!/bin/sh\necho hi"), Mode: 0755},
		{Path: "commands/greet.md", Content: []byte("# greet"), Mode: 0644},
	})

	wantDir := filepath.Join(tempHome, ".claude", "plugins", "my-plugin")
	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "my-plugin",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"hooks": 1, "commands": 1},
	})
	require.NoError(t, err)

	// The hook script must be executable on disk.
	info, err := os.Stat(filepath.Join(wantDir, "hooks", "preinstall.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o100,
		"executable bit should be preserved on disk, got mode %o", info.Mode().Perm())

	// A regular markdown file must NOT be executable.
	mdInfo, err := os.Stat(filepath.Join(wantDir, "commands", "greet.md"))
	require.NoError(t, err)
	assert.Zero(t, mdInfo.Mode().Perm()&0o100,
		"non-executable file should not gain exec bit, got mode %o", mdInfo.Mode().Perm())
}

func TestClaudeCodeAdapter_DroppedComponents(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	// Declare all six component types; Claude Code drops mcpServers + lspServers.
	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/x.md", Content: []byte("x"), Mode: 0644},
	})

	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:      "drop-test",
		LayerData: layer,
		Scope:     plugins.ScopeUser,
		Components: plugins.ComponentInventory{
			"commands":   1,
			"agents":     1,
			"skills":     1,
			"hooks":      1,
			"mcpServers": 1,
			"lspServers": 1,
		},
	})
	require.NoError(t, err)

	// Dropped set is unordered; compare as a set.
	got := map[plugins.ComponentType]bool{}
	for _, c := range res.DroppedComponents {
		got[c] = true
	}
	assert.Equal(t, map[plugins.ComponentType]bool{
		plugins.ComponentMCP: true,
		plugins.ComponentLSP: true,
	}, got)
}

func TestClaudeCodeAdapter_DematerializeRemovesDir(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/greet.md", Content: []byte("# greet"), Mode: 0644},
	})

	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "remove-me",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)

	dir := filepath.Join(tempHome, ".claude", "plugins", "remove-me")
	_, err = os.Stat(dir)
	require.NoError(t, err)

	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "remove-me", Scope: plugins.ScopeUser}))

	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "dir should be gone after dematerialize")

	// The shared marketplace.json must be gone once the last plugin is removed.
	_, err = os.Stat(filepath.Join(filepath.Dir(dir), ".claude-plugin", "marketplace.json"))
	assert.True(t, os.IsNotExist(err), "shared marketplace.json removed when no plugins remain")

	// The enabledPlugins entry for remove-me@toolhive must be gone, and the
	// marketplace entry must also be gone (no remaining toolhive plugins).
	settings := readSettings(t, userSettingsPath(tempHome))
	if enabled, ok := settings["enabledPlugins"].(map[string]any); ok {
		_, present := enabled["remove-me@toolhive"]
		assert.False(t, present, "enabledPlugins entry should be removed")
	}
	_, hasMarketplaces := settings["extraKnownMarketplaces"]
	assert.False(t, hasMarketplaces, "marketplace entry should be removed when no toolhive plugins remain")
}

func TestClaudeCodeAdapter_DematerializeIdempotent(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	// Dematerializing something that was never installed is not an error.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "never-there", Scope: plugins.ScopeUser}))
	// A second dematerialize is also fine.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "never-there", Scope: plugins.ScopeUser}))

	// Dematerializing a never-installed plugin must NOT create settings.json.
	_, err := os.Stat(userSettingsPath(tempHome))
	assert.True(t, os.IsNotExist(err), "no settings.json should be created by dematerialize")
}

func TestClaudeCodeAdapter_RematerializeOverwrites(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	dir := filepath.Join(tempHome, ".claude", "plugins", "reinstall")

	first := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/v1.md", Content: []byte("v1"), Mode: 0644},
	})
	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "reinstall",
		LayerData:  first,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "commands", "v1.md"))
	require.NoError(t, err)

	// Re-materialize with different content; force=true must overwrite.
	second := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/v2.md", Content: []byte("v2"), Mode: 0644},
	})
	_, err = a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "reinstall",
		LayerData:  second,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "commands", "v2.md"))
	require.NoError(t, err, "new file should exist after reinstall")
	// v1.md is removed because force overwrites the whole directory.
	_, err = os.Stat(filepath.Join(dir, "commands", "v1.md"))
	assert.True(t, os.IsNotExist(err), "old file should be gone after forced reinstall")

	// The settings.json entries should not be duplicated.
	settings := readSettings(t, userSettingsPath(tempHome))
	enabled, ok := settings["enabledPlugins"].(map[string]any)
	require.True(t, ok, "enabledPlugins present")
	assert.Equal(t, true, enabled["reinstall@toolhive"])
}

func TestClaudeCodeAdapter_ScopeSupport(t *testing.T) {
	t.Parallel()
	a := &ClaudeCodeAdapter{}
	ss := a.ScopeSupport()
	assert.False(t, ss.DegradesOnProjectScope)
	assert.Empty(t, ss.Reason)
}

func TestClaudeCodeAdapter_MaterializePreservesUnrelatedSettingsKeys(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	// Seed ~/.claude/settings.json with an unrelated top-level key and an
	// unrelated enabled plugin under a different marketplace.
	seed := map[string]any{
		"foo": "bar",
		"enabledPlugins": map[string]any{
			"other@somewhere": true,
		},
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(userSettingsPath(tempHome)), 0o700))
	seedData, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(userSettingsPath(tempHome), seedData, 0o600))

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/greet.md", Content: []byte("# greet"), Mode: 0644},
	})
	_, err = a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "preserve",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)

	// Unrelated keys must survive materialize.
	settings := readSettings(t, userSettingsPath(tempHome))
	assert.Equal(t, "bar", settings["foo"])
	enabled, ok := settings["enabledPlugins"].(map[string]any)
	require.True(t, ok, "enabledPlugins present")
	assert.Equal(t, true, enabled["other@somewhere"], "unrelated enabled plugin survives")
	assert.Equal(t, true, enabled["preserve@toolhive"], "new plugin enabled")

	// Dematerialize and confirm unrelated keys still survive.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "preserve", Scope: plugins.ScopeUser}))
	settings = readSettings(t, userSettingsPath(tempHome))
	assert.Equal(t, "bar", settings["foo"])
	enabled, ok = settings["enabledPlugins"].(map[string]any)
	require.True(t, ok, "enabledPlugins still present")
	assert.Equal(t, true, enabled["other@somewhere"], "unrelated enabled plugin survives dematerialize")
	_, present := enabled["preserve@toolhive"]
	assert.False(t, present, "toolhive plugin entry removed")
}

func TestClaudeCodeAdapter_DematerializeRemovesMarketplaceEntryWhenNoToolhivePluginsRemain(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/x.md", Content: []byte("x"), Mode: 0644},
	})

	// Install two plugins.
	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "alpha",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)
	_, err = a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "beta",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)

	// Remove alpha: marketplace entry stays because beta is still enabled.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "alpha", Scope: plugins.ScopeUser}))
	settings := readSettings(t, userSettingsPath(tempHome))
	enabled := settings["enabledPlugins"].(map[string]any)
	_, alphaPresent := enabled["alpha@toolhive"]
	assert.False(t, alphaPresent, "alpha removed")
	assert.Equal(t, true, enabled["beta@toolhive"], "beta still enabled")
	_, hasMarketplaces := settings["extraKnownMarketplaces"]
	assert.True(t, hasMarketplaces, "marketplace entry stays while beta enabled")

	// Remove beta: marketplace entry is now removed (no toolhive plugins left).
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "beta", Scope: plugins.ScopeUser}))
	settings = readSettings(t, userSettingsPath(tempHome))
	_, hasMarketplaces = settings["extraKnownMarketplaces"]
	assert.False(t, hasMarketplaces, "marketplace entry removed when no toolhive plugins remain")
	_, hasEnabled := settings["enabledPlugins"]
	assert.False(t, hasEnabled, "enabledPlugins removed when empty")
}

func TestClaudeCodeAdapter_NonLifoUninstallKeepsMarketplacePathValid(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/greet.md", Content: []byte("# greet"), Mode: 0644},
	})

	pluginsDir := filepath.Join(tempHome, ".claude", "plugins")

	// Install alpha, then beta (same user scope).
	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "alpha",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)
	_, err = a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "beta",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)

	alphaDir := filepath.Join(pluginsDir, "alpha")
	betaDir := filepath.Join(pluginsDir, "beta")
	require.DirExists(t, alphaDir)
	require.DirExists(t, betaDir)

	// Non-LIFO: beta was installed second but removed first.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "beta", Scope: plugins.ScopeUser}))

	// The shared manifest must still list alpha (with its ./alpha source) and no
	// longer list beta.
	mp := readClaudeMarketplaceManifest(t, pluginsDir)
	requireMarketplacePlugin(t, mp, "alpha")
	for _, p := range mp.Plugins {
		assert.NotEqual(t, "beta", p.Name, "beta removed from manifest")
	}

	settings := readSettings(t, userSettingsPath(tempHome))

	// alpha@toolhive must still be enabled.
	enabled := settings["enabledPlugins"].(map[string]any)
	assert.Equal(t, true, enabled["alpha@toolhive"], "alpha still enabled after non-LIFO uninstall of beta")

	// The toolhive marketplace entry must still exist because alpha is enabled,
	// and its path must point at the plugins parent directory, which still
	// exists on disk (not at beta's now-deleted directory).
	requireMarketplaceEntry(t, settings, alphaDir)

	marketplaces := settings["extraKnownMarketplaces"].(map[string]any)
	tv := marketplaces["toolhive"].(map[string]any)
	src := tv["source"].(map[string]any)
	mpPath, _ := src["path"].(string)
	assert.Equal(t, pluginsDir, mpPath, "marketplace path is the plugins parent dir")
	_, err = os.Stat(mpPath)
	assert.NoError(t, err, "marketplace path directory still exists on disk")

	// beta's directory is gone, alpha's remains.
	assert.NoDirExists(t, betaDir, "beta directory removed")
	assert.DirExists(t, alphaDir, "alpha directory still present")
}

func TestClaudeCodeAdapter_ProjectScopeUsesProjectSettings(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	projectRoot := resolvedTempDir(t)
	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "commands/greet.md", Content: []byte("# greet"), Mode: 0644},
	})

	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:        "proj-plugin",
		LayerData:   layer,
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Components:  plugins.ComponentInventory{"commands": 1},
	})
	require.NoError(t, err)

	wantDir := filepath.Join(projectRoot, ".claude", "plugins", "proj-plugin")
	assert.Equal(t, wantDir, res.InstallPath)

	// The settings file written must be the project one, not the user one.
	projectSettings := filepath.Join(projectRoot, ".claude", "settings.json")
	_, err = os.Stat(projectSettings)
	require.NoError(t, err, "project settings.json must exist")
	_, err = os.Stat(userSettingsPath(tempHome))
	assert.True(t, os.IsNotExist(err), "user settings.json must not be created for project scope")

	settings := readSettings(t, projectSettings)
	enabled := settings["enabledPlugins"].(map[string]any)
	assert.Equal(t, true, enabled["proj-plugin@toolhive"])
	requireMarketplaceEntry(t, settings, wantDir)

	// Dematerialize reverts the project settings.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{
		Name:        "proj-plugin",
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
	}))
	settings = readSettings(t, projectSettings)
	_, hasMarketplaces := settings["extraKnownMarketplaces"]
	assert.False(t, hasMarketplaces, "project marketplace entry removed")
	_, hasEnabled := settings["enabledPlugins"]
	assert.False(t, hasEnabled, "project enabledPlugins removed when empty")
}

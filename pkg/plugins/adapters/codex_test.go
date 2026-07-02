// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// seedCodexConfig writes an initial ~/.codex/config.toml containing unrelated
// tables that must survive plugin materialize/dematerialize.
func seedCodexConfig(t *testing.T, tempHome string) {
	t.Helper()
	cfgDir := filepath.Join(tempHome, ".codex")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	seed := `[mcp_servers.bar]
command = "echo"

[other]
key = "value"
`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(seed), 0o600))
}

func readCodexConfig(t *testing.T, tempHome string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tempHome, ".codex", "config.toml"))
	require.NoError(t, err)
	return string(b)
}

// codexMarketplaceFile returns the shared marketplace.json path under tempHome.
func codexMarketplaceFile(tempHome string) string {
	return filepath.Join(tempHome, ".codex", "plugins", "marketplace.json")
}

// codexCachePath returns the Codex cache dir for a user-scope plugin:
// ~/.codex/plugins/cache/toolhive/<name>/local.
func codexCachePath(tempHome, name string) string {
	return filepath.Join(tempHome, ".codex", "plugins", "cache", "toolhive", name, "local")
}

// readCodexMarketplaceFile parses the shared marketplace.json; fails the test
// if it is absent.
func readCodexMarketplaceFile(t *testing.T, tempHome string) codexMarketplace {
	t.Helper()
	path := codexMarketplaceFile(tempHome)
	b, err := os.ReadFile(path)
	require.NoError(t, err, "marketplace.json should exist")
	var mp codexMarketplace
	require.NoError(t, json.Unmarshal(b, &mp))
	return mp
}

// findCodexPlugin returns the marketplace entry for name, or fails the test.
func findCodexPlugin(t *testing.T, mp codexMarketplace, name string) codexMarketplacePlugin {
	t.Helper()
	assert.Equal(t, "toolhive", mp.Name, "marketplace name")
	for _, p := range mp.Plugins {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("plugin %q not found in marketplace %+v", name, mp.Plugins)
	return codexMarketplacePlugin{}
}

func TestCodexAdapter_SupportedComponents(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	got := a.SupportedComponents()
	assert.Equal(t, []plugins.ComponentType{
		plugins.ComponentSkills,
		plugins.ComponentMCP,
		plugins.ComponentHooks,
	}, got)
}

func TestCodexAdapter_MaterializeAddsPluginTableAndPreservesUnrelatedTables(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	seedCodexConfig(t, tempHome)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/useful/SKILL.md", Content: []byte("# useful"), Mode: 0644},
	})

	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "foo",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"skills": 1},
	})
	require.NoError(t, err)

	// Cache dir follows the Codex layout: ~/.codex/plugins/cache/toolhive/foo/local.
	wantCache := codexCachePath(tempHome, "foo")
	assert.Equal(t, wantCache, res.InstallPath)
	_, err = os.Stat(filepath.Join(wantCache, "skills", "useful", "SKILL.md"))
	require.NoError(t, err)

	// config.toml must now contain [plugins.'foo@toolhive'] with enabled = true
	// (no invented `path` key), and the unrelated tables must survive.
	content := readCodexConfig(t, tempHome)
	assert.Contains(t, content, `[plugins.'foo@toolhive']`)
	assert.Contains(t, content, "enabled = true")
	assert.NotContains(t, content, wantCache)
	assert.Contains(t, content, "[mcp_servers.bar]")
	assert.Contains(t, content, "echo")
	assert.Contains(t, content, "[other]")
	assert.Contains(t, content, "value")

	// The shared marketplace.json must exist with the toolhive marketplace and a
	// plugin entry whose local source points (relative to the marketplace root)
	// at the plugin's cache dir, with a policy and category.
	mp := readCodexMarketplaceFile(t, tempHome)
	p := findCodexPlugin(t, mp, "foo")
	assert.Equal(t, "local", p.Source.Source)
	assert.Equal(t, "./cache/toolhive/foo/local", p.Source.Path, "relative source path inside the marketplace root")
	assert.Equal(t, codexPolicyInstallation, p.Policy.Installation)
	assert.Equal(t, codexPolicyAuthentication, p.Policy.Authentication)
	assert.NotEmpty(t, p.Category, "category is a required field")
}

func TestCodexAdapter_DematerializeRemovesPluginTableAndPreservesUnrelatedTables(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	seedCodexConfig(t, tempHome)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/useful/SKILL.md", Content: []byte("# useful"), Mode: 0644},
	})

	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "foo",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"skills": 1},
	})
	require.NoError(t, err)

	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "foo", Scope: plugins.ScopeUser}))

	// [plugins.'foo@toolhive'] must be gone; cache dir must be gone.
	content := readCodexConfig(t, tempHome)
	assert.NotContains(t, content, "foo@toolhive")
	assert.NotContains(t, content, "[plugins]")

	// Unrelated tables must STILL survive.
	assert.Contains(t, content, "[mcp_servers.bar]")
	assert.Contains(t, content, "echo")
	assert.Contains(t, content, "[other]")
	assert.Contains(t, content, "value")

	_, err = os.Stat(codexCachePath(tempHome, "foo"))
	assert.True(t, os.IsNotExist(err), "cache dir should be gone after dematerialize")

	// The last toolhive plugin was removed, so the marketplace.json must be
	// deleted.
	_, err = os.Stat(codexMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "marketplace.json should be deleted after removing the last toolhive plugin")
}

func TestCodexAdapter_DroppedComponentsAllSix(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	// Declare all six component types; Codex drops commands, agents, lspServers.
	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:      "drop-all",
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

	got := append([]plugins.ComponentType(nil), res.DroppedComponents...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	assert.Equal(t, []plugins.ComponentType{
		plugins.ComponentAgents,
		plugins.ComponentCommands,
		plugins.ComponentLSP,
	}, got)

	// Clean up so this test leaves no global state behind.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "drop-all", Scope: plugins.ScopeUser}))
}

func TestCodexAdapter_ProjectScopeDegraded(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	// Project scope degrades because config registration is always user-scoped.
	projectRoot := t.TempDir()
	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:        "proj-plugin",
		LayerData:   layer,
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Components:  plugins.ComponentInventory{"skills": 1},
	})
	require.NoError(t, err)
	assert.True(t, res.ProjectScopeDegraded, "project scope must be degraded for codex")
	content := readCodexConfig(t, tempHome)
	assert.Contains(t, content, `[plugins.'proj-plugin@toolhive']`)

	// Clean up so the user-scope config.toml is left empty for the next assertion.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "proj-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot}))

	// User scope is NOT degraded.
	res2, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "user-plugin",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"skills": 1},
	})
	require.NoError(t, err)
	assert.False(t, res2.ProjectScopeDegraded, "user scope must not be degraded for codex")
	content = readCodexConfig(t, tempHome)
	assert.Contains(t, content, `[plugins.'user-plugin@toolhive']`)

	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "user-plugin", Scope: plugins.ScopeUser}))
}

func TestCodexAdapter_DematerializeIdempotent(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	// Dematerializing something never installed is not an error, and does not
	// create a config file or a marketplace.json.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "ghost", Scope: plugins.ScopeUser}))
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "ghost", Scope: plugins.ScopeUser}))

	_, err := os.Stat(filepath.Join(tempHome, ".codex", "config.toml"))
	assert.True(t, os.IsNotExist(err), "no config file should be created by dematerialize")
	_, err = os.Stat(codexMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "no marketplace.json should be created by dematerialize")
}

func TestCodexAdapter_ScopeSupport(t *testing.T) {
	t.Parallel()
	a := &CodexAdapter{}
	ss := a.ScopeSupport()
	assert.True(t, ss.DegradesOnProjectScope)
	assert.NotEmpty(t, ss.Reason)
}

// TestCodexAdapter_NonTablePluginsKeyRejectsInstall seeds config.toml with a
// non-table `plugins` key and asserts Materialize rejects it (wrapping
// errPluginsKeyNotTable) without mangling the original config.
func TestCodexAdapter_NonTablePluginsKeyRejectsInstall(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	seedCodexConfig(t, tempHome)
	// Overwrite config.toml with a `plugins` scalar that is NOT a table.
	cfgDir := filepath.Join(tempHome, ".codex")
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, "config.toml"),
		[]byte(`plugins = "not-a-table"`+"\n"),
		0o600,
	))
	original, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
	require.NoError(t, err)

	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	_, err = a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "foo",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"skills": 1},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errPluginsKeyNotTable), "error should wrap errPluginsKeyNotTable, got: %v", err)

	// Original config must be untouched (reject before write).
	after, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after))

	// No marketplace.json should have been created (marketplace write happens
	// after a successful config enable).
	_, err = os.Stat(codexMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "no marketplace.json should be created on rejected install")
}

// TestCodexAdapter_MarketplaceSurvivesWhenOtherToolhivePluginRemains installs
// two plugins, removes one, and asserts the marketplace.json survives; removing
// the second deletes it.
func TestCodexAdapter_MarketplaceSurvivesWhenOtherToolhivePluginRemains(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	seedCodexConfig(t, tempHome)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	require.NoError(t, materializeCodex(a, "alpha", layer))
	require.NoError(t, materializeCodex(a, "beta", layer))

	// Remove alpha: marketplace.json survives because beta is still enabled.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "alpha", Scope: plugins.ScopeUser}))
	_, err := os.Stat(codexMarketplaceFile(tempHome))
	require.NoError(t, err, "marketplace.json should survive while beta remains")
	content := readCodexConfig(t, tempHome)
	assert.NotContains(t, content, "alpha@toolhive")
	assert.Contains(t, content, `[plugins.'beta@toolhive']`)

	// Remove beta: marketplace.json is deleted (no toolhive plugins left).
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "beta", Scope: plugins.ScopeUser}))
	_, err = os.Stat(codexMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "marketplace.json should be deleted when no toolhive plugins remain")
}

// TestCodexAdapter_NonLifoUninstallKeepsMarketplaceValid installs alpha then
// beta, dematerializes beta (non-LIFO), and asserts alpha remains enabled, the
// marketplace.json still exists, and alpha's cache dir is intact.
func TestCodexAdapter_NonLifoUninstallKeepsMarketplaceValid(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	seedCodexConfig(t, tempHome)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	require.NoError(t, materializeCodex(a, "alpha", layer))
	require.NoError(t, materializeCodex(a, "beta", layer))

	alphaDir := codexCachePath(tempHome, "alpha")
	betaDir := codexCachePath(tempHome, "beta")
	require.DirExists(t, alphaDir)
	require.DirExists(t, betaDir)

	// Non-LIFO: beta was installed second but removed first.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "beta", Scope: plugins.ScopeUser}))

	// alpha@toolhive must still be enabled.
	content := readCodexConfig(t, tempHome)
	assert.Contains(t, content, `[plugins.'alpha@toolhive']`)
	assert.NotContains(t, content, "beta@toolhive")

	// marketplace.json still exists and lists alpha (with its relative source
	// path) but no longer lists beta.
	mp := readCodexMarketplaceFile(t, tempHome)
	alpha := findCodexPlugin(t, mp, "alpha")
	assert.Equal(t, "./cache/toolhive/alpha/local", alpha.Source.Path)
	for _, p := range mp.Plugins {
		assert.NotEqual(t, "beta", p.Name, "beta removed from marketplace")
	}

	// alpha's cache dir survives; beta's is gone.
	assert.DirExists(t, alphaDir, "alpha directory still present")
	assert.NoDirExists(t, betaDir, "beta directory removed")
}

// materializeCodex is a small helper to install a named user-scope plugin.
func materializeCodex(a *CodexAdapter, name string, layer []byte) error {
	_, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       name,
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"skills": 1},
	})
	return err
}

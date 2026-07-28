// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// codexMarketplaceFile returns the shared marketplace.json path for user scope:
// ~/.agents/plugins/marketplace.json.
func codexUserMarketplaceFile(tempHome string) string {
	return filepath.Join(tempHome, ".agents", "plugins", "marketplace.json")
}

// codexPluginDir returns the plugin source dir for a user-scope plugin:
// ~/.agents/plugins/toolhive/<name>.
func codexPluginDir(tempHome, name string) string {
	return filepath.Join(tempHome, ".agents", "plugins", "toolhive", name)
}

// readCodexMarketplaceFile parses the marketplace.json at path; fails the test
// if it is absent.
func readCodexMarketplaceFileAt(t *testing.T, path string) codexMarketplace {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err, "marketplace.json should exist at %s", path)
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
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	got := a.SupportedComponents()
	assert.Equal(t, []plugins.ComponentType{
		plugins.ComponentSkills,
		plugins.ComponentMCP,
		plugins.ComponentHooks,
	}, got)
}

func TestCodexAdapter_MaterializeExtractsAndRegisters(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
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

	// Plugin source is extracted under the marketplace root:
	// ~/.agents/plugins/toolhive/foo.
	wantDir := codexPluginDir(tempHome, "foo")
	assert.Equal(t, wantDir, res.InstallPath)
	_, err = os.Stat(filepath.Join(wantDir, "skills", "useful", "SKILL.md"))
	require.NoError(t, err)
	assert.False(t, res.ProjectScopeDegraded, "user scope must not be degraded")

	// The adapter must NOT touch ~/.codex/config.toml.
	_, err = os.Stat(filepath.Join(tempHome, ".codex", "config.toml"))
	assert.True(t, os.IsNotExist(err), "config.toml must not be created")

	// The shared marketplace.json must list the plugin with a relative source,
	// policy, and category.
	mp := readCodexMarketplaceFileAt(t, codexUserMarketplaceFile(tempHome))
	p := findCodexPlugin(t, mp, "foo")
	assert.Equal(t, "local", p.Source.Source)
	assert.Equal(t, "./toolhive/foo", p.Source.Path, "relative source path inside the marketplace root")
	assert.Equal(t, codexPolicyInstallation, p.Policy.Installation)
	assert.Equal(t, codexPolicyAuthentication, p.Policy.Authentication)
	assert.NotEmpty(t, p.Category, "category is a required field")
}

func TestCodexAdapter_DematerializeRemovesPluginAndEntry(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
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

	// Plugin source dir must be gone.
	_, err = os.Stat(codexPluginDir(tempHome, "foo"))
	assert.True(t, os.IsNotExist(err), "plugin dir should be gone after dematerialize")

	// The last plugin was removed, so the marketplace.json must be deleted.
	_, err = os.Stat(codexUserMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "marketplace.json should be deleted after removing the last plugin")
}

func TestCodexAdapter_DroppedComponentsAllSix(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
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

	// Clean up so this test leaves no state behind.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "drop-all", Scope: plugins.ScopeUser}))
}

func TestCodexAdapter_ProjectScopeUsesProjectMarketplace(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	// Project scope writes to <projectRoot>/.agents/plugins, a Codex discovery
	// path, so it does NOT degrade.
	projectRoot := resolvedTempDir(t)
	res, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:        "proj-plugin",
		LayerData:   layer,
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Components:  plugins.ComponentInventory{"skills": 1},
	})
	require.NoError(t, err)
	assert.False(t, res.ProjectScopeDegraded, "project scope must not be degraded for codex")

	wantDir := filepath.Join(projectRoot, ".agents", "plugins", "toolhive", "proj-plugin")
	assert.Equal(t, wantDir, res.InstallPath)

	// The marketplace must be the project one, not the user one.
	projMarketplace := filepath.Join(projectRoot, ".agents", "plugins", "marketplace.json")
	mp := readCodexMarketplaceFileAt(t, projMarketplace)
	findCodexPlugin(t, mp, "proj-plugin")
	_, err = os.Stat(codexUserMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "user marketplace.json must not be created for project scope")

	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "proj-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot}))
}

func TestCodexAdapter_DematerializeIdempotent(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	// Dematerializing something never installed is not an error, and does not
	// create a marketplace.json.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "ghost", Scope: plugins.ScopeUser}))
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "ghost", Scope: plugins.ScopeUser}))

	_, err := os.Stat(codexUserMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "no marketplace.json should be created by dematerialize")
}

func TestCodexAdapter_ScopeSupport(t *testing.T) {
	t.Parallel()
	a := &CodexAdapter{}
	ss := a.ScopeSupport()
	assert.False(t, ss.DegradesOnProjectScope)
	assert.Empty(t, ss.Reason)
}

// TestCodexAdapter_MarketplaceSurvivesWhenOtherPluginRemains installs two
// plugins, removes one, and asserts the marketplace.json survives; removing the
// second deletes it.
func TestCodexAdapter_MarketplaceSurvivesWhenOtherPluginRemains(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	require.NoError(t, materializeCodex(a, "alpha", layer))
	require.NoError(t, materializeCodex(a, "beta", layer))

	// Remove alpha: marketplace.json survives because beta remains.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "alpha", Scope: plugins.ScopeUser}))
	mp := readCodexMarketplaceFileAt(t, codexUserMarketplaceFile(tempHome))
	findCodexPlugin(t, mp, "beta")
	for _, p := range mp.Plugins {
		assert.NotEqual(t, "alpha", p.Name, "alpha removed from marketplace")
	}

	// Remove beta: marketplace.json is deleted (no plugins left).
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "beta", Scope: plugins.ScopeUser}))
	_, err := os.Stat(codexUserMarketplaceFile(tempHome))
	assert.True(t, os.IsNotExist(err), "marketplace.json should be deleted when no plugins remain")
}

// TestCodexAdapter_NonLifoUninstallKeepsMarketplaceValid installs alpha then
// beta, dematerializes beta (non-LIFO), and asserts alpha remains registered
// with its relative source and alpha's dir is intact.
func TestCodexAdapter_NonLifoUninstallKeepsMarketplaceValid(t *testing.T) {
	t.Parallel()
	tempHome := resolvedTempDir(t)
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	layer := makePluginLayer(t, []ociskills.FileEntry{
		{Path: "skills/x/SKILL.md", Content: []byte("x"), Mode: 0644},
	})

	require.NoError(t, materializeCodex(a, "alpha", layer))
	require.NoError(t, materializeCodex(a, "beta", layer))

	alphaDir := codexPluginDir(tempHome, "alpha")
	betaDir := codexPluginDir(tempHome, "beta")
	require.DirExists(t, alphaDir)
	require.DirExists(t, betaDir)

	// Non-LIFO: beta was installed second but removed first.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "beta", Scope: plugins.ScopeUser}))

	mp := readCodexMarketplaceFileAt(t, codexUserMarketplaceFile(tempHome))
	alpha := findCodexPlugin(t, mp, "alpha")
	assert.Equal(t, "./toolhive/alpha", alpha.Source.Path)
	for _, p := range mp.Plugins {
		assert.NotEqual(t, "beta", p.Name, "beta removed from marketplace")
	}

	// alpha's dir survives; beta's is gone.
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

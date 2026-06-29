// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
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
	require.NoError(t, os.MkdirAll(cfgDir, 0700))
	seed := `[mcp_servers.bar]
command = "echo"

[other]
key = "value"
`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(seed), 0600))
}

func readCodexConfig(t *testing.T, tempHome string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tempHome, ".codex", "config.toml"))
	require.NoError(t, err)
	return string(b)
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

	// Cache dir is ~/.codex/plugins/cache/foo.
	wantCache := filepath.Join(tempHome, ".codex", "plugins", "cache", "foo")
	assert.Equal(t, wantCache, res.InstallPath)
	_, err = os.Stat(filepath.Join(wantCache, "skills", "useful", "SKILL.md"))
	require.NoError(t, err)

	// config.toml must now contain [plugins.foo] with the cache path, AND the
	// unrelated [mcp_servers.bar] and [other] tables must survive. (go-toml/v2
	// emits strings with single quotes, so check for the bare values.)
	content := readCodexConfig(t, tempHome)
	assert.Contains(t, content, "[plugins.foo]")
	assert.Contains(t, content, wantCache)
	assert.Contains(t, content, "[mcp_servers.bar]")
	assert.Contains(t, content, "echo")
	assert.Contains(t, content, "[other]")
	assert.Contains(t, content, "value")
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

	require.NoError(t, a.Dematerialize(context.Background(), "foo", plugins.ScopeUser, ""))

	// [plugins.foo] must be gone; cache dir must be gone.
	content := readCodexConfig(t, tempHome)
	assert.NotContains(t, content, "[plugins.foo]")
	assert.NotContains(t, content, "[plugins]")

	// Unrelated tables must STILL survive.
	assert.Contains(t, content, "[mcp_servers.bar]")
	assert.Contains(t, content, "echo")
	assert.Contains(t, content, "[other]")
	assert.Contains(t, content, "value")

	_, err = os.Stat(filepath.Join(tempHome, ".codex", "plugins", "cache", "foo"))
	assert.True(t, os.IsNotExist(err), "cache dir should be gone after dematerialize")
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

	// Clean up so the user-scope config.toml is left empty for the next assertion.
	require.NoError(t, a.Dematerialize(context.Background(), "proj-plugin", plugins.ScopeProject, projectRoot))

	// User scope is NOT degraded.
	res2, err := a.Materialize(context.Background(), plugins.MaterializeRequest{
		Name:       "user-plugin",
		LayerData:  layer,
		Scope:      plugins.ScopeUser,
		Components: plugins.ComponentInventory{"skills": 1},
	})
	require.NoError(t, err)
	assert.False(t, res2.ProjectScopeDegraded, "user scope must not be degraded for codex")

	require.NoError(t, a.Dematerialize(context.Background(), "user-plugin", plugins.ScopeUser, ""))
}

func TestCodexAdapter_DematerializeIdempotent(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	cm := newTestClientManager(t, tempHome)
	a := NewCodexAdapter(cm)

	// Dematerializing something never installed is not an error, and does not
	// create a config file.
	require.NoError(t, a.Dematerialize(context.Background(), "ghost", plugins.ScopeUser, ""))
	require.NoError(t, a.Dematerialize(context.Background(), "ghost", plugins.ScopeUser, ""))

	_, err := os.Stat(filepath.Join(tempHome, ".codex", "config.toml"))
	assert.True(t, os.IsNotExist(err), "no config file should be created by dematerialize")
}

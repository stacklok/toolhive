// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/plugins"
)

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

func TestClaudeCodeAdapter_SupportedComponents(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
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
	tempHome := t.TempDir()
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
}

func TestClaudeCodeAdapter_DroppedComponents(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
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
	tempHome := t.TempDir()
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
}

func TestClaudeCodeAdapter_DematerializeIdempotent(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	cm := newTestClientManager(t, tempHome)
	a := NewClaudeCodeAdapter(cm)

	// Dematerializing something that was never installed is not an error.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "never-there", Scope: plugins.ScopeUser}))
	// A second dematerialize is also fine.
	require.NoError(t, a.Dematerialize(context.Background(), plugins.DematerializeRequest{Name: "never-there", Scope: plugins.ScopeUser}))
}

func TestClaudeCodeAdapter_RematerializeOverwrites(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
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
}

func TestClaudeCodeAdapter_ScopeSupport(t *testing.T) {
	t.Parallel()
	a := &ClaudeCodeAdapter{}
	ss := a.ScopeSupport()
	assert.False(t, ss.DegradesOnProjectScope)
	assert.Empty(t, ss.Reason)
}

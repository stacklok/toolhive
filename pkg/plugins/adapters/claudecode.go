// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package adapters contains MaterializationAdapter implementations for
// each MCP client that supports plugins.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// ClaudeCodeAdapter materializes plugins into Claude Code's
// ~/.claude/plugins/<name> directory and registers them in Claude Code's
// settings.json so the plugin is actually loaded. Claude Code loads a plugin via
// a marketplace: a single shared marketplace.json at the plugins root
// (~/.claude/plugins/.claude-plugin/marketplace.json) lists every ToolHive
// plugin, and settings.json carries an extraKnownMarketplaces entry (a
// "directory" source pointing at that root) plus an enabledPlugins entry.
//
// The marketplace manifest and settings source type follow the Claude Code
// plugin marketplace schema: the manifest has top-level name/owner/plugins, each
// plugin's source is a "./<name>" relative path resolved against the marketplace
// root, and the extraKnownMarketplaces source type is "directory".
// See https://code.claude.com/docs/en/plugin-marketplaces.
type ClaudeCodeAdapter struct {
	cm        *client.ClientManager
	installer skills.Installer
}

// NewClaudeCodeAdapter returns a ClaudeCodeAdapter backed by the given
// ClientManager and a production skills.Installer.
func NewClaudeCodeAdapter(cm *client.ClientManager) *ClaudeCodeAdapter {
	return &ClaudeCodeAdapter{
		cm:        cm,
		installer: skills.NewInstaller(),
	}
}

var claudeCodeSupported = []plugins.ComponentType{
	plugins.ComponentCommands,
	plugins.ComponentAgents,
	plugins.ComponentSkills,
	plugins.ComponentHooks,
}

// marketplaceName is the marketplace key under which ToolHive-installed
// plugins are declared, both in the shared marketplace.json manifest and in
// settings.json's extraKnownMarketplaces object.
const marketplaceName = "toolhive"

// Materialize extracts the plugin layer into the Claude Code plugins directory
// and registers it in settings.json so Claude Code loads the plugin.
func (a *ClaudeCodeAdapter) Materialize(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	dir, err := a.cm.GetPluginPath(client.ClaudeCode, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving plugin path: %w", err)
	}

	if _, err := a.installer.ExtractPlugin(req.LayerData, dir, true); err != nil {
		return nil, fmt.Errorf("extracting plugin: %w", err)
	}

	// The marketplace root is the plugins parent directory; each plugin lives in
	// a "<name>" subdirectory referenced as a "./<name>" source.
	marketplaceRoot := filepath.Dir(dir)

	// Upsert the plugin into the shared marketplace.json at the plugins root so
	// Claude Code resolves it under the "toolhive" marketplace.
	if err := upsertClaudeMarketplace(marketplaceRoot, req.Name); err != nil {
		return nil, fmt.Errorf("writing marketplace.json: %w", err)
	}

	// Patch settings.json to enable the plugin under the toolhive marketplace.
	settingsPath := a.settingsPath(req.Scope, req.ProjectRoot)
	if err := enablePluginInSettings(settingsPath, req.Name, marketplaceRoot); err != nil {
		return nil, fmt.Errorf("enabling plugin in settings.json: %w", err)
	}

	return &plugins.MaterializeResult{
		InstalledComponents:  claudeCodeSupported,
		DroppedComponents:    droppedComponents(req.Components, claudeCodeSupported),
		InstallPath:          dir,
		ProjectScopeDegraded: false,
	}, nil
}

// Dematerialize removes the plugin directory, cleans up empty parents, and
// reverts the settings.json mutations made during Materialize.
func (a *ClaudeCodeAdapter) Dematerialize(_ context.Context, req plugins.DematerializeRequest) error {
	dir, err := a.cm.GetPluginPath(client.ClaudeCode, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolving plugin path: %w", err)
	}

	if err := a.installer.Remove(dir); err != nil {
		return fmt.Errorf("removing plugin directory: %w", err)
	}

	// Remove the plugin from the shared marketplace.json (idempotent). Done
	// before the empty-parent cleanup so that, once the marketplace file and its
	// .claude-plugin directory are gone, cleanup can reclaim the now-empty
	// plugins root too.
	marketplaceRoot := filepath.Dir(dir)
	if err := removeClaudeMarketplace(marketplaceRoot, req.Name); err != nil {
		return fmt.Errorf("removing plugin from marketplace.json: %w", err)
	}

	// Best-effort empty-parent cleanup.
	cleanupAfterRemove(dir, req.Scope, req.ProjectRoot, a.cm.HomeDir())

	// Revert the settings.json mutations (idempotent).
	settingsPath := a.settingsPath(req.Scope, req.ProjectRoot)
	if err := disablePluginInSettings(settingsPath, req.Name); err != nil {
		return fmt.Errorf("disabling plugin in settings.json: %w", err)
	}

	return nil
}

// SupportedComponents returns the component types Claude Code loads.
func (*ClaudeCodeAdapter) SupportedComponents() []plugins.ComponentType {
	return claudeCodeSupported
}

// ScopeSupport returns false for Claude Code: it supports both user and
// project plugin directories, so a project-scoped install lands in the project
// directory without degradation.
func (*ClaudeCodeAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{DegradesOnProjectScope: false}
}

// --- settings.json mutation helpers ---

// settingsPath returns the absolute path to the Claude Code settings.json for
// the given scope: user scope uses <home>/.claude/settings.json, project scope
// uses <projectRoot>/.claude/settings.json.
func (a *ClaudeCodeAdapter) settingsPath(scope plugins.Scope, projectRoot string) string {
	if scope == plugins.ScopeProject && projectRoot != "" {
		return filepath.Join(projectRoot, ".claude", "settings.json")
	}
	return filepath.Join(a.cm.HomeDir(), ".claude", "settings.json")
}

// claudeMarketplace is the Claude Code marketplace manifest schema
// (.claude-plugin/marketplace.json): a top-level name, owner, and a list of
// plugin entries. See https://code.claude.com/docs/en/plugin-marketplaces.
type claudeMarketplace struct {
	Name    string                    `json:"name"`
	Owner   claudeMarketplaceOwner    `json:"owner"`
	Plugins []claudeMarketplacePlugin `json:"plugins"`
}

// claudeMarketplaceOwner identifies the marketplace maintainer. Claude Code
// requires the owner object with a non-empty name.
type claudeMarketplaceOwner struct {
	Name string `json:"name"`
}

// claudeMarketplacePlugin is a single plugin entry. Source is a "./<name>"
// relative path resolved against the marketplace root (the plugins parent dir).
type claudeMarketplacePlugin struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// claudeMarketplaceOwnerName is the owner name recorded in the manifest.
const claudeMarketplaceOwnerName = "ToolHive"

// claudeMarketplaceFilePath returns the shared marketplace manifest path for a
// marketplace rooted at marketplaceRoot: <root>/.claude-plugin/marketplace.json.
func claudeMarketplaceFilePath(marketplaceRoot string) string {
	return filepath.Join(marketplaceRoot, ".claude-plugin", "marketplace.json")
}

// upsertClaudeMarketplace adds (or updates) the plugin entry in the shared
// marketplace.json at <marketplaceRoot>/.claude-plugin/marketplace.json under a
// file lock. The manifest lists every ToolHive plugin with a "./<name>" source
// resolved against marketplaceRoot, so a single "directory" marketplace serves
// all installed plugins regardless of install/uninstall order.
func upsertClaudeMarketplace(marketplaceRoot, pluginName string) error {
	marketplaceFile := claudeMarketplaceFilePath(marketplaceRoot)
	if err := os.MkdirAll(filepath.Dir(marketplaceFile), 0o700); err != nil {
		return fmt.Errorf("creating marketplace dir: %w", err)
	}
	return fileutils.WithFileLock(marketplaceFile, func() error {
		mp, err := readClaudeMarketplace(marketplaceFile)
		if err != nil {
			return err
		}
		entry := claudeMarketplacePlugin{Name: pluginName, Source: "./" + pluginName}
		replaced := false
		for i := range mp.Plugins {
			if mp.Plugins[i].Name == pluginName {
				mp.Plugins[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			mp.Plugins = append(mp.Plugins, entry)
		}
		sortClaudeMarketplacePlugins(mp.Plugins)
		return writeClaudeMarketplace(marketplaceFile, mp)
	})
}

// removeClaudeMarketplace removes the plugin entry from the shared
// marketplace.json under a file lock. When no plugins remain, the manifest file
// and its (now-empty) .claude-plugin directory are removed. Idempotent: a
// missing file or missing entry is not an error.
func removeClaudeMarketplace(marketplaceRoot, pluginName string) error {
	marketplaceFile := claudeMarketplaceFilePath(marketplaceRoot)
	if _, err := os.Stat(marketplaceFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking marketplace file: %w", err)
	}
	return fileutils.WithFileLock(marketplaceFile, func() error {
		mp, err := readClaudeMarketplace(marketplaceFile)
		if err != nil {
			return err
		}
		filtered := mp.Plugins[:0]
		for _, p := range mp.Plugins {
			if p.Name != pluginName {
				filtered = append(filtered, p)
			}
		}
		mp.Plugins = filtered
		if len(mp.Plugins) == 0 {
			if err := os.Remove(marketplaceFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("removing marketplace.json: %w", err)
			}
			// Best-effort removal of the now-empty .claude-plugin directory.
			_ = os.Remove(filepath.Dir(marketplaceFile))
			return nil
		}
		return writeClaudeMarketplace(marketplaceFile, mp)
	})
}

// readClaudeMarketplace reads and parses the marketplace manifest, returning a
// freshly-initialized manifest (with name/owner set) when the file is missing
// or empty.
func readClaudeMarketplace(path string) (*claudeMarketplace, error) {
	mp := &claudeMarketplace{
		Name:  marketplaceName,
		Owner: claudeMarketplaceOwner{Name: claudeMarketplaceOwnerName},
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is a known tool config file location
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mp, nil
		}
		return nil, fmt.Errorf("reading marketplace.json: %w", err)
	}
	if len(data) == 0 {
		return mp, nil
	}
	if err := json.Unmarshal(data, mp); err != nil {
		return nil, fmt.Errorf("parsing marketplace.json: %w", err)
	}
	// Ensure required identity fields are set even if a hand-edited file omitted
	// them, so the rewritten manifest stays schema-valid.
	if mp.Name == "" {
		mp.Name = marketplaceName
	}
	if mp.Owner.Name == "" {
		mp.Owner.Name = claudeMarketplaceOwnerName
	}
	return mp, nil
}

// writeClaudeMarketplace marshals and atomically writes the marketplace manifest.
func writeClaudeMarketplace(path string, mp *claudeMarketplace) error {
	data, err := json.MarshalIndent(mp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling marketplace.json: %w", err)
	}
	return fileutils.AtomicWriteFile(path, data, 0o600)
}

// sortClaudeMarketplacePlugins orders entries by name for a stable manifest.
func sortClaudeMarketplacePlugins(ps []claudeMarketplacePlugin) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
}

// enablePluginInSettings patches settings.json under a file lock to add:
//   - extraKnownMarketplaces.toolhive = {"source": {"source":"directory","path":<marketplaceRoot>}}
//   - enabledPlugins["<name>@toolhive"] = true
//
// The marketplace source is a "directory" source pointing at the plugins root
// (the directory containing all installed plugins and the shared
// .claude-plugin/marketplace.json), matching the Claude Code settings schema.
// The toolhive marketplace key is shared across every ToolHive-installed plugin,
// so pointing it at the stable root keeps it valid regardless of
// install/uninstall order.
//
// Unrelated keys are preserved. Comments are dropped on write (hujson
// standardize→mutate→re-parse→format), matching the pattern used by
// pkg/client/llm_gateway.go.
func enablePluginInSettings(settingsPath, pluginName, marketplaceRoot string) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return fmt.Errorf("creating settings dir: %w", err)
	}
	return fileutils.WithFileLock(settingsPath, func() error {
		content, err := readOrInitSettings(settingsPath)
		if err != nil {
			return err
		}
		root, err := parseSettings(content, settingsPath)
		if err != nil {
			return err
		}

		// extraKnownMarketplaces.toolhive is a "directory" source pointing at the
		// plugins root so the shared marketplace key stays valid regardless of
		// install/uninstall order across multiple plugins.
		marketplaces := ensureObject(root, "extraKnownMarketplaces")
		marketplaces[marketplaceName] = map[string]any{
			"source": map[string]any{
				"source": "directory",
				"path":   marketplaceRoot,
			},
		}

		// enabledPlugins["<name>@toolhive"] = true
		enabled := ensureObject(root, "enabledPlugins")
		enabled[pluginKey(pluginName)] = true

		return writeSettings(root, settingsPath)
	})
}

// disablePluginInSettings patches settings.json under a file lock to remove:
//   - enabledPlugins["<name>@toolhive"]
//   - extraKnownMarketplaces.toolhive, when no remaining "@toolhive" entries
//     are enabled.
//
// Idempotent: a missing settings file or missing entry is not an error.
func disablePluginInSettings(settingsPath, pluginName string) error {
	// Short-circuit when the settings file doesn't exist so we don't fail
	// trying to acquire a lock on a non-existent path's parent directory.
	if _, err := os.Stat(settingsPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking settings file: %w", err)
	}
	return fileutils.WithFileLock(settingsPath, func() error {
		content, err := os.ReadFile(settingsPath) // #nosec G304 -- path is a known tool config file location
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("reading %s: %w", settingsPath, err)
		}
		if len(content) == 0 {
			return nil
		}
		root, err := parseSettings(content, settingsPath)
		if err != nil {
			return err
		}

		// Remove the enabledPlugins entry.
		if enabled, ok := root["enabledPlugins"].(map[string]any); ok {
			delete(enabled, pluginKey(pluginName))
			if len(enabled) == 0 {
				delete(root, "enabledPlugins")
			}
		}

		// Remove the marketplace entry when no remaining @toolhive plugins
		// are enabled.
		if !hasEnabledToolhivePlugin(root) {
			if marketplaces, ok := root["extraKnownMarketplaces"].(map[string]any); ok {
				delete(marketplaces, marketplaceName)
				if len(marketplaces) == 0 {
					delete(root, "extraKnownMarketplaces")
				}
			}
		}

		return writeSettings(root, settingsPath)
	})
}

// pluginKey returns the enabledPlugins key for a plugin under the toolhive
// marketplace: "<name>@toolhive".
func pluginKey(pluginName string) string {
	return pluginName + "@" + marketplaceName
}

// hasEnabledToolhivePlugin reports whether any enabledPlugins key ends with
// "@toolhive".
func hasEnabledToolhivePlugin(root map[string]any) bool {
	enabled, ok := root["enabledPlugins"].(map[string]any)
	if !ok {
		return false
	}
	suffix := "@" + marketplaceName
	for k := range enabled {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// ensureObject returns the object at root[key], creating it as an empty object
// when missing or of the wrong type.
func ensureObject(root map[string]any, key string) map[string]any {
	if existing, ok := root[key].(map[string]any); ok {
		return existing
	}
	m := make(map[string]any)
	root[key] = m
	return m
}

// readOrInitSettings reads settingsPath, returning {} when missing or empty.
func readOrInitSettings(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is a known tool config file location
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("{}"), nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return []byte("{}"), nil
	}
	return data, nil
}

// parseSettings parses hujson/JSONC content into a generic map. hujson is used
// so JSONC (comments, trailing commas) input parses correctly; comments are
// dropped by the subsequent marshal/re-parse cycle in writeSettings.
func parseSettings(content []byte, path string) (map[string]any, error) {
	standardized, err := hujson.Standardize(content)
	if err != nil {
		return nil, fmt.Errorf("standardizing %s: %w", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(standardized, &root); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if root == nil {
		root = make(map[string]any)
	}
	return root, nil
}

// writeSettings marshals root to JSON, re-parses with hujson for formatting,
// and atomically writes it to path.
func writeSettings(root map[string]any, path string) error {
	data, err := json.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	v, err := hujson.Parse(data)
	if err != nil {
		return fmt.Errorf("re-parsing %s: %w", path, err)
	}
	formatted, err := hujson.Format(v.Pack())
	if err != nil {
		return fmt.Errorf("formatting %s: %w", path, err)
	}
	return fileutils.AtomicWriteFile(path, formatted, 0o600)
}

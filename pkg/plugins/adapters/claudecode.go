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
	"strings"

	"github.com/tailscale/hujson"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// ClaudeCodeAdapter materializes plugins into Claude Code's
// ~/.claude/plugins/<name> directory and registers them in Claude Code's
// settings.json so the plugin is actually loaded. Claude Code requires both a
// marketplace.json inside the plugin directory (declaring the plugin under the
// "toolhive" marketplace with a local source) and an enabledPlugins entry plus
// an extraKnownMarketplaces entry in settings.json.
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
// plugins are declared, both in the per-plugin marketplace.json and in
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

	// Write the per-plugin marketplace.json so Claude Code resolves the plugin
	// under the "toolhive" marketplace with a local source pointing at the
	// plugin directory itself.
	if err := writeMarketplaceFile(dir); err != nil {
		return nil, fmt.Errorf("writing marketplace.json: %w", err)
	}

	// Patch settings.json to enable the plugin under the toolhive marketplace.
	settingsPath := a.settingsPath(req.Scope, req.ProjectRoot)
	if err := enablePluginInSettings(settingsPath, req.Name, dir); err != nil {
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

// writeMarketplaceFile writes <cacheDir>/.claude-plugin/marketplace.json
// declaring the plugin under the "toolhive" marketplace with source "./"
// (local, relative to the cache directory).
func writeMarketplaceFile(cacheDir string) error {
	marketplaceDir := filepath.Join(cacheDir, ".claude-plugin")
	if err := os.MkdirAll(marketplaceDir, 0o700); err != nil {
		return fmt.Errorf("creating marketplace dir: %w", err)
	}
	doc := map[string]any{
		marketplaceName: map[string]any{"source": "./"},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshaling marketplace.json: %w", err)
	}
	return fileutils.AtomicWriteFile(filepath.Join(marketplaceDir, "marketplace.json"), data, 0o600)
}

// enablePluginInSettings patches settings.json under a file lock to add:
//   - extraKnownMarketplaces.toolhive = {"source": {"source":"local","path":<pluginsDir>}}
//   - enabledPlugins["<name>@toolhive"] = true
//
// The marketplace path points at the shared plugins parent directory (the
// directory containing all installed plugins), not the per-plugin cacheDir.
// The toolhive marketplace key is shared across every ToolHive-installed
// plugin, so pointing it at a per-plugin directory would let a later install
// (or a non-LIFO uninstall) silently break an earlier plugin by overwriting or
// invalidating the path. Each plugin carries its own marketplace.json with
// source "./" relative to its own directory; this shared entry only tells Claude
// Code the toolhive marketplace exists.
//
// Unrelated keys are preserved. Comments are dropped on write (hujson
// standardize→mutate→re-parse→format), matching the pattern used by
// pkg/client/llm_gateway.go.
func enablePluginInSettings(settingsPath, pluginName, cacheDir string) error {
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

		// extraKnownMarketplaces.toolhive points at the plugins parent dir so
		// the shared marketplace key stays valid regardless of install/uninstall
		// order across multiple plugins.
		pluginsDir := filepath.Dir(cacheDir)
		marketplaces := ensureObject(root, "extraKnownMarketplaces")
		marketplaces[marketplaceName] = map[string]any{
			"source": map[string]any{
				"source": "local",
				"path":   pluginsDir,
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

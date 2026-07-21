// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// CodexAdapter materializes plugins for the OpenAI Codex CLI.
//
// Codex discovers marketplaces at a fixed set of paths, including the personal
// ~/.agents/plugins/marketplace.json and the per-repo
// $REPO_ROOT/.agents/plugins/marketplace.json. ToolHive extracts each plugin's
// source under that marketplace root (namespaced by the "toolhive" marketplace
// name, i.e. <root>/toolhive/<name>) and maintains the marketplace.json so Codex
// can discover it, with a relative "./toolhive/<name>" source per the docs.
//
// Loading the plugin is then a one-time manual step the user runs with the Codex
// CLI (`codex plugin install <name>@toolhive`). ToolHive deliberately does NOT
// shell out to the `codex` binary or write speculative `~/.codex/config.toml`
// enable state: enabling an un-installed plugin doesn't load it, and writing to
// the user's shared config has blast radius for no benefit. The manual step is
// documented in docs/arch/14-plugins-system.md.
//
// See https://developers.openai.com/codex/plugins/build.
type CodexAdapter struct {
	cm        *client.ClientManager
	installer skills.Installer
}

// NewCodexAdapter returns a CodexAdapter backed by the given ClientManager
// and a production skills.Installer.
func NewCodexAdapter(cm *client.ClientManager) *CodexAdapter {
	return &CodexAdapter{
		cm:        cm,
		installer: skills.NewInstaller(),
	}
}

var codexSupported = []plugins.ComponentType{
	plugins.ComponentSkills,
	plugins.ComponentMCP,
	plugins.ComponentHooks,
}

const (
	// codexPluginCategory is the category recorded for ToolHive plugins in the
	// Codex marketplace manifest (a required field).
	codexPluginCategory = "Productivity"
	// codexPolicyInstallation / codexPolicyAuthentication are the marketplace
	// entry policy values: the plugin is available and authenticates on install.
	codexPolicyInstallation   = "AVAILABLE"
	codexPolicyAuthentication = "ON_INSTALL"
)

// codexMarketplaceRoot returns the Codex marketplace root for the given scope:
// the personal ~/.agents/plugins for user scope, or <projectRoot>/.agents/plugins
// for project scope. Both are marketplace-discovery paths Codex reads.
func (a *CodexAdapter) codexMarketplaceRoot(scope plugins.Scope, projectRoot string) string {
	if scope == plugins.ScopeProject && projectRoot != "" {
		return filepath.Join(projectRoot, ".agents", "plugins")
	}
	return filepath.Join(a.cm.HomeDir(), ".agents", "plugins")
}

// codexMarketplaceFile returns the marketplace manifest path for a marketplace
// rooted at root: <root>/marketplace.json.
func codexMarketplaceFile(root string) string {
	return filepath.Join(root, "marketplace.json")
}

// codexSourcePath returns the relative "./toolhive/<name>" source path recorded
// in the manifest for a plugin, resolved against the marketplace root.
func codexSourcePath(name string) string {
	return "./" + filepath.ToSlash(filepath.Join(marketplaceName, name))
}

// Materialize extracts the plugin's source under the Codex marketplace root and
// registers it in the shared marketplace.json. It does not touch config.toml or
// invoke the Codex CLI; the user completes loading with a one-time
// `codex plugin install` (see the package doc).
func (a *CodexAdapter) Materialize(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	// GetPluginPath returns <marketplaceRoot>/toolhive/<name>; it validates the
	// name (no traversal) and resolves the scope-specific root.
	pluginDir, err := a.cm.GetPluginPath(client.Codex, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving plugin path: %w", err)
	}

	if _, err := a.installer.ExtractPlugin(req.LayerData, pluginDir, true); err != nil {
		return nil, fmt.Errorf("extracting plugin: %w", err)
	}

	// Register the plugin in the shared marketplace.json at the marketplace root
	// so Codex can discover it.
	root := a.codexMarketplaceRoot(req.Scope, req.ProjectRoot)
	if err := upsertCodexMarketplace(codexMarketplaceFile(root), req.Name); err != nil {
		return nil, fmt.Errorf("writing codex marketplace: %w", err)
	}

	return &plugins.MaterializeResult{
		InstalledComponents:  codexSupported,
		DroppedComponents:    droppedComponents(req.Components, codexSupported),
		InstallPath:          pluginDir,
		ProjectScopeDegraded: false,
	}, nil
}

// Dematerialize removes the plugin's source directory and its marketplace.json
// entry (deleting the manifest when no plugins remain).
func (a *CodexAdapter) Dematerialize(_ context.Context, req plugins.DematerializeRequest) error {
	var errs []error

	pluginDir, err := a.cm.GetPluginPath(client.Codex, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		errs = append(errs, fmt.Errorf("resolving plugin path: %w", err))
	} else {
		if rmErr := a.installer.Remove(pluginDir); rmErr != nil {
			errs = append(errs, fmt.Errorf("removing plugin directory: %w", rmErr))
		} else {
			// Best-effort empty-parent cleanup.
			cleanupAfterRemove(pluginDir, req.Scope, req.ProjectRoot, a.cm.HomeDir())
		}
	}

	// Remove the plugin from the shared marketplace.json (idempotent). The file
	// is deleted once no plugins remain.
	root := a.codexMarketplaceRoot(req.Scope, req.ProjectRoot)
	if mErr := removeCodexMarketplace(codexMarketplaceFile(root), req.Name); mErr != nil {
		errs = append(errs, fmt.Errorf("removing plugin from codex marketplace: %w", mErr))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// SupportedComponents returns the component types the Codex CLI loads.
func (*CodexAdapter) SupportedComponents() []plugins.ComponentType {
	return codexSupported
}

// ScopeSupport returns no degradation for Codex: user scope uses the personal
// ~/.agents/plugins marketplace and project scope uses the per-repo
// <root>/.agents/plugins marketplace, both of which Codex discovers.
func (*CodexAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{DegradesOnProjectScope: false}
}

// --- marketplace.json helpers ---

// codexMarketplace is the Codex marketplace manifest: a top-level name and a
// list of plugin entries. See https://developers.openai.com/codex/plugins/build.
type codexMarketplace struct {
	Name    string                   `json:"name"`
	Plugins []codexMarketplacePlugin `json:"plugins"`
}

// codexMarketplacePlugin is a single Codex marketplace entry.
type codexMarketplacePlugin struct {
	Name     string            `json:"name"`
	Source   codexPluginSource `json:"source"`
	Policy   codexPluginPolicy `json:"policy"`
	Category string            `json:"category"`
}

// codexPluginSource is the local source descriptor for a marketplace entry. Path
// is resolved relative to the marketplace root and starts with "./".
type codexPluginSource struct {
	Source string `json:"source"` // "local"
	Path   string `json:"path"`   // "./toolhive/<name>"
}

// codexPluginPolicy carries the marketplace entry's install/auth policy.
type codexPluginPolicy struct {
	Installation   string `json:"installation"`   // "AVAILABLE"
	Authentication string `json:"authentication"` // "ON_INSTALL"
}

// upsertCodexMarketplace writes/maintains the marketplace.json, adding or
// updating the plugin entry for the toolhive marketplace under a file lock.
func upsertCodexMarketplace(marketplacePath, name string) error {
	if err := os.MkdirAll(filepath.Dir(marketplacePath), 0o700); err != nil {
		return fmt.Errorf("creating marketplace dir: %w", err)
	}

	return fileutils.WithFileLock(marketplacePath, func() error {
		mp, err := readCodexMarketplace(marketplacePath)
		if err != nil {
			return err
		}
		entry := codexMarketplacePlugin{
			Name: name,
			Source: codexPluginSource{
				Source: "local",
				Path:   codexSourcePath(name),
			},
			Policy: codexPluginPolicy{
				Installation:   codexPolicyInstallation,
				Authentication: codexPolicyAuthentication,
			},
			Category: codexPluginCategory,
		}
		replaced := false
		for i := range mp.Plugins {
			if mp.Plugins[i].Name == name {
				mp.Plugins[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			mp.Plugins = append(mp.Plugins, entry)
		}
		sortCodexMarketplacePlugins(mp.Plugins)
		return writeCodexMarketplace(marketplacePath, mp)
	})
}

// removeCodexMarketplace removes the plugin entry from the marketplace.json.
// When no entries remain, the file is deleted. Idempotent: a missing file or
// missing entry is not an error.
func removeCodexMarketplace(marketplacePath, name string) error {
	if _, err := os.Stat(marketplacePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking marketplace file: %w", err)
	}

	return fileutils.WithFileLock(marketplacePath, func() error {
		mp, err := readCodexMarketplace(marketplacePath)
		if err != nil {
			return err
		}
		filtered := mp.Plugins[:0]
		for _, p := range mp.Plugins {
			if p.Name != name {
				filtered = append(filtered, p)
			}
		}
		mp.Plugins = filtered
		if len(mp.Plugins) == 0 {
			if err := os.Remove(marketplacePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("removing marketplace.json: %w", err)
			}
			return nil
		}
		return writeCodexMarketplace(marketplacePath, mp)
	})
}

// readCodexMarketplace reads and parses the marketplace.json, returning a
// freshly-initialized manifest (with name set) when the file is missing or empty.
func readCodexMarketplace(path string) (*codexMarketplace, error) {
	mp := &codexMarketplace{Name: marketplaceName}
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
	if mp.Name == "" {
		mp.Name = marketplaceName
	}
	return mp, nil
}

// writeCodexMarketplace marshals and atomically writes the marketplace.json.
func writeCodexMarketplace(path string, mp *codexMarketplace) error {
	data, err := json.MarshalIndent(mp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling marketplace.json: %w", err)
	}
	return fileutils.AtomicWriteFile(path, data, 0o600)
}

// sortCodexMarketplacePlugins orders entries by name for a stable manifest.
func sortCodexMarketplacePlugins(ps []codexMarketplacePlugin) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
}

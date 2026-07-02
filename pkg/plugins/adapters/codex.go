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
	"strings"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// CodexAdapter materializes plugins for the OpenAI Codex CLI. Codex uses a
// marketplace model: a shared ~/.codex/plugins/marketplace.json declares the
// "toolhive" marketplace (top-level name + a plugins array), and
// ~/.codex/config.toml holds per-plugin enable state as
// [plugins."<name>@toolhive"] tables.
//
// Codex loads a local plugin from its cache at
// ~/.codex/plugins/cache/<marketplace>/<plugin>/<version>, where <version> is
// "local" for local sources. ToolHive extracts each plugin directly into that
// layout (cache/toolhive/<name>/local) and points the marketplace entry's
// source at it with a "./"-relative path resolved against the marketplace root
// (~/.codex/plugins). See https://developers.openai.com/codex/plugins/build.
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
	// codexPluginVersion is the version segment Codex uses for local-source
	// plugins in its cache layout (cache/<marketplace>/<plugin>/<version>).
	codexPluginVersion = "local"
	// codexPluginCategory is the category recorded for ToolHive plugins in the
	// Codex marketplace manifest (a required field).
	codexPluginCategory = "Productivity"
	// codexPolicyInstallation / codexPolicyAuthentication are the marketplace
	// entry policy values: the plugin is available and authenticates on install.
	codexPolicyInstallation   = "AVAILABLE"
	codexPolicyAuthentication = "ON_INSTALL"
)

// codexPluginsRoot returns the Codex plugins root (the marketplace root) under
// the manager's home directory: ~/.codex/plugins.
func codexPluginsRoot(homeDir string) string {
	return filepath.Join(homeDir, ".codex", "plugins")
}

// codexMarketplacePath returns the path to Codex's shared plugins marketplace
// file: ~/.codex/plugins/marketplace.json.
func codexMarketplacePath(homeDir string) string {
	return filepath.Join(codexPluginsRoot(homeDir), "marketplace.json")
}

// codexCacheDir resolves the Codex cache directory for a plugin in the layout
// Codex loads from: <cache>/<marketplace>/<plugin>/<version>. It builds on
// ClientManager.GetPluginPath (which validates the name and resolves the
// scope-specific cache base) and then appends the marketplace/plugin/version
// segments.
func (a *CodexAdapter) codexCacheDir(name string, scope plugins.Scope, projectRoot string) (string, error) {
	// GetPluginPath returns <base>/cache/<name>; it validates the name (no
	// traversal) and applies the scope-specific base.
	leaf, err := a.cm.GetPluginPath(client.Codex, name, scope, projectRoot)
	if err != nil {
		return "", err
	}
	cacheBase := filepath.Dir(leaf) // <base>/cache
	return filepath.Join(cacheBase, marketplaceName, name, codexPluginVersion), nil
}

// Materialize extracts the plugin into the Codex cache layout, writes the shared
// Codex marketplace entry, and enables the plugin in ~/.codex/config.toml.
func (a *CodexAdapter) Materialize(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	cacheDir, err := a.codexCacheDir(req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving plugin cache path: %w", err)
	}

	if _, err := a.installer.ExtractPlugin(req.LayerData, cacheDir, true); err != nil {
		return nil, fmt.Errorf("extracting plugin: %w", err)
	}

	// Compute dropped components before mutating config (so we don't lose
	// the info on a partially successful operation).
	dropped := droppedComponents(req.Components, codexSupported)

	// Register in ~/.codex/config.toml — the config file is always user-scoped.
	configPath, err := a.cm.GetConfigPath(client.Codex)
	if err != nil {
		return nil, fmt.Errorf("resolving codex config path: %w", err)
	}

	// Enable the plugin in config.toml first. Doing this before writing the
	// marketplace means a malformed config (e.g. a non-table `plugins` key)
	// is rejected without leaving a stale marketplace.json behind.
	if err := upsertCodexPlugin(configPath, req.Name); err != nil {
		return nil, fmt.Errorf("registering plugin in codex config: %w", err)
	}

	// Write/maintain the shared marketplace.json declaring the toolhive
	// marketplace with a plugin entry whose local source points (relative to the
	// marketplace root) at the plugin's cache directory.
	marketplacePath := codexMarketplacePath(a.cm.HomeDir())
	marketplaceRoot := codexPluginsRoot(a.cm.HomeDir())
	if err := upsertCodexMarketplace(marketplacePath, marketplaceRoot, cacheDir, req.Name); err != nil {
		return nil, fmt.Errorf("writing codex marketplace: %w", err)
	}

	// Project scope install degrades because config registration is always
	// in the user-scoped ~/.codex/config.toml, even though the cache path
	// is project-local.
	projectDegraded := req.Scope == plugins.ScopeProject

	return &plugins.MaterializeResult{
		InstalledComponents:  codexSupported,
		DroppedComponents:    dropped,
		InstallPath:          cacheDir,
		ProjectScopeDegraded: projectDegraded,
	}, nil
}

// Dematerialize removes the plugin from the cache directory, disables it in
// config.toml, and removes it from the shared marketplace.json (deleting the
// file when no plugins remain).
func (a *CodexAdapter) Dematerialize(_ context.Context, req plugins.DematerializeRequest) error {
	var errs []error

	cacheDir, err := a.codexCacheDir(req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		errs = append(errs, fmt.Errorf("resolving plugin cache path: %w", err))
	} else {
		if rmErr := a.installer.Remove(cacheDir); rmErr != nil {
			errs = append(errs, fmt.Errorf("removing plugin directory: %w", rmErr))
		} else {
			// Best-effort empty-parent cleanup.
			cleanupAfterRemove(cacheDir, req.Scope, req.ProjectRoot, a.cm.HomeDir())
		}
	}

	// Remove the [plugins."<name>@toolhive"] table from config.toml (idempotent).
	configPath, cfgErr := a.cm.GetConfigPath(client.Codex)
	if cfgErr != nil {
		errs = append(errs, fmt.Errorf("resolving codex config path: %w", cfgErr))
	} else if rmErr := removeCodexPlugin(configPath, req.Name); rmErr != nil {
		errs = append(errs, fmt.Errorf("removing plugin from codex config: %w", rmErr))
	}

	// Remove the plugin from the shared marketplace.json (idempotent). The file
	// is deleted once no plugins remain.
	marketplacePath := codexMarketplacePath(a.cm.HomeDir())
	if mErr := removeCodexMarketplace(marketplacePath, req.Name); mErr != nil {
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

// ScopeSupport returns true for Codex: plugin registration is written to the
// user-scoped ~/.codex/config.toml regardless of install scope, so a
// project-scoped install degrades (the cache is project-local, but the config
// entry is user-wide).
func (*CodexAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{
		DegradesOnProjectScope: true,
		Reason:                 "Codex plugin registration is user-scoped; project-scope cache installs write to the user-wide config",
	}
}

// --- config.toml mutation helpers ---

const pluginsKey = "plugins"

// errPluginsKeyNotTable is returned when the config's `plugins` key exists but
// is not a TOML table, which means ToolHive cannot safely manage plugin
// entries under it.
var errPluginsKeyNotTable = errors.New("plugins key exists but is not a table")

// upsertCodexPlugin enables the plugin in ~/.codex/config.toml under a
// [plugins."<name>@toolhive"] table with `enabled = true`. Uses the same
// lock+atomic-write pattern as config_editor.go.
func upsertCodexPlugin(configPath, name string) error {
	// Ensure the config file's parent directory exists so the lock file can be
	// created. Mirrors CreateClientConfig's MkdirAll of the parent dir.
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("creating config parent dir: %w", err)
	}

	return fileutils.WithFileLock(configPath, func() error {
		cfg, err := client.ReadTOMLConfig(configPath)
		if err != nil {
			return err
		}

		pluginsSection, err := getPluginsMap(cfg)
		if err != nil {
			return err
		}
		pluginsSection[pluginKey(name)] = map[string]any{"enabled": true}
		cfg[pluginsKey] = pluginsSection

		return client.WriteTOMLConfig(configPath, cfg)
	})
}

// removeCodexPlugin removes the [plugins."<name>@toolhive"] table from the
// Codex config.toml. Idempotent: a missing config file or missing table is not
// an error.
func removeCodexPlugin(configPath, name string) error {
	// Short-circuit when the config file doesn't exist so we don't fail trying
	// to acquire a lock on a non-existent path's parent directory.
	if _, err := os.Stat(configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking config file: %w", err)
	}

	return fileutils.WithFileLock(configPath, func() error {
		cfg, readErr := client.ReadTOMLConfig(configPath)
		if readErr != nil {
			return readErr
		}
		if len(cfg) == 0 {
			return nil
		}

		pluginsSection, pErr := getPluginsMap(cfg)
		if pErr != nil {
			return pErr
		}
		delete(pluginsSection, pluginKey(name))
		if len(pluginsSection) == 0 {
			delete(cfg, pluginsKey)
		} else {
			cfg[pluginsKey] = pluginsSection
		}

		return client.WriteTOMLConfig(configPath, cfg)
	})
}

// getPluginsMap returns the `plugins` table from the config. It returns a
// fresh empty map when the key is absent, and an error when the key exists but
// is not a table (so a malformed config is never silently clobbered).
func getPluginsMap(cfg map[string]any) (map[string]any, error) {
	existing, ok := cfg[pluginsKey]
	if !ok {
		return make(map[string]any), nil
	}
	m, ok := existing.(map[string]any)
	if !ok {
		return nil, errPluginsKeyNotTable
	}
	return m, nil
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
	Path   string `json:"path"`   // "./cache/toolhive/<name>/local"
}

// codexPluginPolicy carries the marketplace entry's install/auth policy.
type codexPluginPolicy struct {
	Installation   string `json:"installation"`   // "AVAILABLE"
	Authentication string `json:"authentication"` // "ON_INSTALL"
}

// codexSourcePath returns the marketplace entry's source path for cacheDir,
// relative to marketplaceRoot and prefixed with "./" when cacheDir is inside the
// root (the normal user-scope case). A project-scoped cache lives outside the
// user marketplace root, so the absolute path is used as a fallback (that case
// already degrades — see ScopeSupport).
func codexSourcePath(marketplaceRoot, cacheDir string) string {
	rel, err := filepath.Rel(marketplaceRoot, cacheDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return cacheDir
	}
	return "./" + filepath.ToSlash(rel)
}

// upsertCodexMarketplace writes/maintains the shared marketplace.json, adding or
// updating the plugin entry for the toolhive marketplace under a file lock.
func upsertCodexMarketplace(marketplacePath, marketplaceRoot, cacheDir, name string) error {
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
				Path:   codexSourcePath(marketplaceRoot, cacheDir),
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

// removeCodexMarketplace removes the plugin entry from the shared
// marketplace.json. When no entries remain, the file is deleted. Idempotent:
// a missing file or missing entry is not an error.
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

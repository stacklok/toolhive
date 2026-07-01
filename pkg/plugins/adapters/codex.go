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
	"strings"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// CodexAdapter materializes plugins for the OpenAI Codex CLI. Codex uses a
// marketplace model: a shared ~/.agents/plugins/marketplace.json declares the
// "toolhive" marketplace with a local source pointing at the plugins cache
// parent directory, and ~/.codex/config.toml holds per-plugin enable/policy
// state as [plugins."<name>@toolhive"] tables. Plugins themselves are
// extracted to ~/.codex/plugins/cache/<name>.
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

// codexMarketplacePath returns the path to Codex's shared plugins marketplace
// file under the manager's home directory: ~/.agents/plugins/marketplace.json.
func codexMarketplacePath(homeDir string) string {
	return filepath.Join(homeDir, ".agents", "plugins", "marketplace.json")
}

// Materialize extracts the plugin, writes the shared Codex marketplace entry,
// and enables the plugin in ~/.codex/config.toml.
func (a *CodexAdapter) Materialize(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	cacheDir, err := a.cm.GetPluginPath(client.Codex, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving plugin cache path: %w", err)
	}

	if _, err := a.installer.Extract(req.LayerData, cacheDir, true); err != nil {
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
	// marketplace with a local source pointing at the plugins cache parent
	// directory (stable across install/uninstall order).
	marketplacePath := codexMarketplacePath(a.cm.HomeDir())
	if err := upsertCodexMarketplace(marketplacePath, cacheDir); err != nil {
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
// config.toml, and removes the shared marketplace.json when no @toolhive
// plugins remain.
func (a *CodexAdapter) Dematerialize(_ context.Context, req plugins.DematerializeRequest) error {
	var errs []error

	cacheDir, err := a.cm.GetPluginPath(client.Codex, req.Name, req.Scope, req.ProjectRoot)
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
	} else if err := removeCodexPlugin(configPath, req.Name); err != nil {
		errs = append(errs, fmt.Errorf("removing plugin from codex config: %w", err))
	} else {
		// If no @toolhive plugins remain in config.toml, remove the shared
		// marketplace.json so Codex doesn't reference a marketplace with no
		// enabled plugins.
		marketplacePath := codexMarketplacePath(a.cm.HomeDir())
		if cfg, readErr := client.ReadTOMLConfig(configPath); readErr != nil {
			errs = append(errs, fmt.Errorf("reading codex config for marketplace check: %w", readErr))
		} else if !hasToolhivePlugin(cfg) {
			if rmErr := removeCodexMarketplace(marketplacePath); rmErr != nil {
				errs = append(errs, fmt.Errorf("removing codex marketplace: %w", rmErr))
			}
		}
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
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking config file: %w", err)
	}

	return fileutils.WithFileLock(configPath, func() error {
		cfg, err := client.ReadTOMLConfig(configPath)
		if err != nil {
			return err
		}
		if len(cfg) == 0 {
			return nil
		}

		pluginsSection, err := getPluginsMap(cfg)
		if err != nil {
			return err
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

// hasToolhivePlugin reports whether any key in the config's `plugins` table
// ends with "@toolhive" (i.e. a ToolHive-managed plugin is still enabled).
func hasToolhivePlugin(cfg map[string]any) bool {
	pluginsSection, ok := cfg[pluginsKey].(map[string]any)
	if !ok {
		return false
	}
	suffix := "@" + marketplaceName
	for k := range pluginsSection {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// --- marketplace.json helpers ---

// codexMarketplaceEntry is a single marketplace entry in Codex's shared
// ~/.agents/plugins/marketplace.json. The "toolhive" marketplace uses a local
// source pointing at the plugins cache parent directory.
type codexMarketplaceEntry struct {
	Source codexMarketplaceSource `json:"source"`
}

// codexMarketplaceSource is the local source descriptor for a marketplace entry.
type codexMarketplaceSource struct {
	Source string `json:"source"` // "local"
	Path   string `json:"path"`   // absolute path to the plugins cache parent dir
}

// upsertCodexMarketplace writes/maintains the shared marketplace.json with a
// "toolhive" marketplace whose local source points at the plugins cache parent
// directory (filepath.Dir(cacheDir)). The parent dir is stable across
// install/uninstall order, so a non-LIFO uninstall cannot invalidate the path
// for a still-installed plugin.
func upsertCodexMarketplace(marketplacePath, cacheDir string) error {
	if err := os.MkdirAll(filepath.Dir(marketplacePath), 0o700); err != nil {
		return fmt.Errorf("creating marketplace dir: %w", err)
	}

	return fileutils.WithFileLock(marketplacePath, func() error {
		entries, err := readCodexMarketplace(marketplacePath)
		if err != nil {
			return err
		}
		entries[marketplaceName] = codexMarketplaceEntry{
			Source: codexMarketplaceSource{
				Source: "local",
				Path:   filepath.Dir(cacheDir),
			},
		}
		return writeCodexMarketplace(marketplacePath, entries)
	})
}

// removeCodexMarketplace removes the "toolhive" entry from the shared
// marketplace.json. When no entries remain, the file is deleted. Idempotent:
// a missing file is not an error.
func removeCodexMarketplace(marketplacePath string) error {
	if _, err := os.Stat(marketplacePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking marketplace file: %w", err)
	}

	return fileutils.WithFileLock(marketplacePath, func() error {
		entries, err := readCodexMarketplace(marketplacePath)
		if err != nil {
			return err
		}
		delete(entries, marketplaceName)
		if len(entries) == 0 {
			return os.Remove(marketplacePath)
		}
		return writeCodexMarketplace(marketplacePath, entries)
	})
}

// readCodexMarketplace reads and parses the marketplace.json, returning an
// empty map when the file is missing or empty.
func readCodexMarketplace(path string) (map[string]codexMarketplaceEntry, error) {
	entries := make(map[string]codexMarketplaceEntry)
	data, err := os.ReadFile(path) // #nosec G304 -- path is a known tool config file location
	if err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return nil, fmt.Errorf("reading marketplace.json: %w", err)
	}
	if len(data) == 0 {
		return entries, nil
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing marketplace.json: %w", err)
	}
	return entries, nil
}

// writeCodexMarketplace marshals and atomically writes the marketplace.json.
func writeCodexMarketplace(path string, entries map[string]codexMarketplaceEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling marketplace.json: %w", err)
	}
	return fileutils.AtomicWriteFile(path, data, 0o600)
}

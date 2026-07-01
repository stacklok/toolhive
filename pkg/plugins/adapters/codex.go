// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// CodexAdapter materializes plugins for the OpenAI Codex CLI. Plugins are
// extracted to ~/.codex/plugins/cache/<name> and registered in the user-scoped
// ~/.codex/config.toml under a [plugins.<name>] table with a `path` key.
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

// Materialize extracts the plugin and registers it in Codex's config.toml.
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

	if err := upsertPluginTable(configPath, req.Name, cacheDir); err != nil {
		return nil, fmt.Errorf("registering plugin in codex config: %w", err)
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

// Dematerialize removes the plugin from the cache directory and its config entry.
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
			stopAt := req.ProjectRoot
			if req.Scope == plugins.ScopeUser {
				if homeDir, homeErr := os.UserHomeDir(); homeErr == nil {
					stopAt = homeDir
				}
			}
			if stopAt != "" {
				skills.RemoveEmptyParents(filepath.Dir(cacheDir), stopAt)
			}
		}
	}

	// Remove the [plugins.<name>] table from config.toml (idempotent).
	configPath, cfgErr := a.cm.GetConfigPath(client.Codex)
	if cfgErr != nil {
		errs = append(errs, fmt.Errorf("resolving codex config path: %w", cfgErr))
	} else if err := removePluginTable(configPath, req.Name); err != nil {
		errs = append(errs, fmt.Errorf("removing plugin from codex config: %w", err))
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

// --- Config.toml mutation helpers ---

const pluginsKey = "plugins"

// upsertPluginTable inserts or updates a [plugins.<name>] table with a `path`
// key in the Codex config.toml. Uses the same lock+atomic-write pattern as
// config_editor.go.
func upsertPluginTable(configPath, name, cacheDir string) error {
	// Ensure the config file's parent directory exists so the lock file can be
	// created. Mirrors CreateClientConfig's MkdirAll of the parent dir.
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return fmt.Errorf("creating config parent dir: %w", err)
	}

	return fileutils.WithFileLock(configPath, func() error {
		cfg, err := client.ReadTOMLConfig(configPath)
		if err != nil {
			return err
		}

		pluginsSection := getPluginsMap(cfg)
		pluginsSection[name] = map[string]any{"path": cacheDir}
		cfg[pluginsKey] = pluginsSection

		return client.WriteTOMLConfig(configPath, cfg)
	})
}

// removePluginTable removes the [plugins.<name>] table from the Codex config.toml.
// Idempotent: a missing config file or missing table is not an error.
func removePluginTable(configPath, name string) error {
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

		pluginsSection := getPluginsMap(cfg)
		delete(pluginsSection, name)
		if len(pluginsSection) == 0 {
			delete(cfg, pluginsKey)
		} else {
			cfg[pluginsKey] = pluginsSection
		}

		return client.WriteTOMLConfig(configPath, cfg)
	})
}

func getPluginsMap(cfg map[string]any) map[string]any {
	existing, ok := cfg[pluginsKey]
	if !ok {
		return make(map[string]any)
	}
	m, ok := existing.(map[string]any)
	if !ok {
		return make(map[string]any)
	}
	return m
}

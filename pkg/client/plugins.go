// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	// ErrPluginsNotSupported is returned when a client does not support plugins.
	ErrPluginsNotSupported = errors.New("client does not support plugins")
	// ErrNoPluginPath is returned when a client has no plugin path for the requested scope.
	ErrNoPluginPath = errors.New("client has no plugin path for the requested scope")
)

// SupportsPlugins returns whether the given client supports plugins.
func (cm *ClientManager) SupportsPlugins(clientType ClientApp) bool {
	cfg := cm.lookupClientAppConfig(clientType)
	return cfg != nil && cfg.SupportsPlugins
}

// ListPluginSupportingClients returns a sorted slice of all clients that support plugins.
func (cm *ClientManager) ListPluginSupportingClients() []ClientApp {
	var clients []ClientApp
	for _, cfg := range cm.clientIntegrations {
		if cfg.SupportsPlugins {
			clients = append(clients, cfg.ClientType)
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		return clients[i] < clients[j]
	})
	return clients
}

// GetPluginPath resolves the filesystem path for a plugin installation.
//
// For [plugins.ScopeUser], it returns ~/<PluginsGlobalPath>/<pluginName>.
// For [plugins.ScopeProject], it returns <projectRoot>/<PluginsProjectPath>/<pluginName>.
//
// Returns an error if the client doesn't support plugins, the scope has no
// configured path, the project root is empty, or the plugin name would result
// in path traversal outside the plugins directory.
func (cm *ClientManager) GetPluginPath(
	clientType ClientApp, pluginName string, scope plugins.Scope, projectRoot string,
) (string, error) {
	if err := skills.ValidateSkillName(pluginName); err != nil {
		return "", err
	}

	cfg := cm.lookupClientAppConfig(clientType)
	if cfg == nil {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedClientType, clientType)
	}
	if !cfg.SupportsPlugins {
		return "", fmt.Errorf("%w: %s", ErrPluginsNotSupported, clientType)
	}

	switch scope {
	case plugins.ScopeUser:
		return cm.buildPluginsGlobalPath(cfg, pluginName)
	case plugins.ScopeProject:
		return buildPluginsProjectPath(cfg, pluginName, projectRoot)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownScope, scope)
	}
}

func (cm *ClientManager) buildPluginsGlobalPath(cfg *clientAppConfig, pluginName string) (string, error) {
	if len(cfg.PluginsGlobalPath) == 0 {
		return "", fmt.Errorf("%w: %s has no global plugin path", ErrNoPluginPath, cfg.ClientType)
	}

	parts := []string{cm.homeDir}
	if prefix, ok := cfg.PluginsPlatformPrefix[Platform(runtime.GOOS)]; ok {
		parts = append(parts, prefix...)
	}
	parts = append(parts, cfg.PluginsGlobalPath...)
	parts = append(parts, pluginName)
	return filepath.Join(parts...), nil
}

func buildPluginsProjectPath(cfg *clientAppConfig, pluginName string, projectRoot string) (string, error) {
	if len(cfg.PluginsProjectPath) == 0 {
		return "", fmt.Errorf("%w: %s has no project plugin path", ErrNoPluginPath, cfg.ClientType)
	}

	if projectRoot == "" {
		return "", ErrProjectRootRequired
	}

	parts := []string{projectRoot}
	parts = append(parts, cfg.PluginsProjectPath...)
	parts = append(parts, pluginName)
	return filepath.Join(parts...), nil
}

// GetConfigPath returns the absolute path to clientType's settings file under
// the manager's home directory. Returns ErrUnsupportedClientType for unknown
// clients. Exported so plugin adapters can locate the Codex config.toml without
// reaching into ClientManager internals.
func (cm *ClientManager) GetConfigPath(clientType ClientApp) (string, error) {
	cfg := cm.lookupClientAppConfig(clientType)
	if cfg == nil {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedClientType, clientType)
	}
	return buildConfigFilePath(cfg.SettingsFile, cfg.RelPath, cfg.PlatformPrefix, []string{cm.homeDir}), nil
}

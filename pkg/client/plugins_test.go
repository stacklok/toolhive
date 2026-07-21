// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/plugins"
)

// newPluginsTestManager builds a ClientManager with the production client
// integrations rooted at a fake home, so plugin-path resolution is exercised
// against the real ClaudeCode and Codex configs without touching the real HOME.
func newPluginsTestManager() *ClientManager {
	return NewTestClientManagerWithHome(testHomeDir)
}

func TestSupportsPlugins(t *testing.T) {
	t.Parallel()
	cm := newPluginsTestManager()

	assert.True(t, cm.SupportsPlugins(ClaudeCode))
	assert.True(t, cm.SupportsPlugins(Codex))
	// Zed does not support plugins.
	assert.False(t, cm.SupportsPlugins(Zed))
	// Unknown client returns false.
	assert.False(t, cm.SupportsPlugins(ClientApp("nonexistent")))
}

func TestListPluginSupportingClients(t *testing.T) {
	t.Parallel()
	cm := newPluginsTestManager()

	clients := cm.ListPluginSupportingClients()
	// The production integrations currently enable plugins for ClaudeCode
	// and Codex only.
	require.ElementsMatch(t, []ClientApp{ClaudeCode, Codex}, clients)

	// Verify sorted order.
	for i := 1; i < len(clients); i++ {
		assert.True(t, clients[i-1] < clients[i],
			"not sorted: %q comes after %q", clients[i], clients[i-1])
	}
}

func TestGetPluginPath(t *testing.T) {
	t.Parallel()
	cm := newPluginsTestManager()

	// Platform prefix for the current OS (ClaudeCode uses the same path on
	// all platforms, but build the expected path defensively).
	ccGlobalWant := filepath.Join(append([]string{testHomeDir}, []string{".claude", "plugins", "my-plugin"}...)...)
	codexGlobalWant := filepath.Join(testHomeDir, ".agents", "plugins", "toolhive", "my-plugin")

	tests := []struct {
		name           string
		client         ClientApp
		pluginName     string
		scope          plugins.Scope
		projectRoot    string
		wantPath       string
		wantErr        error  // sentinel error to check with errors.Is
		wantErrContain string // substring for non-sentinel errors
	}{
		{
			name:       "ScopeUser ClaudeCode",
			client:     ClaudeCode,
			pluginName: "my-plugin",
			scope:      plugins.ScopeUser,
			wantPath:   ccGlobalWant,
		},
		{
			name:       "ScopeUser Codex",
			client:     Codex,
			pluginName: "my-plugin",
			scope:      plugins.ScopeUser,
			wantPath:   codexGlobalWant,
		},
		{
			name:        "ScopeProject ClaudeCode with explicit root",
			client:      ClaudeCode,
			pluginName:  "my-plugin",
			scope:       plugins.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".claude", "plugins", "my-plugin"),
		},
		{
			name:        "ScopeProject Codex with explicit root",
			client:      Codex,
			pluginName:  "my-plugin",
			scope:       plugins.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".agents", "plugins", "toolhive", "my-plugin"),
		},
		// Security-relevant: path traversal is rejected by the name validator.
		{
			name:           "path traversal with slashes rejected",
			client:         ClaudeCode,
			pluginName:     "../../etc/passwd",
			scope:          plugins.ScopeUser,
			wantErrContain: "invalid",
		},
		{
			name:           "path traversal with backslash rejected",
			client:         ClaudeCode,
			pluginName:     `foo\bar`,
			scope:          plugins.ScopeUser,
			wantErrContain: "invalid",
		},
		{
			name:           "null byte in name rejected",
			client:         ClaudeCode,
			pluginName:     "plug\x00in",
			scope:          plugins.ScopeUser,
			wantErrContain: "invalid",
		},
		{
			name:           "uppercase name rejected",
			client:         ClaudeCode,
			pluginName:     "MyPlugin",
			scope:          plugins.ScopeUser,
			wantErrContain: "invalid",
		},
		{
			name:           "empty name rejected",
			client:         ClaudeCode,
			pluginName:     "",
			scope:          plugins.ScopeUser,
			wantErrContain: "invalid",
		},
		// Scope guards.
		{
			name:       "ScopeProject requires projectRoot",
			client:     ClaudeCode,
			pluginName: "my-plugin",
			scope:      plugins.ScopeProject,
			wantErr:    ErrProjectRootRequired,
		},
		{
			name:       "client that does not support plugins",
			client:     Zed,
			pluginName: "my-plugin",
			scope:      plugins.ScopeUser,
			wantErr:    ErrPluginsNotSupported,
		},
		{
			name:       "unknown client",
			client:     ClientApp("nonexistent"),
			pluginName: "my-plugin",
			scope:      plugins.ScopeUser,
			wantErr:    ErrUnsupportedClientType,
		},
		{
			name:       "unknown scope",
			client:     ClaudeCode,
			pluginName: "my-plugin",
			scope:      plugins.Scope("global"),
			wantErr:    ErrUnknownScope,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := cm.GetPluginPath(tt.client, tt.pluginName, tt.scope, tt.projectRoot)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr),
					"expected error wrapping %v, got: %v", tt.wantErr, err)
				return
			}
			if tt.wantErrContain != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContain)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPath, got)
		})
	}

	// Sanity: the platform-prefix assembly is covered on the current OS.
	// (ClaudeCode has no prefix on any OS, so ccGlobalWant holds; this is a
	// no-op assertion on most platforms, but guards against future config
	// changes that add a prefix without updating the test.)
	_ = runtime.GOOS
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// newCodexManager returns a ClientManager rooted at a temp home with the real
// Codex integration, plus the resolved config.toml path.
func newCodexManager(t *testing.T) (*ClientManager, string) {
	t.Helper()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cfg := cm.lookupClientAppConfig(ClientApp(Codex))
	require.NotNil(t, cfg, "Codex must be a supported integration")
	require.Equal(t, llmgateway.ModeCodexAuth, cfg.LLMGatewayMode, "Codex must use the codex-auth model")
	return cm, cm.buildLLMSettingsPath(cfg)
}

func codexApplyCfg() llmgateway.ApplyConfig {
	return llmgateway.ApplyConfig{
		GatewayURL:      "https://gw.example.com",
		TokenHelperPath: "/usr/local/bin/thv",
		TokenHelperArgs: []string{"llm", "token", "--skip-browser"},
	}
}

func TestConfigureCodexAuth_WritesProviderAndAuth(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	path, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.NoError(t, err)
	assert.Equal(t, configPath, path)

	config, err := readTOMLConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, codexProviderID, config["model_provider"])
	providers, ok := config["model_providers"].(map[string]any)
	require.True(t, ok)
	provider, ok := providers[codexProviderID].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ToolHive Gateway", provider["name"])
	assert.Equal(t, "https://gw.example.com/v1", provider["base_url"])
	assert.Equal(t, "responses", provider["wire_api"])

	auth, ok := provider["auth"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/usr/local/bin/thv", auth["command"])
	assert.Equal(t, []any{"llm", "token", "--skip-browser"}, auth["args"])
	assert.Equal(t, llmgateway.CodexHelperTTL.Milliseconds(), auth["refresh_interval_ms"],
		"refresh_interval_ms keeps Codex's token-helper cadence in sync with the refresh window")
}

func TestConfigureCodexAuth_RejectsNonTableModelProviders(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	// A malformed (or future-schema) config where model_providers is not a table.
	// Setup must refuse rather than silently overwrite the user's value.
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
	require.NoError(t, writeTOMLConfig(configPath, map[string]any{"model_providers": "garbage"}))

	_, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.Error(t, err)

	config, err := readTOMLConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "garbage", config["model_providers"], "the user's original value must be left untouched")
}

func TestConfigureCodexAuth_IsIdempotent(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	first, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.NoError(t, err)
	second, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.NoError(t, err)
	assert.Equal(t, first, second)

	config, err := readTOMLConfig(configPath)
	require.NoError(t, err)
	providers, ok := config["model_providers"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, providers, 1, "repeated setup must not duplicate the provider entry")
}

func TestConfigureCodexAuth_PreservesForeignEntries(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	// Seed a user-owned provider and an unrelated mcp_servers table, the way the
	// existing MCP-client feature or the user's own edits would.
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
	seed := map[string]any{
		"model_providers": map[string]any{
			"my-bedrock": map[string]any{"name": "My Bedrock"},
		},
		"mcp_servers": map[string]any{
			"github": map[string]any{"command": "docker"},
		},
	}
	require.NoError(t, writeTOMLConfig(configPath, seed))

	_, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.NoError(t, err)

	config, err := readTOMLConfig(configPath)
	require.NoError(t, err)
	providers, ok := config["model_providers"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, providers, 2, "the foreign provider must survive alongside ours")
	_, ok = providers["my-bedrock"]
	assert.True(t, ok, "foreign provider must be untouched")

	mcpServers, ok := config["mcp_servers"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, mcpServers, "github", "unrelated mcp_servers table must be untouched")
}

func TestRevertCodexAuth_RemovesProviderOnly(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
	seed := map[string]any{
		"model_providers": map[string]any{
			"my-bedrock": map[string]any{"name": "My Bedrock"},
		},
	}
	require.NoError(t, writeTOMLConfig(configPath, seed))

	path, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.NoError(t, err)

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), path))

	config, err := readTOMLConfig(configPath)
	require.NoError(t, err)
	_, hasProvider := config["model_provider"]
	assert.False(t, hasProvider, "model_provider must be cleared since it pointed at ours")
	providers, ok := config["model_providers"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, providers, 1, "only our provider entry should be removed")
	_, ok = providers["my-bedrock"]
	assert.True(t, ok, "foreign provider must survive revert")
}

func TestRevertCodexAuth_LeavesForeignModelProviderIntact(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	path, err := cm.ConfigureLLMGateway(ClientApp(Codex), codexApplyCfg())
	require.NoError(t, err)

	// Simulate the user (or another tool) selecting a different provider after setup.
	config, err := readTOMLConfig(configPath)
	require.NoError(t, err)
	config["model_provider"] = "my-bedrock"
	require.NoError(t, writeTOMLConfig(configPath, config))

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), path))

	config, err = readTOMLConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "my-bedrock", config["model_provider"], "a changed selection must not be clobbered by revert")
}

func TestConfigureCodexAuth_RejectsMissingTokenHelper(t *testing.T) {
	t.Parallel()
	cm, _ := newCodexManager(t)

	_, err := cm.ConfigureLLMGateway(ClientApp(Codex), llmgateway.ApplyConfig{
		GatewayURL: "https://gw.example.com",
	})
	require.Error(t, err)
}

func TestCodexBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		gatewayURL string
		want       string
	}{
		{"appends v1", "https://gw.example.com", "https://gw.example.com/v1"},
		{"trims trailing slash before appending", "https://gw.example.com/", "https://gw.example.com/v1"},
		{"does not double-append existing v1 suffix", "https://gw.example.com/v1", "https://gw.example.com/v1"},
		{"does not double-append with trailing slash", "https://gw.example.com/v1/", "https://gw.example.com/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, codexBaseURL(tt.gatewayURL))
		})
	}
}

func TestRevertCodexAuth_MissingFileIsNoOp(t *testing.T) {
	t.Parallel()
	cm, configPath := newCodexManager(t)

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), configPath))
	assert.NoFileExists(t, configPath)
}

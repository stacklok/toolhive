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

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), path, true))

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

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), path, true))

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

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), configPath, true))
	assert.NoFileExists(t, configPath)
}

func codexBedrockApplyCfg(model string, contextWindow int) llmgateway.ApplyConfig {
	cfg := codexApplyCfg()
	cfg.BedrockCompat = true
	cfg.BedrockCodexModel = model
	cfg.BedrockCodexContextWindow = contextWindow
	cfg.BedrockCodexConsolidationModel = "us.openai.gpt-6.1-terra"
	cfg.BedrockCodexExtractModel = "us.openai.gpt-6-luna"
	return cfg
}

// withClearBedrockKeys returns cfg for a setup whose previous run wrote the
// Bedrock keys.
func withClearBedrockKeys(cfg llmgateway.ApplyConfig) llmgateway.ApplyConfig {
	cfg.ClearBedrockKeys = true
	return cfg
}

func TestConfigureCodexAuth_BedrockKeys(t *testing.T) {
	t.Parallel()

	compatWithoutModel := codexApplyCfg()
	compatWithoutModel.BedrockCompat = true
	wantMemories := map[string]any{
		codexConsolidationModelKey: "us.openai.gpt-6.1-terra",
		codexExtractModelKey:       "us.openai.gpt-6-luna",
	}

	tests := []struct {
		name string
		// seed is the config.toml content before setup.
		seed          map[string]any
		cfg           llmgateway.ApplyConfig
		wantModel     any
		wantWebSearch any
		wantContext   any
		wantMemories  any
		wantErr       string
	}{
		{
			name:          "bedrock writes the model, web_search, context window, and memory models",
			cfg:           codexBedrockApplyCfg("us.openai.gpt-6.1-sol", 400000),
			wantModel:     "us.openai.gpt-6.1-sol",
			wantWebSearch: "disabled",
			wantContext:   int64(400000),
			wantMemories:  wantMemories,
		},
		{
			name:          "an unknown context window is not written",
			cfg:           codexBedrockApplyCfg("us.openai.gpt-6.1-sol", 0),
			wantModel:     "us.openai.gpt-6.1-sol",
			wantWebSearch: "disabled",
			wantMemories:  wantMemories,
		},
		{name: "a non-Bedrock setup writes no Bedrock keys", cfg: codexApplyCfg()},
		{name: "bedrock compat without a resolved model writes no Bedrock keys", cfg: compatWithoutModel},
		{
			name:    "a memories entry that is not a table is refused and left alone",
			seed:    map[string]any{codexMemoriesKey: "not-a-table"},
			cfg:     codexBedrockApplyCfg("us.openai.gpt-6.1-sol", 0),
			wantErr: "existing memories",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cm, configPath := newCodexManager(t)
			if tt.seed != nil {
				require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
				require.NoError(t, writeTOMLConfig(configPath, tt.seed))
			}

			_, err := cm.ConfigureLLMGateway(ClientApp(Codex), tt.cfg)
			config, readErr := readTOMLConfig(configPath)
			require.NoError(t, readErr)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, tt.seed, config, "a refused setup must not write config.toml")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, codexProviderID, config["model_provider"])
			assert.Equal(t, tt.wantModel, config[codexModelKey])
			assert.Equal(t, tt.wantWebSearch, config[codexWebSearchKey])
			assert.Equal(t, tt.wantContext, config[codexContextWindowKey])
			assert.Equal(t, tt.wantMemories, config[codexMemoriesKey])
		})
	}
}

// TestCodexBedrockKeys_SetupAndRevert covers the ownership rule for the Bedrock
// keys, the same as Claude Code's Bedrock env keys: once a Bedrock setup wrote
// them, a setup without Bedrock and revert remove them whatever they hold,
// leaving unrelated keys alone. When no Bedrock setup wrote them they are the
// user's own and both leave them alone.
func TestCodexBedrockKeys_SetupAndRevert(t *testing.T) {
	t.Parallel()

	const (
		sol  = "us.openai.gpt-6.1-sol"
		luna = "us.openai.gpt-6-luna"
	)

	tests := []struct {
		name string
		// seed is the config.toml content before the first setup.
		seed map[string]any
		// first and second are the setups to run; second is skipped when nil.
		first  llmgateway.ApplyConfig
		second *llmgateway.ApplyConfig
		// edit simulates the user changing config.toml before revert.
		edit func(map[string]any)
		// keepBedrockOnRevert reverts a setup that wrote no Bedrock keys.
		keepBedrockOnRevert bool
		// wantAfterSecond is the whole config after the second setup, without
		// ToolHive's provider table.
		wantAfterSecond map[string]any
		// wantAfterRevert is the whole config expected after revert.
		wantAfterRevert map[string]any
	}{
		{
			name:  "revert removes every Bedrock key, even after the user changed them",
			first: codexBedrockApplyCfg(sol, 400000),
			edit: func(c map[string]any) {
				c[codexModelKey] = luna
				c[codexWebSearchKey] = "cached"
				c[codexContextWindowKey] = int64(272000)
				c[codexMemoriesKey] = map[string]any{codexConsolidationModelKey: "gpt-5.6-terra"}
			},
			wantAfterRevert: map[string]any{},
		},
		{
			name: "revert keeps unrelated user keys, including other memories settings",
			seed: map[string]any{
				"approval_policy": "on-request",
				codexMemoriesKey:  map[string]any{"generate_memories": true},
			},
			first: codexBedrockApplyCfg(sol, 0),
			wantAfterRevert: map[string]any{
				"approval_policy": "on-request",
				codexMemoriesKey:  map[string]any{"generate_memories": true},
			},
		},
		{
			name:   "re-setup with a new model replaces the old one and drops its stale context window",
			first:  codexBedrockApplyCfg(sol, 400000),
			second: new(codexBedrockApplyCfg(luna, 0)),
			wantAfterSecond: map[string]any{
				"model_provider": codexProviderID, codexModelKey: luna, codexWebSearchKey: "disabled",
				codexMemoriesKey: map[string]any{
					codexConsolidationModelKey: "us.openai.gpt-6.1-terra",
					codexExtractModelKey:       "us.openai.gpt-6-luna",
				},
			},
			wantAfterRevert: map[string]any{},
		},
		{
			name:   "re-setup without Bedrock after a Bedrock one removes every Bedrock key, including the memory models",
			seed:   map[string]any{codexModelKey: "gpt-5.5"},
			first:  codexBedrockApplyCfg(sol, 400000),
			second: new(withClearBedrockKeys(codexApplyCfg())),
			// The second setup wrote no Bedrock keys, so its revert keeps them.
			keepBedrockOnRevert: true,
			wantAfterSecond: map[string]any{
				"model_provider": codexProviderID,
			},
			wantAfterRevert: map[string]any{},
		},
		{
			name: "setup and revert without Bedrock leave the user's own keys alone",
			seed: map[string]any{
				codexModelKey:     "gpt-5.5",
				codexWebSearchKey: "disabled",
				codexMemoriesKey:  map[string]any{codexConsolidationModelKey: "gpt-5.6-terra"},
			},
			first:               codexApplyCfg(),
			second:              new(codexApplyCfg()),
			keepBedrockOnRevert: true,
			wantAfterSecond: map[string]any{
				"model_provider":  codexProviderID,
				codexModelKey:     "gpt-5.5",
				codexWebSearchKey: "disabled",
				codexMemoriesKey:  map[string]any{codexConsolidationModelKey: "gpt-5.6-terra"},
			},
			wantAfterRevert: map[string]any{
				codexModelKey:     "gpt-5.5",
				codexWebSearchKey: "disabled",
				codexMemoriesKey:  map[string]any{codexConsolidationModelKey: "gpt-5.6-terra"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cm, configPath := newCodexManager(t)
			if tt.seed != nil {
				require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
				require.NoError(t, writeTOMLConfig(configPath, tt.seed))
			}

			path, err := cm.ConfigureLLMGateway(ClientApp(Codex), tt.first)
			require.NoError(t, err)
			if tt.second != nil {
				_, err = cm.ConfigureLLMGateway(ClientApp(Codex), *tt.second)
				require.NoError(t, err)
				config, err := readTOMLConfig(configPath)
				require.NoError(t, err)
				delete(config, "model_providers")
				assert.Equal(t, tt.wantAfterSecond, config)
			}
			if tt.edit != nil {
				config, err := readTOMLConfig(configPath)
				require.NoError(t, err)
				tt.edit(config)
				require.NoError(t, writeTOMLConfig(configPath, config))
			}

			require.NoError(t, cm.RevertLLMGateway(ClientApp(Codex), path, !tt.keepBedrockOnRevert))

			config, err := readTOMLConfig(configPath)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAfterRevert, config)
		})
	}
}

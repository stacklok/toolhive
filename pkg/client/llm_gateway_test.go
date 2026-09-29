// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tailscale/hujson"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// jsonPointerGet resolves a JSON Pointer (RFC 6901) in data and returns the
// string value at that path, or ("", false) if the pointer does not exist or
// the value is not a string. data may be JSONC (hujson).
func jsonPointerGet(data []byte, pointer string) (string, bool) {
	std, err := hujson.Standardize(data)
	if err != nil {
		return "", false
	}
	var root any
	if err := json.Unmarshal(std, &root); err != nil {
		return "", false
	}
	current := root
	for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = m[seg]
		if !ok {
			return "", false
		}
	}
	s, ok := current.(string)
	return s, ok
}

// fakeLLMBinary is the sentinel binary name used in tests that exercise the
// exec.LookPath check inside DetectedLLMGatewayClients. The real lookup is
// replaced by an injected stub, so no actual binary needs to exist.
const fakeLLMBinary = "fake-llm-tool"

// ── real production configs ───────────────────────────────────────────────────

// TestRealClientConfigs_ConfigureAndRevert exercises ConfigureLLMGateway and
// RevertLLMGateway against every entry in supportedClientIntegrations that has
// LLMGatewayMode set. This catches typos in JSON pointer paths, wrong
// ValueField names, or missing intermediate-object creation in the real config
// table — none of which are caught by tests that use fake clientAppConfig
// entries via LLMTestIntegrations.
//
// Values are asserted via exact JSON-pointer lookups rather than raw substring
// checks, so a value landing at a wrong pointer (or as a stray key name) will
// fail the test rather than silently pass.
func TestRealClientConfigs_ConfigureAndRevert(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Use real supportedClientIntegrations so we exercise the actual paths and keys.
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)

	applyCfg := llmgateway.ApplyConfig{
		GatewayURL:         "https://gw.example.com",
		ProxyBaseURL:       "http://localhost:14000/v1",
		TokenHelperCommand: `thv llm token`,
	}

	// wantPointers maps RFC 6901 JSON pointer → expected string value after
	// Configure. After Revert every pointer must be absent.
	cases := []struct {
		clientType   ClientApp
		wantPointers map[string]string
	}{
		{
			// ~/.claude/settings.json
			clientType: ClaudeCode,
			wantPointers: map[string]string{
				"/apiKeyHelper":                          `thv llm token`,
				"/env/ANTHROPIC_BASE_URL":                "https://gw.example.com",
				"/env/CLAUDE_CODE_API_KEY_HELPER_TTL_MS": "300000",
			},
		},
		{
			// ~/.gemini/settings.json
			// NODE_TLS_REJECT_UNAUTHORIZED must NOT appear when TLSSkipVerify is false.
			clientType: GeminiCli,
			wantPointers: map[string]string{
				"/security/auth/selectedType": "gemini-api-key",
			},
		},
		{
			// ~/.<platform>/Cursor/User/settings.json
			clientType: Cursor,
			wantPointers: map[string]string{
				"/cursor.general.openAIBaseURL": "http://localhost:14000/v1",
				"/cursor.general.openAIAPIKey":  "thv-proxy",
			},
		},
		{
			// ~/Library/Application Support/GitHub Copilot for Xcode/editorSettings.json
			clientType: ClientApp(Xcode),
			wantPointers: map[string]string{
				"/openAIBaseURL": "http://localhost:14000/v1",
				"/apiKey":        "thv-proxy",
			},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.clientType), func(t *testing.T) {
			t.Parallel()

			cfg := cm.lookupClientAppConfig(tc.clientType)
			require.NotNil(t, cfg, "missing entry in supportedClientIntegrations")
			require.NotEmpty(t, cfg.LLMGatewayMode, "no LLMGatewayMode set")

			// Create the settings directory so detection and configure succeed.
			settingsPath := cm.buildLLMSettingsPath(cfg)
			require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o700))

			// Configure and verify each pointer resolves to the expected value.
			path, err := cm.ConfigureLLMGateway(tc.clientType, applyCfg)
			require.NoError(t, err)

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			for ptr, want := range tc.wantPointers {
				got, ok := jsonPointerGet(data, ptr)
				assert.True(t, ok, "pointer %q missing after Configure for %s", ptr, tc.clientType)
				assert.Equal(t, want, got, "wrong value at %q after Configure for %s", ptr, tc.clientType)
			}

			// Revert and verify every pointer is gone.
			require.NoError(t, cm.RevertLLMGateway(tc.clientType, path))

			data, err = os.ReadFile(path)
			require.NoError(t, err)
			for ptr := range tc.wantPointers {
				_, ok := jsonPointerGet(data, ptr)
				assert.False(t, ok, "pointer %q still present after Revert for %s", ptr, tc.clientType)
			}
		})
	}
}

func TestConfigureLLMGateway_VSCodeCustomEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clientType ClientApp
		pathPart   string
	}{
		{name: "stable", clientType: VSCode, pathPart: "Code"},
		{name: "insiders", clientType: VSCodeInsider, pathPart: "Code - Insiders"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
			appCfg := cm.lookupClientAppConfig(tt.clientType)
			require.NotNil(t, appCfg)
			assert.Equal(t, llmgateway.ModeVSCode, appCfg.LLMGatewayMode)
			assert.Equal(t, "chatLanguageModels.json", appCfg.LLMSettingsFile)
			assert.Contains(t, appCfg.LLMSettingsRelPath, tt.pathPart)

			path, err := cm.ConfigureLLMGateway(tt.clientType, llmgateway.ApplyConfig{
				ProxyBaseURL:     "http://localhost:14000/v1",
				DiscoveredModels: []string{"model-a", "model-b"},
			})
			require.NoError(t, err)
			assert.Equal(t, "chatLanguageModels.json", filepath.Base(path))

			var groups []vsCodeProviderGroup
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &groups))
			require.Len(t, groups, 1)
			assert.Equal(t, "ToolHive", groups[0].Name)
			assert.Equal(t, "customendpoint", groups[0].Vendor)
			require.Len(t, groups[0].Models, 2)
			for i, modelID := range []string{"model-a", "model-b"} {
				model := groups[0].Models[i]
				assert.Equal(t, modelID, model.ID)
				assert.Equal(t, modelID, model.Name)
				assert.Equal(t, "http://localhost:14000/v1", model.URL)
				assert.True(t, model.ToolCalling)
				assert.False(t, model.Vision)
				assert.Positive(t, model.MaxInputTokens)
				assert.Positive(t, model.MaxOutputTokens)
				assert.Equal(t, "Bearer thv-proxy", model.RequestHeaders["Authorization"])
			}
		})
	}
}

func TestConfigureLLMGateway_VSCodePreservesContentAndIsIdempotent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	appCfg := cm.lookupClientAppConfig(VSCode)
	chatPath := cm.buildLLMSettingsPath(appCfg)
	require.NoError(t, os.MkdirAll(filepath.Dir(chatPath), 0o700))
	require.NoError(t, os.WriteFile(chatPath, []byte(`[
		// Keep this provider and its comment.
		{"name":"Other","vendor":"other","models":[],"unrelated":true},
		{"name":"ToolHive","vendor":"customendpoint","models":[{"id":"stale"}]}
	]`), 0o600))
	settingsPath := filepath.Join(filepath.Dir(chatPath), "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{
		"editor.fontSize": 15,
		"github.copilot.enable": {"*": true},
		"github.copilot.advanced.serverUrl": "http://obsolete",
		"github.copilot.advanced.apiKey": "obsolete"
	}`), 0o600))

	applyCfg := llmgateway.ApplyConfig{
		ProxyBaseURL:     "http://localhost:14000/v1",
		DiscoveredModels: []string{"model-a"},
	}
	for range 2 {
		_, err := cm.ConfigureLLMGateway(VSCode, applyCfg)
		require.NoError(t, err)
	}

	data, err := os.ReadFile(chatPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "// Keep this provider and its comment.")
	var groups []map[string]any
	standardized, err := hujson.Standardize(data)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(standardized, &groups))
	require.Len(t, groups, 2, "repeat setup must replace, not duplicate, the ToolHive group")
	assert.Equal(t, "Other", groups[0]["name"])
	assert.Equal(t, true, groups[0]["unrelated"])
	assert.Equal(t, "ToolHive", groups[1]["name"])

	settings, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(settings), `"editor.fontSize"`)
	assert.Contains(t, string(settings), `"github.copilot.enable"`)
	assert.NotContains(t, string(settings), "github.copilot.advanced.serverUrl")
	assert.NotContains(t, string(settings), "github.copilot.advanced.apiKey")
}

func TestRevertLLMGateway_VSCodeMigratesLegacyConfigPath(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	legacyPath := filepath.Join(filepath.Dir(cm.buildLLMSettingsPath(cm.lookupClientAppConfig(VSCode))), "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o700))
	require.NoError(t, os.WriteFile(legacyPath, []byte(`{
		"editor.fontSize": 15,
		"github.copilot.advanced.serverUrl": "http://localhost:14000/v1",
		"github.copilot.advanced.apiKey": "thv-proxy"
	}`), 0o600))

	require.NoError(t, cm.RevertLLMGateway(VSCode, legacyPath))
	data, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"editor.fontSize"`)
	assert.NotContains(t, string(data), "github.copilot.advanced")
}

func TestConfigureLLMGateway_VSCodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		models  []string
		wantErr string
	}{
		{name: "no discovered models", models: nil, wantErr: "returned no models"},
		{name: "malformed JSON", content: `[`, models: []string{"model-a"}, wantErr: "parsing"},
		{name: "wrong root type", content: `{}`, models: []string{"model-a"}, wantErr: "expected an array"},
		{name: "malformed provider", content: `[42]`, models: []string{"model-a"}, wantErr: "provider group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
			path := cm.buildLLMSettingsPath(cm.lookupClientAppConfig(VSCode))
			if tt.content != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			}
			_, err := cm.ConfigureLLMGateway(VSCode, llmgateway.ApplyConfig{
				ProxyBaseURL: "http://localhost:14000/v1", DiscoveredModels: tt.models,
			})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRevertLLMGateway_VSCodePreservesUnrelatedGroups(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	path, err := cm.ConfigureLLMGateway(VSCodeInsider, llmgateway.ApplyConfig{
		ProxyBaseURL: "http://localhost:14000/v1", DiscoveredModels: []string{"model-a"},
	})
	require.NoError(t, err)
	data := []byte(`[
		{"name":"Other","vendor":"other","models":[]},
		{"name":"ToolHive","vendor":"customendpoint","models":[]}
	]`)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	require.NoError(t, cm.RevertLLMGateway(VSCodeInsider, path))
	result, err := os.ReadFile(path)
	require.NoError(t, err)
	var groups []vsCodeProviderGroup
	require.NoError(t, json.Unmarshal(result, &groups))
	require.Len(t, groups, 1)
	assert.Equal(t, "Other", groups[0].Name)
	assert.Equal(t, "other", groups[0].Vendor)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// TestConfigureLLMGateway_ClaudeCodeBedrock verifies that BedrockCompat writes
// CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1 and the three per-tier model IDs into
// Claude Code's settings.json, and that without BedrockCompat those keys are
// absent (the ClearWhenEmpty behaviour), all against the real config table.
func TestConfigureLLMGateway_ClaudeCodeBedrock(t *testing.T) {
	t.Parallel()

	bedrockPointers := map[string]string{
		"/env/CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
		"/env/ANTHROPIC_DEFAULT_HAIKU_MODEL":          "us.anthropic.claude-haiku-x",
		"/env/ANTHROPIC_DEFAULT_OPUS_MODEL":           "us.anthropic.claude-opus-x[1m]",
		"/env/ANTHROPIC_DEFAULT_SONNET_MODEL":         "us.anthropic.claude-sonnet-x[1m]",
	}

	t.Run("BedrockCompat writes keys", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))

		path, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
			GatewayURL:         "https://gw.example.com",
			TokenHelperCommand: `thv llm token`,
			BedrockCompat:      true,
			BedrockHaikuModel:  "us.anthropic.claude-haiku-x",
			BedrockOpusModel:   "us.anthropic.claude-opus-x[1m]",
			BedrockSonnetModel: "us.anthropic.claude-sonnet-x[1m]",
		})
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for ptr, want := range bedrockPointers {
			got, ok := jsonPointerGet(data, ptr)
			assert.True(t, ok, "pointer %q missing", ptr)
			assert.Equal(t, want, got, "wrong value at %q", ptr)
		}
	})

	t.Run("without BedrockCompat keys are absent", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))

		path, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
			GatewayURL:         "https://gw.example.com",
			TokenHelperCommand: `thv llm token`,
		})
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for ptr := range bedrockPointers {
			_, ok := jsonPointerGet(data, ptr)
			assert.False(t, ok, "pointer %q should be absent without BedrockCompat", ptr)
		}
	})
}

func TestConfigureLLMGateway_ClaudeCodeExtendedTTLCache(t *testing.T) {
	t.Parallel()

	cachePointers := map[string]string{
		"/promptCacheTtl":               "1h",
		"/subagentPromptCacheTtl":       "1h",
		"/env/ENABLE_PROMPT_CACHING_1H": "1",
	}
	baseCfg := llmgateway.ApplyConfig{
		GatewayURL:         "https://gw.example.com",
		TokenHelperCommand: `thv llm token`,
	}

	t.Run("enabled writes both request buckets and legacy fallback", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))

		cfg := baseCfg
		cfg.ExtendedTTLCache = true
		path, err := cm.ConfigureLLMGateway(ClaudeCode, cfg)
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for ptr, want := range cachePointers {
			got, ok := jsonPointerGet(data, ptr)
			assert.True(t, ok, "pointer %q missing", ptr)
			assert.Equal(t, want, got, "wrong value at %q", ptr)
		}
	})

	t.Run("explicit false removes previously written keys", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))

		enabledCfg := baseCfg
		enabledCfg.ExtendedTTLCache = true
		path, err := cm.ConfigureLLMGateway(ClaudeCode, enabledCfg)
		require.NoError(t, err)
		_, err = cm.ConfigureLLMGateway(ClaudeCode, baseCfg)
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for ptr := range cachePointers {
			_, ok := jsonPointerGet(data, ptr)
			assert.False(t, ok, "pointer %q should be absent after disabling extended TTL", ptr)
		}
	})

	t.Run("teardown removes all extended TTL keys", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))

		cfg := baseCfg
		cfg.ExtendedTTLCache = true
		path, err := cm.ConfigureLLMGateway(ClaudeCode, cfg)
		require.NoError(t, err)
		require.NoError(t, cm.RevertLLMGateway(ClaudeCode, path))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for ptr := range cachePointers {
			_, ok := jsonPointerGet(data, ptr)
			assert.False(t, ok, "pointer %q should be absent after teardown", ptr)
		}
	})
}

func TestClearExtendedTTLCache_PreservesOtherClaudeCodeSettings(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	path, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		GatewayURL:         "https://gw.example.com",
		TokenHelperCommand: "thv llm token",
		ExtendedTTLCache:   true,
	})
	require.NoError(t, err)

	require.NoError(t, cm.ClearExtendedTTLCache(ClaudeCode, path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, ptr := range []string{
		"/promptCacheTtl",
		"/subagentPromptCacheTtl",
		"/env/ENABLE_PROMPT_CACHING_1H",
	} {
		_, ok := jsonPointerGet(data, ptr)
		assert.False(t, ok, "pointer %q should be removed", ptr)
	}
	got, ok := jsonPointerGet(data, "/apiKeyHelper")
	assert.True(t, ok)
	assert.Equal(t, "thv llm token", got)
	got, ok = jsonPointerGet(data, "/env/ANTHROPIC_BASE_URL")
	assert.True(t, ok)
	assert.Equal(t, "https://gw.example.com", got)
}

func TestClientManager_ExtendedTTLCacheSupport(t *testing.T) {
	t.Parallel()

	cm := NewTestClientManager(t.TempDir(), nil, supportedClientIntegrations, nil)
	assert.True(t, cm.SupportsExtendedTTLCache(ClaudeCode))
	for _, unsupported := range []ClientApp{
		ClientApp(ClaudeDesktop), Codex, GeminiCli, Cursor, VSCode, VSCodeInsider, ClientApp(Xcode),
	} {
		assert.False(t, cm.SupportsExtendedTTLCache(unsupported), "%s unexpectedly supports extended TTL", unsupported)
	}
}

func TestClientManager_ExtendedTTLCacheConflict(t *testing.T) {
	conflicts := []struct {
		name     string
		variable string
		value    string
		fromEnv  bool
	}{
		{name: "global five-minute override in process environment", variable: "FORCE_PROMPT_CACHING_5M", value: "1", fromEnv: true},
		{name: "main bucket five-minute override in settings", variable: "CLAUDE_CODE_PROMPT_CACHE_TTL", value: "5m"},
		{name: "auxiliary bucket five-minute override in settings", variable: "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL", value: "5m"},
	}

	for _, tt := range conflicts {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
			for _, variable := range []string{
				"FORCE_PROMPT_CACHING_5M",
				"CLAUDE_CODE_PROMPT_CACHE_TTL",
				"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL",
			} {
				if !tt.fromEnv && variable == tt.variable {
					original, existed := os.LookupEnv(variable)
					require.NoError(t, os.Unsetenv(variable))
					t.Cleanup(func() {
						if existed {
							_ = os.Setenv(variable, original)
						}
					})
					continue
				}
				t.Setenv(variable, "non-conflicting")
			}

			if tt.fromEnv {
				t.Setenv(tt.variable, tt.value)
			} else {
				settingsDir := filepath.Join(home, ".claude")
				require.NoError(t, os.MkdirAll(settingsDir, 0o700))
				settings := []byte(`{"env":{"` + tt.variable + `":"` + tt.value + `"}}`)
				require.NoError(t, os.WriteFile(filepath.Join(settingsDir, "settings.json"), settings, 0o600))
			}

			conflict, err := cm.ExtendedTTLCacheConflict(ClaudeCode)
			require.NoError(t, err)
			assert.Contains(t, conflict, tt.variable)
		})
	}

	t.Run("non-conflicting values are ignored", func(t *testing.T) {
		home := t.TempDir()
		cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
		for _, variable := range []string{
			"FORCE_PROMPT_CACHING_5M",
			"CLAUDE_CODE_PROMPT_CACHE_TTL",
			"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL",
		} {
			t.Setenv(variable, "1h")
		}

		conflict, err := cm.ExtendedTTLCacheConflict(ClaudeCode)
		require.NoError(t, err)
		assert.Empty(t, conflict)
	})
}

func TestExtendedTTLCacheConflictSettingsScopes(t *testing.T) {
	t.Parallel()

	conflicts := []struct {
		name         string
		relativePath []string
		settings     string
		wantControl  string
	}{
		{
			name:         "project shared top-level setting",
			relativePath: []string{"project", ".claude", "settings.json"},
			settings:     `{"promptCacheTtl":"5m"}`,
			wantControl:  "promptCacheTtl=5m",
		},
		{
			name:         "project local top-level setting",
			relativePath: []string{"project", ".claude", "settings.local.json"},
			settings:     `{"subagentPromptCacheTtl":"5m"}`,
			wantControl:  "subagentPromptCacheTtl=5m",
		},
		{
			name:         "managed base setting",
			relativePath: []string{"managed", "managed-settings.json"},
			settings:     `{"promptCacheTtl":"5m"}`,
			wantControl:  "promptCacheTtl=5m",
		},
		{
			name:         "managed drop-in environment override",
			relativePath: []string{"managed", "managed-settings.d", "10-cache-policy.json"},
			settings:     `{"env":{"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL":"5m"}}`,
			wantControl:  "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL=5m",
		},
	}

	for _, tt := range conflicts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home")
			project := filepath.Join(root, "project")
			managed := filepath.Join(root, "managed")

			path := filepath.Join(append([]string{root}, tt.relativePath...)...)
			writePromptCacheSettings(t, path, tt.settings)

			conflict := promptCacheConflictFromSettings(t, home, project, managed)
			assert.Contains(t, conflict, tt.wantControl)
			assert.Contains(t, conflict, path)
		})
	}
}

func TestPromptCacheConflictPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		controls map[string]promptCacheControl
		want     string
	}{
		{
			name: "bucket environment one hour overrides top-level five minutes",
			controls: map[string]promptCacheControl{
				"CLAUDE_CODE_PROMPT_CACHE_TTL": {value: "1h", source: "environment"},
				"promptCacheTtl":               {value: "5m", source: "settings"},
			},
		},
		{
			name: "bucket environment five minutes overrides top-level one hour",
			controls: map[string]promptCacheControl{
				"CLAUDE_CODE_PROMPT_CACHE_TTL": {value: "5m", source: "environment"},
				"promptCacheTtl":               {value: "1h", source: "settings"},
			},
			want: "CLAUDE_CODE_PROMPT_CACHE_TTL=5m in environment",
		},
		{
			name: "global five-minute override wins over bucket one-hour controls",
			controls: map[string]promptCacheControl{
				"FORCE_PROMPT_CACHING_5M":               {value: "1", source: "managed settings"},
				"CLAUDE_CODE_PROMPT_CACHE_TTL":          {value: "1h", source: "environment"},
				"CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL": {value: "1h", source: "environment"},
			},
			want: "FORCE_PROMPT_CACHING_5M=1 in managed settings",
		},
		{
			name: "one-hour settings are non-conflicting",
			controls: map[string]promptCacheControl{
				"promptCacheTtl":         {value: "1h", source: "settings"},
				"subagentPromptCacheTtl": {value: "1h", source: "settings"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, promptCacheConflictDescription(tt.controls))
		})
	}
}

func TestPromptCacheSettingsFilePrecedence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	managed := filepath.Join(root, "managed")

	writePromptCacheSettings(t, filepath.Join(home, ".claude", "settings.json"), `{"promptCacheTtl":"5m"}`)
	writePromptCacheSettings(t, filepath.Join(project, ".claude", "settings.json"), `{"promptCacheTtl":"5m"}`)
	writePromptCacheSettings(t, filepath.Join(project, ".claude", "settings.local.json"), `{"promptCacheTtl":"5m"}`)
	writePromptCacheSettings(t, filepath.Join(managed, "managed-settings.json"), `{"promptCacheTtl":"5m"}`)
	writePromptCacheSettings(t, filepath.Join(managed, "managed-settings.d", "10-five-minutes.json"), `{"promptCacheTtl":"5m"}`)
	writePromptCacheSettings(t, filepath.Join(managed, "managed-settings.d", "20-one-hour.json"), `{"promptCacheTtl":"1h"}`)
	writePromptCacheSettings(t, filepath.Join(managed, "managed-settings.d", ".hidden.json"), `{"promptCacheTtl":"5m"}`)
	writePromptCacheSettings(t, filepath.Join(managed, "managed-settings.d", "README.txt"), `{"promptCacheTtl":"5m"}`)

	conflict := promptCacheConflictFromSettings(t, home, project, managed)
	assert.Empty(t, conflict,
		"the lexically last managed JSON drop-in must override lower-precedence five-minute settings")
}

func TestPromptCacheSettingsScopePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		lowerPath  []string
		higherPath []string
	}{
		{
			name:       "project shared overrides user",
			lowerPath:  []string{"home", ".claude", "settings.json"},
			higherPath: []string{"project", ".claude", "settings.json"},
		},
		{
			name:       "project local overrides project shared",
			lowerPath:  []string{"project", ".claude", "settings.json"},
			higherPath: []string{"project", ".claude", "settings.local.json"},
		},
		{
			name:       "managed base overrides project local",
			lowerPath:  []string{"project", ".claude", "settings.local.json"},
			higherPath: []string{"managed", "managed-settings.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home")
			project := filepath.Join(root, "project")
			managed := filepath.Join(root, "managed")
			writePromptCacheSettings(t, filepath.Join(append([]string{root}, tt.lowerPath...)...),
				`{"promptCacheTtl":"5m"}`)
			writePromptCacheSettings(t, filepath.Join(append([]string{root}, tt.higherPath...)...),
				`{"promptCacheTtl":"1h"}`)

			conflict := promptCacheConflictFromSettings(t, home, project, managed)
			assert.Empty(t, conflict)
		})
	}
}

func TestExtendedTTLCacheSettingsPathsUsesGitRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o700))
	workingDir := filepath.Join(root, "cmd", "thv")
	require.NoError(t, os.MkdirAll(workingDir, 0o700))

	paths, err := extendedTTLCacheSettingsPaths("user.json", workingDir, "")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"user.json",
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".claude", "settings.local.json"),
	}, paths)
}

func promptCacheConflictFromSettings(t *testing.T, home, workingDir, managedDir string) string {
	t.Helper()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cfg := cm.lookupClientAppConfig(ClaudeCode)
	require.NotNil(t, cfg)
	paths, err := extendedTTLCacheSettingsPaths(cm.buildLLMSettingsPath(cfg), workingDir, managedDir)
	require.NoError(t, err)
	controls := make(map[string]promptCacheControl)
	for _, path := range paths {
		require.NoError(t, mergePromptCacheControls(path, controls))
	}
	return promptCacheConflictDescription(controls)
}

func writePromptCacheSettings(t *testing.T, path, settings string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(settings), 0o600))
}

// newLLMManager builds a ClientManager with a single direct-mode LLM entry
// whose settings dir is homeDir/<dir>.
func newLLMManager(t *testing.T, clientType ClientApp, mode, dir string, ptrs, vals []string) (*ClientManager, string) {
	t.Helper()
	home := t.TempDir()
	cfgs := LLMTestIntegrations([]LLMTestEntry{{
		ClientType:   clientType,
		Mode:         mode,
		SettingsDir:  []string{dir},
		SettingsFile: "settings.json",
		JSONPointers: ptrs,
		ValueFields:  vals,
	}})
	cm := NewTestClientManager(home, nil, cfgs, nil)
	return cm, home
}

// ── multi-level ancestor creation ────────────────────────────────────────────

// TestConfigureLLMGateway_DeepNestedKey verifies that a key three levels deep
// (e.g. "/a/b/c") is written correctly even when neither "/a" nor "/a/b"
// exist in the settings file yet. This exercises the ensureLLMAncestors path.
func TestConfigureLLMGateway_DeepNestedKey(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude",
		[]string{"/a/b/c"}, []string{"GatewayURL"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	path, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		GatewayURL: "https://gw.example.com",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got, ok := jsonPointerGet(data, "/a/b/c")
	assert.True(t, ok, "deep nested key /a/b/c must exist (ancestor objects must be created)")
	assert.Equal(t, "https://gw.example.com", got, "deep nested value must match")
}

// ── IsLLMGatewaySupported / LLMGatewayModeFor ─────────────────────────────────

func TestIsLLMGatewaySupported(t *testing.T) {
	t.Parallel()
	cm, _ := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	assert.True(t, cm.IsLLMGatewaySupported(ClaudeCode))
	assert.False(t, cm.IsLLMGatewaySupported(Cursor)) // not in cfgs → unsupported
}

func TestLLMGatewayModeFor(t *testing.T) {
	t.Parallel()
	cm, _ := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	assert.Equal(t, "direct", cm.LLMGatewayModeFor(ClaudeCode))
	assert.Equal(t, "", cm.LLMGatewayModeFor(Cursor))
}

// ── DetectedLLMGatewayClients ─────────────────────────────────────────────────

func TestDetectedLLMGatewayClients_DirAbsent(t *testing.T) {
	t.Parallel()
	cm, _ := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	// settings dir not created → nothing detected
	assert.Empty(t, cm.DetectedLLMGatewayClients())
}

func TestDetectedLLMGatewayClients_DirPresent(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	detected := cm.DetectedLLMGatewayClients()
	require.Len(t, detected, 1)
	assert.Equal(t, ClaudeCode, detected[0])
}

// ── ConfigureLLMGateway ───────────────────────────────────────────────────────

func TestConfigureLLMGateway_CreatesFile(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	path, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		TokenHelperCommand: `thv llm token`,
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(claudeDir, "settings.json"), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got, ok := jsonPointerGet(data, "/apiKeyHelper")
	assert.True(t, ok, "/apiKeyHelper pointer must be present")
	assert.Equal(t, `thv llm token`, got, "/apiKeyHelper must contain the token helper command")
}

func TestConfigureLLMGateway_PreservesExistingKeys(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	// pre-populate with an existing key that should survive
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{"permissions":{"allow":["read"]}}`), 0o600))

	_, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		TokenHelperCommand: `thv llm token`,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "permissions") // non-string object — checked as raw substring
	got, ok := jsonPointerGet(data, "/apiKeyHelper")
	assert.True(t, ok, "/apiKeyHelper pointer must be present after configure")
	assert.Equal(t, `thv llm token`, got)
}

func TestConfigureLLMGateway_JSONCPreservesExistingParent(t *testing.T) {
	t.Parallel()
	// JSONC file with an existing "/env" object and a comment. Before the fix,
	// gjson could not parse JSONC and would see "/env" as absent, causing an
	// "add {}" patch that wiped the existing object.
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude",
		[]string{"/env/ANTHROPIC_BASE_URL"}, []string{"GatewayURL"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	settingsPath := filepath.Join(claudeDir, "settings.json")
	// Write JSONC with an existing "env" object containing another key.
	require.NoError(t, os.WriteFile(settingsPath,
		[]byte(`{ // this is JSONC
  "env": { "EXISTING_KEY": "keep-me" },
}`), 0o600))

	_, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		GatewayURL: "https://gw.example.com",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	s := string(data)
	// Comment and trailing comma must survive (JSONC round-trip).
	assert.Contains(t, s, "// this is JSONC", "JSONC comment must be preserved after configure")
	// Pre-existing sibling key inside the parent object must not be wiped.
	assert.Contains(t, s, "EXISTING_KEY", "existing key inside parent object must be preserved")
	assert.Contains(t, s, "keep-me", "existing value inside parent object must be preserved")
	assert.Contains(t, s, "ANTHROPIC_BASE_URL", "new key must be added")
	assert.Contains(t, s, "https://gw.example.com", "gateway URL must be written")
}

func TestConfigureLLMGateway_UnsupportedClient(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, nil, nil)

	_, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support LLM gateway")
}

func TestConfigureLLMGateway_Idempotent(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	cfg := llmgateway.ApplyConfig{TokenHelperCommand: `thv llm token`}
	_, err := cm.ConfigureLLMGateway(ClaudeCode, cfg)
	require.NoError(t, err)
	_, err = cm.ConfigureLLMGateway(ClaudeCode, cfg)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	// key should appear exactly once
	assert.Equal(t, 1, countSubstring(string(data), "apiKeyHelper"))
}

// ── RevertLLMGateway ──────────────────────────────────────────────────────────

func TestRevertLLMGateway_RemovesKey(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath,
		[]byte(`{"apiKeyHelper":"thv llm token","permissions":{"allow":["read"]}}`), 0o600))

	require.NoError(t, cm.RevertLLMGateway(ClaudeCode, settingsPath))

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	_, ok := jsonPointerGet(data, "/apiKeyHelper")
	assert.False(t, ok, "/apiKeyHelper must be removed after revert")
	assert.Contains(t, string(data), "permissions") // non-string object — checked as raw substring
}

func TestRevertLLMGateway_MissingFile(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	// File does not exist → no-op, no error
	missing := filepath.Join(home, ".claude", "settings.json")
	assert.NoError(t, cm.RevertLLMGateway(ClaudeCode, missing))
}

func TestRevertLLMGateway_MissingDir(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	// Neither the dir nor the file exist → no-op, no error
	missing := filepath.Join(home, ".no-such-dir", "settings.json")
	assert.NoError(t, cm.RevertLLMGateway(ClaudeCode, missing))
}

func TestRevertLLMGateway_EmptyFile(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte{}, 0o600))

	assert.NoError(t, cm.RevertLLMGateway(ClaudeCode, settingsPath))
}

func TestRevertLLMGateway_UnsupportedClient(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, nil, nil)

	err := cm.RevertLLMGateway(ClaudeCode, "/some/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support LLM gateway")
}

// ── proxy-mode (nested key) ───────────────────────────────────────────────────

func TestConfigureLLMGateway_ProxyMode(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, Cursor, "proxy", ".cursor-test", []string{"/github.copilot.advanced.serverUrl", "/github.copilot.advanced.apiKey"},
		[]string{"ProxyBaseURL", "PlaceholderAPIKey"})

	dir := filepath.Join(home, ".cursor-test")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	path, err := cm.ConfigureLLMGateway(Cursor, llmgateway.ApplyConfig{
		ProxyBaseURL: "http://localhost:14000/v1",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	serverURL, okURL := jsonPointerGet(data, "/github.copilot.advanced.serverUrl")
	assert.True(t, okURL, "/github.copilot.advanced.serverUrl pointer must be present")
	assert.Equal(t, "http://localhost:14000/v1", serverURL)
	apiKey, okKey := jsonPointerGet(data, "/github.copilot.advanced.apiKey")
	assert.True(t, okKey, "/github.copilot.advanced.apiKey pointer must be present")
	assert.Equal(t, "thv-proxy", apiKey)
}

// ── DetectedLLMGatewayClients ─────────────────────────────────────────────────

func TestDetectedLLMGatewayClients_Codex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configDir   bool
		binary      bool
		desktop     bool
		detectorErr error
		nilDetector bool
		want        bool
	}{
		{name: "CLI only", configDir: true, binary: true, want: true},
		{name: "app only without config directory or PATH", desktop: true, want: true},
		{name: "CLI and app append once", configDir: true, binary: true, desktop: true, want: true},
		{name: "stale settings only", configDir: true},
		{name: "no CLI or desktop evidence"},
		{name: "nil desktop detector is not detected", nilDetector: true},
		{name: "detector failure is not detected", detectorErr: errors.New("query failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cfgs := []clientAppConfig{{
				ClientType:         Codex,
				LLMGatewayMode:     llmgateway.ModeCodexAuth,
				LLMBinaryName:      "codex",
				LLMSettingsFile:    "config.toml",
				LLMSettingsRelPath: []string{".codex"},
			}}
			if !tt.nilDetector {
				cfgs[0].LLMInstalledDetector = func() (bool, error) { return tt.desktop, tt.detectorErr }
			}
			if tt.configDir {
				require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o700))
			}
			cm := NewTestClientManager(home, nil, cfgs, nil)
			cm.lookPath = func(_ string) (string, error) {
				if tt.binary {
					return "/bin/codex", nil
				}
				return "", os.ErrNotExist
			}

			detected := cm.DetectedLLMGatewayClients()
			if tt.want {
				require.Equal(t, []ClientApp{Codex}, detected)
			} else {
				assert.Empty(t, detected)
			}
		})
	}
}

// TestDetectedLLMGatewayClients_DirOnly verifies that a client without a
// BinaryName set is detected based solely on the settings directory existing.
func TestDetectedLLMGatewayClients_DirOnly(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	cfgs := LLMTestIntegrations([]LLMTestEntry{{
		ClientType:   ClaudeCode,
		Mode:         "direct",
		SettingsDir:  []string{".claude"},
		SettingsFile: "settings.json",
		JSONPointers: []string{"/apiKeyHelper"},
		ValueFields:  []string{"TokenHelperCommand"},
	}})
	// LLMBinaryName is intentionally left empty — dir check only.
	cm := NewTestClientManager(home, nil, cfgs, nil)

	// Directory absent → not detected.
	require.Empty(t, cm.DetectedLLMGatewayClients())

	// Create the settings directory → now detected.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	detected := cm.DetectedLLMGatewayClients()
	require.Len(t, detected, 1)
	assert.Equal(t, ClaudeCode, detected[0])
}

// TestDetectedLLMGatewayClients_BinaryAndDirExist verifies that a client is
// detected when both the settings directory and the binary are present.
func TestDetectedLLMGatewayClients_BinaryAndDirExist(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	cfgs := LLMTestIntegrations([]LLMTestEntry{{
		ClientType:   GeminiCli,
		Mode:         "direct",
		SettingsDir:  []string{".gemini"},
		SettingsFile: "settings.json",
		JSONPointers: []string{"/baseUrl"},
		ValueFields:  []string{"GatewayURL"},
	}})
	cfgs[0].LLMBinaryName = fakeLLMBinary
	cm := NewTestClientManager(home, nil, cfgs, nil)
	// Inject a lookPath that reports the fake binary as found.
	cm.lookPath = func(name string) (string, error) { return "/usr/local/bin/" + name, nil }

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini"), 0o700))

	detected := cm.DetectedLLMGatewayClients()
	require.Len(t, detected, 1)
	assert.Equal(t, GeminiCli, detected[0])
}

// TestDetectedLLMGatewayClients_DirExistsButBinaryAbsent verifies that a
// client is NOT detected when the settings directory exists but the binary is
// absent from $PATH — the false-positive case the fix addresses.
func TestDetectedLLMGatewayClients_DirExistsButBinaryAbsent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	cfgs := LLMTestIntegrations([]LLMTestEntry{{
		ClientType:   ClaudeCode,
		Mode:         "direct",
		SettingsDir:  []string{".claude"},
		SettingsFile: "settings.json",
		JSONPointers: []string{"/apiKeyHelper"},
		ValueFields:  []string{"TokenHelperCommand"},
	}})
	cfgs[0].LLMBinaryName = fakeLLMBinary
	cm := NewTestClientManager(home, nil, cfgs, nil)
	// Inject a lookPath that always reports the binary as missing.
	cm.lookPath = func(_ string) (string, error) { return "", os.ErrNotExist }

	// Create the settings directory to simulate a leftover install.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))

	// Should NOT be detected because the binary is not on $PATH.
	assert.Empty(t, cm.DetectedLLMGatewayClients())
}

// TestDetectedLLMGatewayClients_NeitherDirNorBinary verifies that a client is
// not detected when neither the directory nor the binary are present.
func TestDetectedLLMGatewayClients_NeitherDirNorBinary(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	cfgs := LLMTestIntegrations([]LLMTestEntry{{
		ClientType:   ClaudeCode,
		Mode:         "direct",
		SettingsDir:  []string{".claude"},
		SettingsFile: "settings.json",
		JSONPointers: []string{"/apiKeyHelper"},
		ValueFields:  []string{"TokenHelperCommand"},
	}})
	cfgs[0].LLMBinaryName = fakeLLMBinary
	cm := NewTestClientManager(home, nil, cfgs, nil)
	cm.lookPath = func(_ string) (string, error) { return "", os.ErrNotExist }

	assert.Empty(t, cm.DetectedLLMGatewayClients())
}

func TestDetectedLLMGatewayClients_GUIEditorsIgnoreMissingBinary(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cm.lookPath = func(_ string) (string, error) { return "", os.ErrNotExist }

	for _, clientType := range []ClientApp{VSCode, VSCodeInsider, Cursor} {
		cfg := cm.lookupClientAppConfig(clientType)
		require.NotNil(t, cfg)
		require.Empty(t, cfg.LLMBinaryName)
		require.NoError(t, os.MkdirAll(filepath.Dir(cm.buildLLMSettingsPath(cfg)), 0o700))
	}

	detected := cm.DetectedLLMGatewayClients()
	assert.Contains(t, detected, VSCode)
	assert.Contains(t, detected, VSCodeInsider)
	assert.Contains(t, detected, Cursor)
}

func TestDetectedLLMGatewayClients_CLILeftoverDirStillRequiresBinary(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cm.lookPath = func(_ string) (string, error) { return "", os.ErrNotExist }

	cfg := cm.lookupClientAppConfig(ClaudeCode)
	require.NotNil(t, cfg)
	require.NoError(t, os.MkdirAll(filepath.Dir(cm.buildLLMSettingsPath(cfg)), 0o700))

	assert.NotContains(t, cm.DetectedLLMGatewayClients(), ClaudeCode)
	assert.Contains(t, cm.LLMClientDetectionHint(ClaudeCode), `was not found on PATH`)
}

func TestLLMClientDetectionHint_MissingDirIsSilent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cm.lookPath = func(_ string) (string, error) { return "", os.ErrNotExist }

	assert.Empty(t, cm.LLMClientDetectionHint(ClaudeCode))
	assert.Empty(t, cm.LLMClientDetectionHint(Cursor))
}

// TestRealClientConfigs_LLMBinaryNames asserts the expected binary name for
// every LLM-gateway-capable entry in supportedClientIntegrations. GUI editors
// must stay empty: a PATH binary check is a false negative when the app is
// installed as a desktop editor. CLI tools keep the binary check so leftover
// config directories do not count as installed.
func TestRealClientConfigs_LLMBinaryNames(t *testing.T) {
	t.Parallel()

	wantBinary := map[ClientApp]string{
		ClaudeCode: "claude",
		GeminiCli:  "gemini",
		Codex:      "codex",
	}
	wantEmpty := []ClientApp{VSCode, VSCodeInsider, Cursor, ClientApp(Xcode)}

	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)

	for clientType, want := range wantBinary {
		t.Run(string(clientType), func(t *testing.T) {
			t.Parallel()
			cfg := cm.lookupClientAppConfig(clientType)
			require.NotNil(t, cfg, "missing entry in supportedClientIntegrations for %s", clientType)
			assert.Equal(t, want, cfg.LLMBinaryName,
				"wrong LLMBinaryName for %s: detection will fail on machines that only have the expected binary", clientType)
		})
	}
	for _, clientType := range wantEmpty {
		t.Run(string(clientType)+"/dir-only", func(t *testing.T) {
			t.Parallel()
			cfg := cm.lookupClientAppConfig(clientType)
			require.NotNil(t, cfg, "missing entry in supportedClientIntegrations for %s", clientType)
			assert.Empty(t, cfg.LLMBinaryName,
				"%s is a GUI editor and must not require a PATH binary", clientType)
		})
	}
}

// ── TLSSkipVerify / NodeTLSRejectUnauthorized / ClearWhenEmpty ───────────────

func newTLSTestManager(t *testing.T) (*ClientManager, string) {
	t.Helper()
	home := t.TempDir()
	cfgs := LLMTestIntegrations([]LLMTestEntry{{
		ClientType:     ClaudeCode,
		Mode:           "direct",
		SettingsDir:    []string{".claude"},
		SettingsFile:   "settings.json",
		JSONPointers:   []string{"/apiKeyHelper", "/env/NODE_TLS_REJECT_UNAUTHORIZED"},
		ValueFields:    []string{"TokenHelperCommand", "NodeTLSRejectUnauthorized"},
		ClearWhenEmpty: []bool{false, true},
	}})
	return NewTestClientManager(home, nil, cfgs, nil), home
}

func TestConfigureLLMGateway_TLSSkipVerify_WritesNodeEnv(t *testing.T) {
	t.Parallel()
	cm, home := newTLSTestManager(t)

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	_, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		TokenHelperCommand: `thv llm token`,
		TLSSkipVerify:      true,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	val, ok := jsonPointerGet(data, "/env/NODE_TLS_REJECT_UNAUTHORIZED")
	assert.True(t, ok, "/env/NODE_TLS_REJECT_UNAUTHORIZED must be present when TLSSkipVerify=true")
	assert.Equal(t, "0", val)
}

func TestConfigureLLMGateway_TLSSkipVerify_NotSet_DoesNotWriteNodeEnv(t *testing.T) {
	t.Parallel()
	cm, home := newTLSTestManager(t)

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	_, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		TokenHelperCommand: `thv llm token`,
		TLSSkipVerify:      false,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	_, ok := jsonPointerGet(data, "/env/NODE_TLS_REJECT_UNAUTHORIZED")
	assert.False(t, ok, "/env/NODE_TLS_REJECT_UNAUTHORIZED must not be written when TLSSkipVerify=false")
}

func TestConfigureLLMGateway_TLSSkipVerify_ClearRemovesKey(t *testing.T) {
	t.Parallel()
	cm, home := newTLSTestManager(t)

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	// First run: set tls-skip-verify
	_, err := cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		TokenHelperCommand: `thv llm token`,
		TLSSkipVerify:      true,
	})
	require.NoError(t, err)

	settingsPath := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	_, present := jsonPointerGet(data, "/env/NODE_TLS_REJECT_UNAUTHORIZED")
	require.True(t, present, "/env/NODE_TLS_REJECT_UNAUTHORIZED must be present after first configure")

	// Second run: clear tls-skip-verify
	_, err = cm.ConfigureLLMGateway(ClaudeCode, llmgateway.ApplyConfig{
		TokenHelperCommand: `thv llm token`,
		TLSSkipVerify:      false,
	})
	require.NoError(t, err)

	data, err = os.ReadFile(settingsPath)
	require.NoError(t, err)
	_, present = jsonPointerGet(data, "/env/NODE_TLS_REJECT_UNAUTHORIZED")
	assert.False(t, present, "/env/NODE_TLS_REJECT_UNAUTHORIZED must be removed when TLSSkipVerify is cleared")
}

// TestRealClientConfigs_GeminiCLI_NeverWritesTLSKey verifies that
// NODE_TLS_REJECT_UNAUTHORIZED is never written for Gemini CLI regardless of
// TLSSkipVerify. In proxy mode the tool connects to localhost over plain HTTP,
// so setting the env var would only globally suppress TLS for other HTTPS
// requests — an unacceptable side-effect. The key spec is intentionally absent.
func TestRealClientConfigs_GeminiCLI_NeverWritesTLSKey(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini"), 0o700))

	for _, skipVerify := range []bool{false, true} {
		path, err := cm.ConfigureLLMGateway(GeminiCli, llmgateway.ApplyConfig{
			ProxyBaseURL:  "http://localhost:14000/v1",
			TLSSkipVerify: skipVerify,
		})
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		_, ok := jsonPointerGet(data, "/env/NODE_TLS_REJECT_UNAUTHORIZED")
		assert.False(t, ok, "TLS key must never be written for Gemini CLI (TLSSkipVerify=%v)", skipVerify)
	}
}

// ── llmValueForSpec unit tests ────────────────────────────────────────────────

func TestLLMValueForSpec(t *testing.T) {
	t.Parallel()

	cfg := llmgateway.ApplyConfig{
		GatewayURL:         "https://gw.example.com",
		ProxyBaseURL:       "http://localhost:14000/v1",
		TokenHelperCommand: `thv llm token`,
		TLSSkipVerify:      false,
	}

	cases := []struct {
		name       string
		valueField string
		cfg        llmgateway.ApplyConfig
		want       string
		wantErr    bool
	}{
		// Known ValueField names resolve correctly
		{name: "GatewayURL", valueField: "GatewayURL", cfg: cfg, want: "https://gw.example.com"},
		{name: "ProxyBaseURL", valueField: "ProxyBaseURL", cfg: cfg, want: "http://localhost:14000/v1"},
		{name: "TokenHelperCommand", valueField: "TokenHelperCommand", cfg: cfg, want: `thv llm token`},
		{name: "PlaceholderAPIKey", valueField: "PlaceholderAPIKey", cfg: cfg, want: "thv-proxy"},
		// NodeTLSRejectUnauthorized: "0" when set, "" when clear
		{name: "NodeTLSRejectUnauthorized/skip=false", valueField: "NodeTLSRejectUnauthorized", cfg: cfg, want: ""},
		{name: "NodeTLSRejectUnauthorized/skip=true", valueField: "NodeTLSRejectUnauthorized", cfg: llmgateway.ApplyConfig{TLSSkipVerify: true}, want: "0"},
		// ProxyOrigin strips path, query, and fragment from ProxyBaseURL
		{name: "ProxyOrigin/strips_path", valueField: "ProxyOrigin", cfg: cfg, want: "http://localhost:14000"},
		{name: "ProxyOrigin/long_path", valueField: "ProxyOrigin", cfg: llmgateway.ApplyConfig{ProxyBaseURL: "http://localhost:9000/v1beta/openai"}, want: "http://localhost:9000"},
		{name: "ProxyOrigin/strips_query_and_fragment", valueField: "ProxyOrigin", cfg: llmgateway.ApplyConfig{ProxyBaseURL: "http://host:8080/path?q=1#frag"}, want: "http://host:8080"},
		// ForceQuery: trailing "?" with no key must not leak into the origin.
		{name: "ProxyOrigin/force_query", valueField: "ProxyOrigin", cfg: llmgateway.ApplyConfig{ProxyBaseURL: "http://host:8080/path?"}, want: "http://host:8080"},
		// ProxyOrigin falls back to the raw value when URL parsing fails
		{name: "ProxyOrigin/invalid_url_fallback", valueField: "ProxyOrigin", cfg: llmgateway.ApplyConfig{ProxyBaseURL: "::invalid"}, want: "::invalid"},
		// Unknown ValueField names are programming errors and must return an error
		{name: "unknown_ValueField/typo", valueField: "GatwayURL", cfg: cfg, wantErr: true},
		{name: "unknown_ValueField/arbitrary", valueField: "gemini-api-key", cfg: cfg, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := llmValueForSpec(tc.valueField, tc.cfg)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// countSubstring counts non-overlapping occurrences of substr in s.
func countSubstring(s, substr string) int {
	count := 0
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			count++
			i += len(substr) - 1
		}
	}
	return count
}

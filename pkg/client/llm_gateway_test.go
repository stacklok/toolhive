// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── real production configs ───────────────────────────────────────────────────

// TestRealClientConfigs_ConfigureAndRevert exercises ConfigureLLMGateway and
// RevertLLMGateway against every entry in supportedClientIntegrations that has
// LLMGatewayMode set. This catches typos in JSON pointer paths, wrong
// ValueField names, or missing intermediate-object creation in the real config
// table — none of which are caught by tests that use fake clientAppConfig
// entries via LLMTestIntegrations.
func TestRealClientConfigs_ConfigureAndRevert(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Use real supportedClientIntegrations so we exercise the actual paths and keys.
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)

	applyCfg := LLMApplyConfig{
		GatewayURL:         "https://gw.example.com",
		ProxyBaseURL:       "http://localhost:14000/v1",
		TokenHelperCommand: `"thv" llm token`,
	}

	// expectedKeys lists substrings that must appear in the settings file after
	// Configure, and must NOT appear after Revert. Each entry maps to one real
	// clientAppConfig in supportedClientIntegrations.
	cases := []struct {
		clientType   ClientApp
		expectedKeys []string
	}{
		{
			// ~/.claude/settings.json
			clientType: ClaudeCode,
			expectedKeys: []string{
				"apiKeyHelper",
				"ANTHROPIC_BASE_URL", "https://gw.example.com",
			},
		},
		{
			// ~/.gemini/settings.json
			clientType: GeminiCli,
			expectedKeys: []string{
				"tokenCommand", "baseUrl", "https://gw.example.com",
			},
		},
		{
			// ~/.<platform>/Cursor/User/settings.json
			clientType: Cursor,
			expectedKeys: []string{
				"cursor.general.openAIBaseURL", "http://localhost:14000/v1",
				"cursor.general.openAIAPIKey", "thv-proxy",
			},
		},
		{
			// ~/.<platform>/Code/User/settings.json
			clientType: VSCode,
			expectedKeys: []string{
				"github.copilot.advanced.serverUrl", "http://localhost:14000/v1",
				"github.copilot.advanced.apiKey", "thv-proxy",
			},
		},
		{
			// ~/.<platform>/Code - Insiders/User/settings.json
			clientType: VSCodeInsider,
			expectedKeys: []string{
				"github.copilot.advanced.serverUrl", "http://localhost:14000/v1",
				"github.copilot.advanced.apiKey", "thv-proxy",
			},
		},
		{
			// ~/Library/Application Support/GitHub Copilot for Xcode/editorSettings.json
			clientType: Xcode,
			expectedKeys: []string{
				"openAIBaseURL", "http://localhost:14000/v1",
				"apiKey", "thv-proxy",
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

			// Configure and verify all expected keys appear.
			path, err := cm.ConfigureLLMGateway(tc.clientType, applyCfg)
			require.NoError(t, err)

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, key := range tc.expectedKeys {
				assert.Contains(t, string(data), key,
					"key %q missing after Configure for %s", key, tc.clientType)
			}

			// Revert and verify all expected keys are gone.
			require.NoError(t, cm.RevertLLMGateway(tc.clientType, path))

			data, err = os.ReadFile(path)
			require.NoError(t, err)
			for _, key := range tc.expectedKeys {
				assert.NotContains(t, string(data), key,
					"key %q still present after Revert for %s", key, tc.clientType)
			}
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

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

	path, err := cm.ConfigureLLMGateway(ClaudeCode, LLMApplyConfig{
		GatewayURL: "https://gw.example.com",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"a"`, "outer ancestor object must be created")
	assert.Contains(t, s, `"b"`, "inner ancestor object must be created")
	assert.Contains(t, s, `"c"`, "leaf key must be written")
	assert.Contains(t, s, "https://gw.example.com", "leaf value must be written")
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

	path, err := cm.ConfigureLLMGateway(ClaudeCode, LLMApplyConfig{
		TokenHelperCommand: `"thv" llm token`,
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(claudeDir, "settings.json"), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "apiKeyHelper")
	assert.Contains(t, string(data), "thv")
	assert.Contains(t, string(data), "llm token")
}

func TestConfigureLLMGateway_PreservesExistingKeys(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	// pre-populate with an existing key that should survive
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{"permissions":{"allow":["read"]}}`), 0o600))

	_, err := cm.ConfigureLLMGateway(ClaudeCode, LLMApplyConfig{
		TokenHelperCommand: `"thv" llm token`,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "permissions")  // original key preserved
	assert.Contains(t, string(data), "apiKeyHelper") // new key added
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

	_, err := cm.ConfigureLLMGateway(ClaudeCode, LLMApplyConfig{
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

	_, err := cm.ConfigureLLMGateway(ClaudeCode, LLMApplyConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support LLM gateway")
}

func TestConfigureLLMGateway_Idempotent(t *testing.T) {
	t.Parallel()
	cm, home := newLLMManager(t, ClaudeCode, "direct", ".claude", []string{"/apiKeyHelper"}, []string{"TokenHelperCommand"})

	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	cfg := LLMApplyConfig{TokenHelperCommand: `"thv" llm token`}
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
	assert.NotContains(t, string(data), "apiKeyHelper")
	assert.Contains(t, string(data), "permissions") // unrelated key survives
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

	path, err := cm.ConfigureLLMGateway(Cursor, LLMApplyConfig{
		ProxyBaseURL: "http://localhost:14000/v1",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "serverUrl")
	assert.Contains(t, string(data), "http://localhost:14000/v1")
	assert.Contains(t, string(data), "apiKey")
	assert.Contains(t, string(data), "thv-proxy")
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

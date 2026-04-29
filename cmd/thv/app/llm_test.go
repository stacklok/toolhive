// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/llm"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// tempProvider writes cfg to a temporary config file and returns a
// config.PathProvider backed by that file.
func tempProvider(t *testing.T, cfg *config.Config) config.Provider {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return config.NewPathProvider(path)
}

// llmProvider is a shorthand for tempProvider with an LLM-configured Config.
func llmProvider(t *testing.T, llmCfg llm.Config) config.Provider {
	t.Helper()
	c := &config.Config{}
	c.LLM = llmCfg
	return tempProvider(t, c)
}

// errOnUpdateProvider wraps a base Provider but returns a fixed error from
// UpdateConfig. Used to inject deterministic failures without relying on
// filesystem permission tricks that are unreliable on Windows.
type errOnUpdateProvider struct {
	config.Provider
	cfg       *config.Config
	updateErr error
}

func (p *errOnUpdateProvider) GetConfig() *config.Config { return p.cfg }
func (p *errOnUpdateProvider) UpdateConfig(_ func(*config.Config) error) error {
	return p.updateErr
}

// ── runLLMSetup ───────────────────────────────────────────────────────────────

func TestRunLLMSetup_NotConfigured(t *testing.T) {
	t.Parallel()
	// Empty Config → LLM.IsConfigured() == false → error before touching files.
	dir := t.TempDir()
	cm := client.NewTestClientManager(dir, nil, nil, nil)
	provider := llmProvider(t, llm.Config{}) // no gateway URL

	var stdout, stderr bytes.Buffer
	err := runLLMSetup(context.Background(), &stdout, &stderr, cm, provider, func(_ context.Context, _ *llm.Config) error { return nil }, llm.SetOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestRunLLMSetup_NoDetectedTools(t *testing.T) {
	t.Parallel()
	// LLM is configured but no tool settings dirs exist on disk → silent no-op.
	dir := t.TempDir()

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)
	provider := llmProvider(t, llm.Config{
		GatewayURL: "https://gw.example.com",
		OIDC:       llm.OIDCConfig{Issuer: "https://auth.example.com", ClientID: "id"},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMSetup(context.Background(), &stdout, &stderr, cm, provider, func(_ context.Context, _ *llm.Config) error { return nil }, llm.SetOptions{})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "No supported AI tools detected")
}

func TestRunLLMSetup_PartialFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission-based failure injection is not reliable on Windows")
	}
	// Two tools detected; claude-code directory is read-only (Apply fails).
	// gemini-cli directory is writable (Apply succeeds) and must be persisted.
	dir := t.TempDir()

	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o500)) // no write
	geminiDir := filepath.Join(dir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0o700))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
		{
			ClientType:   client.GeminiCli,
			Mode:         "direct",
			SettingsDir:  []string{".gemini"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/baseUrl"},
			ValueFields:  []string{"GatewayURL"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)
	provider := llmProvider(t, llm.Config{
		GatewayURL: "https://gw.example.com",
		OIDC:       llm.OIDCConfig{Issuer: "https://auth.example.com", ClientID: "id"},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMSetup(context.Background(), &stdout, &stderr, cm, provider, func(_ context.Context, _ *llm.Config) error { return nil }, llm.SetOptions{})
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "Warning: failed to configure claude-code")
	assert.Contains(t, stdout.String(), "Configured gemini-cli")
}

func TestRunLLMSetup_RollbackOnConfigUpdateFailure(t *testing.T) {
	t.Parallel()
	// Apply succeeds but UpdateConfig fails (injected stub error, cross-platform).
	// Revert must be called so the settings file is left clean.
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)

	c := &config.Config{}
	c.LLM = llm.Config{
		GatewayURL: "https://gw.example.com",
		OIDC:       llm.OIDCConfig{Issuer: "https://auth.example.com", ClientID: "id"},
	}
	provider := &errOnUpdateProvider{cfg: c, updateErr: errors.New("disk full")}

	var stdout, stderr bytes.Buffer
	err := runLLMSetup(context.Background(), &stdout, &stderr, cm, provider, func(_ context.Context, _ *llm.Config) error { return nil }, llm.SetOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisting tool configuration")

	// Rollback must have removed the patched key from the settings file.
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if data, readErr := os.ReadFile(settingsPath); readErr == nil {
		assert.NotContains(t, string(data), "apiKeyHelper",
			"rollback must remove the patched key")
	}
}

func TestRunLLMSetup_RollbackBothToolsOnConfigUpdateFailure(t *testing.T) {
	t.Parallel()
	// Two tools configured successfully, then UpdateConfig fails.
	// Both settings files must be reverted so neither is left in a patched state.
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	geminiDir := filepath.Join(dir, ".gemini")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	require.NoError(t, os.MkdirAll(geminiDir, 0o700))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
		{
			ClientType:   client.GeminiCli,
			Mode:         "direct",
			SettingsDir:  []string{".gemini"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/baseUrl"},
			ValueFields:  []string{"GatewayURL"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)

	c := &config.Config{}
	c.LLM = llm.Config{
		GatewayURL: "https://gw.example.com",
		OIDC:       llm.OIDCConfig{Issuer: "https://auth.example.com", ClientID: "id"},
	}
	provider := &errOnUpdateProvider{cfg: c, updateErr: errors.New("disk full")}

	var stdout, stderr bytes.Buffer
	err := runLLMSetup(context.Background(), &stdout, &stderr, cm, provider, func(_ context.Context, _ *llm.Config) error { return nil }, llm.SetOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisting tool configuration")

	// Both settings files must have been rolled back.
	for _, tc := range []struct {
		dir, key string
	}{
		{claudeDir, "apiKeyHelper"},
		{geminiDir, "baseUrl"},
	} {
		settingsPath := filepath.Join(tc.dir, "settings.json")
		if data, readErr := os.ReadFile(settingsPath); readErr == nil {
			assert.NotContains(t, string(data), tc.key,
				"rollback must remove %q from %s", tc.key, settingsPath)
		}
	}
}

func TestRunLLMSetup_LoginFailureLeavesNoState(t *testing.T) {
	t.Parallel()
	// Login returns an error — no tool config files should be touched and no
	// ConfiguredTools entry should be persisted.
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)
	provider := llmProvider(t, llm.Config{
		GatewayURL: "https://gw.example.com",
		OIDC:       llm.OIDCConfig{Issuer: "https://auth.example.com", ClientID: "id"},
	})

	loginErr := errors.New("auth server unreachable")
	var stdout, stderr bytes.Buffer
	err := runLLMSetup(context.Background(), &stdout, &stderr, cm, provider,
		func(_ context.Context, _ *llm.Config) error { return loginErr },
		llm.SetOptions{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC login failed")

	// No tool config file should have been created or modified.
	settingsPath := filepath.Join(claudeDir, "settings.json")
	_, statErr := os.Stat(settingsPath)
	assert.True(t, os.IsNotExist(statErr), "settings.json must not exist after login failure")

	// ConfiguredTools must remain empty.
	cfg := provider.GetConfig()
	assert.Empty(t, cfg.LLM.ConfiguredTools, "ConfiguredTools must not be persisted after login failure")
}

// ── runLLMTeardown ────────────────────────────────────────────────────────────

func TestRunLLMTeardown_NoConfiguredTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cm := client.NewTestClientManager(dir, nil, nil, nil)
	provider := llmProvider(t, llm.Config{}) // no configured tools

	var stdout, stderr bytes.Buffer
	err := runLLMTeardown(context.Background(), &stdout, &stderr, cm, nil, false, provider)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "No tools are currently configured")
}

func TestRunLLMTeardown_UnknownTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cm := client.NewTestClientManager(dir, nil, nil, nil)
	provider := llmProvider(t, llm.Config{
		ConfiguredTools: []llm.ToolConfig{{Tool: "cursor", ConfigPath: "/x"}},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMTeardown(context.Background(), &stdout, &stderr, cm, []string{"unknown-tool"}, false, provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"unknown-tool" is not configured`)
}

func TestRunLLMTeardown_AllTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	geminiDir := filepath.Join(dir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0o700))
	settingsPath := filepath.Join(geminiDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath,
		[]byte(`{"baseUrl":"https://gw.example.com"}`), 0o600))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.GeminiCli,
			Mode:         "direct",
			SettingsDir:  []string{".gemini"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/baseUrl"},
			ValueFields:  []string{"GatewayURL"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)
	provider := llmProvider(t, llm.Config{
		ConfiguredTools: []llm.ToolConfig{
			{Tool: "gemini-cli", Mode: "direct", ConfigPath: settingsPath},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMTeardown(context.Background(), &stdout, &stderr, cm, nil, false, provider)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "Reverted gemini-cli")

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "baseUrl")
}

func TestRunLLMTeardown_ConfigUpdateFailureLeavesFilesUntouched(t *testing.T) {
	t.Parallel()
	// UpdateConfig fails → tool config files must NOT be modified, so the state
	// remains consistent (config still lists the tool, file still configured).
	dir := t.TempDir()

	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	claudePath := filepath.Join(claudeDir, "settings.json")
	originalContent := `{"apiKeyHelper":"thv llm token"}`
	require.NoError(t, os.WriteFile(claudePath, []byte(originalContent), 0o600))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)

	c := &config.Config{}
	c.LLM = llm.Config{
		ConfiguredTools: []llm.ToolConfig{
			{Tool: "claude-code", Mode: "direct", ConfigPath: claudePath},
		},
	}
	provider := &errOnUpdateProvider{cfg: c, updateErr: errors.New("disk full")}

	var stdout, stderr bytes.Buffer
	err := runLLMTeardown(context.Background(), &stdout, &stderr, cm, nil, false, provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisting tool configuration")

	// The settings file must be untouched because UpdateConfig failed before
	// any revert was attempted.
	data, err := os.ReadFile(claudePath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, string(data),
		"tool config file must not be modified when UpdateConfig fails")
}

func TestRunLLMTeardown_SingleTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	claudePath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(claudePath,
		[]byte(`{"apiKeyHelper":"thv llm token"}`), 0o600))

	cfgs := client.LLMTestIntegrations([]client.LLMTestEntry{
		{
			ClientType:   client.ClaudeCode,
			Mode:         "direct",
			SettingsDir:  []string{".claude"},
			SettingsFile: "settings.json",
			JSONPointers: []string{"/apiKeyHelper"},
			ValueFields:  []string{"TokenHelperCommand"},
		},
	})
	cm := client.NewTestClientManager(dir, nil, cfgs, nil)
	provider := llmProvider(t, llm.Config{
		ConfiguredTools: []llm.ToolConfig{
			{Tool: "claude-code", Mode: "direct", ConfigPath: claudePath},
			{Tool: "cursor", Mode: "proxy", ConfigPath: "/some/cursor/path"},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMTeardown(context.Background(), &stdout, &stderr, cm, []string{"claude-code"}, false, provider)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "Reverted claude-code")

	data, err := os.ReadFile(claudePath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "apiKeyHelper")
}

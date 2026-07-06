// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// newClaudeDesktopManager returns a ClientManager rooted at a temp home with the
// real Claude Desktop integration, plus the resolved configLibrary metaPath.
func newClaudeDesktopManager(t *testing.T) (*ClientManager, string) {
	t.Helper()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cfg := cm.lookupClientAppConfig(ClientApp(ClaudeDesktop))
	require.NotNil(t, cfg, "ClaudeDesktop must be a supported integration")
	require.Equal(t, credentialHelperMode, cfg.LLMGatewayMode, "ClaudeDesktop must use the credential-helper model")
	return cm, cm.buildLLMSettingsPath(cfg)
}

// readMeta decodes _meta.json.
func readMeta(t *testing.T, metaPath string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(metaPath) // #nosec G304 -- test-controlled path
	require.NoError(t, err)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(data, &meta))
	return meta
}

// readConfigDoc decodes a <uuid>.json config document.
func readConfigDoc(t *testing.T, path string) claudeDesktopConfig {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-controlled path
	require.NoError(t, err)
	var doc claudeDesktopConfig
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

func claudeDesktopApplyCfg() llmgateway.ApplyConfig {
	return llmgateway.ApplyConfig{
		GatewayURL:         "https://gw.example.com",
		AnthropicBaseURL:   "https://gw.example.com/anthropic",
		TokenHelperCommand: `"thv" llm token`,
	}
}

func TestConfigureCredentialHelper_WritesConfigMetaAndShim(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	cfg := claudeDesktopApplyCfg()
	cfg.Models = []string{"claude-opus-4-8", "claude-sonnet-4-6"}

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), cfg)
	require.NoError(t, err)

	// Config document contents.
	doc := readConfigDoc(t, configPath)
	assert.Equal(t, "gateway", doc.InferenceProvider)
	assert.Equal(t, "helper-script", doc.InferenceCredentialKind)
	assert.Equal(t, "bearer", doc.InferenceGatewayAuthScheme)
	assert.Equal(t, "https://gw.example.com/anthropic", doc.InferenceGatewayBaseURL)
	assert.Equal(t, []string{"claude-opus-4-8", "claude-sonnet-4-6"}, doc.InferenceModels)
	assert.Equal(t, int(llmgateway.ClaudeDesktopHelperTTL.Seconds()), doc.InferenceCredentialHelperTtlSec)
	assert.Equal(t, int(llmgateway.ClaudeDesktopHelperTimeout.Seconds()), doc.InferenceCredentialHelperTimeoutSec)

	// Shim: executable, references the token command, and silent contexts skip
	// the browser.
	shimPath := cm.credentialHelperShimPath()
	assert.Equal(t, shimPath, doc.InferenceCredentialHelper)
	info, err := os.Stat(shimPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	shim, err := os.ReadFile(shimPath) // #nosec G304 -- test-controlled path
	require.NoError(t, err)
	assert.Contains(t, string(shim), `"thv" llm token`)
	assert.Contains(t, string(shim), "--skip-browser")

	// _meta.json selects our config by the config document's id.
	id := strings.TrimSuffix(filepath.Base(configPath), ".json")
	meta := readMeta(t, metaPath)
	assert.Equal(t, id, meta["appliedId"])
	assert.Equal(t, id, metaEntryID(meta, claudeDesktopManagedEntryName))
}

func TestConfigureCredentialHelper_OmitsModelsWhenEmpty(t *testing.T) {
	t.Parallel()
	cm, _ := newClaudeDesktopManager(t)

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)

	// The key must be absent (not an empty array) so Claude Desktop falls back to
	// gateway-side auto-discovery.
	data, err := os.ReadFile(configPath) // #nosec G304 -- test-controlled path
	require.NoError(t, err)
	assert.NotContains(t, string(data), "inferenceModels")
}

func TestConfigureCredentialHelper_IsIdempotent(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	first, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)
	second, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)

	// Same stable id reused — no orphaned config documents or duplicate entries.
	assert.Equal(t, first, second, "repeated setup must reuse the same config id")
	meta := readMeta(t, metaPath)
	assert.Len(t, metaEntries(meta), 1, "repeated setup must not duplicate the ToolHive entry")
}

func TestConfigureCredentialHelper_PreservesForeignEntries(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	// Seed a user-owned config the way Claude Desktop's own UI would.
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o700))
	seed := map[string]any{
		"appliedId": "user-config",
		"entries": []any{
			map[string]any{"id": "user-config", "name": "My Bedrock"},
		},
	}
	seedBytes, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metaPath, seedBytes, 0o600))

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)

	meta := readMeta(t, metaPath)
	// Our entry is added and selected; the user's entry survives untouched.
	id := strings.TrimSuffix(filepath.Base(configPath), ".json")
	assert.Equal(t, id, meta["appliedId"])
	assert.Len(t, metaEntries(meta), 2)
	assert.Equal(t, "user-config", metaEntryID(meta, "My Bedrock"))
}

func TestRevertCredentialHelper_RemovesEntryConfigAndShim(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)
	shimPath := cm.credentialHelperShimPath()

	require.NoError(t, cm.RevertLLMGateway(ClientApp(ClaudeDesktop), configPath))

	// Config document and shim are gone; entry removed; appliedId cleared because
	// it pointed at our config.
	assert.NoFileExists(t, configPath)
	assert.NoFileExists(t, shimPath)
	meta := readMeta(t, metaPath)
	assert.Empty(t, metaEntries(meta))
	assert.Equal(t, "", meta["appliedId"])
}

func TestConfigureCredentialHelper_RejectsPathTraversalID(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)
	dir := filepath.Dir(metaPath)

	// Seed _meta.json with a ToolHive-named entry whose id escapes configLibrary,
	// as a corrupted/hand-edited file might.
	require.NoError(t, os.MkdirAll(dir, 0o700))
	seed := map[string]any{
		"appliedId": "../../evil",
		"entries": []any{
			map[string]any{"id": "../../evil", "name": claudeDesktopManagedEntryName},
		},
	}
	seedBytes, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metaPath, seedBytes, 0o600))

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)

	// The tainted id must be rejected: the written config stays inside
	// configLibrary and appliedId points at a fresh, safe id — not the escape.
	assert.Equal(t, dir, filepath.Dir(configPath), "config document must stay inside configLibrary")
	id := strings.TrimSuffix(filepath.Base(configPath), ".json")
	assert.True(t, isSafeConfigID(id), "minted id must be a safe bare filename")
	meta := readMeta(t, metaPath)
	assert.Equal(t, id, meta["appliedId"], "appliedId must point at the safe minted id, not the traversal value")
	assert.NoFileExists(t, filepath.Join(dir, "..", "..", "evil.json"), "must not write outside configLibrary")
}

func TestRevertCredentialHelper_EmptyConfigPathLeavesShim(t *testing.T) {
	t.Parallel()
	cm, _ := newClaudeDesktopManager(t)

	_, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)
	shimPath := cm.credentialHelperShimPath()
	require.FileExists(t, shimPath)

	// With no recorded config path we cannot confirm _meta.json no longer
	// references the shim, so revert must be a no-op and leave it in place rather
	// than risk breaking a still-applied config.
	require.NoError(t, cm.RevertLLMGateway(ClientApp(ClaudeDesktop), ""))
	assert.FileExists(t, shimPath, "shim must not be deleted when configPath is empty")
}

func TestRevertCredentialHelper_LeavesForeignAppliedIDIntact(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)

	// Simulate the user re-selecting their own config after setup.
	meta := readMeta(t, metaPath)
	meta["appliedId"] = "user-config"
	meta["entries"] = append(metaEntries(meta), map[string]any{"id": "user-config", "name": "My Bedrock"})
	writeBytes, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metaPath, writeBytes, 0o600))

	require.NoError(t, cm.RevertLLMGateway(ClientApp(ClaudeDesktop), configPath))

	meta = readMeta(t, metaPath)
	// Our entry is removed but the user's active selection is left alone.
	assert.Equal(t, "user-config", meta["appliedId"])
	assert.Len(t, metaEntries(meta), 1)
	assert.Equal(t, "user-config", metaEntryID(meta, "My Bedrock"))
}

func TestConfigureCredentialHelper_CleansUpOnWriteFailure(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	// First setup succeeds: creates the entry, config document, and shim.
	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)
	shimPath := cm.credentialHelperShimPath()
	require.FileExists(t, shimPath)
	_ = metaPath

	// Force the config-document write to fail on the next (idempotent) setup:
	// replace the config document with a non-empty directory at the same path.
	// The reused id targets it and AtomicWriteFile cannot overwrite a directory,
	// so the in-lock cleanup path runs.
	require.NoError(t, os.Remove(configPath))
	require.NoError(t, os.MkdirAll(configPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configPath, "block"), []byte("x"), 0o600))

	_, err = cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.Error(t, err, "setup must fail when the config document cannot be written")

	// Cleanup must NOT delete the shim an earlier successful setup created
	// (only a shim minted in the same failed call is removed).
	assert.FileExists(t, shimPath, "cleanup must preserve a pre-existing shim on failure")
}

func TestManagedProfileExistsUnder(t *testing.T) {
	t.Parallel()
	const domain = "com.anthropic.claudefordesktop.plist"

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		assert.False(t, managedProfileExistsUnder(t.TempDir(), domain))
	})
	t.Run("direct path", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, domain), []byte("x"), 0o600))
		assert.True(t, managedProfileExistsUnder(root, domain))
	})
	t.Run("per-user subdir", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		userDir := filepath.Join(root, "alice")
		require.NoError(t, os.MkdirAll(userDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(userDir, domain), []byte("x"), 0o600))
		assert.True(t, managedProfileExistsUnder(root, domain))
	})
}

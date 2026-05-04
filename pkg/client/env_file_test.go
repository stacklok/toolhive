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

// ── parseEnvFile / marshalEnvFile ─────────────────────────────────────────────

func TestParseEnvFile_Empty(t *testing.T) {
	t.Parallel()
	entries, err := parseEnvFile(nil)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestParseEnvFile_KeyValueLines(t *testing.T) {
	t.Parallel()
	content := []byte("FOO=bar\nBAZ=qux\n")
	entries, err := parseEnvFile(content)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "FOO", entries[0].key)
	assert.Equal(t, "bar", entries[0].value)
	assert.Equal(t, "BAZ", entries[1].key)
	assert.Equal(t, "qux", entries[1].value)
}

func TestParseEnvFile_CommentsAndBlanks(t *testing.T) {
	t.Parallel()
	content := []byte("# comment\n\nFOO=bar\n")
	entries, err := parseEnvFile(content)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "# comment", entries[0].raw)
	assert.Equal(t, "", entries[1].raw) // blank line
	assert.Equal(t, "FOO", entries[2].key)
}

func TestParseEnvFile_MalformedLinePreserved(t *testing.T) {
	t.Parallel()
	content := []byte("NOT_VALID_LINE\nFOO=bar\n")
	entries, err := parseEnvFile(content)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "NOT_VALID_LINE", entries[0].raw)
	assert.Equal(t, "FOO", entries[1].key)
}

func TestParseEnvFile_ValueWithEquals(t *testing.T) {
	t.Parallel()
	// Values may contain '=' (e.g. base64-encoded tokens).
	content := []byte("KEY=abc=xyz\n")
	entries, err := parseEnvFile(content)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "KEY", entries[0].key)
	assert.Equal(t, "abc=xyz", entries[0].value)
}

func TestMarshalEnvFile_RoundTrip(t *testing.T) {
	t.Parallel()
	original := []byte("# header\nFOO=bar\nBAZ=qux\n")
	entries, err := parseEnvFile(original)
	require.NoError(t, err)
	got := marshalEnvFile(entries)
	assert.Equal(t, string(original), string(got))
}

// ── setEnvEntry / removeEnvEntry ──────────────────────────────────────────────

func TestSetEnvEntry_AddsNew(t *testing.T) {
	t.Parallel()
	entries := setEnvEntry(nil, "KEY", "value")
	require.Len(t, entries, 1)
	assert.Equal(t, "KEY", entries[0].key)
	assert.Equal(t, "value", entries[0].value)
}

func TestSetEnvEntry_UpdatesExisting(t *testing.T) {
	t.Parallel()
	entries := []envEntry{{key: "KEY", value: "old"}}
	entries = setEnvEntry(entries, "KEY", "new")
	require.Len(t, entries, 1)
	assert.Equal(t, "new", entries[0].value)
}

func TestRemoveEnvEntry_RemovesKey(t *testing.T) {
	t.Parallel()
	entries := []envEntry{{key: "KEEP", value: "1"}, {key: "REMOVE", value: "2"}}
	entries = removeEnvEntry(entries, "REMOVE")
	require.Len(t, entries, 1)
	assert.Equal(t, "KEEP", entries[0].key)
}

func TestRemoveEnvEntry_NoopWhenMissing(t *testing.T) {
	t.Parallel()
	entries := []envEntry{{key: "KEEP", value: "1"}}
	entries = removeEnvEntry(entries, "MISSING")
	require.Len(t, entries, 1)
}

// ── ConfigureEnvFile ──────────────────────────────────────────────────────────

func TestConfigureEnvFile_CreatesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cm := NewTestClientManager(dir, nil, supportedClientIntegrations, nil)

	_, err := cm.ConfigureEnvFile(GeminiCli, llmgateway.ApplyConfig{
		ProxyBaseURL: "http://localhost:14000/v1",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".gemini", ".env"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "GEMINI_API_KEY=thv-proxy\n")
	assert.Contains(t, content, "GOOGLE_GEMINI_BASE_URL=http://localhost:14000\n")
}

func TestConfigureEnvFile_UpdatesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	geminiDir := filepath.Join(dir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0o700))
	existing := "# user comment\nOTHER_VAR=keep\nGOOGLE_GEMINI_BASE_URL=old\n"
	require.NoError(t, os.WriteFile(filepath.Join(geminiDir, ".env"), []byte(existing), 0o600))

	cm := NewTestClientManager(dir, nil, supportedClientIntegrations, nil)
	_, err := cm.ConfigureEnvFile(GeminiCli, llmgateway.ApplyConfig{
		ProxyBaseURL: "http://localhost:14000/v1",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(geminiDir, ".env"))
	require.NoError(t, err)
	content := string(data)
	// Original entries preserved.
	assert.Contains(t, content, "# user comment\n")
	assert.Contains(t, content, "OTHER_VAR=keep\n")
	// thv-managed entries written or updated.
	assert.Contains(t, content, "GEMINI_API_KEY=thv-proxy\n")
	assert.Contains(t, content, "GOOGLE_GEMINI_BASE_URL=http://localhost:14000\n")
	// Old value replaced (not duplicated).
	assert.NotContains(t, content, "GOOGLE_GEMINI_BASE_URL=old")
}

func TestConfigureEnvFile_NoopForClientWithoutEnvKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cm := NewTestClientManager(dir, nil, supportedClientIntegrations, nil)

	// Claude Code has no LLMEnvFileKeys → returns ("", nil).
	path, err := cm.ConfigureEnvFile(ClaudeCode, llmgateway.ApplyConfig{})
	require.NoError(t, err)
	assert.Empty(t, path)
}

// ── RevertEnvFile ─────────────────────────────────────────────────────────────

func TestRevertEnvFile_RemovesEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	geminiDir := filepath.Join(dir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0o700))
	initial := "OTHER_VAR=keep\nGEMINI_API_KEY=thv-proxy\nGOOGLE_GEMINI_BASE_URL=http://localhost:14000\n"
	envPath := filepath.Join(geminiDir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte(initial), 0o600))

	cm := NewTestClientManager(dir, nil, supportedClientIntegrations, nil)
	require.NoError(t, cm.RevertEnvFile(GeminiCli, envPath))

	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "OTHER_VAR=keep\n")
	assert.NotContains(t, content, "GEMINI_API_KEY")
	assert.NotContains(t, content, "GOOGLE_GEMINI_BASE_URL")
}

func TestRevertEnvFile_NoopWhenFileMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cm := NewTestClientManager(dir, nil, supportedClientIntegrations, nil)
	// File does not exist — must not return an error.
	err := cm.RevertEnvFile(GeminiCli, filepath.Join(dir, ".gemini", ".env"))
	require.NoError(t, err)
}

func TestRevertEnvFile_NoopWhenPathEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cm := NewTestClientManager(dir, nil, supportedClientIntegrations, nil)
	require.NoError(t, cm.RevertEnvFile(GeminiCli, ""))
}

// ── envFileValueForSpec ───────────────────────────────────────────────────────

func TestEnvFileValueForSpec_Literal(t *testing.T) {
	t.Parallel()
	spec := LLMEnvFileKeySpec{Name: "KEY", Literal: "constant"}
	val, err := envFileValueForSpec(spec, llmgateway.ApplyConfig{})
	require.NoError(t, err)
	assert.Equal(t, "constant", val)
}

func TestEnvFileValueForSpec_ProxyOrigin(t *testing.T) {
	t.Parallel()
	spec := LLMEnvFileKeySpec{Name: "KEY", ValueField: "ProxyOrigin"}
	val, err := envFileValueForSpec(spec, llmgateway.ApplyConfig{ProxyBaseURL: "http://localhost:14000/v1"})
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:14000", val)
}

func TestEnvFileValueForSpec_PlaceholderAPIKey(t *testing.T) {
	t.Parallel()
	spec := LLMEnvFileKeySpec{Name: "KEY", ValueField: "PlaceholderAPIKey"}
	val, err := envFileValueForSpec(spec, llmgateway.ApplyConfig{})
	require.NoError(t, err)
	assert.Equal(t, llmPlaceholderAPIKey, val)
}

func TestEnvFileValueForSpec_UnknownFieldReturnsError(t *testing.T) {
	t.Parallel()
	spec := LLMEnvFileKeySpec{Name: "KEY", ValueField: "NotAField"}
	_, err := envFileValueForSpec(spec, llmgateway.ApplyConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NotAField")
}

func TestEnvFileValueForSpec_BothSetReturnsError(t *testing.T) {
	t.Parallel()
	spec := LLMEnvFileKeySpec{Name: "KEY", Literal: "val", ValueField: "GatewayURL"}
	_, err := envFileValueForSpec(spec, llmgateway.ApplyConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both Literal and ValueField")
}

func TestEnvFileValueForSpec_NeitherSetReturnsError(t *testing.T) {
	t.Parallel()
	spec := LLMEnvFileKeySpec{Name: "KEY"}
	_, err := envFileValueForSpec(spec, llmgateway.ApplyConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither Literal nor ValueField")
}

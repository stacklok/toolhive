// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stacklok/toolhive/pkg/llm/tools"
)

const (
	testProxyBaseURL = "http://localhost:14000/v1"
	testOSDarwin     = "darwin"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return m
}

// ── Claude Code ───────────────────────────────────────────────────────────────

func TestClaudeCode_DetectFalseWhenAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := tools.NewClaudeCodeAdapterWithHome(dir)
	if a.Detect() {
		t.Error("Detect() should return false when ~/.claude is missing")
	}
}

func TestClaudeCode_DetectTrueWhenPresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := tools.NewClaudeCodeAdapterWithHome(dir)
	if !a.Detect() {
		t.Error("Detect() should return true when ~/.claude exists")
	}
}

func TestClaudeCode_ApplyAndRevert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	a := tools.NewClaudeCodeAdapterWithHome(dir)
	cfg := tools.ApplyConfig{
		GatewayURL:         "https://gateway.example.com",
		TokenHelperCommand: "/usr/local/bin/thv llm token",
	}

	path, err := a.Apply(cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	m := readJSON(t, path)
	if m["apiKeyHelper"] != "/usr/local/bin/thv llm token" {
		t.Errorf("apiKeyHelper = %v", m["apiKeyHelper"])
	}
	env, _ := m["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://gateway.example.com" {
		t.Errorf("env.ANTHROPIC_BASE_URL = %v", env["ANTHROPIC_BASE_URL"])
	}

	if err := a.Revert(path); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	m = readJSON(t, path)
	if _, ok := m["apiKeyHelper"]; ok {
		t.Error("apiKeyHelper should be gone after Revert")
	}
	if _, ok := m["env"]; ok {
		t.Error("env map should be pruned after Revert")
	}
}

// ── Gemini CLI ────────────────────────────────────────────────────────────────

func TestGeminiCLI_DetectFalseWhenAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := tools.NewGeminiCLIAdapterWithHome(dir)
	if a.Detect() {
		t.Error("Detect() should return false when ~/.gemini is missing")
	}
}

func TestGeminiCLI_ApplyAndRevert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}

	a := tools.NewGeminiCLIAdapterWithHome(dir)
	cfg := tools.ApplyConfig{
		GatewayURL:         "https://gateway.example.com",
		TokenHelperCommand: "/usr/local/bin/thv llm token",
	}

	path, err := a.Apply(cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	m := readJSON(t, path)
	auth, _ := m["auth"].(map[string]any)
	if auth["tokenCommand"] != "/usr/local/bin/thv llm token" {
		t.Errorf("auth.tokenCommand = %v", auth["tokenCommand"])
	}
	if m["baseUrl"] != "https://gateway.example.com" {
		t.Errorf("baseUrl = %v", m["baseUrl"])
	}

	if err := a.Revert(path); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	m = readJSON(t, path)
	if _, ok := m["auth"]; ok {
		t.Error("auth map should be pruned after Revert")
	}
	if _, ok := m["baseUrl"]; ok {
		t.Error("baseUrl should be gone after Revert")
	}
}

// ── Cursor ────────────────────────────────────────────────────────────────────

func cursorSettingsDirForOS(home string) string {
	switch runtime.GOOS {
	case testOSDarwin:
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User")
	case "windows":
		// NewCursorAdapterWithHome injects home as appDataFn too, so on Windows
		// the settings path is <home>/Cursor/User/settings.json.
		return filepath.Join(home, "Cursor", "User")
	default:
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			configDir = filepath.Join(home, ".config")
		}
		return filepath.Join(configDir, "Cursor", "User")
	}
}

func TestCursor_DetectFalseWhenAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := tools.NewCursorAdapterWithHome(dir)
	if a.Detect() {
		t.Error("Detect() should return false when Cursor settings dir is missing")
	}
}

func TestCursor_ApplyAndRevert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(cursorSettingsDirForOS(dir), 0o700); err != nil {
		t.Fatal(err)
	}

	a := tools.NewCursorAdapterWithHome(dir)
	cfg := tools.ApplyConfig{ProxyBaseURL: testProxyBaseURL}

	path, err := a.Apply(cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	m := readJSON(t, path)
	if m["cursor.general.openAIBaseURL"] != testProxyBaseURL {
		t.Errorf("cursor.general.openAIBaseURL = %v", m["cursor.general.openAIBaseURL"])
	}
	if m["cursor.general.openAIAPIKey"] != tools.PlaceholderAPIKey {
		t.Errorf("cursor.general.openAIAPIKey = %v", m["cursor.general.openAIAPIKey"])
	}

	if err := a.Revert(path); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	m = readJSON(t, path)
	if _, ok := m["cursor.general.openAIBaseURL"]; ok {
		t.Error("cursor.general.openAIBaseURL should be gone after Revert")
	}
}

// ── VS Code ───────────────────────────────────────────────────────────────────

func vscodeSettingsDirForOS(home string) string {
	switch runtime.GOOS {
	case testOSDarwin:
		return filepath.Join(home, "Library", "Application Support", "Code", "User")
	case "windows":
		// NewVSCodeAdapterWithHome injects home as appDataFn too, so on Windows
		// the settings path is <home>/Code/User/settings.json.
		return filepath.Join(home, "Code", "User")
	default:
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			configDir = filepath.Join(home, ".config")
		}
		return filepath.Join(configDir, "Code", "User")
	}
}

func TestVSCode_ApplyAndRevert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(vscodeSettingsDirForOS(dir), 0o700); err != nil {
		t.Fatal(err)
	}

	a := tools.NewVSCodeAdapterWithHome(dir)
	cfg := tools.ApplyConfig{ProxyBaseURL: testProxyBaseURL}

	path, err := a.Apply(cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	m := readJSON(t, path)
	if m["github.copilot.advanced.serverUrl"] != testProxyBaseURL {
		t.Errorf("serverUrl = %v", m["github.copilot.advanced.serverUrl"])
	}
	if m["github.copilot.advanced.apiKey"] != tools.PlaceholderAPIKey {
		t.Errorf("apiKey = %v", m["github.copilot.advanced.apiKey"])
	}

	if err := a.Revert(path); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	m = readJSON(t, path)
	if _, ok := m["github.copilot.advanced.serverUrl"]; ok {
		t.Error("serverUrl should be gone after Revert")
	}
}

// ── Xcode ─────────────────────────────────────────────────────────────────────

func TestXcode_DetectFalseOnNonDarwin(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == testOSDarwin {
		t.Skip("skipped on darwin — test is for non-darwin platforms")
	}
	a := tools.NewXcodeAdapterWithHome(t.TempDir())
	if a.Detect() {
		t.Error("Detect() should return false on non-darwin platforms")
	}
}

func TestXcode_ApplyAndRevert(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != testOSDarwin {
		t.Skip("xcode adapter is macOS-only")
	}
	dir := t.TempDir()
	xcodeDir := filepath.Join(dir, "Library", "Application Support", "GitHub Copilot for Xcode")
	if err := os.MkdirAll(xcodeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	a := tools.NewXcodeAdapterWithHome(dir)
	cfg := tools.ApplyConfig{ProxyBaseURL: testProxyBaseURL}

	path, err := a.Apply(cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	m := readJSON(t, path)
	if m["openAIBaseURL"] != testProxyBaseURL {
		t.Errorf("openAIBaseURL = %v", m["openAIBaseURL"])
	}
	if m["apiKey"] != tools.PlaceholderAPIKey {
		t.Errorf("apiKey = %v", m["apiKey"])
	}

	if err := a.Revert(path); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	m = readJSON(t, path)
	if _, ok := m["openAIBaseURL"]; ok {
		t.Error("openAIBaseURL should be gone after Revert")
	}
}

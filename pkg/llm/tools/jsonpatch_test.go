// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const testThemeValue = "dark"

func TestPatchJSONFile_CreatesMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	err := patchJSONFile(path, func(m map[string]any) {
		m["apiKeyHelper"] = "thv llm token"
	})
	if err != nil {
		t.Fatalf("patchJSONFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading patched file: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if got, ok := m["apiKeyHelper"]; !ok || got != "thv llm token" {
		t.Errorf("apiKeyHelper = %v, want \"thv llm token\"", got)
	}
}

func TestPatchJSONFile_AcceptsJSONC(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	jsonc := []byte(`{
  // VS Code comment
  "editor.fontSize": 14, // trailing comma
}`)
	_ = os.WriteFile(path, jsonc, 0o600)

	err := patchJSONFile(path, func(m map[string]any) {
		m["github.copilot.advanced.serverUrl"] = "http://localhost:14000/v1"
	})
	if err != nil {
		t.Fatalf("patchJSONFile with JSONC input: %v", err)
	}

	data, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if m["editor.fontSize"] != float64(14) {
		t.Errorf("editor.fontSize = %v, want 14", m["editor.fontSize"])
	}
	if m["github.copilot.advanced.serverUrl"] != "http://localhost:14000/v1" {
		t.Errorf("serverUrl = %v", m["github.copilot.advanced.serverUrl"])
	}
}

func TestPatchJSONFile_PreservesExistingKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// Seed the file with an existing key.
	_ = os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600)

	err := patchJSONFile(path, func(m map[string]any) {
		m["newKey"] = "newValue"
	})
	if err != nil {
		t.Fatalf("patchJSONFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	var m map[string]any
	_ = json.Unmarshal(data, &m)

	if m["theme"] != testThemeValue {
		t.Errorf("existing key theme lost, got %v", m["theme"])
	}
	if m["newKey"] != "newValue" {
		t.Errorf("newKey = %v, want \"newValue\"", m["newKey"])
	}
}

func TestPatchJSONFile_CreatesParentDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "settings.json")

	err := patchJSONFile(path, func(m map[string]any) {
		m["x"] = 1.0
	})
	if err != nil {
		t.Fatalf("patchJSONFile with nested dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestRevertJSONFile_RemovesKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	_ = os.WriteFile(path, []byte(`{"apiKeyHelper":"thv llm token","theme":"dark"}`), 0o600)

	if err := revertJSONFile(path, "apiKeyHelper"); err != nil {
		t.Fatalf("revertJSONFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	var m map[string]any
	_ = json.Unmarshal(data, &m)

	if _, ok := m["apiKeyHelper"]; ok {
		t.Error("apiKeyHelper should have been removed")
	}
	if m["theme"] != testThemeValue {
		t.Errorf("theme lost, got %v", m["theme"])
	}
}

func TestRevertFlatJSONFile_RemovesFlatDottedKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	_ = os.WriteFile(path, []byte(`{"cursor.general.openAIBaseURL":"http://localhost:14000/v1","theme":"dark"}`), 0o600)

	if err := revertFlatJSONFile(path, "cursor.general.openAIBaseURL"); err != nil {
		t.Fatalf("revertFlatJSONFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	var m map[string]any
	_ = json.Unmarshal(data, &m)

	if _, ok := m["cursor.general.openAIBaseURL"]; ok {
		t.Error("flat dotted key should have been removed")
	}
	if m["theme"] != testThemeValue {
		t.Errorf("theme lost, got %v", m["theme"])
	}
}

func TestRevertFlatJSONFile_MissingFileIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")
	if err := revertFlatJSONFile(path, "someKey"); err != nil {
		t.Fatalf("expected no-op for missing file, got: %v", err)
	}
}

func TestRevertJSONFile_MissingFileIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	if err := revertJSONFile(path, "someKey"); err != nil {
		t.Fatalf("expected no-op for missing file, got: %v", err)
	}
}

func TestSetNestedKey(t *testing.T) {
	t.Parallel()
	m := map[string]any{}
	setNestedKey(m, "env.ANTHROPIC_BASE_URL", "https://gateway.example.com")

	env, ok := m["env"].(map[string]any)
	if !ok {
		t.Fatalf("env not a map, got %T", m["env"])
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://gateway.example.com" {
		t.Errorf("ANTHROPIC_BASE_URL = %v, want https://gateway.example.com", got)
	}
}

func TestSetNestedKey_TopLevel(t *testing.T) {
	t.Parallel()
	m := map[string]any{}
	setNestedKey(m, "apiKeyHelper", "cmd")
	if m["apiKeyHelper"] != "cmd" {
		t.Errorf("apiKeyHelper = %v", m["apiKeyHelper"])
	}
}

func TestDeleteNestedKey_RemovesEmptyIntermediateMap(t *testing.T) {
	t.Parallel()
	m := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "https://example.com",
		},
		"theme": testThemeValue,
	}
	deleteNestedKey(m, "env.ANTHROPIC_BASE_URL")
	if _, ok := m["env"]; ok {
		t.Error("empty env map should have been pruned")
	}
	if m["theme"] != testThemeValue {
		t.Errorf("theme should still be set, got %v", m["theme"])
	}
}

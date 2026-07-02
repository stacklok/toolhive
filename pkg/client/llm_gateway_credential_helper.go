// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// Claude Desktop's "third-party inference" config lives in a configLibrary
// directory: one <id>.json document per saved configuration, plus a _meta.json
// selector that names the active one. This is a different shape from the single
// JSON settings file that direct/proxy clients patch, so it gets a dedicated
// writer here rather than going through the LLMGatewayKeys machinery.

const (
	// claudeDesktopManagedEntryName is the stable display name of the config
	// entry ToolHive owns in _meta.json. Reusing a fixed name (rather than a
	// fresh UUID per run) keeps setup idempotent — repeated "thv llm setup"
	// calls update the same entry instead of accumulating orphans.
	claudeDesktopManagedEntryName = "ToolHive Gateway"

	// claudeDesktopCredentialKind selects the credential-helper auth model
	// (a local executable that prints a token), as opposed to "interactive"
	// (Claude Desktop runs its own OIDC flow).
	claudeDesktopCredentialKind = "helper-script"
)

// claudeDesktopConfig is the <id>.json document written into the configLibrary.
// ToolHive fully owns this file, so a typed struct is safe (no user fields to
// preserve). inferenceModels is omitted when empty so Claude Desktop falls back
// to gateway-side model auto-discovery once the gateway serves it.
type claudeDesktopConfig struct {
	InferenceProvider                   string   `json:"inferenceProvider"`
	InferenceGatewayBaseURL             string   `json:"inferenceGatewayBaseUrl"`
	InferenceGatewayAuthScheme          string   `json:"inferenceGatewayAuthScheme"`
	InferenceCredentialKind             string   `json:"inferenceCredentialKind"`
	InferenceCredentialHelper           string   `json:"inferenceCredentialHelper"`
	InferenceCredentialHelperTtlSec     int      `json:"inferenceCredentialHelperTtlSec"`
	InferenceCredentialHelperTimeoutSec int      `json:"inferenceCredentialHelperTimeoutSec"`
	InferenceModels                     []string `json:"inferenceModels,omitempty"`
}

// configureCredentialHelper writes (or updates) ToolHive's Claude Desktop
// configuration: it generates the credential-helper shim, writes the config
// document, and points _meta.json at it. It returns the absolute path of the
// config document, which RevertLLMGateway later uses to undo the change.
//
// The _meta.json update is done under a file lock and preserves every entry and
// key ToolHive does not own, so a user's other saved configurations are left
// intact. Writes are crash-safe via AtomicWriteFile.
func (cm *ClientManager) configureCredentialHelper(appCfg *clientAppConfig, cfg llmgateway.ApplyConfig) (string, error) {
	if runtime.GOOS == "windows" {
		// The shim is a POSIX /bin/sh script — consistent with the rest of the
		// LLM gateway token-helper feature, which is POSIX-only (see
		// buildTokenHelperCommand in pkg/llm). Windows support is a follow-up.
		return "", fmt.Errorf("claude-desktop LLM gateway setup is not supported on Windows yet")
	}

	metaPath := cm.buildLLMSettingsPath(appCfg)
	dir := filepath.Dir(metaPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	shimPath, err := cm.writeCredentialHelperShim(cfg.TokenHelperCommand)
	if err != nil {
		return "", err
	}

	baseURL := cfg.AnthropicBaseURL
	if baseURL == "" {
		baseURL = cfg.GatewayURL
	}

	var configPath string
	err = fileutils.WithFileLock(metaPath, func() error {
		meta, err := readClaudeDesktopMeta(metaPath)
		if err != nil {
			return err
		}

		id := metaEntryID(meta, claudeDesktopManagedEntryName)
		if id == "" {
			id = uuid.NewString()
			meta["entries"] = append(metaEntries(meta), map[string]any{
				"id":   id,
				"name": claudeDesktopManagedEntryName,
			})
		}
		meta["appliedId"] = id

		doc := claudeDesktopConfig{
			InferenceProvider:                   "gateway",
			InferenceGatewayBaseURL:             baseURL,
			InferenceGatewayAuthScheme:          "bearer",
			InferenceCredentialKind:             claudeDesktopCredentialKind,
			InferenceCredentialHelper:           shimPath,
			InferenceCredentialHelperTtlSec:     int(llmgateway.ClaudeDesktopHelperTTL.Seconds()),
			InferenceCredentialHelperTimeoutSec: int(llmgateway.ClaudeDesktopHelperTimeout.Seconds()),
			InferenceModels:                     cfg.Models,
		}
		docBytes, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding Claude Desktop config: %w", err)
		}
		configPath = filepath.Join(dir, id+".json")
		if err := fileutils.AtomicWriteFile(configPath, docBytes, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", configPath, err)
		}

		return writeClaudeDesktopMeta(metaPath, meta)
	})
	if err != nil {
		return "", err
	}
	return configPath, nil
}

// revertCredentialHelper undoes configureCredentialHelper: it removes ToolHive's
// entry from _meta.json (leaving other entries untouched), clears appliedId when
// it pointed at our config, deletes the config document, and removes the shim.
// A missing file at any step is treated as already-reverted.
func (cm *ClientManager) revertCredentialHelper(appCfg *clientAppConfig, configPath string) error {
	// Always attempt shim removal — it is ToolHive-owned and lives at a fixed path.
	shimPath := cm.credentialHelperShimPath()
	if err := os.Remove(shimPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing credential helper shim %s: %w", shimPath, err)
	}

	if configPath == "" {
		return nil
	}
	id := metaIDFromConfigPath(configPath)
	metaPath := filepath.Join(filepath.Dir(configPath), appCfg.LLMSettingsFile)

	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		// Selector already gone; just make sure the config document is removed.
		return removeIfExists(configPath)
	}

	return fileutils.WithFileLock(metaPath, func() error {
		meta, err := readClaudeDesktopMeta(metaPath)
		if err != nil {
			return err
		}
		meta["entries"] = removeMetaEntry(metaEntries(meta), id)
		// Only clear appliedId if it still points at our config; leave a user's
		// own active config selection alone.
		if applied, _ := meta["appliedId"].(string); applied == id {
			meta["appliedId"] = ""
		}
		if err := writeClaudeDesktopMeta(metaPath, meta); err != nil {
			return err
		}
		return removeIfExists(configPath)
	})
}

// credentialHelperShimPath is the fixed location of the generated shim.
func (cm *ClientManager) credentialHelperShimPath() string {
	return filepath.Join(cm.homeDir, ".toolhive", "llm", "claude-desktop-helper.sh")
}

// writeCredentialHelperShim generates the no-arg executable that Claude Desktop
// invokes as its inferenceCredentialHelper. Claude Desktop requires an absolute
// path to an executable (no arguments), whereas tokenHelperCommand is a shell
// command string (e.g. `"…thv" llm token`); the shim bridges the two.
//
// Claude Desktop sets CLAUDE_HELPER_CONTEXT on each call. Only "interactive"
// permits an OIDC browser flow; silent contexts (background / setup-test /
// mid-session-refresh) must not hijack the user's browser, so they use
// --skip-browser and rely on the cached/refresh token that "thv llm setup"
// primed up front.
func (cm *ClientManager) writeCredentialHelperShim(tokenHelperCommand string) (string, error) {
	if tokenHelperCommand == "" {
		return "", fmt.Errorf("no token-helper command available for credential helper shim")
	}
	shimPath := cm.credentialHelperShimPath()
	if err := os.MkdirAll(filepath.Dir(shimPath), 0o700); err != nil {
		return "", fmt.Errorf("creating credential helper directory: %w", err)
	}
	script := "#!/bin/sh\n" +
		"# Generated by `thv llm setup` — Claude Desktop credential helper.\n" +
		"# Prints a fresh LLM gateway token. Do not edit; `thv llm teardown` removes it.\n" +
		"if [ \"$CLAUDE_HELPER_CONTEXT\" = \"interactive\" ]; then\n" +
		"  exec " + tokenHelperCommand + "\n" +
		"fi\n" +
		"exec " + tokenHelperCommand + " --skip-browser\n"
	if err := fileutils.AtomicWriteFile(shimPath, []byte(script), 0o700); err != nil {
		return "", fmt.Errorf("writing credential helper shim %s: %w", shimPath, err)
	}
	return shimPath, nil
}

// managedProfilePresent reports whether an MDM/managed-preferences profile for
// the given plist domain is present. A managed profile overrides a client's
// local config, so "thv llm setup" warns when one is detected (the local config
// it writes would be ignored). macOS only; returns false elsewhere or when the
// client declares no managed-profile domain.
func managedProfilePresent(domain string) bool {
	if domain == "" || runtime.GOOS != "darwin" {
		return false
	}
	if _, err := os.Stat(filepath.Join("/Library/Managed Preferences", domain)); err == nil {
		return true
	}
	// Per-user managed prefs live under /Library/Managed Preferences/<user>/.
	matches, _ := filepath.Glob(filepath.Join("/Library/Managed Preferences", "*", domain))
	return len(matches) > 0
}

// ── _meta.json helpers ─────────────────────────────────────────────────────

// readClaudeDesktopMeta reads _meta.json into a generic map so unknown keys are
// preserved on write-back. A missing or empty file yields a fresh selector.
func readClaudeDesktopMeta(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- known config file location
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"appliedId": "", "entries": []any{}}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{"appliedId": "", "entries": []any{}}, nil
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	return meta, nil
}

// writeClaudeDesktopMeta encodes meta and writes it atomically.
func writeClaudeDesktopMeta(path string, meta map[string]any) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := fileutils.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// metaEntries returns the entries slice from meta, or an empty slice.
func metaEntries(meta map[string]any) []any {
	entries, _ := meta["entries"].([]any)
	return entries
}

// metaEntryID returns the id of the entry with the given name, or "" if absent.
func metaEntryID(meta map[string]any, name string) string {
	for _, e := range metaEntries(meta) {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := entry["name"].(string); n == name {
			id, _ := entry["id"].(string)
			return id
		}
	}
	return ""
}

// removeMetaEntry returns entries with the entry matching id removed.
func removeMetaEntry(entries []any, id string) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		if entry, ok := e.(map[string]any); ok {
			if eid, _ := entry["id"].(string); eid == id {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// metaIDFromConfigPath derives the config id from a "<id>.json" path.
func metaIDFromConfigPath(configPath string) string {
	base := filepath.Base(configPath)
	return base[:len(base)-len(filepath.Ext(base))]
}

// removeIfExists deletes path, treating a missing file as success.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

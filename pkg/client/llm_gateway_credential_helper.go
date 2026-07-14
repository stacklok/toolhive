// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

	// Reuse the shared AnthropicBaseURL resolution (falls back to GatewayURL) so
	// this stays in sync with direct-mode clients if the rule ever changes.
	baseURL, _ := resolveApplyConfigField("AnthropicBaseURL", cfg)

	// Write the shim, config document, and _meta.json selector all inside the
	// lock so concurrent setup/teardown runs cannot interleave — e.g. one run's
	// failure-cleanup deleting a shim another run's committed config depends on.
	// _meta.json is written last, so a mid-write failure never leaves Claude
	// Desktop pointing at our config; best-effort cleanup then removes the
	// unreferenced files it created.
	var configPath string
	err := fileutils.WithFileLock(metaPath, func() error {
		// Track whether the shim already existed so failure cleanup does not
		// delete a shim an earlier successful setup still depends on.
		shimExisted := fileExistsAt(cm.credentialHelperShimPath())
		shimPath, err := cm.writeCredentialHelperShim(cfg.TokenHelperCommand)
		if err != nil {
			return err
		}
		cleanup := func(cp string) {
			if !shimExisted {
				_ = os.Remove(shimPath)
			}
			if cp != "" {
				_ = os.Remove(cp)
			}
		}

		meta, err := readClaudeDesktopMeta(metaPath)
		if err != nil {
			cleanup("")
			return err
		}
		id := metaEntryID(meta, claudeDesktopManagedEntryName)
		if id == "" {
			id = uuid.NewString()
		}
		// Upsert by name so an existing (possibly malformed) "ToolHive Gateway"
		// entry is corrected in place rather than leaving a duplicate — preserving
		// the idempotency the stable name is there to provide.
		meta["entries"] = upsertMetaEntry(metaEntries(meta), id, claudeDesktopManagedEntryName)
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
			cleanup("")
			return fmt.Errorf("encoding Claude Desktop config: %w", err)
		}
		cp := filepath.Join(dir, id+".json")
		if err := fileutils.AtomicWriteFile(cp, docBytes, 0o600); err != nil {
			cleanup(cp)
			return fmt.Errorf("writing %s: %w", cp, err)
		}
		if err := writeClaudeDesktopMeta(metaPath, meta); err != nil {
			cleanup(cp)
			return err
		}
		configPath = cp
		return nil
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
//
// Ordering matters: the selector (_meta.json) is de-referenced and the config
// document deleted BEFORE the shim, so a mid-revert failure never leaves Claude
// Desktop pointing at a config that references a missing helper executable. Any
// leftover file after a partial failure is unreferenced and harmless.
func (cm *ClientManager) revertCredentialHelper(appCfg *clientAppConfig, configPath string) error {
	// Nothing recorded to revert. Do NOT touch the shim here: without the config
	// path we cannot confirm _meta.json no longer references it, and removing it
	// could break a still-applied config.
	if configPath == "" {
		return nil
	}
	// Derive the configLibrary dir from appCfg (not from configPath) so a
	// tampered stored configPath cannot redirect teardown at an arbitrary
	// directory. The config document must live inside this dir.
	metaPath := cm.buildLLMSettingsPath(appCfg)
	dir := filepath.Dir(metaPath)
	if !fileExistsAt(dir) {
		// Config directory already gone — nothing to revert.
		return nil
	}

	id := metaIDFromConfigPath(configPath)
	if !isSafeConfigID(id) {
		// Symmetric with configureCredentialHelper: a corrupted or hand-edited
		// stored configPath whose id is not a bare filename is treated as
		// already-reverted rather than risk unlinking through it.
		return nil
	}
	// Reconstruct the expected path and require the stored configPath to match
	// it. os.Remove follows symlinks, so a stored path outside configLibrary
	// (or a symlink inside it) must not let teardown unlink an arbitrary file.
	expectedPath := filepath.Join(dir, id+".json")
	if configPath != expectedPath {
		return nil
	}
	shimPath := cm.credentialHelperShimPath()

	// All steps run under the metaPath lock so a concurrent setup/teardown cannot
	// interleave. Ordering: de-reference _meta.json, delete the config document,
	// then remove the shim last — so a mid-revert failure never leaves Claude
	// Desktop pointing at a config whose helper is gone.
	return fileutils.WithFileLock(metaPath, func() error {
		if fileExistsAt(metaPath) {
			meta, err := readClaudeDesktopMeta(metaPath)
			if err != nil {
				return err
			}
			meta["entries"] = removeMetaEntry(metaEntries(meta), id)
			// Only clear appliedId if it still points at our config; leave a
			// user's own active config selection alone.
			applied, ok := meta["appliedId"].(string)
			if !ok && meta["appliedId"] != nil {
				slog.Warn("Claude Desktop _meta.json has a non-string appliedId; leaving it unchanged",
					"path", metaPath)
			}
			if applied == id {
				meta["appliedId"] = ""
			}
			if err := writeClaudeDesktopMeta(metaPath, meta); err != nil {
				return err
			}
		}
		if err := removeIfExists(configPath); err != nil {
			return err
		}
		if err := os.Remove(shimPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing credential helper shim %s: %w", shimPath, err)
		}
		return nil
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
	if !isSafeTokenHelperCommand(tokenHelperCommand) {
		// Defence in depth: the producer (buildTokenHelperCommand) is shell-safe
		// today, but the writer must not silently emit an injectable 0700 script
		// if a future caller constructs tokenHelperCommand differently.
		return "", fmt.Errorf("refusing to write credential helper shim: token-helper command is not shell-safe")
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

// isSafeTokenHelperCommand reports whether tokenHelperCommand matches the shape
// produced by buildTokenHelperCommand: a double-quoted path followed by the
// literal args "llm token", with no shell metacharacters that could break out
// of the exec line the shim concatenates. The shim is a 0700 /bin/sh script
// built by string concatenation, so a caller-supplied command containing ";",
// "&", "|", "`", "$", "#", or newlines would be stored command injection.
//
// buildTokenHelperCommand (pkg/llm/setup.go) is shell-safe today — it rejects
// paths containing those characters before formatting — but TokenHelperCommand
// is exposed as a general ApplyConfig field consumed by multiple writers. This
// check makes the shim writer fail closed on any command it cannot prove safe,
// rather than trusting every future caller to uphold the contract.
func isSafeTokenHelperCommand(tokenHelperCommand string) bool {
	if tokenHelperCommand == "" {
		return false
	}
	for _, r := range tokenHelperCommand {
		switch r {
		case ';', '&', '|', '`', '$', '#', '\n', '\r':
			return false
		}
	}
	// Must be a double-quoted path followed by exactly " llm token".
	if len(tokenHelperCommand) < 2 || tokenHelperCommand[0] != '"' {
		return false
	}
	closeQuote := strings.IndexByte(tokenHelperCommand[1:], '"')
	if closeQuote == -1 {
		return false
	}
	return tokenHelperCommand[2+closeQuote:] == " llm token"
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
	return managedProfileExistsUnder(managedPreferencesRoot, domain)
}

// managedPreferencesRoot is the macOS managed-preferences directory. A package
// variable (not a const) so tests can point it at a temp directory.
var managedPreferencesRoot = "/Library/Managed Preferences"

// managedProfileExistsUnder reports whether a managed-preferences plist for
// domain exists directly under root or under a per-user subdirectory
// (root/<user>/domain). Platform-independent so it is unit-testable.
func managedProfileExistsUnder(root, domain string) bool {
	if _, err := os.Stat(filepath.Join(root, domain)); err == nil {
		return true
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", domain))
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
	// Guard the "valid JSON, wrong shape" case: if entries is present but not a
	// JSON array, bail rather than silently dropping it — metaEntries would treat
	// it as empty and a subsequent append would overwrite the user's data.
	if e, ok := meta["entries"]; ok && e != nil {
		if _, isArray := e.([]any); !isArray {
			return nil, fmt.Errorf("parsing %s: %q must be a JSON array", path, "entries")
		}
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
// _meta.json lives in a directory Claude Desktop and users also write to, so the
// stored id is untrusted: an unsafe value is treated as absent so the caller
// mints a fresh, safe UUID rather than joining a path-traversing id into the
// configLibrary path. See isSafeConfigID.
func metaEntryID(meta map[string]any, name string) string {
	for _, e := range metaEntries(meta) {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := entry["name"].(string); n == name {
			id, _ := entry["id"].(string)
			if !isSafeConfigID(id) {
				return ""
			}
			return id
		}
	}
	return ""
}

// isSafeConfigID reports whether id is a bare filename safe to join into the
// configLibrary path — non-empty, no path separators, and no "..". Guards
// against a corrupted or hand-edited _meta.json entry escaping configLibrary.
func isSafeConfigID(id string) bool {
	return id != "" && filepath.Base(id) == id && !strings.Contains(id, "..")
}

// upsertMetaEntry sets the id of the entry with the given name if one exists
// (correcting a malformed or stale same-name entry in place), or appends a new
// entry otherwise. Prevents duplicate ToolHive-owned entries.
func upsertMetaEntry(entries []any, id, name string) []any {
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := entry["name"].(string); n == name {
			entry["id"] = id
			return entries
		}
	}
	return append(entries, map[string]any{"id": id, "name": name})
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

// fileExistsAt reports whether a file exists at path.
func fileExistsAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// removeIfExists deletes path, treating a missing file as success.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

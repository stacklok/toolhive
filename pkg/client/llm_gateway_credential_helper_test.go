// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	require.Equal(t, llmgateway.ModeCredentialHelper, cfg.LLMGatewayMode, "ClaudeDesktop must use the credential-helper model")
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

func claudeDesktopTestHelperPath() string {
	if runtime.GOOS == "windows" {
		return `C:\opt\toolhive\bin\thv.exe`
	}
	return "/opt/toolhive/bin/thv"
}

func claudeDesktopApplyCfg() llmgateway.ApplyConfig {
	return llmgateway.ApplyConfig{
		GatewayURL:       "https://gw.example.com",
		AnthropicBaseURL: "https://gw.example.com/anthropic",
		// The shim consumes TokenHelperPath (the absolute thv path), not the
		// shell-string TokenHelperCommand that direct-mode clients use.
		TokenHelperPath: claudeDesktopTestHelperPath(),
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
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
	shim, err := os.ReadFile(shimPath) // #nosec G304 -- test-controlled path
	require.NoError(t, err)
	if runtime.GOOS == "windows" {
		assert.True(t, strings.HasSuffix(shimPath, "claude-desktop-helper.cmd"))
		assert.Contains(t, string(shim), `call "`+claudeDesktopTestHelperPath()+`" llm token`)
	} else {
		assert.Contains(t, string(shim), `exec '/opt/toolhive/bin/thv' llm token`)
	}
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

// TestRevertCredentialHelper_RejectsUnsafeConfigPath proves the revert-side
// guards refuse to unlink through a tampered stored configPath (os.Remove
// follows symlinks). A stored path outside configLibrary — or one with a
// traversal segment — must be treated as already-reverted, not deleted.
// Each case gets its own sentinel file so subtests run in parallel safely.
func TestRevertCredentialHelper_RejectsUnsafeConfigPath(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)
	// configLibrary must exist so revert reaches the guard rather than
	// early-returning on a missing dir.
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o700))

	cases := []struct {
		name   string
		stored string
	}{
		{"absolute path outside configLibrary", filepath.Join(t.TempDir(), "do-not-delete.txt")},
		{"traversal segment", filepath.Join(cm.homeDir, "..", "evil.json")},
		{"deeper traversal", filepath.Join(cm.homeDir, "..", "..", "evil.json")},
	}
	for _, tc := range cases {
		tc := tc
		// Each subtest owns its own sentinel so parallelism is safe.
		require.NoError(t, os.WriteFile(tc.stored, []byte("sentinel"), 0o600))
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, cm.RevertLLMGateway(ClientApp(ClaudeDesktop), tc.stored))
			assert.FileExists(t, tc.stored, "teardown must not unlink a tampered configPath")
		})
	}
}

// TestQuoteForPOSIXShell pins the escaping shape for the characters that matter.
func TestQuoteForPOSIXShell(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain path", "/usr/local/bin/thv", `'/usr/local/bin/thv'`},
		{"space", "/App Support/thv", `'/App Support/thv'`},
		{"single quote", "/it's/thv", `'/it'\''s/thv'`},
		{"double quote", `/say "hi"/thv`, `'/say "hi"/thv'`},
		{"dollar and backtick", "/$HOME/`id`/thv", "'/$HOME/`id`/thv'"},
		{"semicolon", "/a;rm -rf/thv", `'/a;rm -rf/thv'`},
		{"newline", "/a\nb/thv", "'/a\nb/thv'"},
		{"empty", "", `''`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, quoteForPOSIXShell(tc.in))
		})
	}
}

// TestQuoteForPOSIXShell_SurvivesRealShell is the evidence behind the claim that
// single-quoting is a total transform, which is what lets the token-helper
// writers drop metacharacter validation entirely. Each string is round-tripped
// through /bin/sh and must come back byte-identical.
func TestQuoteForPOSIXShell_SurvivesRealShell(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting; /bin/sh unavailable")
	}

	inputs := []string{
		"/usr/local/bin/thv",
		"/App Support/thv",
		"/it's/thv",
		`/say "hi"/thv`,
		"/$HOME/`id`/thv",
		"/a;rm -rf/thv",
		"/a&&b/thv",
		"/a|b/thv",
		"/a#b/thv",
		"/a$(id)b/thv",
		"/a\nb/thv",
		"/a\\b/thv",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			// printf %s re-emits the argument verbatim, so any shell
			// interpretation of the quoted form shows up as a mismatch.
			script := "printf %s " + quoteForPOSIXShell(in)
			out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput() // #nosec G204 -- test-controlled input
			require.NoError(t, err, "sh failed: %s", out)
			assert.Equal(t, in, string(out), "quoted string must survive /bin/sh verbatim")
		})
	}

	// Control: the same hostile strings unquoted do NOT survive, proving the
	// test would catch a broken escaper rather than passing trivially.
	out, err := exec.Command("/bin/sh", "-c", "printf %s /a$(id)b").CombinedOutput()
	require.NoError(t, err, "sh failed: %s", out)
	assert.NotEqual(t, "/a$(id)b", string(out))
	assert.True(t, strings.HasPrefix(string(out), "/a"))
}

func TestQuoteForCmd(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain path", `C:\opt\thv.exe`, `"C:\opt\thv.exe"`},
		{"space", `C:\Program Files\thv.exe`, `"C:\Program Files\thv.exe"`},
		{"embedded quote", `C:\say "hi"\thv.exe`, `"C:\say ""hi""\thv.exe"`},
		{"percent", `C:\100%\thv.exe`, `"C:\100%%\thv.exe"`},
		{"empty", "", `""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, quoteForCmd(tc.in))
		})
	}
}

func TestWriteCredentialHelperShim_ExecutesWithHostilePath_Windows(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows .cmd shim")
	}

	hostileDir := filepath.Join(t.TempDir(), "O'Brien we ird")
	require.NoError(t, os.MkdirAll(hostileDir, 0o700))
	fakeThv := filepath.Join(hostileDir, "thv.cmd")
	require.NoError(t, os.WriteFile(fakeThv, []byte("@echo off\r\necho ARGS: %*\r\n"), 0o700))

	cm := &ClientManager{homeDir: t.TempDir()}
	shimPath, err := cm.writeCredentialHelperShim(fakeThv)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(shimPath, "claude-desktop-helper.cmd"))

	out, err := exec.Command("cmd.exe", "/c", shimPath).CombinedOutput() // #nosec G204 -- test-controlled path
	require.NoError(t, err, "shim failed: %s", out)
	assert.Equal(t, "ARGS: llm token --skip-browser", strings.TrimSpace(string(out)))

	cmd := exec.Command("cmd.exe", "/c", shimPath) // #nosec G204 -- test-controlled path
	cmd.Env = append(os.Environ(), "CLAUDE_HELPER_CONTEXT=interactive")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "shim failed: %s", out)
	assert.Equal(t, "ARGS: llm token", strings.TrimSpace(string(out)))
}

// TestWriteCredentialHelperShim_RequiresAbsolutePath proves the writer fails
// closed on anything that is not an absolute path. Defeating PATH resolution is
// the whole point of the shim, so a relative path — which Claude Desktop would
// resolve against an arbitrary working directory — must not produce a shim at
// all rather than one that silently cannot find thv.
func TestWriteCredentialHelperShim_RequiresAbsolutePath(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"",      // os.Executable() failed upstream
		"thv",   // bare command: would resolve via PATH, the bug being fixed
		"./thv", // relative to an arbitrary working directory
		"../bin/thv",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			cm := &ClientManager{homeDir: t.TempDir()}
			_, err := cm.writeCredentialHelperShim(path)
			require.Error(t, err, "expected rejection of %q", path)
			assert.NoFileExists(t, cm.credentialHelperShimPath(),
				"no shim may be written when the path is rejected")
		})
	}
}

// TestWriteCredentialHelperShim_UsesAbsolutePath proves the shim execs the
// absolute thv path rather than a bare "thv". A bare command is unusable here:
// Claude Desktop is only ever GUI-launched, so it inherits launchd's PATH,
// which does not contain thv's install directory (~/.toolhive/bin).
func TestWriteCredentialHelperShim_UsesAbsolutePath(t *testing.T) {
	t.Parallel()
	cm := &ClientManager{homeDir: t.TempDir()}

	helperPath := claudeDesktopTestHelperPath()
	shimPath, err := cm.writeCredentialHelperShim(helperPath)
	require.NoError(t, err)
	shim, err := os.ReadFile(shimPath) // #nosec G304 -- test-controlled path
	require.NoError(t, err)

	if runtime.GOOS == "windows" {
		assert.Contains(t, string(shim), `call "`+helperPath+`" llm token`)
		assert.Contains(t, string(shim), `call "`+helperPath+`" llm token --skip-browser`)
		silent, interactive, found := strings.Cut(string(shim), ":interactive")
		require.True(t, found, "Windows shim must have a :interactive label")
		assert.Contains(t, silent, "--skip-browser")
		assert.NotContains(t, interactive, "--skip-browser")
		return
	}

	assert.Contains(t, string(shim), `exec '`+helperPath+`' llm token`)
	assert.Contains(t, string(shim), `exec '`+helperPath+`' llm token --skip-browser`)
	// The interactive branch must NOT pass --skip-browser: it is the only
	// context permitted to open a browser for a full OIDC re-auth.
	interactive, _, found := strings.Cut(string(shim), "\nfi\n")
	require.True(t, found, "shim must have an interactive branch terminated by fi")
	assert.NotContains(t, interactive, "--skip-browser")
}

// TestWriteCredentialHelperShim_ExecutesWithHostilePath is the load-bearing test
// for deleting the old metacharacter blocklist: it runs the generated shim under
// /bin/sh with a thv path containing a space, a single quote, a double quote, a
// dollar sign, a backtick, a semicolon and a newline, and asserts the arguments
// arrive intact. Proving execution (not just string shape) is what establishes
// that single-quoting is a total transform, so no path needs to be rejected.
func TestWriteCredentialHelperShim_ExecutesWithHostilePath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shim is a POSIX /bin/sh script")
	}

	// A directory name exercising every character the old blocklist rejected,
	// plus a newline — which survives single-quoting intact and so would have
	// been the one case a "reject metacharacters" scheme could not have fixed.
	hostileDir := filepath.Join(t.TempDir(), "we ird's \"$(id)\" `id`;\nrm -rf")
	require.NoError(t, os.MkdirAll(hostileDir, 0o700))
	fakeThv := filepath.Join(hostileDir, "thv")
	// A stand-in for thv that echoes the args it received.
	require.NoError(t, os.WriteFile(fakeThv, []byte("#!/bin/sh\necho \"ARGS: $*\"\n"), 0o700)) //nolint:gosec // G306: must be executable

	cm := &ClientManager{homeDir: t.TempDir()}
	shimPath, err := cm.writeCredentialHelperShim(fakeThv)
	require.NoError(t, err)

	// Silent context: --skip-browser is appended.
	out, err := exec.Command("/bin/sh", shimPath).CombinedOutput() // #nosec G204 -- test-controlled path
	require.NoError(t, err, "shim failed: %s", out)
	assert.Equal(t, "ARGS: llm token --skip-browser", strings.TrimSpace(string(out)))

	// Interactive context: no --skip-browser, so a browser flow is permitted.
	cmd := exec.Command("/bin/sh", shimPath) // #nosec G204 -- test-controlled path
	cmd.Env = append(os.Environ(), "CLAUDE_HELPER_CONTEXT=interactive")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "shim failed: %s", out)
	assert.Equal(t, "ARGS: llm token", strings.TrimSpace(string(out)))
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

func TestConfigureCredentialHelper_RejectsNonArrayEntries(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	// Valid JSON but wrong shape: entries is an object, not an array. Setup must
	// bail rather than silently drop it (which would overwrite user data).
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o700))
	require.NoError(t, os.WriteFile(metaPath, []byte(`{"appliedId":"","entries":{"oops":true}}`), 0o600))

	_, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.Error(t, err, "setup must fail when _meta.json entries is not an array")
	assert.Contains(t, err.Error(), "entries")
}

func TestConfigureCredentialHelper_NonStringIDDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	cm, metaPath := newClaudeDesktopManager(t)

	// A name-matching entry with a non-string id must be corrected in place, not
	// left alongside a freshly minted duplicate "ToolHive Gateway" entry.
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o700))
	seed := map[string]any{
		"appliedId": "",
		"entries": []any{
			map[string]any{"id": 123, "name": claudeDesktopManagedEntryName},
		},
	}
	seedBytes, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metaPath, seedBytes, 0o600))

	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)

	meta := readMeta(t, metaPath)
	assert.Len(t, metaEntries(meta), 1, "malformed same-name entry must be corrected in place, not duplicated")
	id := strings.TrimSuffix(filepath.Base(configPath), ".json")
	assert.Equal(t, id, meta["appliedId"])
	assert.Equal(t, id, metaEntryID(meta, claudeDesktopManagedEntryName))
}

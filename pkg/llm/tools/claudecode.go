// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
)

func init() {
	Register(newClaudeCodeAdapter(os.UserHomeDir))
}

// claudeCodeAdapter configures Claude Code to use the LLM gateway via the
// token-helper mechanism. It patches ~/.claude/settings.json to set:
//   - apiKeyHelper — the command that prints a fresh OIDC token to stdout
//   - env.ANTHROPIC_BASE_URL — the upstream gateway URL
type claudeCodeAdapter struct {
	homeDirFn func() (string, error)
}

func newClaudeCodeAdapter(homeDirFn func() (string, error)) *claudeCodeAdapter {
	return &claudeCodeAdapter{homeDirFn: homeDirFn}
}

func (*claudeCodeAdapter) Name() string { return "claude-code" }
func (*claudeCodeAdapter) Mode() string { return ModeDirect }

// Detect reports whether ~/.claude/ exists (proxy for Claude Code being installed).
func (a *claudeCodeAdapter) Detect() bool {
	home, err := a.homeDirFn()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".claude"))
	return err == nil
}

func (a *claudeCodeAdapter) Apply(cfg ApplyConfig) (string, error) {
	return applyPatch(a.settingsPath, func(m map[string]any) {
		m["apiKeyHelper"] = cfg.TokenHelperCommand
		setNestedKey(m, "env.ANTHROPIC_BASE_URL", cfg.GatewayURL)
	})
}

func (*claudeCodeAdapter) Revert(configPath string) error {
	return revertJSONFile(configPath, "apiKeyHelper", "env.ANTHROPIC_BASE_URL")
}

func (a *claudeCodeAdapter) settingsPath() (string, error) {
	home, err := a.homeDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

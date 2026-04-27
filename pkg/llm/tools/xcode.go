// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"runtime"
)

func init() {
	Register(newXcodeAdapter(os.UserHomeDir))
}

// xcodeAdapter configures the GitHub Copilot for Xcode extension to use the
// LLM gateway via the localhost reverse proxy. It patches the extension's
// editorSettings.json (macOS only) to set:
//   - openAIBaseURL — the proxy base URL
//   - apiKey — a static placeholder API key
//
// On non-macOS platforms Detect() always returns false so the adapter is
// silently skipped during setup.
type xcodeAdapter struct {
	homeDirFn func() (string, error)
}

func newXcodeAdapter(homeDirFn func() (string, error)) *xcodeAdapter {
	return &xcodeAdapter{homeDirFn: homeDirFn}
}

func (*xcodeAdapter) Name() string { return "xcode" }
func (*xcodeAdapter) Mode() string { return ModeProxy }

// Detect reports whether the GitHub Copilot for Xcode extension directory
// exists. Only relevant on macOS.
func (a *xcodeAdapter) Detect() bool {
	if runtime.GOOS != osDarwin {
		return false
	}
	path, err := a.settingsPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Dir(path))
	return err == nil
}

func (a *xcodeAdapter) Apply(cfg ApplyConfig) (string, error) {
	return applyPatch(a.settingsPath, func(m map[string]any) {
		m["openAIBaseURL"] = cfg.ProxyBaseURL
		m["apiKey"] = PlaceholderAPIKey
	})
}

func (*xcodeAdapter) Revert(configPath string) error {
	return revertJSONFile(configPath, "openAIBaseURL", "apiKey")
}

// settingsPath returns the path to the GitHub Copilot for Xcode
// editorSettings.json. This path is macOS-specific; callers should check
// Detect() first.
func (a *xcodeAdapter) settingsPath() (string, error) {
	home, err := a.homeDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(
		home, "Library", "Application Support",
		"GitHub Copilot for Xcode", "editorSettings.json",
	), nil
}

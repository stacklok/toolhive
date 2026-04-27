// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"runtime"
)

func init() {
	Register(newVSCodeAdapter(os.UserHomeDir))
}

// vscodeAdapter configures VS Code (GitHub Copilot) to use the LLM gateway
// via the localhost reverse proxy. It patches VS Code's user settings.json to:
//   - github.copilot.advanced.serverUrl — the proxy base URL
//   - github.copilot.advanced.apiKey — a static placeholder API key
//
// VS Code settings.json uses literal dotted key names (not nested maps).
type vscodeAdapter struct {
	homeDirFn func() (string, error)
}

func newVSCodeAdapter(homeDirFn func() (string, error)) *vscodeAdapter {
	return &vscodeAdapter{homeDirFn: homeDirFn}
}

func (*vscodeAdapter) Name() string { return "vscode" }
func (*vscodeAdapter) Mode() string { return ModeProxy }

// Detect reports whether VS Code's user settings directory exists.
func (a *vscodeAdapter) Detect() bool {
	path, err := a.settingsPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Dir(path))
	return err == nil
}

func (a *vscodeAdapter) Apply(cfg ApplyConfig) (string, error) {
	return applyPatch(a.settingsPath, func(m map[string]any) {
		m["github.copilot.advanced.serverUrl"] = cfg.ProxyBaseURL
		m["github.copilot.advanced.apiKey"] = PlaceholderAPIKey
	})
}

func (*vscodeAdapter) Revert(configPath string) error {
	return revertFlatJSONFile(configPath,
		"github.copilot.advanced.serverUrl",
		"github.copilot.advanced.apiKey",
	)
}

func (a *vscodeAdapter) settingsPath() (string, error) {
	switch runtime.GOOS {
	case osDarwin:
		home, err := a.homeDirFn()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json"), nil
	case "windows":
		appData, err := windowsAppData()
		if err != nil {
			return "", err
		}
		return filepath.Join(appData, "Code", "User", "settings.json"), nil
	default:
		home, err := a.homeDirFn()
		if err != nil {
			return "", err
		}
		return filepath.Join(xdgConfigHome(home), "Code", "User", "settings.json"), nil
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"runtime"
)

func init() {
	Register(newCursorAdapter(os.UserHomeDir, windowsAppData))
}

// cursorAdapter configures Cursor to use the LLM gateway via the localhost
// reverse proxy. It patches Cursor's user settings.json to set:
//   - cursor.general.openAIBaseURL — the proxy base URL
//   - cursor.general.openAIAPIKey — a static placeholder API key
//
// Cursor settings.json uses literal dotted key names (not nested maps).
type cursorAdapter struct {
	homeDirFn func() (string, error)
	appDataFn func() (string, error) // Windows %APPDATA%; unused on other platforms
}

func newCursorAdapter(homeDirFn, appDataFn func() (string, error)) *cursorAdapter {
	return &cursorAdapter{homeDirFn: homeDirFn, appDataFn: appDataFn}
}

func (*cursorAdapter) Name() string { return "cursor" }
func (*cursorAdapter) Mode() string { return ModeProxy }

// Detect reports whether Cursor's settings directory exists.
func (a *cursorAdapter) Detect() bool {
	path, err := a.settingsPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Dir(path))
	return err == nil
}

func (a *cursorAdapter) Apply(cfg ApplyConfig) (string, error) {
	return applyPatch(a.settingsPath, func(m map[string]any) {
		m["cursor.general.openAIBaseURL"] = cfg.ProxyBaseURL
		m["cursor.general.openAIAPIKey"] = PlaceholderAPIKey
	})
}

func (*cursorAdapter) Revert(configPath string) error {
	return revertFlatJSONFile(configPath,
		"cursor.general.openAIBaseURL",
		"cursor.general.openAIAPIKey",
	)
}

func (a *cursorAdapter) settingsPath() (string, error) {
	switch runtime.GOOS {
	case osDarwin:
		home, err := a.homeDirFn()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "settings.json"), nil
	case "windows":
		appData, err := a.appDataFn()
		if err != nil {
			return "", err
		}
		return filepath.Join(appData, "Cursor", "User", "settings.json"), nil
	default:
		home, err := a.homeDirFn()
		if err != nil {
			return "", err
		}
		return filepath.Join(xdgConfigHome(home), "Cursor", "User", "settings.json"), nil
	}
}

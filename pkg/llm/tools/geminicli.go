// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
)

func init() {
	Register(newGeminiCLIAdapter(os.UserHomeDir))
}

// geminiCLIAdapter configures Google's Gemini CLI to use the LLM gateway via
// the token-helper mechanism. It patches ~/.gemini/settings.json to set:
//   - auth.tokenCommand — the command that prints a fresh OIDC token to stdout
//   - baseUrl — the upstream gateway URL
type geminiCLIAdapter struct {
	homeDirFn func() (string, error)
}

func newGeminiCLIAdapter(homeDirFn func() (string, error)) *geminiCLIAdapter {
	return &geminiCLIAdapter{homeDirFn: homeDirFn}
}

func (*geminiCLIAdapter) Name() string { return "gemini-cli" }
func (*geminiCLIAdapter) Mode() string { return ModeDirect }

// Detect reports whether ~/.gemini/ exists (proxy for Gemini CLI being installed).
func (a *geminiCLIAdapter) Detect() bool {
	home, err := a.homeDirFn()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".gemini"))
	return err == nil
}

func (a *geminiCLIAdapter) Apply(cfg ApplyConfig) (string, error) {
	return applyPatch(a.settingsPath, func(m map[string]any) {
		setNestedKey(m, "auth.tokenCommand", cfg.TokenHelperCommand)
		m["baseUrl"] = cfg.GatewayURL
	})
}

func (*geminiCLIAdapter) Revert(configPath string) error {
	return revertJSONFile(configPath, "auth.tokenCommand", "baseUrl")
}

func (a *geminiCLIAdapter) settingsPath() (string, error) {
	home, err := a.homeDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "settings.json"), nil
}

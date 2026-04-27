// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// osDarwin is the runtime.GOOS value for macOS, used across platform-specific
// switch statements in this package.
const osDarwin = "darwin"

// xdgConfigHome returns the XDG_CONFIG_HOME directory, falling back to
// ~/.config when the env var is not set.
func xdgConfigHome(home string) string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".config")
}

// windowsAppData returns the %APPDATA% directory (e.g. C:\Users\<user>\AppData\Roaming).
// Returns an error if the env var is not set, which should not happen on a
// properly configured Windows installation.
func windowsAppData() (string, error) {
	dir := os.Getenv("APPDATA")
	if dir == "" {
		return "", fmt.Errorf("%%APPDATA%% environment variable is not set")
	}
	return dir, nil
}

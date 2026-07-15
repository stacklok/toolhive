// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	codexBundleIdentifier     = "com.openai.codex"
	codexWindowsPackageFamily = "OpenAI.Codex_2p2nqsd0c76g0"
	codexWindowsPackageName   = "OpenAI.Codex"
	codexWindowsInstalledMark = "installed"
	// plutilPath is the absolute path to plutil on macOS. Using the absolute
	// path avoids PATH-resolution failures in stripped-environment contexts
	// (launchd jobs, sandboxes, CI) where /usr/bin is not on $PATH.
	plutilPath = "/usr/bin/plutil"
)

type codexDesktopDetector struct {
	homeDir string
	goos    string
	stat    func(string) (os.FileInfo, error)
	command func(string, ...string) ([]byte, error)
}

func newCodexDesktopDetector(homeDir, goos string) codexDesktopDetector {
	return codexDesktopDetector{
		homeDir: homeDir,
		goos:    goos,
		stat:    os.Stat,
		command: func(name string, args ...string) ([]byte, error) {
			// #nosec G204 -- the command name is a fixed compile-time literal
			// ("plutil" or "powershell.exe"); args include derived file paths
			// from os.UserHomeDir, not user input.
			return exec.Command(name, args...).Output()
		},
	}
}

func (d codexDesktopDetector) installed() (bool, error) {
	switch d.goos {
	case "darwin":
		return d.installedOnDarwin()
	case "windows":
		return d.installedOnWindows()
	default:
		return false, nil
	}
}

func (d codexDesktopDetector) installedOnDarwin() (bool, error) {
	applicationDirs := []string{"/Applications", filepath.Join(d.homeDir, "Applications")}
	for _, dir := range applicationDirs {
		for _, appName := range []string{"Codex.app", "ChatGPT.app"} {
			infoPlist := filepath.Join(dir, appName, "Contents", "Info.plist")
			if _, err := d.stat(infoPlist); err != nil {
				continue
			}
			output, err := d.command(plutilPath, "-extract", "CFBundleIdentifier", "raw", "-o", "-", infoPlist)
			if err != nil {
				// Log the plutil failure so it's distinguishable from "app absent."
				// A failure here means the plist exists but its bundle identifier
				// could not be extracted (e.g. plist corruption or plutil error).
				slog.Warn("failed to extract CFBundleIdentifier from Codex app plist",
					"app", appName, "plist", infoPlist, "error", err)
				continue
			}
			if string(bytes.TrimSpace(output)) == codexBundleIdentifier {
				return true, nil
			}
		}
	}
	return false, nil
}

func (d codexDesktopDetector) installedOnWindows() (bool, error) {
	const query = "$p = Get-AppxPackage -Name '" + codexWindowsPackageName + "' -ErrorAction SilentlyContinue | " +
		"Where-Object { $_.PackageFamilyName -eq '" + codexWindowsPackageFamily + "' -and $_.SignatureKind -eq 'Store' } | " +
		"Select-Object -First 1; if ($p) { Write-Output '" + codexWindowsInstalledMark + "' }"
	output, err := d.command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", query)
	if err != nil {
		return false, fmt.Errorf("querying Codex Microsoft Store package: %w", err)
	}
	return string(bytes.TrimSpace(output)) == codexWindowsInstalledMark, nil
}

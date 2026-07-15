// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	codexBundleIdentifier     = "com.openai.codex"
	codexWindowsPackageFamily = "OpenAI.Codex_2p2nqsd0c76g0"
	codexWindowsPackageName   = "OpenAI.Codex"
	codexWindowsInstalledMark = "installed"
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
			return exec.Command(name, args...).Output() // #nosec G204 -- name and args are fixed below
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
			output, err := d.command("plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", infoPlist)
			if err != nil {
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

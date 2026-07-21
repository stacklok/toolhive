// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexDesktopDetectorDarwin(t *testing.T) {
	t.Parallel()

	home := filepath.Join(string(filepath.Separator), "Users", "test")
	candidates := []string{
		filepath.Join("/Applications", "Codex.app", "Contents", "Info.plist"),
		filepath.Join("/Applications", "ChatGPT.app", "Contents", "Info.plist"),
		filepath.Join(home, "Applications", "Codex.app", "Contents", "Info.plist"),
		filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Info.plist"),
	}

	t.Run("checks all exact candidates without invoking plutil when absent", func(t *testing.T) {
		t.Parallel()
		var checked []string
		commandCalled := false
		detector := codexDesktopDetector{
			homeDir: home,
			goos:    "darwin",
			stat: func(path string) (os.FileInfo, error) {
				checked = append(checked, path)
				return nil, os.ErrNotExist
			},
			command: func(_ string, _ ...string) ([]byte, error) {
				commandCalled = true
				return nil, nil
			},
		}

		installed, err := detector.installed()
		require.NoError(t, err)
		assert.False(t, installed)
		assert.Equal(t, candidates, checked)
		assert.False(t, commandCalled)
	})

	tests := []struct {
		name       string
		bundleID   string
		commandErr error
		want       bool
	}{
		{name: "accepts exact Codex bundle identifier", bundleID: codexBundleIdentifier, want: true},
		{name: "rejects ChatGPT bundle identifier", bundleID: "com.openai.chat"},
		{name: "plutil failure is not detected", commandErr: errors.New("plutil failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plist := candidates[0]
			detector := codexDesktopDetector{
				homeDir: home,
				goos:    "darwin",
				stat: func(path string) (os.FileInfo, error) {
					if path == plist {
						return nil, nil
					}
					return nil, os.ErrNotExist
				},
				command: func(name string, args ...string) ([]byte, error) {
					assert.Equal(t, plutilPath, name)
					assert.Equal(t, []string{"-extract", "CFBundleIdentifier", "raw", "-o", "-", plist}, args)
					return []byte(tt.bundleID + "\n"), tt.commandErr
				},
			}

			installed, err := detector.installed()
			require.NoError(t, err)
			assert.Equal(t, tt.want, installed)
		})
	}
}

func TestCodexDesktopDetectorWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		output     string
		commandErr error
		want       bool
		wantErr    bool
	}{
		{name: "accepts installed marker after filtering multiple objects", output: codexWindowsInstalledMark + "\r\n", want: true},
		{name: "rejects marker mixed with output from another object", output: "noise\r\n" + codexWindowsInstalledMark + "\r\n"},
		{name: "rejects no matching package", output: ""},
		{name: "query failure", commandErr: errors.New("query failed"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			detector := codexDesktopDetector{
				goos: "windows",
				command: func(name string, args ...string) ([]byte, error) {
					assert.Equal(t, "powershell.exe", name)
					require.Len(t, args, 4)
					assert.Equal(t, []string{"-NoProfile", "-NonInteractive", "-Command"}, args[:3])
					assert.Contains(t, args[3], "Get-AppxPackage -Name 'OpenAI.Codex'")
					assert.Contains(t, args[3], "Where-Object")
					assert.Contains(t, args[3], "$_.PackageFamilyName -eq 'OpenAI.Codex_2p2nqsd0c76g0'")
					assert.Contains(t, args[3], "$_.SignatureKind -eq 'Store'")
					assert.Contains(t, args[3], "Select-Object -First 1")
					assert.Contains(t, args[3], "Write-Output 'installed'")
					return []byte(tt.output), tt.commandErr
				},
			}

			installed, err := detector.installed()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, installed)
		})
	}
}

func TestCodexDesktopDetectorUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	installed, err := (codexDesktopDetector{goos: "linux"}).installed()
	require.NoError(t, err)
	assert.False(t, installed)
}

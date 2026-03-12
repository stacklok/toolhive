// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

func TestWriteFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		files       []FileEntry
		force       bool
		preExist    bool
		expectError string
		expectFiles int
	}{
		{
			name: "write single file",
			files: []FileEntry{
				{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
			},
			expectFiles: 1,
		},
		{
			name: "write multiple files",
			files: []FileEntry{
				{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
				{Path: "README.md", Content: []byte("# Readme"), Mode: 0644},
			},
			expectFiles: 2,
		},
		{
			name: "existing directory without force",
			files: []FileEntry{
				{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
			},
			preExist:    true,
			expectError: "already exists",
		},
		{
			name: "existing directory with force",
			files: []FileEntry{
				{Path: "SKILL.md", Content: []byte("# New Skill"), Mode: 0644},
			},
			preExist:    true,
			force:       true,
			expectFiles: 1,
		},
		{
			name: "path traversal rejected",
			files: []FileEntry{
				{Path: "../../../etc/passwd", Content: []byte("evil"), Mode: 0644},
			},
			expectError: "path traversal detected",
		},
		{
			name: "permissions capped at 0644",
			files: []FileEntry{
				{Path: "script.sh", Content: []byte("#!/bin/bash"), Mode: 0755},
			},
			expectFiles: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			baseDir := resolvedTempDir(t)
			targetDir := filepath.Join(baseDir, "my-skill")

			if tt.preExist {
				require.NoError(t, os.MkdirAll(targetDir, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, "old.txt"), []byte("old"), 0644))
			}

			err := WriteFiles(tt.files, targetDir, tt.force)

			if tt.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
				return
			}

			require.NoError(t, err)

			entries, err := os.ReadDir(targetDir)
			require.NoError(t, err)
			assert.Len(t, entries, tt.expectFiles)

			// Verify file contents
			for _, f := range tt.files {
				content, readErr := os.ReadFile(filepath.Join(targetDir, f.Path))
				require.NoError(t, readErr)
				assert.Equal(t, f.Content, content)
			}

			// Verify permissions are capped
			for _, f := range tt.files {
				info, statErr := os.Stat(filepath.Join(targetDir, f.Path))
				require.NoError(t, statErr)
				mode := info.Mode().Perm()
				assert.True(t, mode <= 0644, "file %q has mode %o, expected <= 0644", f.Path, mode)
			}
		})
	}
}

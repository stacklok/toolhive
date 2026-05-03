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

	t.Run("parent directory does not exist", func(t *testing.T) {
		t.Parallel()
		baseDir := resolvedTempDir(t)
		// Point targetDir one level deeper than a non-existent subdirectory.
		targetDir := filepath.Join(baseDir, "nonexistent", "my-skill")

		files := []FileEntry{{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644}}
		err := WriteFiles(files, targetDir, false)
		require.NoError(t, err)

		// Both the parent and the skill directory must now exist.
		_, statErr := os.Stat(targetDir)
		require.NoError(t, statErr)

		content, readErr := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
		require.NoError(t, readErr)
		assert.Equal(t, []byte("# Skill"), content)
	})

	t.Run("nested file paths create intermediate directories", func(t *testing.T) {
		t.Parallel()
		baseDir := resolvedTempDir(t)
		targetDir := filepath.Join(baseDir, "my-skill")

		files := []FileEntry{
			{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
			{Path: "references/foo.md", Content: []byte("ref-foo"), Mode: 0644},
			{Path: "scripts/nested/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
			{Path: "deep/nested/dir/note.txt", Content: []byte("deep"), Mode: 0644},
		}
		require.NoError(t, WriteFiles(files, targetDir, false))

		for _, f := range files {
			full := filepath.Join(targetDir, filepath.FromSlash(f.Path))
			content, readErr := os.ReadFile(full)
			require.NoError(t, readErr, "file %q should exist on disk", f.Path)
			assert.Equal(t, f.Content, content)

			info, statErr := os.Stat(full)
			require.NoError(t, statErr)
			mode := info.Mode().Perm()
			assert.True(t, mode <= 0644, "file %q has mode %o, expected <= 0644", f.Path, mode)
		}

		// Spot-check that an intermediate directory was created.
		info, statErr := os.Stat(filepath.Join(targetDir, "scripts", "nested"))
		require.NoError(t, statErr)
		assert.True(t, info.IsDir(), "intermediate dir scripts/nested should be a directory")
	})

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

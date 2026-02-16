// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
)

func makeLayerData(t *testing.T, files []ociskills.FileEntry) []byte {
	t.Helper()
	data, err := ociskills.CompressTar(files, ociskills.DefaultTarOptions(), ociskills.DefaultGzipOptions())
	require.NoError(t, err)
	return data
}

func TestExtract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		files     []ociskills.FileEntry
		force     bool
		preCreate bool // create targetDir before extraction
		wantErr   string
		wantFiles int
	}{
		{
			name: "valid extraction to empty directory",
			files: []ociskills.FileEntry{
				{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
				{Path: "README.md", Content: []byte("# README"), Mode: 0644},
			},
			wantFiles: 2,
		},
		{
			name: "nested subdirectories",
			files: []ociskills.FileEntry{
				{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
				{Path: "a/b/c/file.txt", Content: []byte("deep"), Mode: 0644},
			},
			wantFiles: 2,
		},
		{
			name: "refuses overwrite when not forced",
			files: []ociskills.FileEntry{
				{Path: "SKILL.md", Content: []byte("# Skill"), Mode: 0644},
			},
			preCreate: true,
			wantErr:   "already exists",
		},
		{
			name: "overwrites when forced",
			files: []ociskills.FileEntry{
				{Path: "SKILL.md", Content: []byte("# New"), Mode: 0644},
			},
			preCreate: true,
			force:     true,
			wantFiles: 1,
		},
		{
			name: "file permissions sanitized",
			files: []ociskills.FileEntry{
				{Path: "script.sh", Content: []byte("#!/bin/sh"), Mode: 0755},
				{Path: "setuid.bin", Content: []byte("data"), Mode: 04755},
			},
			wantFiles: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			targetDir := filepath.Join(t.TempDir(), "skill-output")

			if tt.preCreate {
				require.NoError(t, os.MkdirAll(targetDir, 0750))
				require.NoError(t, os.WriteFile(
					filepath.Join(targetDir, "old-file.txt"),
					[]byte("old"),
					0600,
				))
			}

			layerData := makeLayerData(t, tt.files)
			result, err := Extract(layerData, targetDir, tt.force)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantFiles, result.Files)
			assert.Equal(t, targetDir, result.SkillDir)

			// Verify files exist
			for _, f := range tt.files {
				destPath := filepath.Join(targetDir, f.Path)
				content, readErr := os.ReadFile(destPath)
				require.NoError(t, readErr, "file %s should exist", f.Path)
				assert.Equal(t, f.Content, content)
			}
		})
	}
}

func TestExtract_PermissionsSanitized(t *testing.T) {
	t.Parallel()

	files := []ociskills.FileEntry{
		{Path: "normal.txt", Content: []byte("normal"), Mode: 0644},
		{Path: "setuid.bin", Content: []byte("data"), Mode: 04755},
		{Path: "setgid.bin", Content: []byte("data"), Mode: 02755},
		{Path: "sticky.bin", Content: []byte("data"), Mode: 01755},
	}

	targetDir := filepath.Join(t.TempDir(), "perms-test")
	layerData := makeLayerData(t, files)
	result, err := Extract(layerData, targetDir, false)
	require.NoError(t, err)
	assert.Equal(t, 4, result.Files)

	for _, f := range files {
		info, statErr := os.Stat(filepath.Join(targetDir, f.Path))
		require.NoError(t, statErr)
		mode := info.Mode().Perm()
		// No setuid/setgid/sticky bits should remain, and mode should be capped at 0644
		assert.Equal(t, os.FileMode(0644), mode,
			"file %s should have sanitized permissions, got %o", f.Path, mode)
	}
}

func TestExtract_MalformedGzip(t *testing.T) {
	t.Parallel()

	targetDir := filepath.Join(t.TempDir(), "bad-gzip")
	_, err := Extract([]byte("not valid gzip data"), targetDir, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompressing layer")
}

func TestExtract_FileCountLimit(t *testing.T) {
	t.Parallel()

	// Create more files than MaxExtractFileCount
	files := make([]ociskills.FileEntry, MaxExtractFileCount+1)
	for i := range files {
		files[i] = ociskills.FileEntry{
			Path:    fmt.Sprintf("f%04d.txt", i),
			Content: []byte("x"),
			Mode:    0644,
		}
	}

	targetDir := filepath.Join(t.TempDir(), "too-many")
	layerData := makeLayerData(t, files)
	_, err := Extract(layerData, targetDir, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeding limit")
}

func TestRemove(t *testing.T) {
	t.Parallel()

	t.Run("non-existent directory is idempotent", func(t *testing.T) {
		t.Parallel()
		err := Remove(filepath.Join(t.TempDir(), "does-not-exist"))
		require.NoError(t, err)
	})

	t.Run("removes existing directory", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "to-remove")
		require.NoError(t, os.MkdirAll(dir, 0750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0600))

		err := Remove(dir)
		require.NoError(t, err)

		_, statErr := os.Stat(dir)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("rejects empty path", func(t *testing.T) {
		t.Parallel()
		err := Remove("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("refuses to remove root", func(t *testing.T) {
		t.Parallel()
		err := Remove("/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dangerous path")
	})
}

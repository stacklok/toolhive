// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateProjectRoot(t *testing.T) {
	t.Parallel()

	gitRoot := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		return root
	}

	tests := []struct {
		name        string
		projectRoot func(t *testing.T) string
		wantErr     string
	}{
		{
			name:        "empty",
			projectRoot: func(_ *testing.T) string { return "" },
			wantErr:     "project_root is required",
		},
		{
			name: "relative",
			projectRoot: func(_ *testing.T) string {
				return "relative/path"
			},
			wantErr: "project_root must be absolute",
		},
		{
			name: "contains traversal",
			projectRoot: func(_ *testing.T) string {
				return "/tmp/../etc"
			},
			wantErr: "must not contain '..'",
		},
		{
			name: "contains null byte",
			projectRoot: func(_ *testing.T) string {
				return "\x00"
			},
			wantErr: "null bytes",
		},
		{
			name: "does not exist",
			projectRoot: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing")
			},
			wantErr: "does not exist",
		},
		{
			name: "not a directory",
			projectRoot: func(t *testing.T) string {
				root := t.TempDir()
				file := filepath.Join(root, "file")
				require.NoError(t, os.WriteFile(file, []byte("test"), 0o600))
				return file
			},
			wantErr: "must be a directory",
		},
		{
			name: "missing git",
			projectRoot: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: "git repository",
		},
		{
			name: "git directory",
			projectRoot: func(t *testing.T) string {
				return gitRoot(t)
			},
		},
		{
			name: "git file",
			projectRoot: func(t *testing.T) string {
				root := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir"), 0o600))
				return root
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := tt.projectRoot(t)
			cleaned, err := ValidateProjectRoot(root)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, filepath.Clean(root), cleaned)
		})
	}
}

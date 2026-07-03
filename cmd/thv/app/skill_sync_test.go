// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nolint:paralleltest // subtests change the process-wide working directory via os.Chdir
func TestResolveProjectRoot(t *testing.T) {
	t.Run("explicit value passes through without touching the filesystem", func(t *testing.T) {
		got, err := resolveProjectRoot("/explicit/path")
		require.NoError(t, err)
		assert.Equal(t, "/explicit/path", got)
	})

	t.Run("empty value auto-detects from the current directory", func(t *testing.T) {
		repoRoot := t.TempDir()
		resolved, err := filepath.EvalSymlinks(repoRoot)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(resolved, ".git"), 0o755))

		nested := filepath.Join(resolved, "a", "b")
		require.NoError(t, os.MkdirAll(nested, 0o755))

		orig, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.Chdir(orig) })
		require.NoError(t, os.Chdir(nested))

		got, err := resolveProjectRoot("")
		require.NoError(t, err)
		assert.Equal(t, resolved, got)
	})

	t.Run("empty value outside a git repo returns a helpful error", func(t *testing.T) {
		dir := t.TempDir()
		resolved, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)

		orig, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.Chdir(orig) })
		require.NoError(t, os.Chdir(resolved))

		_, err = resolveProjectRoot("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--project-root")
	})
}

func TestShortDigest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "short digest unchanged", in: "sha256:abc", want: "sha256:abc"},
		{
			name: "long digest truncated to 19 chars",
			in:   "sha256:abcdef0123456789fedcba9876543210",
			want: "sha256:abcdef012345",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, shortDigest(tt.in))
		})
	}
}

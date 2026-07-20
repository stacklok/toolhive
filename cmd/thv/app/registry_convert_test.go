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

// Test mutates package-level flag state so subtests run sequentially.
//
//nolint:paralleltest // Sequential by design — package globals shared across subtests.
func TestRegistryConvertPreRunE(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		out       string
		inPlace   bool
		noBackup  bool
		expectErr bool
	}{
		{name: "no flags is valid", expectErr: false},
		{name: "in only is valid", in: "registry.json", expectErr: false},
		{name: "out only is valid", out: "out.json", expectErr: false},
		{name: "in and out is valid", in: "registry.json", out: "out.json", expectErr: false},
		{name: "in-place with in is valid", in: "registry.json", inPlace: true, expectErr: false},
		{name: "in-place without in is invalid", inPlace: true, expectErr: true},
		{name: "in-place with out is invalid", in: "registry.json", out: "out.json", inPlace: true, expectErr: true},
		{name: "no-backup without in-place is invalid", in: "registry.json", noBackup: true, expectErr: true},
		{name: "in-place with no-backup is valid", in: "registry.json", inPlace: true, noBackup: true, expectErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convertIn = tt.in
			convertOut = tt.out
			convertInPlace = tt.inPlace
			convertNoBackup = tt.noBackup
			t.Cleanup(func() {
				convertIn = ""
				convertOut = ""
				convertInPlace = false
				convertNoBackup = false
			})

			err := registryConvertPreRunE(nil, nil)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestWriteInPlace(t *testing.T) {
	t.Parallel()

	t.Run("writes output and creates .bak when backup enabled", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		original := []byte(`{"original":true}`)
		output := []byte(`{"converted":true}`)
		require.NoError(t, os.WriteFile(path, original, 0o600))

		require.NoError(t, writeInPlace(path, original, output, true))

		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, output, got, "in-place file should hold the converted output")

		bak, err := os.ReadFile(path + ".bak")
		require.NoError(t, err)
		assert.Equal(t, original, bak, ".bak should hold the original bytes")
	})

	t.Run("skips backup when disabled", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"original":true}`), 0o600))

		require.NoError(t, writeInPlace(path, []byte(`{"original":true}`), []byte(`{"converted":true}`), false))

		_, err := os.Stat(path + ".bak")
		assert.True(t, os.IsNotExist(err), ".bak must not be written when backup is disabled")
	})

	t.Run("refuses to clobber existing .bak", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		bakPath := path + ".bak"
		previousBackup := []byte(`{"previous":true}`)
		require.NoError(t, os.WriteFile(path, []byte(`{"original":true}`), 0o600))
		require.NoError(t, os.WriteFile(bakPath, previousBackup, 0o600))

		err := writeInPlace(path, []byte(`{"original":true}`), []byte(`{"converted":true}`), true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")

		// Original input must still hold its old bytes — refusing to back up
		// must not partially mutate state.
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, []byte(`{"original":true}`), got)

		// Existing .bak must be preserved.
		bak, err := os.ReadFile(bakPath)
		require.NoError(t, err)
		assert.Equal(t, previousBackup, bak, "pre-existing .bak must be preserved")
	})

	t.Run("preserves file mode after rename", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"original":true}`), 0o640))

		require.NoError(t, writeInPlace(path, []byte(`{"original":true}`), []byte(`{"converted":true}`), false))

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "rename must preserve original perms")
	})
}

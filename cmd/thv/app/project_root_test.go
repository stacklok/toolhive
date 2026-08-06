// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // uses t.Chdir, incompatible with t.Parallel
func TestAbsProjectRoot(t *testing.T) {
	cwd := t.TempDir()
	resolvedCwd, err := filepath.EvalSymlinks(cwd)
	require.NoError(t, err)
	t.Chdir(resolvedCwd)

	tests := []struct {
		name     string
		explicit string
		want     string
	}{
		{
			name:     "empty stays empty so the scope is not silently changed",
			explicit: "",
			want:     "",
		},
		{
			name:     "dot resolves to the working directory",
			explicit: ".",
			want:     resolvedCwd,
		},
		{
			name:     "relative child resolves against the working directory",
			explicit: "child",
			want:     filepath.Join(resolvedCwd, "child"),
		},
		{
			name:     "an absolute path is returned unchanged",
			explicit: resolvedCwd,
			want:     resolvedCwd,
		},
		{
			name:     "traversal is cleaned rather than rejected",
			explicit: filepath.Join(resolvedCwd, "a", ".."),
			want:     resolvedCwd,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := absProjectRoot(tc.explicit)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestAbsProjectRootSharedBySkillAndPlugin documents why this helper is not
// in skill_helpers.go: thv ai-plugin takes the same --project-root flag and
// its server-side validation is literally the same function (pkg/plugins
// re-exports skills.ValidateProjectRoot), so both surfaces need identical
// normalization. A relative value from either must not reach the API.
//
//nolint:paralleltest // uses t.Chdir, incompatible with t.Parallel
func TestAbsProjectRootSharedBySkillAndPlugin(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	t.Chdir(resolved)

	for _, flagValue := range []string{".", "./", "sub/dir"} {
		got, err := absProjectRoot(flagValue)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(got),
			"--project-root %q must be absolute before it reaches the API server", flagValue)
	}
}

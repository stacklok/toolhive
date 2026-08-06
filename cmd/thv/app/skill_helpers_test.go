// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveProjectRootAbsolutizesExplicit covers the sync/upgrade path,
// where an explicit value short-circuits auto-detection — it must still be
// made absolute, which is the regression behind #6211.
//
//nolint:paralleltest // uses t.Chdir, incompatible with t.Parallel
func TestResolveProjectRootAbsolutizesExplicit(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	t.Chdir(resolved)

	got, err := resolveProjectRoot(".")
	require.NoError(t, err)
	assert.Equal(t, resolved, got)
	assert.True(t, filepath.IsAbs(got), "an explicit --project-root must reach the API server absolute")
}

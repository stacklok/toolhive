// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

func TestComputeContentDigest_MatchesExtractPluginTree(t *testing.T) {
	t.Parallel()

	layer := makePluginLayerData(t, "digest-plugin")
	got, err := computeContentDigest(layer)
	require.NoError(t, err)

	dir := filepath.Join(makeProjectRoot(t), "extracted")
	_, err = skills.ExtractPlugin(layer, dir, true)
	require.NoError(t, err)
	want, err := lockfile.ContentDigestFromDir(dir)
	require.NoError(t, err)
	assert.Equal(t, want, got, "in-memory layer hash must match ExtractPlugin's on-disk tree")
}

func TestComputeContentDigest_EmptyLayer(t *testing.T) {
	t.Parallel()
	_, err := computeContentDigest(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

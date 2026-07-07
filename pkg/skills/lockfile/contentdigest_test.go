// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentDigestDeterministic(t *testing.T) {
	t.Parallel()

	files := []ContentFile{
		{Path: "SKILL.md", Content: []byte("# Skill")},
		{Path: "refs/guide.md", Content: []byte("guide")},
	}
	d1, err := ContentDigest(files)
	require.NoError(t, err)
	d2, err := ContentDigest(files)
	require.NoError(t, err)
	assert.Equal(t, d1, d2)
	assert.True(t, len(d1) > len(ContentDigestPrefix))
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)
	data := []byte("version: 99\nskills: []\n")
	require.NoError(t, os.WriteFile(Path(dir), data, 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedVersion)
}

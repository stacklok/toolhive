// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Golden vectors freeze the contentDigest algorithm. If these ever need to
// change, every lock file in the wild reports drift — treat a change here as
// a breaking change to the lock file format, not a routine test update.
func TestContentDigestGoldenVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []ContentFile
		want  string
	}{
		{
			name: "single file",
			files: []ContentFile{
				{Path: "a.txt", Content: []byte("hello world")},
			},
			want: "sha256:79d1b89c9733bc5a3c51bd715612a85ab13add6933cb9aa7cc1ae44fd5d181a3",
		},
		{
			name: "multiple files sorted by path",
			files: []ContentFile{
				{Path: "SKILL.md", Content: []byte("# Skill")},
				{Path: "refs/guide.md", Content: []byte("guide")},
			},
			want: "sha256:9b46342aaebede490f12bf67a5ecb4be507c7684ee5e11a0511b54c701f464bd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ContentDigest(tt.files)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContentDigestDeterministicAndOrderIndependent(t *testing.T) {
	t.Parallel()

	a := []ContentFile{
		{Path: "SKILL.md", Content: []byte("# Skill")},
		{Path: "refs/guide.md", Content: []byte("guide")},
	}
	// Same files, different input order: the digest must not depend on
	// caller-supplied ordering.
	b := []ContentFile{
		{Path: "refs/guide.md", Content: []byte("guide")},
		{Path: "SKILL.md", Content: []byte("# Skill")},
	}

	d1, err := ContentDigest(a)
	require.NoError(t, err)
	d2, err := ContentDigest(b)
	require.NoError(t, err)
	assert.Equal(t, d1, d2)
}

func TestContentDigestDiffersOnContentChange(t *testing.T) {
	t.Parallel()

	d1, err := ContentDigest([]ContentFile{{Path: "a.txt", Content: []byte("v1")}})
	require.NoError(t, err)
	d2, err := ContentDigest([]ContentFile{{Path: "a.txt", Content: []byte("v2")}})
	require.NoError(t, err)
	assert.NotEqual(t, d1, d2)
}

func TestContentDigestRejectsEmptyFileSet(t *testing.T) {
	t.Parallel()
	_, err := ContentDigest(nil)
	require.Error(t, err)
}

func TestContentDigestRejectsTraversalPaths(t *testing.T) {
	t.Parallel()

	tests := []string{"..", "../escape.txt", "a/../../escape.txt", "/abs/path.txt"}
	for _, p := range tests {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			_, err := ContentDigest([]ContentFile{{Path: p, Content: []byte("x")}})
			require.Error(t, err)
		})
	}
}

func TestContentDigestNormalizesDotSlashPrefix(t *testing.T) {
	t.Parallel()

	withPrefix, err := ContentDigest([]ContentFile{{Path: "./a.txt", Content: []byte("hello")}})
	require.NoError(t, err)
	withoutPrefix, err := ContentDigest([]ContentFile{{Path: "a.txt", Content: []byte("hello")}})
	require.NoError(t, err)
	assert.Equal(t, withoutPrefix, withPrefix)
}

func TestContentDigestFromDir(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Skill"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "refs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "refs", "guide.md"), []byte("guide"), 0o644))

	got, err := ContentDigestFromDir(dir)
	require.NoError(t, err)

	want, err := ContentDigest([]ContentFile{
		{Path: "SKILL.md", Content: []byte("# Skill")},
		{Path: "refs/guide.md", Content: []byte("guide")},
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestContentDigestFromDirRejectsMissingDir(t *testing.T) {
	t.Parallel()
	_, err := ContentDigestFromDir(filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
}

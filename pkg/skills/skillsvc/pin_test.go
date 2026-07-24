// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

func TestBuildPinnedReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry lockfile.Entry
		want  string
	}{
		{
			name: "OCI reference pins to digest",
			entry: lockfile.Entry{
				ResolvedReference: "ghcr.io/org/code-review:1.0.0",
				Digest:            "sha256:" + hexDigestForTest(),
			},
			want: "ghcr.io/org/code-review@sha256:" + hexDigestForTest(),
		},
		{
			name: "git reference pins to commit hash, dropping any tag/branch ref",
			entry: lockfile.Entry{
				ResolvedReference: "git://github.com/org/skills@main#testing-conventions",
				Digest:            testCommitHash,
			},
			want: "git://github.com/org/skills@" + testCommitHash + "#testing-conventions",
		},
		{
			name: "git reference without a subdir",
			entry: lockfile.Entry{
				ResolvedReference: "git://github.com/org/skills",
				Digest:            testCommitHash,
			},
			want: "git://github.com/org/skills@" + testCommitHash,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildPinnedReference(tt.entry)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildPinnedReferenceRejectsUnparsable(t *testing.T) {
	t.Parallel()
	_, err := buildPinnedReference(lockfile.Entry{ResolvedReference: "not a valid reference!!", Digest: "sha256:abc"})
	require.Error(t, err)
}

func TestIsImmutableSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry lockfile.Entry
		want  bool
	}{
		{
			name:  "OCI digest source is immutable",
			entry: lockfile.Entry{Source: "ghcr.io/org/skill@sha256:" + hexDigestForTest()},
			want:  true,
		},
		{
			name:  "OCI tag source is mutable",
			entry: lockfile.Entry{Source: "ghcr.io/org/skill:1.0.0"},
			want:  false,
		},
		{
			name:  "git full commit hash source is immutable",
			entry: lockfile.Entry{Source: "git://github.com/org/skill@" + testCommitHash},
			want:  true,
		},
		{
			name:  "git branch source is mutable",
			entry: lockfile.Entry{Source: "git://github.com/org/skill@main"},
			want:  false,
		},
		{
			name:  "git source with no ref is mutable",
			entry: lockfile.Entry{Source: "git://github.com/org/skill"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isImmutableSource(tt.entry))
		})
	}
}

// hexDigestForTest returns a fixed, valid 64-char hex string for OCI digest fixtures.
func hexDigestForTest() string {
	const s = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	return s[:64]
}

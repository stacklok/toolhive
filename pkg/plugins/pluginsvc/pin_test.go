// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

const testCommitHash = "abcdef1234567890abcdef1234567890abcdef12"

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
				ResolvedReference: "git://github.com/org/plugins@main#testing-conventions",
				Digest:            testCommitHash,
			},
			want: "git://github.com/org/plugins@" + testCommitHash + "#testing-conventions",
		},
		{
			name: "git reference without a subdir",
			entry: lockfile.Entry{
				ResolvedReference: "git://github.com/org/plugins",
				Digest:            testCommitHash,
			},
			want: "git://github.com/org/plugins@" + testCommitHash,
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
			entry: lockfile.Entry{Source: "ghcr.io/org/plugin@sha256:" + hexDigestForTest()},
			want:  true,
		},
		{
			name:  "OCI tag source is mutable",
			entry: lockfile.Entry{Source: "ghcr.io/org/plugin:1.0.0"},
			want:  false,
		},
		{
			name:  "git full commit hash source is immutable",
			entry: lockfile.Entry{Source: "git://github.com/org/plugin@" + testCommitHash},
			want:  true,
		},
		{
			name:  "git branch source is mutable",
			entry: lockfile.Entry{Source: "git://github.com/org/plugin@main"},
			want:  false,
		},
		{
			name:  "git uppercase full commit hash source is immutable",
			entry: lockfile.Entry{Source: "git://github.com/org/plugin@ABCDEF0123456789ABCDEF0123456789ABCDEF01"},
			want:  true,
		},
		{
			name:  "git source with no ref is mutable",
			entry: lockfile.Entry{Source: "git://github.com/org/plugin"},
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

func TestRepositoryMoved(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + hexDigestForTest()
	tests := []struct {
		name   string
		oldRef string
		newRef string
		want   bool
	}{
		{
			name:   "identical references have not moved",
			oldRef: "ghcr.io/org/plugin:v1",
			newRef: "ghcr.io/org/plugin:v1",
		},
		{
			name:   "a version bump is not a move",
			oldRef: "ghcr.io/org/plugin:0.1.0",
			newRef: "ghcr.io/org/plugin:0.2.0",
		},
		{
			name:   "moving to a digest in the same repository is not a move",
			oldRef: "ghcr.io/org/plugin:0.1.0",
			newRef: "ghcr.io/org/plugin@" + digest,
		},
		{
			name:   "an implicit latest tag matches an explicit one",
			oldRef: "ghcr.io/org/plugin",
			newRef: "ghcr.io/org/plugin:latest",
		},
		{
			name:   "a different repository path is a move",
			oldRef: "ghcr.io/org/plugin:v1",
			newRef: "ghcr.io/org/other-plugin:v1",
			want:   true,
		},
		{
			name:   "a different org on the same registry is a move",
			oldRef: "ghcr.io/org/plugin:v1",
			newRef: "ghcr.io/attacker/plugin:v1",
			want:   true,
		},
		{
			name:   "a different registry is a move",
			oldRef: "ghcr.io/org/plugin:v1",
			newRef: "elsewhere.io/org/plugin:v1",
			want:   true,
		},
		{
			name:   "git references fall back to exact comparison",
			oldRef: "git://github.com/org/repo#plugins/a",
			newRef: "git://github.com/org/repo#plugins/b",
			want:   true,
		},
		{
			name:   "an OCI reference replaced by a git one is a move",
			oldRef: "ghcr.io/org/plugin:v1",
			newRef: "git://github.com/org/repo",
			want:   true,
		},
		{
			name:   "unparsable input fails closed",
			oldRef: "ghcr.io/org/plugin:v1",
			newRef: "not a valid reference at all",
			want:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, repositoryMoved(tc.oldRef, tc.newRef))
		})
	}
}

func hexDigestForTest() string {
	return "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
}

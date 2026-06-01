// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareImageTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		current   string
		candidate string
		want      tagComparison
	}{
		{
			name:      "candidate newer semver",
			current:   "ghcr.io/example/server:1.0.0",
			candidate: "ghcr.io/example/server:1.1.0",
			want:      comparisonNewer,
		},
		{
			name:      "candidate newer semver with v prefix",
			current:   "ghcr.io/example/server:v1.0.0",
			candidate: "ghcr.io/example/server:v2.0.0",
			want:      comparisonNewer,
		},
		{
			name:      "double-digit minor orders numerically not lexically",
			current:   "ghcr.io/example/server:1.9.0",
			candidate: "ghcr.io/example/server:1.10.0",
			want:      comparisonNewer,
		},
		{
			name:      "prerelease is older than its release",
			current:   "ghcr.io/example/server:1.2.3-rc1",
			candidate: "ghcr.io/example/server:1.2.3",
			want:      comparisonNewer,
		},
		{
			name:      "candidate equal semver",
			current:   "ghcr.io/example/server:1.2.3",
			candidate: "ghcr.io/example/server:1.2.3",
			want:      comparisonSameOrOlder,
		},
		{
			name:      "candidate older semver",
			current:   "ghcr.io/example/server:2.0.0",
			candidate: "ghcr.io/example/server:1.0.0",
			want:      comparisonSameOrOlder,
		},
		{
			name:      "mixed v-prefix normalization",
			current:   "ghcr.io/example/server:1.0.0",
			candidate: "ghcr.io/example/server:v1.5.0",
			want:      comparisonNewer,
		},
		{
			name:      "equal non-semver literal tag",
			current:   "ghcr.io/example/server:stable",
			candidate: "ghcr.io/example/server:stable",
			want:      comparisonSameOrOlder,
		},
		{
			name:      "different non-semver tags undecidable",
			current:   "ghcr.io/example/server:stable",
			candidate: "ghcr.io/example/server:edge",
			want:      comparisonUndecidable,
		},
		{
			name:      "different repositories undecidable",
			current:   "ghcr.io/example/server:1.0.0",
			candidate: "ghcr.io/other/server:1.1.0",
			want:      comparisonUndecidable,
		},
		{
			name:      "latest on candidate undecidable",
			current:   "ghcr.io/example/server:1.0.0",
			candidate: "ghcr.io/example/server:latest",
			want:      comparisonUndecidable,
		},
		{
			name:      "latest on current undecidable",
			current:   "ghcr.io/example/server:latest",
			candidate: "ghcr.io/example/server:1.0.0",
			want:      comparisonUndecidable,
		},
		{
			name:      "implicit latest (no tag) undecidable",
			current:   "ghcr.io/example/server",
			candidate: "ghcr.io/example/server:1.0.0",
			want:      comparisonUndecidable,
		},
		{
			name:      "docker hub short name normalized to same repo",
			current:   "library/nginx:1.0.0",
			candidate: "library/nginx:1.1.0",
			want:      comparisonNewer,
		},
		{
			name:      "digest reference is undecidable",
			current:   "ghcr.io/example/server@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
			candidate: "ghcr.io/example/server:1.1.0",
			want:      comparisonUndecidable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := compareImageTags(tt.current, tt.candidate)
			assert.Equal(t, tt.want, got)
			if got == comparisonUndecidable {
				assert.NotEmpty(t, reason, "undecidable result must carry a reason")
			} else {
				assert.Empty(t, reason, "decidable result must not carry a reason")
			}
		})
	}
}

func TestSplitRepoTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      string
		wantRepo string
		wantTag  string
		wantErr  bool
	}{
		{
			name:     "fully qualified ref",
			ref:      "ghcr.io/example/server:1.0.0",
			wantRepo: "ghcr.io/example/server",
			wantTag:  "1.0.0",
		},
		{
			name:     "docker hub short name normalized",
			ref:      "library/nginx:1.0.0",
			wantRepo: "index.docker.io/library/nginx",
			wantTag:  "1.0.0",
		},
		{
			name:     "no tag defaults to latest",
			ref:      "ghcr.io/example/server",
			wantRepo: "ghcr.io/example/server",
			wantTag:  "latest",
		},
		{
			name:    "digest reference rejected",
			ref:     "ghcr.io/example/server@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
		{
			name:    "invalid reference",
			ref:     "::::not a ref::::",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo, tag, err := splitRepoTag(tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

func TestNormalizeSemver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare version gets v prefix", in: "1.2.3", want: "v1.2.3"},
		{name: "already prefixed unchanged", in: "v1.2.3", want: "v1.2.3"},
		{name: "uppercase V unchanged", in: "V1.2.3", want: "V1.2.3"},
		{name: "non-semver tag unchanged", in: "stable", want: "stable"},
		{name: "empty unchanged", in: "", want: ""},
		{name: "numeric prefix gets v", in: "2024.01", want: "v2024.01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeSemver(tt.in))
		})
	}
}

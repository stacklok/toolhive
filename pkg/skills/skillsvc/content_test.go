// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
)

func TestGetContent(t *testing.T) {
	t.Parallel()

	t.Run("nil oci store returns 500", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.GetContent(t.Context(), skills.ContentOptions{Reference: "my-skill"})
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("empty reference returns 400", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)
		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: ""})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("local build tag resolves without registry", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), d, "my-skill"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{Reference: "my-skill"})
		require.NoError(t, err)

		assert.Equal(t, "my-skill", content.Name)
		assert.Equal(t, "1.0.0", content.Version)
		assert.NotEmpty(t, content.Body)
		assert.NotEmpty(t, content.Files)

		// SKILL.md must appear in the file list.
		var skillMDFound bool
		for _, f := range content.Files {
			if strings.EqualFold(filepath.Base(f.Path), "SKILL.md") {
				skillMDFound = true
				assert.Greater(t, f.Size, 0)
			}
		}
		assert.True(t, skillMDFound, "SKILL.md should be listed in Files")
	})

	t.Run("remote OCI reference triggers pull", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)
		indexDigest := buildTestArtifact(t, ociStore, "my-skill", "2.0.0")

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v2").
			Return(indexDigest, nil)

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
		)
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "ghcr.io/org/my-skill:v2",
		})
		require.NoError(t, err)

		assert.Equal(t, "my-skill", content.Name)
		assert.Equal(t, "2.0.0", content.Version)
		assert.NotEmpty(t, content.Body)
	})

	t.Run("unqualified name not in store without registry returns 400", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "nonexistent"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("nil registry with unresolvable remote ref returns 400", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		// "ghcr.io/org/skill:v1" is a valid OCI ref but registry is nil.
		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/skill:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("pull failure propagates as 400", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
			Return(godigest.Digest(""), fmt.Errorf("registry unreachable"))

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
		)
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		assert.Contains(t, err.Error(), "registry unreachable")
	})

	t.Run("git reference resolves via git resolver", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		gr := gitmocks.NewMockResolver(ctrl)
		gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
			SkillConfig: &skills.ParseResult{
				Name:        "my-skill",
				Description: "a git skill",
				Version:     "1.0.0",
				Body:        []byte("# My Skill\nHello from git"),
			},
			Files: []gitresolver.FileEntry{
				{Path: "SKILL.md", Content: []byte("# My Skill\nHello from git"), Mode: 0644},
				{Path: "hooks.sh", Content: []byte("#!/bin/sh"), Mode: 0644},
			},
			CommitHash: testCommitHash,
		}, nil)

		svc := New(&storage.NoopSkillStore{}, WithGitResolver(gr))
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "git://github.com/test/my-skill#skills/my-skill",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-skill", content.Name)
		assert.Equal(t, "a git skill", content.Description)
		assert.Equal(t, "1.0.0", content.Version)
		assert.Contains(t, content.Body, "Hello from git")
		assert.Len(t, content.Files, 2)
	})

	t.Run("git resolve failure returns 502", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		gr := gitmocks.NewMockResolver(ctrl)
		gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("clone failed"))

		svc := New(&storage.NoopSkillStore{}, WithGitResolver(gr))
		_, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "git://github.com/test/my-skill",
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadGateway, httperr.Code(err))
		assert.Contains(t, err.Error(), "resolving git skill")
	})

	t.Run("nil git resolver returns 500", func(t *testing.T) {
		t.Parallel()
		svc := &service{}
		_, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "git://github.com/test/my-skill",
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("registry name falls back to git resolver", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("skill-creator").Return([]regtypes.Skill{
			{
				Namespace: "io.github.stacklok",
				Name:      "skill-creator",
				Packages: []regtypes.SkillPackage{
					{
						RegistryType: "git",
						URL:          "https://github.com/stacklok/toolhive-catalog",
						Subfolder:    "registries/toolhive/skills/skill-creator",
					},
				},
			},
		}, nil)

		gr := gitmocks.NewMockResolver(ctrl)
		gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, ref *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
				assert.Equal(t, "registries/toolhive/skills/skill-creator", ref.Path)
				return &gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{
						Name:        "skill-creator",
						Description: "creates skills",
						Body:        []byte("# Skill Creator"),
					},
					Files: []gitresolver.FileEntry{
						{Path: "SKILL.md", Content: []byte("# Skill Creator"), Mode: 0644},
					},
					CommitHash: testCommitHash,
				}, nil
			})

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithSkillLookup(lookup),
			WithGitResolver(gr),
		)
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "io.github.stacklok/skill-creator",
		})
		require.NoError(t, err)
		assert.Equal(t, "skill-creator", content.Name)
		assert.Contains(t, content.Body, "Skill Creator")
	})

	t.Run("qualified namespace/name filters registry matches", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
			{Namespace: "io.github.alice", Name: "my-skill",
				Packages: []regtypes.SkillPackage{{RegistryType: "git", URL: "https://github.com/alice/repo"}}},
			{Namespace: "io.github.bob", Name: "my-skill",
				Packages: []regtypes.SkillPackage{{RegistryType: "git", URL: "https://github.com/bob/repo"}}},
		}, nil)

		gr := gitmocks.NewMockResolver(ctrl)
		gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
			SkillConfig: &skills.ParseResult{Name: "my-skill", Body: []byte("# Bob's skill")},
			Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("# Bob"), Mode: 0644}},
			CommitHash:  testCommitHash,
		}, nil)

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithSkillLookup(lookup),
			WithGitResolver(gr),
		)
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "io.github.bob/my-skill",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-skill", content.Name)
	})
}

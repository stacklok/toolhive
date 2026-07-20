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

	t.Run("pull failure propagates as 502", func(t *testing.T) {
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
		assert.Equal(t, http.StatusBadGateway, httperr.Code(err))
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

	t.Run("remote pull does not pollute ListBuilds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		// The real Pull would tag the pulled artifact in the local store.
		// Simulate that side-effect here so we can verify ListBuilds still
		// reports an empty list: pulls tag by digest, which yields a plain
		// descriptor, so the local-build marker is never applied.
		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().
			Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
			DoAndReturn(func(ctx context.Context, store *ociskills.Store, _ string) (godigest.Digest, error) {
				d := buildTestArtifact(t, store, "my-skill", "1.0.0")
				require.NoError(t, store.Tag(ctx, d, "ghcr.io/org/my-skill:v1"))
				return d, nil
			})

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
		)

		// Baseline: no builds before the content request.
		builds, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Empty(t, builds)

		content, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "ghcr.io/org/my-skill:v1",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-skill", content.Name)

		// After a content-preview pull, ListBuilds must still be empty: the
		// blobs stay on disk as a cache but the tag is not treated as a local build.
		builds, err = svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, builds, "content API must not leak pulled artifacts into ListBuilds")
	})

	t.Run("unambiguous OCI falls back to registry-declared git package on pull failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/stacklok/dockyard/skills/yara-rule-authoring:0.1.0").
			Return(godigest.Digest(""), fmt.Errorf("registry unreachable"))

		// Registry entry has both an OCI package (the one we just failed to
		// pull) and a git package with pinned commit. The fallback should
		// reach the git resolver.
		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("yara-rule-authoring").Return([]regtypes.Skill{
			{
				Namespace: "io.github.stacklok",
				Name:      "yara-rule-authoring",
				Packages: []regtypes.SkillPackage{
					{
						RegistryType: "oci",
						Identifier:   "ghcr.io/stacklok/dockyard/skills/yara-rule-authoring:0.1.0",
					},
					{
						RegistryType: "git",
						URL:          "https://github.com/trailofbits/skills",
						Ref:          "e8cc5baf9329ccb491bfa200e82eacbac83b1ead",
						Subfolder:    "plugins/yara-authoring/skills/yara-rule-authoring",
					},
				},
			},
		}, nil)

		gr := gitmocks.NewMockResolver(ctrl)
		gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, ref *gitresolver.GitReference) (*gitresolver.ResolveResult, error) {
				// Verify the fallback pinned ref and subfolder. Scheme depends on
				// TOOLHIVE_DEV (http in dev, https in prod) so we only assert the
				// suffix here.
				assert.True(t, strings.HasSuffix(ref.URL, "://github.com/trailofbits/skills"),
					"unexpected clone URL %q", ref.URL)
				assert.Equal(t, "e8cc5baf9329ccb491bfa200e82eacbac83b1ead", ref.Ref)
				assert.Equal(t, "plugins/yara-authoring/skills/yara-rule-authoring", ref.Path)
				return &gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{
						Name: "yara-rule-authoring",
						Body: []byte("# YARA Rule Authoring"),
					},
					Files:      []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("# YARA"), Mode: 0644}},
					CommitHash: testCommitHash,
				}, nil
			})

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithSkillLookup(lookup),
			WithGitResolver(gr),
		)
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{
			Reference: "ghcr.io/stacklok/dockyard/skills/yara-rule-authoring:0.1.0",
		})
		require.NoError(t, err)
		assert.Equal(t, "yara-rule-authoring", content.Name)
		assert.Contains(t, content.Body, "YARA Rule Authoring")
	})

	t.Run("registry fallback tolerates different OCI version tag", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v9").
			Return(godigest.Digest(""), fmt.Errorf("manifest unknown"))

		// The registry entry records :0.1.0 while the caller asked for :v9.
		// Both resolve to the same repository path so the fallback must fire.
		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
			{
				Namespace: "io.github.example",
				Name:      "my-skill",
				Packages: []regtypes.SkillPackage{
					{RegistryType: "oci", Identifier: "ghcr.io/org/my-skill:0.1.0"},
					{RegistryType: "git", URL: "https://github.com/example/repo"},
				},
			},
		}, nil)

		gr := gitmocks.NewMockResolver(ctrl)
		gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
			SkillConfig: &skills.ParseResult{Name: "my-skill", Body: []byte("# git fallback")},
			Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("# x"), Mode: 0644}},
			CommitHash:  testCommitHash,
		}, nil)

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithSkillLookup(lookup),
			WithGitResolver(gr),
		)
		content, err := svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v9"})
		require.NoError(t, err)
		assert.Equal(t, "my-skill", content.Name)
	})

	t.Run("OCI failure with registry match but no git package returns original error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
			Return(godigest.Digest(""), fmt.Errorf("registry offline"))

		// Registry entry matches but only has an OCI package.
		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
			{
				Namespace: "io.github.example",
				Name:      "my-skill",
				Packages: []regtypes.SkillPackage{
					{RegistryType: "oci", Identifier: "ghcr.io/org/my-skill:v1"},
				},
			},
		}, nil)

		// Git resolver must NOT be invoked.
		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithSkillLookup(lookup),
		)
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadGateway, httperr.Code(err))
		assert.Contains(t, err.Error(), "registry offline")
	})

	t.Run("OCI failure with ambiguous registry matches skips git fallback", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
			Return(godigest.Digest(""), fmt.Errorf("registry offline"))

		// Two registry entries both point at the same repo path — ambiguous.
		// We refuse to guess and propagate the original OCI error.
		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
			{
				Namespace: "io.github.alice",
				Name:      "my-skill",
				Packages: []regtypes.SkillPackage{
					{RegistryType: "oci", Identifier: "ghcr.io/org/my-skill:v1"},
					{RegistryType: "git", URL: "https://github.com/alice/repo"},
				},
			},
			{
				Namespace: "io.github.bob",
				Name:      "my-skill",
				Packages: []regtypes.SkillPackage{
					{RegistryType: "oci", Identifier: "ghcr.io/org/my-skill:v2"},
					{RegistryType: "git", URL: "https://github.com/bob/repo"},
				},
			},
		}, nil)

		// Git resolver must NOT be invoked.
		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithSkillLookup(lookup),
		)
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadGateway, httperr.Code(err))
		assert.Contains(t, err.Error(), "registry offline")
	})

	t.Run("OCI success skips registry lookup entirely", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)
		indexDigest := buildTestArtifact(t, ociStore, "my-skill", "2.0.0")

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v2").
			Return(indexDigest, nil)

		// lookup mock with NO expectations — gomock will fail the test if
		// SearchSkills is ever invoked.
		lookup := regmocks.NewMockProvider(ctrl)

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithSkillLookup(lookup),
		)
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v2"})
		require.NoError(t, err)
	})

	t.Run("registry lookup error treated as no fallback, returns original OCI error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
			Return(godigest.Digest(""), fmt.Errorf("registry offline"))

		lookup := regmocks.NewMockProvider(ctrl)
		lookup.EXPECT().SearchSkills("my-skill").Return(nil, fmt.Errorf("registry index unreachable"))

		svc := New(&storage.NoopSkillStore{},
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithSkillLookup(lookup),
		)
		_, err = svc.GetContent(t.Context(), skills.ContentOptions{Reference: "ghcr.io/org/my-skill:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadGateway, httperr.Code(err))
		assert.Contains(t, err.Error(), "registry offline")
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid skill directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		skillDir := filepath.Join(dir, "test-skill")
		require.NoError(t, os.MkdirAll(skillDir, 0o750))
		require.NoError(t, os.WriteFile(
			filepath.Join(skillDir, "SKILL.md"),
			[]byte("---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\n"),
			0o600,
		))

		svc := New(&storage.NoopSkillStore{})
		result, err := svc.Validate(t.Context(), skillDir)
		require.NoError(t, err)
		assert.True(t, result.Valid)
	})

	t.Run("missing SKILL.md", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		result, err := svc.Validate(t.Context(), t.TempDir())
		require.NoError(t, err)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors, "SKILL.md not found in skill directory")
	})

	t.Run("empty path returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.Validate(t.Context(), "")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("relative path returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.Validate(t.Context(), "relative/path")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("path traversal returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.Validate(t.Context(), "/foo/../../../etc")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})
}

// putTestManifest stores a minimal manifest in the OCI store and returns its digest.
func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     skills.BuildOptions
		setup    func(*gomock.Controller) (ociskills.SkillPackager, *ociskills.Store)
		wantCode int
		wantRef  string
		wantErr  string
	}{
		{
			name: "nil packager returns 500",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(_ *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				return nil, nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "empty path returns 400",
			opts: skills.BuildOptions{Path: ""},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "relative path returns 400",
			opts: skills.BuildOptions{Path: "relative/path"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "path traversal returns 400",
			opts: skills.BuildOptions{Path: "/some/dir/../../../etc"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "invalid tag returns 400",
			opts: skills.BuildOptions{Path: "/some/dir", Tag: "invalid tag!@#"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{},
					}, nil)
				return p, ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "packager error propagates",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("packaging failed"))
				return p, ociStore
			},
			wantErr: "packaging skill",
		},
		{
			name: "successful build with explicit tag",
			opts: skills.BuildOptions{Path: "/some/dir", Tag: "v1.0.0"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{Name: "my-skill"},
					}, nil)
				return p, ociStore
			},
			wantRef: "v1.0.0",
		},
		{
			name: "build without tag uses config name",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{Name: "my-skill"},
					}, nil)
				return p, ociStore
			},
			wantRef: "my-skill",
		},
		{
			name: "build without tag or config name returns digest",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{},
					}, nil)
				return p, ociStore
			},
			// wantRef is set dynamically below since the digest depends on store content
		},
		{
			name: "invalid fallback config name returns 400",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{Name: "invalid name!@#"},
					}, nil)
				return p, ociStore
			},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			packager, ociStore := tt.setup(ctrl)

			svc := New(&storage.NoopSkillStore{},
				WithPackager(packager),
				WithOCIStore(ociStore),
			)

			result, err := svc.Build(t.Context(), tt.opts)
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantRef != "" {
				assert.Equal(t, tt.wantRef, result.Reference)
			} else {
				// Fallback case returns a digest string
				assert.Contains(t, result.Reference, "sha256:")
			}
		})
	}
}

func TestPush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     skills.PushOptions
		setup    func(*gomock.Controller) (ociskills.RegistryClient, *ociskills.Store)
		wantCode int
		wantErr  string
	}{
		{
			name: "nil registry returns 500",
			opts: skills.PushOptions{Reference: "ghcr.io/test/skill:v1"},
			setup: func(_ *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				return nil, nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "empty reference returns 400",
			opts: skills.PushOptions{Reference: ""},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "resolve not found returns 404",
			opts: skills.PushOptions{Reference: "nonexistent"},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore
			},
			wantCode: http.StatusNotFound,
		},
		{
			name: "registry push error propagates",
			opts: skills.PushOptions{Reference: "my-tag"},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				// Create a manifest so Resolve succeeds.
				d, tagErr := ociStore.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
				require.NoError(t, tagErr)
				require.NoError(t, ociStore.Tag(t.Context(), d, "my-tag"))

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Push(gomock.Any(), ociStore, d, "my-tag").
					Return(fmt.Errorf("auth failed"))
				return reg, ociStore
			},
			wantErr: "pushing to registry",
		},
		{
			name: "successful push",
			opts: skills.PushOptions{Reference: "my-tag"},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d, tagErr := ociStore.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
				require.NoError(t, tagErr)
				require.NoError(t, ociStore.Tag(t.Context(), d, "my-tag"))

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Push(gomock.Any(), ociStore, d, "my-tag").Return(nil)
				return reg, ociStore
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			registry, ociStore := tt.setup(ctrl)

			svc := New(&storage.NoopSkillStore{},
				WithRegistryClient(registry),
				WithOCIStore(ociStore),
			)

			err := svc.Push(t.Context(), tt.opts)
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateOCITagOrReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		// Valid bare tags
		{name: "simple version", tag: "v1.0.0", wantErr: false},
		{name: "latest", tag: "latest", wantErr: false},
		{name: "numeric", tag: "123", wantErr: false},
		{name: "with dots", tag: "1.2.3", wantErr: false},
		{name: "with hyphens", tag: "my-skill", wantErr: false},
		{name: "with underscores", tag: "my_skill", wantErr: false},
		{name: "mixed alphanumeric", tag: "v1.0.0-rc.1", wantErr: false},
		{name: "uppercase", tag: "MyTag", wantErr: false},
		{name: "single char", tag: "a", wantErr: false},
		{name: "max length 128 chars", tag: strings.Repeat("a", 128), wantErr: false},
		{name: "exceeds max length 129 chars", tag: strings.Repeat("a", 129), wantErr: true},

		// Valid full OCI references
		{name: "ghcr tagged reference", tag: "ghcr.io/stacklok/toolhive-skills/my-skill:v1.0.0", wantErr: false},
		{name: "CI format tag", tag: "ghcr.io/stacklok/toolhive-skills/my-skill:0.0.1-dev.123_abc1234", wantErr: false},
		{name: "docker hub reference", tag: "docker.io/library/nginx:1.25", wantErr: false},
		{name: "localhost with port", tag: "localhost:5000/my-skill:v1", wantErr: false},
		{name: "digest reference", tag: "ghcr.io/org/repo@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", wantErr: false},

		// Invalid bare tags
		{name: "empty string", tag: "", wantErr: true},
		{name: "contains space", tag: "invalid tag", wantErr: true},
		{name: "contains exclamation", tag: "invalid!", wantErr: true},
		{name: "contains hash", tag: "invalid#tag", wantErr: true},

		// Invalid full references
		{name: "space in tag of reference", tag: "ghcr.io/org/repo:invalid tag", wantErr: true},
		{name: "empty tag after colon", tag: "ghcr.io/org/repo:", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateOCITagOrReference(tt.tag)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid OCI reference or tag")
				// Verify it returns a proper HTTP status code.
				var coded *httperr.CodedError
				require.ErrorAs(t, err, &coded)
				assert.Equal(t, http.StatusBadRequest, coded.HTTPCode())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestListBuilds(t *testing.T) {
	t.Parallel()

	t.Run("nil oci store returns 500", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.ListBuilds(t.Context())
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("empty store returns empty list", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, artifacts)
	})

	t.Run("lists tagged artifacts with metadata", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		// Build a real artifact via the packager so extractOCIContent works.
		d := buildTestArtifact(t, ociStore, "my-skill", "1.2.3")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "my-skill"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)

		assert.Equal(t, "my-skill", artifacts[0].Tag)
		assert.Contains(t, artifacts[0].Digest, "sha256:")
		assert.Equal(t, "my-skill", artifacts[0].Name)
		assert.Equal(t, "1.2.3", artifacts[0].Version)
	})

	t.Run("lists multiple tagged artifacts", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		d1 := buildTestArtifact(t, ociStore, "skill-a", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d1, "skill-a"))
		d2 := buildTestArtifact(t, ociStore, "skill-b", "2.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d2, "skill-b"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Len(t, artifacts, 2)

		// Collect names for assertion (order may vary).
		names := make(map[string]string)
		for _, a := range artifacts {
			names[a.Tag] = a.Version
		}
		assert.Equal(t, "1.0.0", names["skill-a"])
		assert.Equal(t, "2.0.0", names["skill-b"])
	})

	t.Run("skill artifact with no extractable metadata still appears", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		// Store an index with ArtifactType set to the skill type but no child manifests —
		// extractOCIContent will fail but the artifact should still appear with empty metadata fields.
		skillIndex := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","artifactType":"dev.toolhive.skills.v1","manifests":[]}`
		d, putErr := ociStore.PutManifest(t.Context(), []byte(skillIndex))
		require.NoError(t, putErr)
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "bare-skill-tag"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)

		assert.Equal(t, "bare-skill-tag", artifacts[0].Tag)
		assert.Contains(t, artifacts[0].Digest, "sha256:")
		assert.Empty(t, artifacts[0].Name)
		assert.Empty(t, artifacts[0].Version)
	})

	t.Run("non-skill artifact is excluded", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		// Store a valid skill artifact that should be returned.
		skillDigest := buildTestArtifact(t, ociStore, "real-skill", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, skillDigest, "real-skill"))

		// Store an index whose ArtifactType is not the skill type. Tagging it
		// as a local build simulates a caller that mistakenly flagged a
		// non-skill artifact — ListBuilds must still exclude it by type.
		otherIndex := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","artifactType":"application/vnd.docker.distribution.manifest.v2","manifests":[]}`
		otherDigest, putErr := ociStore.PutManifest(t.Context(), []byte(otherIndex))
		require.NoError(t, putErr)
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, otherDigest, "non-skill-tag"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)
		assert.Equal(t, "real-skill", artifacts[0].Tag)
	})

	t.Run("pulled tags are hidden from ListBuilds", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))

		// Simulate a pull: tag the artifact via the plain ociStore.Tag path,
		// which mirrors what Registry.Pull does internally (resolve by digest
		// → plain descriptor → no local-build annotation).
		d := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), d, "ghcr.io/org/my-skill:v1.0.0"))

		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, artifacts, "pulled tags must not appear in ListBuilds")
	})

	t.Run("only locally-built tags are listed when pull and build coexist", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))

		// Pulled artifact: tagged without the local-build marker.
		pulled := buildTestArtifact(t, ociStore, "pulled-skill", "9.9.9")
		require.NoError(t, ociStore.Tag(t.Context(), pulled, "ghcr.io/org/pulled-skill:v9.9.9"))

		// Locally-built artifact: tagged with the marker.
		built := buildTestArtifact(t, ociStore, "built-skill", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, built, "built-skill"))

		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)
		assert.Equal(t, "built-skill", artifacts[0].Tag)
	})

	t.Run("pre-feature tags without the marker do not appear", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		// Tag via the plain path, as if the artifact had been built before
		// this feature existed. The honest gap: ListBuilds hides it until
		// the user rebuilds and re-tags.
		d := buildTestArtifact(t, ociStore, "legacy-build", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), d, "legacy-build"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, artifacts)
	})
}

func TestDeleteBuild(t *testing.T) {
	t.Parallel()

	t.Run("nil oci store returns 500", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		err := svc.DeleteBuild(t.Context(), "my-skill")
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("removes tag and blobs", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "my-skill"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		require.NoError(t, svc.DeleteBuild(t.Context(), "my-skill"))

		// Tag should be gone — ListBuilds should return empty.
		builds, listErr := svc.ListBuilds(t.Context())
		require.NoError(t, listErr)
		assert.Empty(t, builds)
	})

	t.Run("tag does not exist returns 404", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		err = svc.DeleteBuild(t.Context(), "nonexistent")
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
	})

	t.Run("blobs retained when another tag shares the same digest", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociskills.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestArtifact(t, ociStore, "shared-skill", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "tag-a"))
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "tag-b"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		require.NoError(t, svc.DeleteBuild(t.Context(), "tag-a"))

		// tag-b still exists and the shared artifact is accessible.
		builds, listErr := svc.ListBuilds(t.Context())
		require.NoError(t, listErr)
		require.Len(t, builds, 1)
		assert.Equal(t, "tag-b", builds[0].Tag)
	})

	t.Run("delete removes local-build marker from index.json", func(t *testing.T) {
		t.Parallel()
		storeRoot := t.TempDir()
		ociStore, err := ociskills.NewStore(storeRoot)
		require.NoError(t, err)

		d := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "my-skill"))

		// Sanity: the marker is on the tagged descriptor.
		require.True(t, indexContainsTaggedMarker(t, storeRoot, "my-skill"))

		svc := New(&storage.NoopSkillStore{}, WithOCIStore(ociStore))
		require.NoError(t, svc.DeleteBuild(t.Context(), "my-skill"))

		assert.False(t, indexContainsTaggedMarker(t, storeRoot, "my-skill"),
			"descriptor carrying the marker must be gone after DeleteBuild")
	})
}

func TestBuild_StampsLocalBuildAnnotation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	storeRoot := t.TempDir()
	ociStore, err := ociskills.NewStore(storeRoot)
	require.NoError(t, err)

	d := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

	p := ocimocks.NewMockSkillPackager(ctrl)
	p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
		Return(&ociskills.PackageResult{
			IndexDigest: d,
			Config:      &ociskills.SkillConfig{Name: "my-skill"},
		}, nil)

	svc := New(&storage.NoopSkillStore{},
		WithPackager(p),
		WithOCIStore(ociStore),
	)

	_, err = svc.Build(t.Context(), skills.BuildOptions{Path: "/some/dir", Tag: "my-skill"})
	require.NoError(t, err)

	// After a successful Build, the tag must be surfaced by ListBuilds
	// because the root-index descriptor carries the local-build marker.
	builds, err := svc.ListBuilds(t.Context())
	require.NoError(t, err)
	require.Len(t, builds, 1)
	assert.Equal(t, "my-skill", builds[0].Tag)

	// The marker must land on the descriptor entry in index.json.
	assert.True(t, indexContainsTaggedMarker(t, storeRoot, "my-skill"),
		"root index.json must carry the local-build annotation for the tag")
}

// indexContainsTaggedMarker reads the OCI store's root index.json and reports
// whether the descriptor entry tagged `tag` has the local-build annotation.
func indexContainsTaggedMarker(t *testing.T, storeRoot, tag string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(storeRoot, "index.json"))
	require.NoError(t, err)
	var idx ocispec.Index
	require.NoError(t, json.Unmarshal(data, &idx))
	for _, m := range idx.Manifests {
		if m.Annotations[ocispec.AnnotationRefName] != tag {
			continue
		}
		if m.Annotations[LocalBuildAnnotation] == "true" {
			return true
		}
	}
	return false
}

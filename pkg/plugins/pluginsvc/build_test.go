// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid plugin directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		pluginDir := filepath.Join(dir, "test-plugin")
		require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o750))
		require.NoError(t, os.WriteFile(
			filepath.Join(pluginDir, ".claude-plugin", "plugin.json"),
			[]byte(`{"name":"test-plugin","description":"A test plugin","keywords":["a"]}`),
			0o600,
		))

		svc := New()
		result, err := svc.Validate(t.Context(), pluginDir)
		require.NoError(t, err)
		assert.True(t, result.Valid, "errors: %v", result.Errors)
	})

	t.Run("missing plugin.json", func(t *testing.T) {
		t.Parallel()
		svc := New()
		result, err := svc.Validate(t.Context(), t.TempDir())
		require.NoError(t, err)
		assert.False(t, result.Valid)
		assert.Contains(t, joinStrings(result.Errors), "plugin.json")
	})

	t.Run("empty path returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New()
		_, err := svc.Validate(t.Context(), "")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("relative path returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New()
		_, err := svc.Validate(t.Context(), "relative/path")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("path traversal returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New()
		_, err := svc.Validate(t.Context(), "/foo/../../../etc")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("path with null byte returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New()
		_, err := svc.Validate(t.Context(), "/safe\x00/evil")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		assert.Contains(t, err.Error(), "null bytes")
	})
}

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      plugins.BuildOptions
		setup     func(*gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store)
		wantCode  int
		wantRef   string
		wantErr   string
		wantErrIs error
	}{
		{
			name: "nil packager returns 500",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(_ *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				return nil, nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "empty path returns 400",
			opts: plugins.BuildOptions{Path: ""},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockPluginPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "relative path returns 400",
			opts: plugins.BuildOptions{Path: "relative/path"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockPluginPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "path traversal returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir/../../../etc"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockPluginPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "invalid tag returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir", Tag: "invalid tag!@#"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociplugins.PackageResult{
						IndexDigest: d,
						Config:      &ociplugins.PluginConfig{},
					}, nil)
				return p, ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "packager error propagates",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("packaging failed"))
				return p, ociStore
			},
			wantErr: "packaging plugin",
		},
		{
			name: "missing manifest returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("reading plugin directory: %w", ociplugins.ErrPluginManifestMissing))
				return p, ociStore
			},
			wantCode:  http.StatusBadRequest,
			wantErrIs: ociplugins.ErrPluginManifestMissing,
		},
		{
			name: "invalid manifest returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("parsing manifest: %w", ociplugins.ErrInvalidPluginManifest))
				return p, ociStore
			},
			wantCode:  http.StatusBadRequest,
			wantErrIs: ociplugins.ErrInvalidPluginManifest,
		},
		{
			name: "invalid plugin dir returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("plugin directory not found: %w", ociplugins.ErrInvalidPluginDir))
				return p, ociStore
			},
			wantCode:  http.StatusBadRequest,
			wantErrIs: ociplugins.ErrInvalidPluginDir,
		},
		{
			name: "symlink in plugin dir returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("symlinks not allowed in plugin directory: sub/link: %w", ociplugins.ErrInvalidPluginFile))
				return p, ociStore
			},
			wantCode:  http.StatusBadRequest,
			wantErrIs: ociplugins.ErrInvalidPluginFile,
		},
		{
			name: "oversized dir returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("plugin directory exceeds maximum total size: %w", ociplugins.ErrPluginTooLarge))
				return p, ociStore
			},
			wantCode:  http.StatusBadRequest,
			wantErrIs: ociplugins.ErrPluginTooLarge,
		},
		{
			name: "too many files returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("plugin directory exceeds maximum files: %w", ociplugins.ErrTooManyFiles))
				return p, ociStore
			},
			wantCode:  http.StatusBadRequest,
			wantErrIs: ociplugins.ErrTooManyFiles,
		},
		{
			name: "successful build with explicit tag",
			opts: plugins.BuildOptions{Path: "/some/dir", Tag: "v1.0.0"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociplugins.PackageResult{
						IndexDigest: d,
						Config:      &ociplugins.PluginConfig{Name: "my-plugin"},
					}, nil)
				return p, ociStore
			},
			wantRef: "v1.0.0",
		},
		{
			name: "build without tag uses config name",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociplugins.PackageResult{
						IndexDigest: d,
						Config:      &ociplugins.PluginConfig{Name: "my-plugin"},
					}, nil)
				return p, ociStore
			},
			wantRef: "my-plugin",
		},
		{
			name: "build without tag or config name returns digest",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociplugins.PackageResult{
						IndexDigest: d,
						Config:      &ociplugins.PluginConfig{},
					}, nil)
				return p, ociStore
			},
			// wantRef set dynamically below
		},
		{
			name: "invalid fallback config name returns 400",
			opts: plugins.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociplugins.PluginPackager, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockPluginPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociplugins.PackageResult{
						IndexDigest: d,
						Config:      &ociplugins.PluginConfig{Name: "invalid name!@#"},
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

			svc := New(
				WithPackager(packager),
				WithOCIStore(ociStore),
			)

			result, err := svc.Build(t.Context(), tt.opts)
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
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
				// Fallback case returns a digest string.
				assert.Contains(t, result.Reference, "sha256:")
			}
		})
	}
}

// TestBuild_Determinism is an EXIT GATE test: building the same plugin dir
// twice into two independent stores must yield identical index digests.
func TestBuild_Determinism(t *testing.T) {
	t.Parallel()

	pluginDir := filepath.Join(t.TempDir(), "det-plugin")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o750))
	manifest := `{"name":"det-plugin","description":"determinism test","version":"1.0.0","license":"Apache-2.0","keywords":["t"]}`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ociplugins.ManifestFileName), []byte(manifest), 0o600))

	store1, err := ociplugins.NewStore(t.TempDir())
	require.NoError(t, err)
	store2, err := ociplugins.NewStore(t.TempDir())
	require.NoError(t, err)

	p1 := ociplugins.NewPackager(store1)
	p2 := ociplugins.NewPackager(store2)

	r1, err := p1.Package(t.Context(), pluginDir, ociplugins.DefaultPackageOptions())
	require.NoError(t, err)
	r2, err := p2.Package(t.Context(), pluginDir, ociplugins.DefaultPackageOptions())
	require.NoError(t, err)

	assert.Equal(t, r1.IndexDigest, r2.IndexDigest, "IndexDigest must be identical for identical input")
	assert.Equal(t, r1.ManifestDigest, r2.ManifestDigest, "ManifestDigest must be identical for identical input")
	assert.Equal(t, r1.ConfigDigest, r2.ConfigDigest, "ConfigDigest must be identical for identical input")
	assert.Equal(t, r1.LayerDigest, r2.LayerDigest, "LayerDigest must be identical for identical input")
}

func TestPush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     plugins.PushOptions
		setup    func(*gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store)
		wantCode int
		wantErr  string
	}{
		{
			name: "nil registry returns 500",
			opts: plugins.PushOptions{Reference: "ghcr.io/test/plugin:v1"},
			setup: func(_ *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store) {
				return nil, nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "empty reference returns 400",
			opts: plugins.PushOptions{Reference: ""},
			setup: func(ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "resolve not found returns 404",
			opts: plugins.PushOptions{Reference: "nonexistent"},
			setup: func(ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore
			},
			wantCode: http.StatusNotFound,
		},
		{
			name: "registry push error propagates",
			opts: plugins.PushOptions{Reference: "my-tag"},
			setup: func(ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
				require.NoError(t, err)
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
			opts: plugins.PushOptions{Reference: "my-tag"},
			setup: func(ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store) {
				ociStore, err := ociplugins.NewStore(t.TempDir())
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

			svc := New(
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
		{name: "simple version", tag: "v1.0.0", wantErr: false},
		{name: "latest", tag: "latest", wantErr: false},
		{name: "numeric", tag: "123", wantErr: false},
		{name: "with dots", tag: "1.2.3", wantErr: false},
		{name: "with hyphens", tag: "my-plugin", wantErr: false},
		{name: "with underscores", tag: "my_plugin", wantErr: false},
		{name: "mixed alphanumeric", tag: "v1.0.0-rc.1", wantErr: false},
		{name: "uppercase", tag: "MyTag", wantErr: false},
		{name: "single char", tag: "a", wantErr: false},
		{name: "max length 128 chars", tag: strings.Repeat("a", 128), wantErr: false},
		{name: "exceeds max length 129 chars", tag: strings.Repeat("a", 129), wantErr: true},

		{name: "ghcr tagged reference", tag: "ghcr.io/stacklok/toolhive-plugins/my-plugin:v1.0.0", wantErr: false},
		{name: "CI format tag", tag: "ghcr.io/stacklok/toolhive-plugins/my-plugin:0.0.1-dev.123_abc1234", wantErr: false},
		{name: "docker hub reference", tag: "docker.io/library/nginx:1.25", wantErr: false},
		{name: "localhost with port", tag: "localhost:5000/my-plugin:v1", wantErr: false},
		{name: "digest reference", tag: "ghcr.io/org/repo@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", wantErr: false},

		{name: "empty string", tag: "", wantErr: true},
		{name: "contains space", tag: "invalid tag", wantErr: true},
		{name: "contains exclamation", tag: "invalid!", wantErr: true},
		{name: "contains hash", tag: "invalid#tag", wantErr: true},

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
		svc := New()
		_, err := svc.ListBuilds(t.Context())
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("empty store returns empty list", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, artifacts)
	})

	t.Run("lists tagged artifacts with metadata", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestPlugin(t, ociStore, "my-plugin", "1.2.3")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "my-plugin"))

		svc := New(WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)

		assert.Equal(t, "my-plugin", artifacts[0].Tag)
		assert.Contains(t, artifacts[0].Digest, "sha256:")
		assert.Equal(t, "my-plugin", artifacts[0].Name)
		assert.Equal(t, "1.2.3", artifacts[0].Version)
	})

	t.Run("lists multiple tagged artifacts", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		d1 := buildTestPlugin(t, ociStore, "plugin-a", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d1, "plugin-a"))
		d2 := buildTestPlugin(t, ociStore, "plugin-b", "2.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d2, "plugin-b"))

		svc := New(WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Len(t, artifacts, 2)

		names := make(map[string]string)
		for _, a := range artifacts {
			names[a.Tag] = a.Version
		}
		assert.Equal(t, "1.0.0", names["plugin-a"])
		assert.Equal(t, "2.0.0", names["plugin-b"])
	})

	t.Run("plugin artifact with no extractable metadata still appears", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		// Store an index with ArtifactType set to the plugin type but no child
		// manifests — extractPluginOCIContent will fail but the artifact should
		// still appear with empty metadata fields.
		pluginIndex := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","artifactType":"dev.toolhive.plugins.v1","manifests":[]}`
		d, putErr := ociStore.PutManifest(t.Context(), []byte(pluginIndex))
		require.NoError(t, putErr)
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "bare-plugin-tag"))

		svc := New(WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)

		assert.Equal(t, "bare-plugin-tag", artifacts[0].Tag)
		assert.Contains(t, artifacts[0].Digest, "sha256:")
		assert.Empty(t, artifacts[0].Name)
		assert.Empty(t, artifacts[0].Version)
	})

	t.Run("non-plugin artifact is excluded", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		pluginDigest := buildTestPlugin(t, ociStore, "real-plugin", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, pluginDigest, "real-plugin"))

		// Store an index whose ArtifactType is not the plugin type. Tagging it
		// as a local build simulates a caller that mistakenly flagged a
		// non-plugin artifact — ListBuilds must still exclude it by type.
		otherIndex := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","artifactType":"application/vnd.docker.distribution.manifest.v2","manifests":[]}`
		otherDigest, putErr := ociStore.PutManifest(t.Context(), []byte(otherIndex))
		require.NoError(t, putErr)
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, otherDigest, "non-plugin-tag"))

		svc := New(WithOCIStore(ociStore))
		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)
		assert.Equal(t, "real-plugin", artifacts[0].Tag)
	})

	t.Run("pulled tags are hidden from ListBuilds", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(WithOCIStore(ociStore))

		// Simulate a pull: tag via the plain ociStore.Tag path, which mirrors
		// what Registry.Pull does (resolve by digest → plain descriptor → no
		// local-build annotation).
		d := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), d, "ghcr.io/org/my-plugin:v1.0.0"))

		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, artifacts, "pulled tags must not appear in ListBuilds")
	})

	t.Run("only locally-built tags are listed when pull and build coexist", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(WithOCIStore(ociStore))

		pulled := buildTestPlugin(t, ociStore, "pulled-plugin", "9.9.9")
		require.NoError(t, ociStore.Tag(t.Context(), pulled, "ghcr.io/org/pulled-plugin:v9.9.9"))

		built := buildTestPlugin(t, ociStore, "built-plugin", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, built, "built-plugin"))

		artifacts, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Len(t, artifacts, 1)
		assert.Equal(t, "built-plugin", artifacts[0].Tag)
	})
}

func TestDeleteBuild(t *testing.T) {
	t.Parallel()

	t.Run("nil oci store returns 500", func(t *testing.T) {
		t.Parallel()
		svc := New()
		err := svc.DeleteBuild(t.Context(), "my-plugin")
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("removes tag and blobs", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "my-plugin"))

		svc := New(WithOCIStore(ociStore))
		require.NoError(t, svc.DeleteBuild(t.Context(), "my-plugin"))

		builds, listErr := svc.ListBuilds(t.Context())
		require.NoError(t, listErr)
		assert.Empty(t, builds)
	})

	t.Run("tag does not exist returns 404", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(WithOCIStore(ociStore))
		err = svc.DeleteBuild(t.Context(), "nonexistent")
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
	})

	t.Run("blobs retained when another tag shares the same digest", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestPlugin(t, ociStore, "shared-plugin", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "tag-a"))
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "tag-b"))

		svc := New(WithOCIStore(ociStore))
		require.NoError(t, svc.DeleteBuild(t.Context(), "tag-a"))

		builds, listErr := svc.ListBuilds(t.Context())
		require.NoError(t, listErr)
		require.Len(t, builds, 1)
		assert.Equal(t, "tag-b", builds[0].Tag)
	})

	t.Run("delete removes local-build marker from index.json", func(t *testing.T) {
		t.Parallel()
		storeRoot := t.TempDir()
		ociStore, err := ociplugins.NewStore(storeRoot)
		require.NoError(t, err)

		d := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")
		require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d, "my-plugin"))

		require.True(t, pluginIndexContainsTaggedMarker(t, storeRoot, "my-plugin"))

		svc := New(WithOCIStore(ociStore))
		require.NoError(t, svc.DeleteBuild(t.Context(), "my-plugin"))

		assert.False(t, pluginIndexContainsTaggedMarker(t, storeRoot, "my-plugin"),
			"descriptor carrying the marker must be gone after DeleteBuild")
	})
}

func TestBuild_StampsLocalBuildAnnotation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	storeRoot := t.TempDir()
	ociStore, err := ociplugins.NewStore(storeRoot)
	require.NoError(t, err)

	d := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

	p := ocimocks.NewMockPluginPackager(ctrl)
	p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
		Return(&ociplugins.PackageResult{
			IndexDigest: d,
			Config:      &ociplugins.PluginConfig{Name: "my-plugin"},
		}, nil)

	svc := New(
		WithPackager(p),
		WithOCIStore(ociStore),
	)

	_, err = svc.Build(t.Context(), plugins.BuildOptions{Path: "/some/dir", Tag: "my-plugin"})
	require.NoError(t, err)

	builds, err := svc.ListBuilds(t.Context())
	require.NoError(t, err)
	require.Len(t, builds, 1)
	assert.Equal(t, "my-plugin", builds[0].Tag)

	assert.True(t, pluginIndexContainsTaggedMarker(t, storeRoot, "my-plugin"),
		"root index.json must carry the local-build annotation for the tag")
}

// TestBuildPushRoundTrip is an EXIT GATE test: build a real plugin into a
// store, push it via a mock registry, then pull it into a second empty store
// and assert the pulled PluginConfig matches the built one. Mirrors the
// skillsvc content_test.go:269-276 round-trip pattern.
func TestBuildPushRoundTrip(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	// Source store: build a real plugin via the shared fixture helper so the
	// Pull mock can reproduce an identical artifact (the plan's prescribed
	// pattern, mirroring skillsvc content_test.go:269-276).
	srcStore, err := ociplugins.NewStore(t.TempDir())
	require.NoError(t, err)

	srcDigest := buildTestPlugin(t, srcStore, "roundtrip-plugin", "3.1.4")
	require.NoError(t, tagAsLocalBuild(t.Context(), srcStore, srcDigest, "roundtrip-plugin"))

	// Read back the built config so assertions compare against the real
	// packaged metadata, not hardcoded strings. extractPluginOCIContent is a
	// service method, so a minimal service satisfies the receiver.
	_, builtCfg, extractErr := (&service{ociStore: srcStore}).
		extractPluginOCIContent(t.Context(), srcDigest)
	require.NotNil(t, builtCfg)
	require.NoError(t, extractErr)

	// Destination store: empty initially.
	dstStore, err := ociplugins.NewStore(t.TempDir())
	require.NoError(t, err)

	ref := "ghcr.io/test/roundtrip-plugin:v3.1.4"

	// Mock registry: Push resolves the local tag "roundtrip-plugin" to its
	// index digest and pushes using that local reference (the digest is the
	// authoritative handle; the tag is the local lookup key). Pull builds the
	// same plugin into dstStore (simulating a real pull by materializing
	// identical blobs) and tags it, mirroring content_test.go:269-276.
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Push(gomock.Any(), srcStore, srcDigest, "roundtrip-plugin").Return(nil)
	reg.EXPECT().Pull(gomock.Any(), dstStore, ref).
		DoAndReturn(func(ctx context.Context, store *ociplugins.Store, _ string) (godigest.Digest, error) {
			d := buildTestPlugin(t, store, "roundtrip-plugin", "3.1.4")
			require.NoError(t, store.Tag(ctx, d, ref))
			return d, nil
		})

	// Push from source.
	srcSvc := New(
		WithRegistryClient(reg),
		WithOCIStore(srcStore),
	)
	require.NoError(t, srcSvc.Push(t.Context(), plugins.PushOptions{Reference: "roundtrip-plugin"}))

	// Pull into destination via GetContent and verify the config matches.
	dstSvc := New(
		WithRegistryClient(reg),
		WithOCIStore(dstStore),
	)
	content, err := dstSvc.GetContent(t.Context(), plugins.ContentOptions{Reference: ref})
	require.NoError(t, err)
	assert.Equal(t, builtCfg.Name, content.Name)
	assert.Equal(t, builtCfg.Version, content.Version)
	assert.Equal(t, builtCfg.Description, content.Description)
	assert.Equal(t, builtCfg.License, content.License)
	assert.NotEmpty(t, content.Manifest, "plugin.json body must be extracted")
	assert.Contains(t, content.Manifest, "roundtrip-plugin")

	// The pulled tag must NOT pollute ListBuilds on the destination.
	builds, err := dstSvc.ListBuilds(t.Context())
	require.NoError(t, err)
	assert.Empty(t, builds, "pulled artifact must not appear in ListBuilds")
}

// pluginIndexContainsTaggedMarker reads the OCI store's root index.json and
// reports whether the descriptor entry tagged `tag` has the local-build
// annotation. Mirrors skillsvc.indexContainsTaggedMarker.
func pluginIndexContainsTaggedMarker(t *testing.T, storeRoot, tag string) bool {
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

// joinStrings concatenates error strings for substring assertions.
func joinStrings(errs []string) string {
	out := ""
	for _, e := range errs {
		out += e + "\n"
	}
	return out
}

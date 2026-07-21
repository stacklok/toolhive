// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

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
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
)

func TestGetContent(t *testing.T) {
	t.Parallel()

	t.Run("nil oci store returns 500", func(t *testing.T) {
		t.Parallel()
		svc := New()
		_, err := svc.GetContent(t.Context(), plugins.ContentOptions{Reference: "my-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("empty reference returns 400", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)
		svc := New(WithOCIStore(ociStore))
		_, err = svc.GetContent(t.Context(), plugins.ContentOptions{Reference: ""})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("local build tag resolves without registry", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		d := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), d, "my-plugin"))

		svc := New(WithOCIStore(ociStore))
		content, err := svc.GetContent(t.Context(), plugins.ContentOptions{Reference: "my-plugin"})
		require.NoError(t, err)

		assert.Equal(t, "my-plugin", content.Name)
		assert.Equal(t, "1.0.0", content.Version)
		assert.NotEmpty(t, content.Manifest, "plugin.json body must be extracted")
		assert.Contains(t, content.Manifest, "my-plugin")
		assert.NotEmpty(t, content.Files)

		// .claude-plugin/plugin.json must appear in the file list.
		var manifestFound bool
		for _, f := range content.Files {
			if strings.EqualFold(filepath.ToSlash(f.Path), ociplugins.ManifestFileName) {
				manifestFound = true
				assert.Greater(t, f.Size, 0)
			}
		}
		assert.True(t, manifestFound, "plugin.json should be listed in Files")
	})

	t.Run("remote OCI reference triggers pull", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)
		indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v2").
			Return(indexDigest, nil)

		svc := New(
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
		)
		content, err := svc.GetContent(t.Context(), plugins.ContentOptions{
			Reference: "ghcr.io/org/my-plugin:v2",
		})
		require.NoError(t, err)

		assert.Equal(t, "my-plugin", content.Name)
		assert.Equal(t, "2.0.0", content.Version)
		assert.NotEmpty(t, content.Manifest)
	})

	t.Run("unqualified name not in store without registry returns 400", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		svc := New(WithOCIStore(ociStore))
		_, err = svc.GetContent(t.Context(), plugins.ContentOptions{Reference: "nonexistent"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("nil registry with unresolvable remote ref returns 400", func(t *testing.T) {
		t.Parallel()
		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		// "ghcr.io/org/plugin:v1" is a valid OCI ref but registry is nil.
		svc := New(WithOCIStore(ociStore))
		_, err = svc.GetContent(t.Context(), plugins.ContentOptions{Reference: "ghcr.io/org/plugin:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("pull failure propagates as 502", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
			Return(godigest.Digest(""), fmt.Errorf("registry unreachable"))

		svc := New(
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
		)
		_, err = svc.GetContent(t.Context(), plugins.ContentOptions{Reference: "ghcr.io/org/my-plugin:v1"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadGateway, httperr.Code(err))
		assert.Contains(t, err.Error(), "registry unreachable")
	})

	t.Run("pull does not pollute ListBuilds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociplugins.NewStore(t.TempDir())
		require.NoError(t, err)

		// The real Pull would tag the pulled artifact in the local store.
		// Simulate that side-effect so we can verify ListBuilds still reports
		// empty: pulls tag by digest, which yields a plain descriptor, so the
		// local-build marker is never applied.
		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().
			Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
			DoAndReturn(func(ctx context.Context, store *ociplugins.Store, _ string) (godigest.Digest, error) {
				d := buildTestPlugin(t, store, "my-plugin", "1.0.0")
				require.NoError(t, store.Tag(ctx, d, "ghcr.io/org/my-plugin:v1"))
				return d, nil
			})

		svc := New(
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
		)

		// Baseline: no builds before the content request.
		builds, err := svc.ListBuilds(t.Context())
		require.NoError(t, err)
		require.Empty(t, builds)

		content, err := svc.GetContent(t.Context(), plugins.ContentOptions{
			Reference: "ghcr.io/org/my-plugin:v1",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", content.Name)

		// After a content-preview pull, ListBuilds must still be empty.
		builds, err = svc.ListBuilds(t.Context())
		require.NoError(t, err)
		assert.Empty(t, builds, "content API must not leak pulled artifacts into ListBuilds")
	})
}

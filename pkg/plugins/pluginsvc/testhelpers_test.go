// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
)

// buildTestPlugin creates a real OCI plugin artifact in the store via the real
// Packager and returns the index digest. Mirrors skillsvc.buildTestArtifact,
// substituting a .claude-plugin/plugin.json manifest and ociplugins packager.
func buildTestPlugin(t *testing.T, store *ociplugins.Store, pluginName, version string) godigest.Digest {
	t.Helper()

	// Create a temporary plugin directory named after the plugin (the packager
	// requires the manifest name to match the directory basename for our
	// validator, but the packager itself does not enforce this).
	pluginDir := filepath.Join(t.TempDir(), pluginName)
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o750))
	manifest := fmt.Sprintf(`{
  "name": %q,
  "description": "test plugin",
  "version": %q,
  "license": "Apache-2.0",
  "keywords": ["test"]
}`, pluginName, version)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ociplugins.ManifestFileName), []byte(manifest), 0o600))

	packager := ociplugins.NewPackager(store)
	result, err := packager.Package(t.Context(), pluginDir, ociplugins.DefaultPackageOptions())
	require.NoError(t, err)
	return result.IndexDigest
}

// putTestManifest stores a minimal manifest in the OCI store and returns its
// digest. Mirrors skillsvc.putTestManifest.
func putTestManifest(t *testing.T, store *ociplugins.Store) godigest.Digest {
	t.Helper()
	d, err := store.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
	require.NoError(t, err)
	return d
}

// buildPluginManifestWithLayerSize stores a plugin artifact whose manifest
// declares an oversized layer (without storing matching blob content), so the
// layer-size guard in extractPluginOCIContent rejects it before GetBlob.
// Mirrors skillsvc.buildManifestWithLayerSize.
func buildPluginManifestWithLayerSize(t *testing.T, store *ociplugins.Store, pluginName string, layerSize int64) godigest.Digest {
	t.Helper()
	imgConfig := ocispec.Image{
		Config: ocispec.ImageConfig{
			Labels: map[string]string{
				ociplugins.LabelPluginName:        pluginName,
				ociplugins.LabelPluginDescription: "test",
				ociplugins.LabelPluginVersion:     "1.0.0",
			},
		},
	}
	configBytes, err := json.Marshal(imgConfig)
	require.NoError(t, err)
	configDigest, err := store.PutBlob(t.Context(), configBytes)
	require.NoError(t, err)

	layerBytes := []byte("layer")
	layerDigest, err := store.PutBlob(t.Context(), layerBytes)
	require.NoError(t, err)

	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ociplugins.ArtifactTypePlugin,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    configDigest,
			Size:      int64(len(configBytes)),
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType: ocispec.MediaTypeImageLayerGzip,
				Digest:    layerDigest,
				Size:      layerSize,
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestDigest, err := store.PutManifest(t.Context(), manifestBytes)
	require.NoError(t, err)
	return manifestDigest
}

// makeProjectRoot returns an eval-symlink-resolved temp directory with a .git
// subdir so NormalizeScopeAndProjectRoot accepts it as a project root. Mirror
// of skillsvc.makeProjectRoot.
func makeProjectRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(resolved, ".git"), 0o755))
	return resolved
}

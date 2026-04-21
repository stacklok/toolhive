// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

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

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
)

const testCommitHash = "abcdef1234567890abcdef1234567890abcdef12"

func makeLayerData(t *testing.T) []byte {
	t.Helper()
	files := []ociskills.FileEntry{
		{Path: "SKILL.md", Content: []byte("---\nname: my-skill\ndescription: test\n---\n# Skill"), Mode: 0644},
	}
	data, err := ociskills.CompressTar(files, ociskills.DefaultTarOptions(), ociskills.DefaultGzipOptions())
	require.NoError(t, err)
	return data
}

func tempDir(t *testing.T) string {
	t.Helper()
	realTmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	return realTmpDir
}

func makeProjectRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(resolved, ".git"), 0o755))
	return resolved
}

// buildTestArtifact creates a real OCI skill artifact in the store and returns
// the index digest. This uses the real Packager so the store has a proper index,
// manifest, config blob, and layer blob — identical to what a real pull would
// produce.
func buildTestArtifact(t *testing.T, store *ociskills.Store, skillName, version string) godigest.Digest {
	t.Helper()

	// Create a temporary skill directory.
	skillDir := filepath.Join(t.TempDir(), skillName)
	require.NoError(t, os.MkdirAll(skillDir, 0o750))
	fm := fmt.Sprintf("---\nname: %s\ndescription: test skill\nversion: %s\n---\n# %s\nA test skill.\n",
		skillName, version, skillName)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(fm), 0o600))

	packager := ociskills.NewPackager(store)
	result, err := packager.Package(t.Context(), skillDir, ociskills.DefaultPackageOptions())
	require.NoError(t, err)
	return result.IndexDigest
}

func buildManifestWithLayerSize(t *testing.T, store *ociskills.Store, skillName string, layerSize int64) godigest.Digest {
	t.Helper()

	imgConfig := ocispec.Image{
		Config: ocispec.ImageConfig{
			Labels: map[string]string{
				ociskills.LabelSkillName:        skillName,
				ociskills.LabelSkillDescription: "test",
				ociskills.LabelSkillVersion:     "1.0.0",
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
		ArtifactType: ociskills.ArtifactTypeSkill,
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

func putTestManifest(t *testing.T, store *ociskills.Store) godigest.Digest {
	t.Helper()
	d, err := store.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
	require.NoError(t, err)
	return d
}

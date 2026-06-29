// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	godigest "github.com/opencontainers/go-digest"
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

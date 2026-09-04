// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// loadCRDManifest reads a generated CRD manifest and decodes it into a
// generic JSON-like tree so the assertions stay independent of the typed
// API types the manifest is generated from.
func loadCRDManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))
	return doc
}

// collectSchemasForProperty walks a generic schema tree and collects every
// nested schema object registered under the given property name.
func collectSchemasForProperty(node any, propertyName string, out *[]map[string]any) {
	switch v := node.(type) {
	case map[string]any:
		if prop, ok := v[propertyName]; ok {
			if schema, ok := prop.(map[string]any); ok {
				*out = append(*out, schema)
			}
		}
		for _, child := range v {
			collectSchemasForProperty(child, propertyName, out)
		}
	case []any:
		for _, item := range v {
			collectSchemasForProperty(item, propertyName, out)
		}
	}
}

// TestCRDUpstreamCredentialScopeSchema asserts the generated CRD schemas
// expose upstreamCredentialScope with the session/platformUser enum and the
// permanent "session" default. This is a generated-artifact assertion: it
// fails until controller-gen regenerates the manifests.
func TestCRDUpstreamCredentialScopeSchema(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		path       string
		container  string
		minMatches int
	}{
		{
			name:       "mcpexternalauthconfigs",
			path:       "../../../../deploy/charts/operator-crds/files/crds/toolhive.stacklok.dev_mcpexternalauthconfigs.yaml",
			container:  "embeddedAuthServer",
			minMatches: 1,
		},
		{
			name:       "virtualmcpservers",
			path:       "../../../../deploy/charts/operator-crds/files/crds/toolhive.stacklok.dev_virtualmcpservers.yaml",
			container:  "authServerConfig",
			minMatches: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := loadCRDManifest(t, tc.path)

			var schemas []map[string]any
			collectSchemasForProperty(doc, tc.container, &schemas)
			require.GreaterOrEqual(t, len(schemas), tc.minMatches,
				"expected at least one %q schema in %s", tc.container, tc.path)

			for i, schema := range schemas {
				props, ok := schema["properties"].(map[string]any)
				require.Truef(t, ok, "%q schema %d has no properties", tc.container, i)

				field, ok := props["upstreamCredentialScope"].(map[string]any)
				require.Truef(t, ok, "%q schema %d has no upstreamCredentialScope property", tc.container, i)

				assert.Equal(t, []any{"session", "platformUser"}, field["enum"],
					"%q schema %d enum mismatch", tc.container, i)
				assert.Equal(t, "session", field["default"],
					"%q schema %d default mismatch", tc.container, i)
			}
		})
	}
}

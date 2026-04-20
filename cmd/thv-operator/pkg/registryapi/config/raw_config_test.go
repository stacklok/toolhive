// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

func TestRawConfigToConfigMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		registryName string
		namespace    string
		configYAML   string
		wantErr      string
		assertions   func(t *testing.T, cm *configMapResult)
	}{
		{
			name:         "valid input creates ConfigMap with correct fields",
			registryName: "my-registry",
			namespace:    "test-ns",
			configYAML:   "sources:\n  - name: default\n",
			assertions: func(t *testing.T, cm *configMapResult) {
				t.Helper()
				assert.Equal(t, "my-registry-registry-server-config", cm.name)
				assert.Equal(t, "test-ns", cm.namespace)

				// Data key is the standard config file name
				content, ok := cm.data[RegistryServerConfigFileName]
				require.True(t, ok, "ConfigMap must contain key %s", RegistryServerConfigFileName)
				assert.Equal(t, "sources:\n  - name: default\n", content)

				// Content checksum annotation is set
				checksumVal, ok := cm.annotations[checksum.ContentChecksumAnnotation]
				require.True(t, ok, "ConfigMap must have content checksum annotation")
				assert.NotEmpty(t, checksumVal)

				// Checksum matches what CalculateConfigHash produces
				expected := ctrlutil.CalculateConfigHash([]byte("sources:\n  - name: default\n"))
				assert.Equal(t, expected, checksumVal)
			},
		},
		{
			name:         "empty registryName returns error",
			registryName: "",
			namespace:    "test-ns",
			configYAML:   "sources:\n  - name: default\n",
			wantErr:      "registry name is required",
		},
		{
			name:         "empty configYAML returns error",
			registryName: "my-registry",
			namespace:    "test-ns",
			configYAML:   "",
			wantErr:      "config YAML is required",
		},
		{
			name:         "content checksum changes when configYAML changes",
			registryName: "my-registry",
			namespace:    "test-ns",
			configYAML:   "sources:\n  - name: other\n",
			assertions: func(t *testing.T, cm *configMapResult) {
				t.Helper()
				checksumVal := cm.annotations[checksum.ContentChecksumAnnotation]

				// Build a second ConfigMap with different content and compare checksums
				differentYAML := "sources:\n  - name: changed\n"
				cm2, err := RawConfigToConfigMap("my-registry", "test-ns", differentYAML)
				require.NoError(t, err)
				checksumVal2 := cm2.Annotations[checksum.ContentChecksumAnnotation]

				assert.NotEqual(t, checksumVal, checksumVal2,
					"checksum must change when configYAML content changes")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cm, err := RawConfigToConfigMap(tt.registryName, tt.namespace, tt.configYAML)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, cm)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cm)

			if tt.assertions != nil {
				tt.assertions(t, &configMapResult{
					name:        cm.Name,
					namespace:   cm.Namespace,
					data:        cm.Data,
					annotations: cm.Annotations,
				})
			}
		})
	}
}

// configMapResult is a test helper to avoid repeating cm.ObjectMeta... in assertions.
type configMapResult struct {
	name        string
	namespace   string
	data        map[string]string
	annotations map[string]string
}

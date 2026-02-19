// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"testing"
	"time"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestUpstreamRegistry_JSONSerialization(t *testing.T) {
	t.Parallel()
	registry := &UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
		Version: "1.0.0",
		Meta: UpstreamMeta{
			LastUpdated: time.Now().Format(time.RFC3339),
		},
		Data: UpstreamData{
			Servers: []upstreamv0.ServerJSON{},
			Groups:  []UpstreamGroup{},
		},
	}

	// Test JSON marshaling
	jsonData, err := json.MarshalIndent(registry, "", "  ")
	require.NoError(t, err)
	assert.Contains(t, string(jsonData), `"$schema"`)
	assert.Contains(t, string(jsonData), `"meta"`)
	assert.Contains(t, string(jsonData), `"data"`)

	// Test JSON unmarshaling
	var decoded UpstreamRegistry
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, registry.Version, decoded.Version)
	assert.Equal(t, registry.Schema, decoded.Schema)
	assert.Equal(t, registry.Meta.LastUpdated, decoded.Meta.LastUpdated)
}

func TestUpstreamRegistry_YAMLSerialization(t *testing.T) {
	t.Parallel()
	registry := &UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
		Version: "1.0.0",
		Meta: UpstreamMeta{
			LastUpdated: "2024-01-15T10:30:00Z",
		},
		Data: UpstreamData{
			Servers: []upstreamv0.ServerJSON{},
			Groups:  []UpstreamGroup{},
		},
	}

	// Test YAML marshaling
	yamlData, err := yaml.Marshal(registry)
	require.NoError(t, err)
	assert.Contains(t, string(yamlData), "meta:")
	assert.Contains(t, string(yamlData), "data:")

	// Test YAML unmarshaling
	var decoded UpstreamRegistry
	err = yaml.Unmarshal(yamlData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, registry.Version, decoded.Version)
	assert.Equal(t, registry.Meta.LastUpdated, decoded.Meta.LastUpdated)
}

func TestUpstreamRegistry_WithGroups(t *testing.T) {
	t.Parallel()
	registry := &UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
		Version: "1.0.0",
		Meta: UpstreamMeta{
			LastUpdated: time.Now().Format(time.RFC3339),
		},
		Data: UpstreamData{
			Servers: []upstreamv0.ServerJSON{},
			Groups: []UpstreamGroup{
				{
					Name:        "test-group",
					Description: "Test group for testing",
					Servers:     []upstreamv0.ServerJSON{},
				},
			},
		},
	}

	jsonData, err := json.Marshal(registry)
	require.NoError(t, err)

	var decoded UpstreamRegistry
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	assert.Len(t, decoded.Data.Groups, 1)
	assert.Equal(t, "test-group", decoded.Data.Groups[0].Name)
}

func TestUpstreamRegistry_SchemaField(t *testing.T) {
	t.Parallel()

	registry := &UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
		Version: "1.0.0",
		Meta: UpstreamMeta{
			LastUpdated: time.Now().Format(time.RFC3339),
		},
		Data: UpstreamData{
			Servers: []upstreamv0.ServerJSON{},
			Groups:  []UpstreamGroup{},
		},
	}

	// Verify schema field is correctly serialized with "$schema" key
	jsonData, err := json.Marshal(registry)
	require.NoError(t, err)
	assert.Contains(t, string(jsonData), `"$schema":"https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json"`)

	// Verify schema field can be deserialized
	var decoded UpstreamRegistry
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, registry.Schema, decoded.Schema)
}

func TestRegistryMeta_TimeFormat(t *testing.T) {
	t.Parallel()

	// Test RFC3339 timestamp format
	timestamp := "2024-01-15T10:30:00Z"
	meta := UpstreamMeta{
		LastUpdated: timestamp,
	}

	jsonData, err := json.Marshal(meta)
	require.NoError(t, err)

	var decoded UpstreamMeta
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, timestamp, decoded.LastUpdated)

	// Verify the timestamp is valid RFC3339
	parsedTime, err := time.Parse(time.RFC3339, decoded.LastUpdated)
	require.NoError(t, err)
	assert.NotZero(t, parsedTime)
}

func TestRegistryData_EmptyOptionalFields(t *testing.T) {
	t.Parallel()

	// Test that groups and skills can be omitted (omitempty)
	data := UpstreamData{
		Servers: []upstreamv0.ServerJSON{},
	}

	jsonData, err := json.Marshal(data)
	require.NoError(t, err)

	// Groups and skills should not appear in JSON when nil (omitempty behavior)
	assert.NotContains(t, string(jsonData), "groups")
	assert.NotContains(t, string(jsonData), "skills")

	// Test with empty slices - also omitted due to omitempty
	data.Groups = []UpstreamGroup{}
	data.Skills = []Skill{}
	jsonData, err = json.Marshal(data)
	require.NoError(t, err)

	// Empty arrays are also omitted with omitempty
	assert.NotContains(t, string(jsonData), "groups")
	assert.NotContains(t, string(jsonData), "skills")
}

func TestUpstreamRegistry_WithSkills(t *testing.T) {
	t.Parallel()
	reg := &UpstreamRegistry{
		Schema:  "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json",
		Version: "1.0.0",
		Meta: UpstreamMeta{
			LastUpdated: time.Now().Format(time.RFC3339),
		},
		Data: UpstreamData{
			Servers: []upstreamv0.ServerJSON{},
			Skills: []Skill{
				{
					Namespace:   "io.github.stacklok",
					Name:        "pdf-processor",
					Description: "Extract text and tables from PDF files",
					Version:     "1.0.0",
					Status:      "active",
					Packages: []SkillPackage{
						{
							RegistryType: "oci",
							Identifier:   "ghcr.io/stacklok/skills/pdf-processor:1.0.0",
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(reg)
	require.NoError(t, err)

	var decoded UpstreamRegistry
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	require.Len(t, decoded.Data.Skills, 1)
	assert.Equal(t, "io.github.stacklok", decoded.Data.Skills[0].Namespace)
	assert.Equal(t, "pdf-processor", decoded.Data.Skills[0].Name)
	assert.Equal(t, "1.0.0", decoded.Data.Skills[0].Version)
	require.Len(t, decoded.Data.Skills[0].Packages, 1)
	assert.Equal(t, "oci", decoded.Data.Skills[0].Packages[0].RegistryType)
}

func TestRegistryGroup_Structure(t *testing.T) {
	t.Parallel()

	group := UpstreamGroup{
		Name:        "test-group",
		Description: "A test group for testing purposes",
		Servers: []upstreamv0.ServerJSON{
			{
				Name:        "io.test/server1",
				Description: "Test server 1",
				Version:     "1.0.0",
			},
		},
	}

	jsonData, err := json.Marshal(group)
	require.NoError(t, err)

	var decoded UpstreamGroup
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, group.Name, decoded.Name)
	assert.Equal(t, group.Description, decoded.Description)
	assert.Len(t, decoded.Servers, 1)
	assert.Equal(t, "io.test/server1", decoded.Servers[0].Name)
}

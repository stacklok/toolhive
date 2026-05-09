// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestRuntimeConfig_MarshalsIdenticallyToConfig pins the invariant that
// today RuntimeConfig adds no extra serialized keys. If a future commit
// adds an operator-resolved field on RuntimeConfig the YAML will diverge,
// at which point this test should be updated to re-marshal and re-load via
// the operator's actual write/read path. The point here is to catch
// accidental divergence before the operator-only fields land.
func TestRuntimeConfig_MarshalsIdenticallyToConfig(t *testing.T) {
	t.Parallel()

	// Use a representative-ish Config value. We don't need every field
	// populated — the YAML library walks reachable fields, and any
	// divergence between Config and RuntimeConfig (extra top-level key)
	// would surface even on a near-empty value.
	cfg := Config{
		Name:  "demo",
		Group: "demo-group",
		Backends: []StaticBackendConfig{
			{Name: "b1", URL: "https://example.test", Transport: "streamable-http"},
		},
	}

	configYAML, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	runtimeYAML, err := yaml.Marshal(RuntimeConfig{Config: cfg})
	require.NoError(t, err)

	require.Equal(
		t, string(configYAML), string(runtimeYAML),
		"RuntimeConfig must marshal identically to Config until operator-only fields are added. "+
			"If you intentionally added a sidecar field, update this test to assert the new shape.",
	)
}

// TestRuntimeConfig_Load_RoundTrip checks that the loader parses YAML the
// operator writes back into the same shape, with strict-unknown-field
// validation intact. Field access through the embed is transparent — rc.Name
// reaches Config.Name without an explicit unwrap.
func TestRuntimeConfig_Load_RoundTrip(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Name:  "demo",
		Group: "demo-group",
		IncomingAuth: &IncomingAuthConfig{
			Type: "anonymous",
		},
	}
	yamlBytes, err := yaml.Marshal(RuntimeConfig{Config: cfg})
	require.NoError(t, err)

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tmp, yamlBytes, 0o600))

	loader := NewYAMLLoader(tmp, &fakeEnv{})
	rc, err := loader.Load()
	require.NoError(t, err)
	require.NotNil(t, rc)
	require.Equal(t, "demo", rc.Name)
	require.Equal(t, "demo-group", rc.Group)
	require.NotNil(t, rc.IncomingAuth)
	require.Equal(t, "anonymous", rc.IncomingAuth.Type)
}

// TestRuntimeConfig_DisjointTopLevelTags pins the invariant that no
// top-level field on RuntimeConfig may share a JSON or YAML key with any
// Config field. encoding/json (anonymous-field promotion, outer wins) and
// yaml.v3 (`,inline`, may error or differ in precedence) handle collisions
// differently — a collision would silently produce divergent serialization
// and ambiguous round-trip behaviour. Today RuntimeConfig has no extra
// fields so this test is trivially green; the value is forward-looking.
func TestRuntimeConfig_DisjointTopLevelTags(t *testing.T) {
	t.Parallel()

	configKeys := topLevelTagKeys(reflect.TypeOf(Config{}))
	runtimeOuterKeys := outerOnlyTagKeys(reflect.TypeOf(RuntimeConfig{}))

	for _, key := range runtimeOuterKeys {
		_, jsonClash := configKeys.json[key.json]
		_, yamlClash := configKeys.yaml[key.yaml]
		require.Falsef(
			t, jsonClash,
			"RuntimeConfig field %q has JSON key %q that collides with a Config field — "+
				"yaml.v3 inline and encoding/json anonymous-promotion handle collisions differently",
			key.fieldName, key.json,
		)
		require.Falsef(
			t, yamlClash,
			"RuntimeConfig field %q has YAML key %q that collides with a Config field",
			key.fieldName, key.yaml,
		)
	}
}

type tagKeySets struct {
	json map[string]struct{}
	yaml map[string]struct{}
}

type fieldTagKey struct {
	fieldName string
	json      string
	yaml      string
}

// topLevelTagKeys returns the set of JSON and YAML names exposed at the top
// level of t. For an embedded anonymous Config field, that's Config's own
// top-level fields (since both encoders flatten it).
func topLevelTagKeys(t reflect.Type) tagKeySets {
	out := tagKeySets{json: map[string]struct{}{}, yaml: map[string]struct{}{}}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			// Embedded fields with no explicit name flatten — recurse.
			inner := topLevelTagKeys(f.Type)
			for k := range inner.json {
				out.json[k] = struct{}{}
			}
			for k := range inner.yaml {
				out.yaml[k] = struct{}{}
			}
			continue
		}
		if jn := tagName(f, "json", f.Name); jn != "" && jn != "-" {
			out.json[jn] = struct{}{}
		}
		if yn := tagName(f, "yaml", f.Name); yn != "" && yn != "-" {
			out.yaml[yn] = struct{}{}
		}
	}
	return out
}

// outerOnlyTagKeys returns ONLY the non-embedded fields of t, with their
// JSON and YAML names. Used to identify "extra" RuntimeConfig fields that
// might collide with the embedded Config's fields.
func outerOnlyTagKeys(t reflect.Type) []fieldTagKey {
	var out []fieldTagKey
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			continue
		}
		jn := tagName(f, "json", f.Name)
		yn := tagName(f, "yaml", f.Name)
		if (jn == "" || jn == "-") && (yn == "" || yn == "-") {
			continue
		}
		out = append(out, fieldTagKey{fieldName: f.Name, json: jn, yaml: yn})
	}
	return out
}

func tagName(f reflect.StructField, key, fallback string) string {
	tag, ok := f.Tag.Lookup(key)
	if !ok {
		return fallback
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return fallback
	}
	return name
}

// fakeEnv satisfies the env.Reader interface for the loader's secret
// resolution path. Round-trip tests don't exercise secret env vars, so
// returning empty for everything is fine.
type fakeEnv struct{}

func (*fakeEnv) Getenv(string) string         { return "" }
func (*fakeEnv) LookupEnv(string) (string, bool) { return "", false }

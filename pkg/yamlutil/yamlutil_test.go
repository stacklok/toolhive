// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package yamlutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestParse_DuplicateKeys(t *testing.T) {
	t.Parallel()

	const size = 64 << 10

	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name:    "duplicate top-level key",
			data:    "name: a\ndescription: d\nname: b\n",
			wantErr: `line 3: mapping key "name" already defined at line 1`,
		},
		{
			name:    "duplicate key in nested mapping",
			data:    "name: a\nmetadata:\n  k: v\n  k: w\n",
			wantErr: `line 4: mapping key "k" already defined at line 3`,
		},
		{
			name:    "duplicate key in flow mapping",
			data:    "extra: {x: 1, x: 2}\n",
			wantErr: `mapping key "x" already defined`,
		},
		{
			name:    "duplicate key in anchored mapping",
			data:    "base: &b {x: 1, x: 2}\nmetadata: *b\n",
			wantErr: `mapping key "x" already defined`,
		},
		{
			name:    "duplicate merge key",
			data:    "a: &a {x: 1}\nm:\n  <<: *a\n  <<: *a\n",
			wantErr: `mapping key "<<" already defined`,
		},
		{
			// A document of nothing but repeated keys takes tens of seconds
			// through yaml.Unmarshal (quadratic duplicate-key reporting).
			name:    "many repeated block keys",
			data:    strings.Repeat("a: \n", size/4),
			wantErr: `mapping key "a" already defined`,
		},
		{
			name:    "many repeated flow keys",
			data:    "m: {" + strings.Repeat("a: ,", size/4) + "}\n",
			wantErr: `mapping key "a" already defined`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(tt.data))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			var out map[string]any
			err = Unmarshal([]byte(tt.data), &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParse_DistinctKeysOfDifferentKinds(t *testing.T) {
	t.Parallel()

	// Same value, different node kind: yaml.Unmarshal accepts this too.
	doc, err := Parse([]byte("? [a]\n: 1\na: 2\n"))
	require.NoError(t, err)
	require.NotNil(t, doc)
}

func TestParse_Empty(t *testing.T) {
	t.Parallel()

	doc, err := Parse(nil)
	require.NoError(t, err)
	assert.Nil(t, doc)

	out := map[string]any{"keep": true}
	require.NoError(t, Unmarshal([]byte(""), &out))
	assert.Equal(t, map[string]any{"keep": true}, out)
}

func TestParse_SyntaxError(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte("a: [unclosed\n"))
	require.Error(t, err)
}

func TestUnmarshal(t *testing.T) {
	t.Parallel()

	var out struct {
		Name     string            `yaml:"name"`
		Metadata map[string]string `yaml:"metadata"`
	}
	data := "base: &b {author: x, team: y}\nname: s\nmetadata:\n  <<: *b\n  team: z\n"
	require.NoError(t, Unmarshal([]byte(data), &out))
	assert.Equal(t, "s", out.Name)
	assert.Equal(t, map[string]string{"author": "x", "team": "z"}, out.Metadata)
}

func TestMappingValue(t *testing.T) {
	t.Parallel()

	doc, err := Parse([]byte("base: &b {version: 1.0.0}\ninfo: *b\nlist: [a]\n"))
	require.NoError(t, err)

	info := MappingValue(doc, "info")
	require.NotNil(t, info)
	assert.Equal(t, yaml.MappingNode, info.Kind)

	version := MappingValue(info, "version")
	require.NotNil(t, version)
	assert.Equal(t, "1.0.0", version.Value)

	assert.Nil(t, MappingValue(doc, "missing"))
	assert.Nil(t, MappingValue(MappingValue(doc, "list"), "a"))
	assert.Nil(t, MappingValue(nil, "info"))
}

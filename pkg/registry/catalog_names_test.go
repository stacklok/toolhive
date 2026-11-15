// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
)

func TestIsEmbeddedCatalogServer(t *testing.T) {
	t.Parallel()

	reg, _, _, err := parseRegistryData(catalog.Upstream())
	require.NoError(t, err)

	var containerName, remoteName string
	for name := range reg.Servers {
		containerName = name
		break
	}
	for name := range reg.RemoteServers {
		remoteName = name
		break
	}
	require.NotEmpty(t, containerName, "embedded catalog has no container servers")

	tests := []struct {
		name   string
		server string
		want   bool
	}{
		{name: "container server in the catalog", server: containerName, want: true},
		{name: "empty name", server: "", want: false},
		{name: "unknown name", server: "definitely-not-a-catalog-server-7f3a", want: false},
	}
	if remoteName != "" {
		tests = append(tests, struct {
			name   string
			server string
			want   bool
		}{name: "remote server in the catalog", server: remoteName, want: true})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsEmbeddedCatalogServer(tt.server))
		})
	}
}

// TestEmbeddedCatalogNamesMatchFullParse pins the names-only decode to the set
// of names the full registry parser produces, so the two cannot drift.
func TestEmbeddedCatalogNamesMatchFullParse(t *testing.T) {
	t.Parallel()

	reg, _, _, err := parseRegistryData(catalog.Upstream())
	require.NoError(t, err)

	want := make(map[string]struct{}, len(reg.Servers)+len(reg.RemoteServers))
	for name := range reg.Servers {
		want[name] = struct{}{}
	}
	for name := range reg.RemoteServers {
		want[name] = struct{}{}
	}
	require.NotEmpty(t, want)
	assert.Equal(t, want, embeddedCatalogNames())
}

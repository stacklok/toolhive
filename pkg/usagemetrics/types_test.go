// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProfileNormalize(t *testing.T) {
	t.Parallel()

	zero, three := 0, 3

	tests := []struct {
		name string
		in   Profile
		want Profile
	}{
		{
			name: "empty profile stays empty",
			in:   Profile{},
			want: Profile{},
		},
		{
			name: "allowed values pass through",
			in: Profile{
				Transport:      "streamable-http",
				ServerKind:     ServerKindRemote,
				RegistrySource: RegistrySourceCustomAPI,
				Runtime:        "podman",
				AuthMode:       AuthModeEmbedded,
				Authz:          ValueOn,
			},
			want: Profile{
				Transport:      "streamable-http",
				ServerKind:     ServerKindRemote,
				RegistrySource: RegistrySourceCustomAPI,
				CatalogServer:  ValueCustom,
				Runtime:        "podman",
				AuthMode:       AuthModeEmbedded,
				Authz:          ValueOn,
			},
		},
		{
			name: "unknown values become other",
			in: Profile{
				Transport:      "inspector",
				ServerKind:     "vm",
				RegistrySource: "https://example.com/registry.json",
				Runtime:        "containerd",
				AuthMode:       "saml",
				Authz:          "maybe",
			},
			want: Profile{
				Transport:      ValueOther,
				ServerKind:     ValueOther,
				RegistrySource: ValueOther,
				CatalogServer:  ValueCustom,
				Runtime:        ValueOther,
				AuthMode:       ValueOther,
				Authz:          ValueOther,
			},
		},
		{
			name: "catalog server kept for builtin source",
			in:   Profile{RegistrySource: ValueBuiltin, CatalogServer: "fetch"},
			want: Profile{RegistrySource: ValueBuiltin, CatalogServer: "fetch"},
		},
		{
			name: "builtin source without a name reports custom",
			in:   Profile{RegistrySource: ValueBuiltin},
			want: Profile{RegistrySource: ValueBuiltin, CatalogServer: ValueCustom},
		},
		{
			name: "catalog server replaced by custom for non-builtin source",
			in:   Profile{RegistrySource: RegistrySourceCustomLocal, CatalogServer: "my-private-server"},
			want: Profile{RegistrySource: RegistrySourceCustomLocal, CatalogServer: ValueCustom},
		},
		{
			name: "catalog server dropped without a registry source",
			in:   Profile{CatalogServer: "my-private-server"},
			want: Profile{},
		},
		{
			name: "no catalog server for a server from no registry",
			in:   Profile{RegistrySource: ValueNone, CatalogServer: "my-private-server"},
			want: Profile{RegistrySource: ValueNone},
		},
		{
			name: "features filtered, de-duplicated and sorted",
			in: Profile{Features: []string{
				FeatureOutgoingAuth, "secret-feature", FeatureCodeMode, FeatureOutgoingAuth, FeatureOptimizer,
			}},
			want: Profile{Features: []string{FeatureCodeMode, FeatureOptimizer, FeatureOutgoingAuth}},
		},
		{
			name: "only unknown features yield no features",
			in:   Profile{Features: []string{"secret-feature"}},
			want: Profile{},
		},
		{
			name: "counts preserved including zero",
			in:   Profile{BackendCount: &zero, ToolCount: &three},
			want: Profile{BackendCount: &zero, ToolCount: &three},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.in.normalize())
		})
	}
}

func TestProfileNormalize_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	features := []string{FeatureOutgoingAuth, FeatureCodeMode}
	in := Profile{Features: features}
	_ = in.normalize()

	assert.Equal(t, []string{FeatureOutgoingAuth, FeatureCodeMode}, features)
}

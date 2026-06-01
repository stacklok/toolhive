// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
)

// TestEnvVarInfoMirrorsRegistryEnvVar is a drift guard: EnvVarInfo carries a
// curated, display-safe subset of regtypes.EnvVar. If a new field is added to
// regtypes.EnvVar, this test fails so a maintainer consciously decides whether
// the new field should be surfaced (and whether it is safe to display).
func TestEnvVarInfoMirrorsRegistryEnvVar(t *testing.T) {
	t.Parallel()

	regFields := structFieldNames(reflect.TypeOf(regtypes.EnvVar{}))
	infoFields := structFieldNames(reflect.TypeOf(EnvVarInfo{}))

	// Every field of regtypes.EnvVar that we expect to mirror must be present in
	// EnvVarInfo. If regtypes.EnvVar grows, update this expectation deliberately.
	expectedRegFields := []string{"Name", "Description", "Required", "Default", "Secret"}
	assert.ElementsMatch(t, expectedRegFields, regFields,
		"regtypes.EnvVar fields changed; review toEnvVarInfo and the EnvVarInfo drift surface")

	for _, f := range expectedRegFields {
		assert.Contains(t, infoFields, f, "EnvVarInfo must mirror regtypes.EnvVar field %q", f)
	}
}

func TestToEnvVarInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   *regtypes.EnvVar
		want EnvVarInfo
	}{
		{
			name: "non-secret keeps default",
			in: &regtypes.EnvVar{
				Name:        "LOG_LEVEL",
				Description: "logging verbosity",
				Required:    false,
				Default:     "info",
				Secret:      false,
			},
			want: EnvVarInfo{
				Name:        "LOG_LEVEL",
				Description: "logging verbosity",
				Required:    false,
				Default:     "info",
				Secret:      false,
			},
		},
		{
			name: "secret clears default",
			in: &regtypes.EnvVar{
				Name:        "API_KEY",
				Description: "service api key",
				Required:    true,
				Default:     "super-secret-default",
				Secret:      true,
			},
			want: EnvVarInfo{
				Name:        "API_KEY",
				Description: "service api key",
				Required:    true,
				Default:     "", // cleared because Secret == true
				Secret:      true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := toEnvVarInfo(tt.in)
			assert.Equal(t, tt.want, got)
			if tt.in.Secret {
				assert.Empty(t, got.Default, "secret env var default must be cleared")
			}
		})
	}
}

func TestApplyOptionsDefaultVerifySetting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts ApplyOptions
		want string
	}{
		{name: "empty defaults to warn", opts: ApplyOptions{}, want: retriever.VerifyImageWarn},
		{name: "explicit setting preserved", opts: ApplyOptions{VerifySetting: retriever.VerifyImageEnabled}, want: retriever.VerifyImageEnabled},
		{name: "disabled preserved", opts: ApplyOptions{VerifySetting: retriever.VerifyImageDisabled}, want: retriever.VerifyImageDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.opts.defaultVerifySetting())
		})
	}
}

// structFieldNames returns the exported field names of a struct type.
func structFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.IsExported() {
			names = append(names, f.Name)
		}
	}
	return names
}

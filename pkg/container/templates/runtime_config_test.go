// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDefaultRuntimeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType TransportType
		wantImage     string
		wantPackages  []string
	}{
		{
			name:          "Go default config",
			transportType: TransportTypeGO,
			wantImage:     "golang:1.26-alpine",
			wantPackages:  []string{"ca-certificates", "git"},
		},
		{
			name:          "NPX default config",
			transportType: TransportTypeNPX,
			wantImage:     "node:24-alpine",
			wantPackages:  []string{"git", "ca-certificates"},
		},
		{
			name:          "UVX default config",
			transportType: TransportTypeUVX,
			wantImage:     "python:3.14-slim",
			wantPackages:  []string{"ca-certificates", "git"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := GetDefaultRuntimeConfig(tt.transportType)

			if got.BuilderImage != tt.wantImage {
				t.Errorf("BuilderImage = %v, want %v", got.BuilderImage, tt.wantImage)
			}

			if len(got.AdditionalPackages) != len(tt.wantPackages) {
				t.Errorf("AdditionalPackages length = %v, want %v", len(got.AdditionalPackages), len(tt.wantPackages))
			}

			for i, pkg := range tt.wantPackages {
				if got.AdditionalPackages[i] != pkg {
					t.Errorf("AdditionalPackages[%d] = %v, want %v", i, got.AdditionalPackages[i], pkg)
				}
			}
		})
	}
}

func TestGetDockerfileTemplateWithCustomRuntimeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType TransportType
		runtimeConfig *RuntimeConfig
		wantInContent string
	}{
		{
			name:          "Custom Go version",
			transportType: TransportTypeGO,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "golang:1.24-alpine",
				AdditionalPackages: []string{"ca-certificates", "git", "gcc"},
			},
			wantInContent: "FROM golang:1.24-alpine AS builder",
		},
		{
			name:          "Custom Node version",
			transportType: TransportTypeNPX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "node:20-alpine",
				AdditionalPackages: []string{"git"},
			},
			wantInContent: "FROM node:20-alpine AS builder",
		},
		{
			name:          "Custom Python version",
			transportType: TransportTypeUVX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "python:3.11-slim",
				AdditionalPackages: []string{"ca-certificates"},
			},
			wantInContent: "FROM python:3.11-slim AS builder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := TemplateData{
				MCPPackage:    "test-package",
				RuntimeConfig: tt.runtimeConfig,
			}

			result, err := GetDockerfileTemplate(tt.transportType, data)
			if err != nil {
				t.Fatalf("GetDockerfileTemplate() error = %v", err)
			}

			if !strings.Contains(result, tt.wantInContent) {
				t.Errorf("Dockerfile does not contain expected content %q", tt.wantInContent)
			}
		})
	}
}

func TestGetDockerfileTemplateUsesDefaultWhenNil(t *testing.T) {
	t.Parallel()

	data := TemplateData{
		MCPPackage:    "test-package",
		RuntimeConfig: nil, // Should use defaults
	}

	result, err := GetDockerfileTemplate(TransportTypeGO, data)
	if err != nil {
		t.Fatalf("GetDockerfileTemplate() error = %v", err)
	}

	// Should use default Go version
	if !strings.Contains(result, "FROM golang:1.26-alpine AS builder") {
		t.Error("Dockerfile does not contain default Go version")
	}
}

func TestRuntimeConfigValidate_ValidPackageNames(t *testing.T) {
	t.Parallel()

	validPackages := []string{
		"git",
		"ca-certificates",
		"libssl1.1",
		"g++",
		"python3.11",
		"build-essential",
		"gcc",
		"make",
		"libc6-dev",
		"curl",
	}

	for _, pkg := range validPackages {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				BuilderImage:       "golang:1.26-alpine",
				AdditionalPackages: []string{pkg},
			}
			assert.NoError(t, rc.Validate())
		})
	}
}

func TestRuntimeConfigValidate_InvalidPackageNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pkg  string
	}{
		{name: "command chaining with &&", pkg: "git && rm -rf /"},
		{name: "command substitution", pkg: "$(curl evil)"},
		{name: "semicolon separator", pkg: "pkg;ls"},
		{name: "pipe operator", pkg: "pkg|cat"},
		{name: "backtick substitution", pkg: "pkg`id`"},
		{name: "newline injection", pkg: "pkg\nRUN evil"},
		{name: "space in name", pkg: "pkg name"},
		{name: "empty string", pkg: ""},
		{name: "starts with hyphen", pkg: "-pkg"},
		{name: "redirect operator", pkg: "pkg>file"},
		{name: "shell variable", pkg: "${HOME}"},
		{name: "wildcard", pkg: "pkg*"},
		{name: "question mark glob", pkg: "pkg?"},
		{name: "parentheses", pkg: "pkg(test)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				BuilderImage:       "golang:1.26-alpine",
				AdditionalPackages: []string{tt.pkg},
			}
			err := rc.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid package name")
		})
	}
}

func TestRuntimeConfigValidate_ValidBuilderImages(t *testing.T) {
	t.Parallel()

	validImages := []string{
		"golang:1.24-alpine",
		"docker.io/library/node:20-alpine",
		"ghcr.io/stacklok/builder:latest",
		"python:3.13-slim",
		"node:24-alpine",
		"mcr.microsoft.com/dotnet/sdk:8.0",
		"registry.example.com/myimage:v1.2.3",
	}

	for _, img := range validImages {
		t.Run(img, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				BuilderImage:       img,
				AdditionalPackages: []string{"git"},
			}
			assert.NoError(t, rc.Validate())
		})
	}
}

func TestRuntimeConfigValidate_InvalidBuilderImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		image string
	}{
		{name: "newline injection", image: "alpine\nRUN curl evil"},
		{name: "space in image", image: "alpine invalid"},
		{name: "blank after trim", image: "   "},
		{name: "shell metachar semicolon", image: "alpine;echo pwned"},
		{name: "shell metachar pipe", image: "alpine|cat /etc/passwd"},
		{name: "shell metachar ampersand", image: "alpine&&curl evil"},
		{name: "backtick injection", image: "alpine`id`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				BuilderImage:       tt.image,
				AdditionalPackages: []string{"git"},
			}
			err := rc.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "builder_image")
		})
	}
}

func TestRuntimeConfigValidate_EmptyBuilderImageIsAllowed(t *testing.T) {
	t.Parallel()

	rc := &RuntimeConfig{
		BuilderImage:       "",
		AdditionalPackages: []string{"git"},
	}
	assert.NoError(t, rc.Validate())
}

func TestRuntimeConfigValidate_EmptyConfig(t *testing.T) {
	t.Parallel()

	rc := &RuntimeConfig{}
	assert.NoError(t, rc.Validate())
}

func TestRuntimeConfigValidate_MultipleErrors(t *testing.T) {
	t.Parallel()

	rc := &RuntimeConfig{
		BuilderImage:       "alpine\nRUN evil",
		AdditionalPackages: []string{"git", "pkg;ls", "curl", "$(evil)"},
	}
	err := rc.Validate()
	require.Error(t, err)
	// Should report both the builder image and the invalid packages
	assert.Contains(t, err.Error(), "builder_image")
	assert.Contains(t, err.Error(), "pkg;ls")
	assert.Contains(t, err.Error(), "$(evil)")
}

func TestRuntimeConfigValidate_PackageNameTooLong(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("a", maxPackageNameLength+1)
	rc := &RuntimeConfig{
		AdditionalPackages: []string{longName},
	}
	err := rc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum length")
}

func TestRuntimeConfigValidate_PackageNameAtMaxLength(t *testing.T) {
	t.Parallel()

	exactName := strings.Repeat("a", maxPackageNameLength)
	rc := &RuntimeConfig{
		AdditionalPackages: []string{exactName},
	}
	assert.NoError(t, rc.Validate())
}

func TestRuntimeConfigValidate_DefaultConfigsAreValid(t *testing.T) {
	t.Parallel()

	for transportType, config := range RuntimeDefaults {
		t.Run(string(transportType), func(t *testing.T) {
			t.Parallel()

			assert.NoError(t, config.Validate())
		})
	}
}

func TestRuntimeConfigValidate_ValidRuntimeEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key   string
		value string
	}{
		{key: "PYTHON_KEYRING_BACKEND", value: "keyrings.alt.file.PlaintextKeyring"},
		{key: "NODE_ENV", value: "production"},
		{key: "FOO_BAR_123", value: "some-value"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				RuntimeEnv: map[string]string{tt.key: tt.value},
			}
			assert.NoError(t, rc.Validate())
		})
	}
}

func TestRuntimeConfigValidate_InvalidRuntimeEnvKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{name: "lowercase key", key: "path_backend"},
		{name: "starts with digit", key: "1FOO"},
		{name: "contains hyphen", key: "FOO-BAR"},
		{name: "empty key", key: ""},
		{name: "contains space", key: "FOO BAR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				RuntimeEnv: map[string]string{tt.key: "some-value"},
			}
			err := rc.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid runtime env key")
		})
	}
}

func TestRuntimeConfigValidate_ReservedRuntimeEnvKeys(t *testing.T) {
	t.Parallel()

	reservedKeys := []string{"PATH", "HOME", "LD_PRELOAD"}

	for _, key := range reservedKeys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				RuntimeEnv: map[string]string{key: "some-value"},
			}
			err := rc.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is reserved and cannot be overridden")
		})
	}
}

func TestRuntimeConfigValidate_InvalidRuntimeEnvValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "backtick command substitution", value: "`id`"},
		{name: "dollar-paren command substitution", value: "$(curl evil)"},
		{name: "dollar-brace expansion", value: "${HOME}"},
		{name: "trailing backslash", value: `value\`},
		{name: "embedded newline", value: "value\nRUN evil"},
		{name: "embedded carriage return", value: "value\rRUN evil"},
		{name: "embedded double quote breaks out of ENV quoting", value: `value" && RUN evil`},
		{name: "semicolon separator", value: "value;rm -rf /"},
		{name: "command chaining with &&", value: "value && evil"},
		{name: "command chaining with ||", value: "value || evil"},
		{name: "pipe operator", value: "value|cat"},
		{name: "redirect operator >", value: "value>file"},
		{name: "redirect operator <", value: "value<file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rc := &RuntimeConfig{
				RuntimeEnv: map[string]string{"FOO": tt.value},
			}
			err := rc.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "contains potentially dangerous characters")
		})
	}
}

func TestRuntimeConfigValidate_MultipleErrorsWithRuntimeEnv(t *testing.T) {
	t.Parallel()

	rc := &RuntimeConfig{
		BuilderImage:       "alpine\nRUN evil",
		AdditionalPackages: []string{"git", "pkg;ls"},
		RuntimeEnv: map[string]string{
			"PATH": "/custom/path",
			"FOO":  "$(evil)",
		},
	}
	err := rc.Validate()
	require.Error(t, err)
	// Should report the builder image, package, and RuntimeEnv errors together.
	assert.Contains(t, err.Error(), "builder_image")
	assert.Contains(t, err.Error(), "pkg;ls")
	assert.Contains(t, err.Error(), "is reserved and cannot be overridden")
	assert.Contains(t, err.Error(), "contains potentially dangerous characters")
}

func TestRuntimeConfigValidate_ValidBuildWith(t *testing.T) {
	t.Parallel()

	valid := []string{
		"mcp<2",
		"mcp>=1.27,<2",
		"mcp==1.29.0",
		"package[extra]==1.2.*",
		"foo~=1.4",
		"Foo_bar!=2.0",
		"pkg >= 1.0, < 3",
	}
	for _, spec := range valid {
		rc := &RuntimeConfig{BuildWith: []string{spec}}
		assert.NoError(t, rc.Validate(), "specifier %q should be valid", spec)
	}
}

func TestRuntimeConfigValidate_InvalidBuildWith(t *testing.T) {
	t.Parallel()

	invalid := []string{
		"",                          // empty
		"mcp<2'; rm -rf /",          // single quote escapes the --with argument
		"mcp<2\" || true",           // double quote
		"mcp<2`id`",                 // backtick command substitution
		"mcp<2$(id)",                // dollar command substitution
		"mcp<2;id",                  // command separator
		"mcp<2|id",                  // pipe
		"mcp<2&id",                  // background
		"mcp<2\\",                   // backslash
		"mcp<2\ninject",             // newline breaks out of the RUN line
		"-e evil",                   // leading dash could become a flag
		strings.Repeat("a", 129),    // over length bound
		"pkg; python_version<'3.8'", // env markers need quotes, deliberately unsupported
	}
	for _, spec := range invalid {
		rc := &RuntimeConfig{BuildWith: []string{spec}}
		assert.Error(t, rc.Validate(), "specifier %q should be rejected", spec)
	}
}

func TestUVXTemplateRendersBuildWith(t *testing.T) {
	t.Parallel()

	rc := GetDefaultRuntimeConfig(TransportTypeUVX)
	rc.BuildWith = []string{"mcp<2", "other>=1,<4"}
	data := TemplateData{
		MCPPackage:    "arxiv-mcp-server",
		RuntimeConfig: &rc,
	}
	dockerfile, err := GetDockerfileTemplate(TransportTypeUVX, data)
	require.NoError(t, err)
	assert.Contains(t, dockerfile, `uv tool install --with 'mcp<2' --with 'other>=1,<4' "$package_spec"`,
		"each BuildWith specifier must be passed as a single-quoted --with argument")
}

func TestUVXTemplateWithoutBuildWithIsUnchanged(t *testing.T) {
	t.Parallel()

	rc := GetDefaultRuntimeConfig(TransportTypeUVX)
	data := TemplateData{
		MCPPackage:    "arxiv-mcp-server",
		RuntimeConfig: &rc,
	}
	dockerfile, err := GetDockerfileTemplate(TransportTypeUVX, data)
	require.NoError(t, err)
	assert.Contains(t, dockerfile, `uv tool install "$package_spec"`,
		"no --with arguments should appear when BuildWith is empty")
	assert.NotContains(t, dockerfile, "--with")
}

func TestRuntimeConfigClone_Nil(t *testing.T) {
	t.Parallel()

	var rc *RuntimeConfig
	assert.Nil(t, rc.Clone())
}

func TestRuntimeConfigClone_Detached(t *testing.T) {
	t.Parallel()

	src := &RuntimeConfig{
		BuilderImage:       "golang:1.26-alpine",
		AdditionalPackages: []string{"git"},
		BuildWith:          []string{"mcp<2"},
		RuntimeEnv:         map[string]string{"FOO": "bar"},
	}
	clone := src.Clone()
	assert.Equal(t, src, clone)

	// Mutating the source afterwards must not affect the clone.
	src.AdditionalPackages[0] = "mutated"
	src.BuildWith[0] = "mutated"
	src.RuntimeEnv["FOO"] = "mutated"
	assert.Equal(t, "git", clone.AdditionalPackages[0])
	assert.Equal(t, "mcp<2", clone.BuildWith[0])
	assert.Equal(t, "bar", clone.RuntimeEnv["FOO"])

	// Mutating the clone must not affect the source.
	clone2 := src.Clone()
	clone2.AdditionalPackages[0] = "mutated-again"
	clone2.RuntimeEnv["FOO"] = "mutated-again"
	assert.Equal(t, "mutated", src.AdditionalPackages[0])
	assert.Equal(t, "mutated", src.RuntimeEnv["FOO"])
}

func TestRuntimeConfigClone_DoesNotAliasRuntimeDefaults(t *testing.T) {
	t.Parallel()

	original := GetDefaultRuntimeConfig(TransportTypeNPX)
	wantPackages := slices.Clone(original.AdditionalPackages)

	clone := original.Clone()
	clone.AdditionalPackages[0] = "mutated"

	fresh := GetDefaultRuntimeConfig(TransportTypeNPX)
	assert.Equal(t, wantPackages, fresh.AdditionalPackages,
		"mutating a Clone() must not reach RuntimeDefaults")
}

func TestRuntimeConfigWithOverrides(t *testing.T) {
	t.Parallel()

	base := &RuntimeConfig{
		BuilderImage:       "python:3.14-slim",
		AdditionalPackages: []string{"ca-certificates", "git"},
		RuntimeEnv:         map[string]string{"BASE_KEY": "base-value", "SHARED_KEY": "base-value"},
	}

	tests := []struct {
		name         string
		override     *RuntimeConfig
		wantImage    string
		wantPackages []string
		wantEnv      map[string]string
		wantBuild    []string
	}{
		{
			name:         "nil override behaves like Clone",
			override:     nil,
			wantImage:    "python:3.14-slim",
			wantPackages: []string{"ca-certificates", "git"},
			wantEnv:      map[string]string{"BASE_KEY": "base-value", "SHARED_KEY": "base-value"},
		},
		{
			name:         "empty override builder image falls back to base",
			override:     &RuntimeConfig{},
			wantImage:    "python:3.14-slim",
			wantPackages: []string{"ca-certificates", "git"},
			wantEnv:      map[string]string{"BASE_KEY": "base-value", "SHARED_KEY": "base-value"},
		},
		{
			name:         "non-empty override builder image wins",
			override:     &RuntimeConfig{BuilderImage: "python:3.11-slim"},
			wantImage:    "python:3.11-slim",
			wantPackages: []string{"ca-certificates", "git"},
			wantEnv:      map[string]string{"BASE_KEY": "base-value", "SHARED_KEY": "base-value"},
		},
		{
			name:         "packages dedupe, base first",
			override:     &RuntimeConfig{AdditionalPackages: []string{"ca-certificates", "curl"}},
			wantImage:    "python:3.14-slim",
			wantPackages: []string{"ca-certificates", "git", "curl"},
			wantEnv:      map[string]string{"BASE_KEY": "base-value", "SHARED_KEY": "base-value"},
		},
		{
			name: "runtime env: override wins on shared key, base-only and override-only keys survive",
			override: &RuntimeConfig{
				RuntimeEnv: map[string]string{"SHARED_KEY": "override-value", "OVERRIDE_KEY": "override-value"},
			},
			wantImage:    "python:3.14-slim",
			wantPackages: []string{"ca-certificates", "git"},
			wantEnv: map[string]string{
				"BASE_KEY": "base-value", "SHARED_KEY": "override-value", "OVERRIDE_KEY": "override-value",
			},
		},
		{
			name:         "build_with taken from override as-is, no defaults",
			override:     &RuntimeConfig{BuildWith: []string{"mcp<2"}},
			wantImage:    "python:3.14-slim",
			wantPackages: []string{"ca-certificates", "git"},
			wantEnv:      map[string]string{"BASE_KEY": "base-value", "SHARED_KEY": "base-value"},
			wantBuild:    []string{"mcp<2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := base.WithOverrides(tt.override)
			assert.Equal(t, tt.wantImage, got.BuilderImage)
			assert.Equal(t, tt.wantPackages, got.AdditionalPackages)
			assert.Equal(t, tt.wantEnv, got.RuntimeEnv)
			assert.Equal(t, tt.wantBuild, got.BuildWith)
		})
	}
}

func TestRuntimeConfigWithOverrides_RuntimeEnvNilWhenBothEmpty(t *testing.T) {
	t.Parallel()

	// Base has no RuntimeEnv and override sets none either: the merged
	// result must stay nil, not an empty map, so `omitempty` still omits
	// runtime_env from serialized output.
	base := &RuntimeConfig{AdditionalPackages: []string{"git"}}
	assert.Nil(t, base.WithOverrides(nil).RuntimeEnv)
	assert.Nil(t, base.WithOverrides(&RuntimeConfig{}).RuntimeEnv)
}

func TestRuntimeConfigWithOverrides_NilEqualsClone(t *testing.T) {
	t.Parallel()

	base := &RuntimeConfig{
		BuilderImage:       "node:24-alpine",
		AdditionalPackages: []string{"git"},
	}
	merged := base.WithOverrides(nil)
	cloned := base.Clone()

	assert.Equal(t, cloned, merged)
	assert.NotSame(t, base, merged)
}

func TestRuntimeConfigWithOverrides_DoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	base := &RuntimeConfig{
		AdditionalPackages: []string{"git"},
		RuntimeEnv:         map[string]string{"FOO": "base"},
	}
	override := &RuntimeConfig{
		AdditionalPackages: []string{"curl"},
		RuntimeEnv:         map[string]string{"FOO": "override"},
	}

	got := base.WithOverrides(override)
	got.AdditionalPackages[0] = "mutated"
	got.RuntimeEnv["FOO"] = "mutated"

	assert.Equal(t, []string{"git"}, base.AdditionalPackages)
	assert.Equal(t, map[string]string{"FOO": "base"}, base.RuntimeEnv)
	assert.Equal(t, []string{"curl"}, override.AdditionalPackages)
	assert.Equal(t, map[string]string{"FOO": "override"}, override.RuntimeEnv)
}

// TestRuntimeConfigWithOverrides_OutputIsDetachedFromInputs covers the opposite
// direction from TestRuntimeConfigWithOverrides_DoesNotMutateInputs: mutating an
// input slice/map *after* WithOverrides must not change the already-returned
// result. BuildWith in particular was passed through as override.BuildWith
// directly (no clone), so it aliased the caller's slice.
func TestRuntimeConfigWithOverrides_OutputIsDetachedFromInputs(t *testing.T) {
	t.Parallel()

	base := &RuntimeConfig{
		AdditionalPackages: []string{"git"},
		RuntimeEnv:         map[string]string{"FOO": "base"},
	}
	override := &RuntimeConfig{
		AdditionalPackages: []string{"curl"},
		RuntimeEnv:         map[string]string{"FOO": "override"},
		BuildWith:          []string{"mcp<2"},
	}

	got := base.WithOverrides(override)

	override.AdditionalPackages[0] = "mutated"
	override.RuntimeEnv["FOO"] = "mutated"
	override.BuildWith[0] = "mutated"

	assert.Equal(t, []string{"git", "curl"}, got.AdditionalPackages)
	assert.Equal(t, "override", got.RuntimeEnv["FOO"])
	assert.Equal(t, []string{"mcp<2"}, got.BuildWith)
}

// TestRuntimeConfigFieldCount guards against a field being added to
// RuntimeConfig without updating Clone and WithOverrides, both of which
// enumerate every field individually.
func TestRuntimeConfigFieldCount(t *testing.T) {
	t.Parallel()

	// Adding a field? Update Clone and WithOverrides, then bump this.
	require.Equal(t, 4, reflect.TypeOf(RuntimeConfig{}).NumField())
}

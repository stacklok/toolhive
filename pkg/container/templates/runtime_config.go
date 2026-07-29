// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"
)

// maxPackageNameLength is the maximum allowed length for a package name.
const maxPackageNameLength = 128

// packageNamePattern matches valid Alpine/Debian package names.
// Must start with an alphanumeric character, followed by alphanumeric characters,
// dots, underscores, plus signs, or hyphens.
var packageNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+\-]*$`)

// buildWithPattern matches a safe subset of PEP 508 requirement specifiers for
// BuildWith entries: a package name (optionally with extras) followed by version
// specifiers, e.g. "mcp<2", "mcp>=1.27,<2", "pkg[extra]==1.2.*", "foo~=1.4".
// The allowlist deliberately excludes quotes, backticks, dollar signs,
// semicolons, parentheses, and backslashes: entries are interpolated into a
// single-quoted shell word inside a Dockerfile RUN instruction, so anything
// that could close the quote or expand in shell context is rejected.
var buildWithPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\[\],<>=!~* -]*$`)

// envKeyPattern matches valid environment variable names for RuntimeEnv.
// Must start with an uppercase letter, followed by uppercase letters, numbers, or underscores.
var envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// reservedRuntimeEnvKeys lists environment variable names that RuntimeEnv must
// not override, either because the generated Dockerfile sets them itself
// (e.g. PATH) or because overriding them could destabilize the runtime image.
var reservedRuntimeEnvKeys = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "SHELL": true, "PWD": true,
	"HOSTNAME": true, "TERM": true, "LANG": true, "LC_ALL": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
}

// runtimeEnvDangerousValuePatterns lists substrings that must not appear in a
// RuntimeEnv value. Values are interpolated verbatim into a Dockerfile ENV
// line (ENV KEY="value") with no shell-escaping, so these characters could
// break out of the quoted value and inject arbitrary Dockerfile/shell content.
var runtimeEnvDangerousValuePatterns = []string{
	"`", "$(", "${", "\\", "\n", "\r", "\"", ";", "&&", "||", "|", ">", "<",
}

// RuntimeConfig defines the base images and versions for a specific runtime
type RuntimeConfig struct {
	// BuilderImage is the full image reference for the builder stage.
	// An empty string signals "use the default for this transport type" during config merging.
	// Examples: "golang:1.26-alpine", "node:24-alpine", "python:3.14-slim"
	BuilderImage string `json:"builder_image" yaml:"builder_image"`

	// AdditionalPackages lists extra packages to install in the builder and
	// runtime stages.
	// Examples for Alpine: ["git", "make", "gcc"]
	// Examples for Debian: ["git", "build-essential"]
	AdditionalPackages []string `json:"additional_packages,omitempty" yaml:"additional_packages,omitempty"`

	// BuildWith lists build-time dependency constraints, interpreted per
	// package ecosystem. For uvx:// builds these are PEP 508 requirement
	// specifiers passed to `uv tool install --with`, used to constrain
	// transitive dependencies the package itself leaves unbounded
	// (e.g. "mcp<2"). Ecosystems without constraint support (npx://, go://)
	// reject a non-empty BuildWith at build time.
	BuildWith []string `json:"build_with,omitempty" yaml:"build_with,omitempty"`

	// RuntimeEnv contains environment variables to inject into the Dockerfile's
	// final runtime stage. Unlike BuildEnv (pkg/container/templates.TemplateData.BuildEnv),
	// which only affects the builder stage, these variables are baked into the
	// shipped image and are present in the running container's process
	// environment at startup. Use this for values a packaged MCP server reads at
	// process start (e.g. feature flags, cache backend selection), not for
	// build-time package manager configuration.
	// Keys must be uppercase with underscores, values are validated for safety.
	RuntimeEnv map[string]string `json:"runtime_env,omitempty" yaml:"runtime_env,omitempty"`
}

// Validate checks that all RuntimeConfig fields contain safe values that cannot
// cause unexpected behavior when interpolated into Dockerfile templates.
// An empty BuilderImage is allowed because it signals "use the default for
// this transport type" during config merging.
// It returns a combined error listing all invalid fields.
func (rc *RuntimeConfig) Validate() error {
	var errs []error

	// Validate BuilderImage using go-containerregistry's ParseReference,
	// which rejects newlines, shell metacharacters, and malformed refs.
	if rc.BuilderImage != "" {
		trimmed := strings.TrimSpace(rc.BuilderImage)
		if trimmed == "" {
			errs = append(errs, fmt.Errorf("builder_image is blank after trimming whitespace"))
		} else if _, err := nameref.ParseReference(trimmed); err != nil {
			errs = append(errs, fmt.Errorf("invalid builder_image %q: %w", rc.BuilderImage, err))
		}
	}

	// Validate each AdditionalPackages entry against a strict allowlist regex
	// and a maximum length bound.
	for _, pkg := range rc.AdditionalPackages {
		if len(pkg) > maxPackageNameLength {
			errs = append(errs, fmt.Errorf(
				"package name %q exceeds maximum length of %d characters",
				pkg, maxPackageNameLength,
			))
		} else if !packageNamePattern.MatchString(pkg) {
			errs = append(errs, fmt.Errorf(
				"invalid package name %q: must match %s",
				pkg, packageNamePattern.String(),
			))
		}
	}

	// Validate each BuildWith entry against a strict allowlist so specifiers
	// cannot escape the single-quoted --with argument in the uvx Dockerfile.
	for _, spec := range rc.BuildWith {
		if len(spec) > maxPackageNameLength {
			errs = append(errs, fmt.Errorf(
				"build_with specifier %q exceeds maximum length of %d characters",
				spec, maxPackageNameLength,
			))
		} else if !buildWithPattern.MatchString(spec) {
			errs = append(errs, fmt.Errorf(
				"invalid build_with specifier %q: must match %s",
				spec, buildWithPattern.String(),
			))
		}
	}

	// Validate each RuntimeEnv entry to ensure keys and values are safe to
	// interpolate into a Dockerfile ENV instruction.
	for key, value := range rc.RuntimeEnv {
		if !envKeyPattern.MatchString(key) {
			errs = append(errs, fmt.Errorf(
				"invalid runtime env key %q: must match %s", key, envKeyPattern.String(),
			))
			continue
		}
		if reservedRuntimeEnvKeys[key] {
			errs = append(errs, fmt.Errorf("runtime env key %q is reserved and cannot be overridden", key))
			continue
		}
		for _, pattern := range runtimeEnvDangerousValuePatterns {
			if strings.Contains(value, pattern) {
				errs = append(errs, fmt.Errorf(
					"runtime env value for key %q contains potentially dangerous characters: %q", key, pattern,
				))
				break
			}
		}
	}

	return errors.Join(errs...)
}

// Clone returns a deep copy of rc, safe for the caller to mutate without
// affecting the original — including RuntimeDefaults entries, whose slices
// are package-global and would otherwise be aliased by a shallow copy.
// A nil receiver returns nil.
func (rc *RuntimeConfig) Clone() *RuntimeConfig {
	if rc == nil {
		return nil
	}
	clone := *rc
	clone.AdditionalPackages = slices.Clone(rc.AdditionalPackages)
	clone.BuildWith = slices.Clone(rc.BuildWith)
	clone.RuntimeEnv = maps.Clone(rc.RuntimeEnv)
	return &clone
}

// WithOverrides returns a new RuntimeConfig with rc as the base and override
// layered on top:
//   - BuilderImage: override wins if non-empty, else falls back to rc's.
//   - AdditionalPackages: the union, rc's entries first, then any override
//     entries not already present.
//   - RuntimeEnv: merged, with override's value winning on a shared key.
//   - BuildWith: taken from override as-is; no defaults exist for it.
//
// WithOverrides(nil) returns rc.Clone() — a distinct object, never rc itself.
// A nil receiver returns override.Clone().
func (rc *RuntimeConfig) WithOverrides(override *RuntimeConfig) *RuntimeConfig {
	if rc == nil {
		return override.Clone()
	}
	if override == nil {
		return rc.Clone()
	}

	// Start from a copy of the base so any future field defaults to
	// base-wins rather than a zero value.
	merged := *rc
	if override.BuilderImage != "" {
		merged.BuilderImage = override.BuilderImage
	}

	seen := make(map[string]bool, len(rc.AdditionalPackages))
	merged.AdditionalPackages = append([]string(nil), rc.AdditionalPackages...)
	for _, pkg := range rc.AdditionalPackages {
		seen[pkg] = true
	}
	for _, pkg := range override.AdditionalPackages {
		if !seen[pkg] {
			merged.AdditionalPackages = append(merged.AdditionalPackages, pkg)
			seen[pkg] = true
		}
	}

	merged.RuntimeEnv = mergeEnvMaps(rc.RuntimeEnv, override.RuntimeEnv)
	merged.BuildWith = slices.Clone(override.BuildWith)

	return &merged
}

// ValidateFor rejects a non-empty BuildWith for transports whose builder
// doesn't support build-time dependency constraints — only the uvx builder
// currently supports them — and otherwise validates rc via Validate.
// A nil receiver returns nil.
func (rc *RuntimeConfig) ValidateFor(transportType TransportType) error {
	if rc == nil {
		return nil
	}
	if transportType != TransportTypeUVX && len(rc.BuildWith) > 0 {
		return fmt.Errorf(
			"build_with is not supported for %s:// builds (only uvx://)", transportType,
		)
	}
	return rc.Validate()
}

// RuntimeDefaults provides default configurations for each runtime type
var RuntimeDefaults = map[TransportType]RuntimeConfig{
	TransportTypeGO: {
		BuilderImage:       "golang:1.26-alpine",
		AdditionalPackages: []string{"ca-certificates", "git"},
	},
	TransportTypeNPX: {
		BuilderImage:       "node:24-alpine",
		AdditionalPackages: []string{"git", "ca-certificates"},
	},
	TransportTypeUVX: {
		BuilderImage:       "python:3.14-slim",
		AdditionalPackages: []string{"ca-certificates", "git"},
	},
}

// GetDefaultRuntimeConfig returns the default runtime configuration for a given
// transport type. The result is a deep copy detached from RuntimeDefaults, so
// callers may freely mutate it without affecting the package-global defaults.
func GetDefaultRuntimeConfig(transportType TransportType) RuntimeConfig {
	config, ok := RuntimeDefaults[transportType]
	if !ok {
		// Return empty config if transport type not found
		return RuntimeConfig{}
	}
	return *config.Clone()
}

// mergeEnvMaps merges two environment variable maps without mutating either
// input. Entries in override take precedence over entries in base.
func mergeEnvMaps(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(override))
	maps.Copy(merged, base)
	maps.Copy(merged, override)
	return merged
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"
)

// maxPackageNameLength is the maximum allowed length for a package name.
const maxPackageNameLength = 128

// packageNamePattern matches valid Alpine/Debian package names.
// Must start with an alphanumeric character, followed by alphanumeric characters,
// dots, underscores, plus signs, or hyphens.
var packageNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+\-]*$`)

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

// GetDefaultRuntimeConfig returns the default runtime configuration for a given transport type
func GetDefaultRuntimeConfig(transportType TransportType) RuntimeConfig {
	config, ok := RuntimeDefaults[transportType]
	if !ok {
		// Return empty config if transport type not found
		return RuntimeConfig{}
	}
	return config
}

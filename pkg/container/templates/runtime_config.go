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

// RuntimeConfig defines the base images and versions for a specific runtime
type RuntimeConfig struct {
	// BuilderImage is the full image reference for the builder stage.
	// An empty string signals "use the default for this transport type" during config merging.
	// Examples: "golang:1.25-alpine", "node:22-alpine", "python:3.13-slim"
	BuilderImage string `json:"builder_image" yaml:"builder_image"`

	// AdditionalPackages lists extra packages to install in the builder and
	// runtime stages.
	// Examples for Alpine: ["git", "make", "gcc"]
	// Examples for Debian: ["git", "build-essential"]
	AdditionalPackages []string `json:"additional_packages,omitempty" yaml:"additional_packages,omitempty"`
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

	return errors.Join(errs...)
}

// RuntimeDefaults provides default configurations for each runtime type
var RuntimeDefaults = map[TransportType]RuntimeConfig{
	TransportTypeGO: {
		BuilderImage:       "golang:1.25-alpine",
		AdditionalPackages: []string{"ca-certificates", "git"},
	},
	TransportTypeNPX: {
		BuilderImage:       "node:22-alpine",
		AdditionalPackages: []string{"git", "ca-certificates"},
	},
	TransportTypeUVX: {
		BuilderImage:       "python:3.13-slim",
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

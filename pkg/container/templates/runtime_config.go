// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

// RuntimeConfig defines the base images and versions for a specific runtime
type RuntimeConfig struct {
	// BuilderImage is the full image reference for the builder stage
	// Examples: "golang:1.25-alpine", "node:22-alpine", "python:3.13-slim"
	BuilderImage string `json:"builder_image" yaml:"builder_image"`

	// AdditionalPackages lists extra packages to install in builder stage
	// Examples for Alpine: ["git", "make", "gcc"]
	// Examples for Debian: ["git", "build-essential"]
	AdditionalPackages []string `json:"additional_packages,omitempty" yaml:"additional_packages,omitempty"`
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

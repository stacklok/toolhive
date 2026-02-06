// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package templates provides utilities for generating Dockerfile templates
// based on different transport types (uvx, npx).
package templates

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"text/template"
)

//go:embed *.tmpl
var templateFS embed.FS

// TemplateData represents the data to be passed to the Dockerfile template.
type TemplateData struct {
	// MCPPackage is the name of the MCP package to run.
	MCPPackage string
	// MCPPackageClean is the package name with version suffix removed.
	// For example: "@org/package@1.2.3" becomes "@org/package", "package@1.0.0" becomes "package"
	// This field is automatically populated by GetDockerfileTemplate.
	MCPPackageClean string
	// CACertContent is the content of the custom CA certificate to include in the image.
	CACertContent string
	// IsLocalPath indicates if the MCPPackage is a local path that should be copied into the container.
	IsLocalPath bool
	// BuildArgs are the arguments to bake into the container's ENTRYPOINT at build time.
	// These are typically required subcommands (e.g., "start") that must always be present.
	// Runtime arguments passed via "-- <args>" will be appended after these build args.
	BuildArgs []string
	// BuildEnv contains environment variables to inject into the Dockerfile builder stage.
	// These are used for configuring package managers (e.g., custom registry URLs).
	// Keys must be uppercase with underscores, values are validated for safety.
	BuildEnv map[string]string
	// BuildAuthFiles contains auth file contents keyed by file type (npmrc, netrc, etc).
	// These files are injected into the builder stage only for authentication.
	BuildAuthFiles map[string]string
	// RuntimeConfig specifies the base images and packages
	// If nil, defaults for the transport type are used
	RuntimeConfig *RuntimeConfig
}

// TransportType represents the type of transport to use.
type TransportType string

const (
	// TransportTypeUVX represents the uvx transport.
	TransportTypeUVX TransportType = "uvx"
	// TransportTypeNPX represents the npx transport.
	TransportTypeNPX TransportType = "npx"
	// TransportTypeGO represents the go transport.
	TransportTypeGO TransportType = "go"
)

// stripVersionSuffix removes version suffixes from package names.
// It strips @version from the end of package names while preserving scoped package prefixes.
// Examples:
//   - "@org/package@1.2.3" -> "@org/package"
//   - "package@1.0.0" -> "package"
//   - "@org/package" -> "@org/package" (no version, unchanged)
//   - "package" -> "package" (no version, unchanged)
func stripVersionSuffix(pkg string) string {
	// Match @version at the end, where version doesn't contain @ or /
	// This preserves scoped packages like @org/package
	re := regexp.MustCompile(`@[^@/]*$`)
	return re.ReplaceAllString(pkg, "")
}

// GetDockerfileTemplate returns the Dockerfile template for the specified transport type.
func GetDockerfileTemplate(transportType TransportType, data TemplateData) (string, error) {
	// Populate MCPPackageClean with version-stripped package name
	data.MCPPackageClean = stripVersionSuffix(data.MCPPackage)

	// Populate RuntimeConfig with defaults if not provided
	if data.RuntimeConfig == nil {
		defaultConfig := GetDefaultRuntimeConfig(transportType)
		data.RuntimeConfig = &defaultConfig
	}

	var templateName string

	// Determine the template name based on the transport type
	switch transportType {
	case TransportTypeUVX:
		templateName = "uvx.tmpl"
	case TransportTypeNPX:
		templateName = "npx.tmpl"
	case TransportTypeGO:
		templateName = "go.tmpl"
	default:
		return "", fmt.Errorf("unsupported transport type: %s", transportType)
	}

	// Read the template file
	tmplContent, err := templateFS.ReadFile(templateName)
	if err != nil {
		return "", fmt.Errorf("failed to read template file: %w", err)
	}

	// Create template with helper functions
	funcMap := template.FuncMap{
		"contains": func(s, substr string) bool {
			return bytes.Contains([]byte(s), []byte(substr))
		},
		"isAlpine": func(image string) bool {
			return bytes.Contains([]byte(image), []byte("alpine"))
		},
		"isDebian": func(image string) bool {
			img := []byte(image)
			return bytes.Contains(img, []byte("slim")) ||
				bytes.Contains(img, []byte("debian")) ||
				bytes.Contains(img, []byte("ubuntu"))
		},
	}

	// Parse the template with helper functions
	tmpl, err := template.New(templateName).Funcs(funcMap).Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Execute the template with the provided data
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// ParseTransportType parses a string into a transport type.
func ParseTransportType(s string) (TransportType, error) {
	switch s {
	case "uvx":
		return TransportTypeUVX, nil
	case "npx":
		return TransportTypeNPX, nil
	case "go":
		return TransportTypeGO, nil
	default:
		return "", fmt.Errorf("unsupported transport type: %s", s)
	}
}

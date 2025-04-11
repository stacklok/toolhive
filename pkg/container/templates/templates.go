// Package templates provides utilities for generating Dockerfile templates
// based on different transport types (uvx, npx).
package templates

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed *.tmpl
var templateFS embed.FS

// TemplateData represents the data to be passed to the Dockerfile template.
type TemplateData struct {
	// MCPPackage is the name of the MCP package to run.
	MCPPackage string
	// MCPArgs are the arguments to pass to the MCP package.
	MCPArgs []string
}

// TransportType represents the type of transport to use.
type TransportType string

const (
	// TransportTypeUVX represents the uvx transport.
	TransportTypeUVX TransportType = "uvx"
	// TransportTypeNPX represents the npx transport.
	TransportTypeNPX TransportType = "npx"
)

// GetDockerfileTemplate returns the Dockerfile template for the specified transport type.
func GetDockerfileTemplate(transportType TransportType, data TemplateData) (string, error) {
	var templateName string

	// Determine the template name based on the transport type
	switch transportType {
	case TransportTypeUVX:
		templateName = "uvx.tmpl"
	case TransportTypeNPX:
		templateName = "npx.tmpl"
	default:
		return "", fmt.Errorf("unsupported transport type: %s", transportType)
	}

	// Read the template file
	tmplContent, err := templateFS.ReadFile(templateName)
	if err != nil {
		return "", fmt.Errorf("failed to read template file: %w", err)
	}

	// Parse the template
	tmpl, err := template.New(templateName).Parse(string(tmplContent))
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
	default:
		return "", fmt.Errorf("unsupported transport type: %s", s)
	}
}

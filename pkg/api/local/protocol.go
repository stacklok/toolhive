// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/StacklokLabs/toolhive/pkg/container/templates"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

// Protocol schemes
const (
	UVXScheme = "uvx://"
	NPXScheme = "npx://"
	GOScheme  = "go://"
)

// handleProtocolScheme checks if the serverOrImage string contains a protocol scheme (uvx:// or npx://)
// and builds a Docker image for it if needed.
// Returns the Docker image name to use and any error encountered.
func (s *Server) handleProtocolScheme(ctx context.Context, serverOrImage string) (string, error) {
	// Check if the serverOrImage starts with a protocol scheme
	if !strings.HasPrefix(serverOrImage, UVXScheme) &&
		!strings.HasPrefix(serverOrImage, NPXScheme) &&
		!strings.HasPrefix(serverOrImage, GOScheme) {
		// No protocol scheme, return the original serverOrImage
		return serverOrImage, nil
	}

	var transportType templates.TransportType
	var packageName string

	// Extract the transport type and package name based on the protocol scheme
	if strings.HasPrefix(serverOrImage, UVXScheme) {
		transportType = templates.TransportTypeUVX
		packageName = strings.TrimPrefix(serverOrImage, UVXScheme)
	} else if strings.HasPrefix(serverOrImage, NPXScheme) {
		transportType = templates.TransportTypeNPX
		packageName = strings.TrimPrefix(serverOrImage, NPXScheme)
	} else if strings.HasPrefix(serverOrImage, GOScheme) {
		transportType = templates.TransportTypeGO
		packageName = strings.TrimPrefix(serverOrImage, GOScheme)
	} else {
		return "", fmt.Errorf("unsupported protocol scheme: %s", serverOrImage)
	}

	// Create template data
	templateData := templates.TemplateData{
		MCPPackage: packageName,
		MCPArgs:    []string{}, // No additional arguments for now
	}

	// Get the Dockerfile content
	dockerfileContent, err := templates.GetDockerfileTemplate(transportType, templateData)
	if err != nil {
		return "", fmt.Errorf("failed to get Dockerfile template: %w", err)
	}

	// Create a temporary directory for the Docker build context
	tempDir, err := os.MkdirTemp("", "toolhive-docker-build-")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Write the Dockerfile to the temporary directory
	dockerfilePath := filepath.Join(tempDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent), 0600); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	//dynamically generate tag from timestamp
	tag := time.Now().Format("20060102150405")

	// Generate a unique image name based on the package name
	imageName := fmt.Sprintf("toolhivelocal/%s-%s:%s",
		string(transportType),
		packageNameToImageName(packageName),
		tag)

	// Log the build process
	s.logDebug("Building Docker image for %s package: %s", transportType, packageName)
	s.logDebug("Using Dockerfile:\n%s", dockerfileContent)

	// Build the Docker image
	logger.Log.Infof("Building Docker image for %s package: %s", transportType, packageName)
	if err := s.runtime.BuildImage(ctx, tempDir, imageName); err != nil {
		return "", fmt.Errorf("failed to build Docker image: %w", err)
	}
	logger.Log.Infof("Successfully built Docker image: %s", imageName)

	return imageName, nil
}

// Replace slashes with dashes to create a valid Docker image name. If there
// is a version in the package name, the @ is replaced with a dash.
func packageNameToImageName(packageName string) string {
	return strings.ReplaceAll(strings.ReplaceAll(packageName, "/", "-"), "@", "-")
}

// isProtocolScheme checks if the given string is a protocol scheme
func isProtocolScheme(s string) bool {
	return strings.HasPrefix(s, UVXScheme) ||
		strings.HasPrefix(s, NPXScheme) ||
		strings.HasPrefix(s, GOScheme)
}

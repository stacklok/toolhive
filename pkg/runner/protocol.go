package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/certs"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/logger"
)

// Protocol schemes
const (
	UVXScheme = "uvx://"
	NPXScheme = "npx://"
	GOScheme  = "go://"
)

// HandleProtocolScheme checks if the serverOrImage string contains a protocol scheme (uvx://, npx://, or go://)
// and builds a Docker image for it if needed.
// Returns the Docker image name to use and any error encountered.
func HandleProtocolScheme(
	ctx context.Context,
	runtime rt.Runtime,
	serverOrImage string,
	caCertPath string,
) (string, error) {
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

	// If a CA certificate path is provided, read the certificate and add it to the template data
	if caCertPath != "" {
		logger.Debugf("Using custom CA certificate from: %s", caCertPath)

		// Read the CA certificate file
		// #nosec G304 -- This is a user-provided file path that we need to read
		caCertContent, err := os.ReadFile(caCertPath)
		if err != nil {
			return "", fmt.Errorf("failed to read CA certificate file: %w", err)
		}

		// Validate that the file contains a valid PEM certificate
		if err := certs.ValidateCACertificate(caCertContent); err != nil {
			return "", fmt.Errorf("invalid CA certificate: %w", err)
		}

		// Add the CA certificate content to the template data
		templateData.CACertContent = string(caCertContent)
		logger.Debugf("Successfully validated and loaded CA certificate")
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

	// If a CA certificate is provided, write it to the build context
	if templateData.CACertContent != "" {
		caCertFilePath := filepath.Join(tempDir, "ca-cert.crt")
		if err := os.WriteFile(caCertFilePath, []byte(templateData.CACertContent), 0600); err != nil {
			return "", fmt.Errorf("failed to write CA certificate file: %w", err)
		}
		logger.Debugf("Added CA certificate to build context: %s", caCertFilePath)
	}

	//dynamically generate tag from timestamp
	tag := time.Now().Format("20060102150405")

	// Generate a unique image name based on the package name
	imageName := fmt.Sprintf("toolhivelocal/%s-%s:%s",
		string(transportType),
		packageNameToImageName(packageName),
		tag)

	// Log the build process
	logger.Debugf("Building Docker image for %s package: %s", transportType, packageName)
	logger.Debugf("Using Dockerfile:\n%s", dockerfileContent)

	// Build the Docker image
	logger.Infof("Building Docker image for %s package: %s", transportType, packageName)
	if err := runtime.BuildImage(ctx, tempDir, imageName); err != nil {
		return "", fmt.Errorf("failed to build Docker image: %w", err)
	}
	logger.Infof("Successfully built Docker image: %s", imageName)

	return imageName, nil
}

// Replace slashes with dashes to create a valid Docker image name. If there
// is a version in the package name, the @ is replaced with a dash.
func packageNameToImageName(packageName string) string {
	return strings.ReplaceAll(strings.ReplaceAll(packageName, "/", "-"), "@", "-")
}

// IsImageProtocolScheme checks if the serverOrImage string contains a protocol scheme (uvx://, npx://, or go://)
func IsImageProtocolScheme(serverOrImage string) bool {
	return strings.HasPrefix(serverOrImage, UVXScheme) ||
		strings.HasPrefix(serverOrImage, NPXScheme) ||
		strings.HasPrefix(serverOrImage, GOScheme)
}

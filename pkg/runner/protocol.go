package runner

import (
	"context"
	"fmt"
	"io"
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
	transportType, packageName, err := parseProtocolScheme(serverOrImage)
	if err != nil {
		return "", err
	}

	templateData, err := createTemplateData(transportType, packageName, caCertPath)
	if err != nil {
		return "", err
	}

	return buildImageFromTemplate(ctx, runtime, transportType, packageName, templateData)
}

// parseProtocolScheme extracts the transport type and package name from the protocol scheme.
func parseProtocolScheme(serverOrImage string) (templates.TransportType, string, error) {
	if strings.HasPrefix(serverOrImage, UVXScheme) {
		return templates.TransportTypeUVX, strings.TrimPrefix(serverOrImage, UVXScheme), nil
	}
	if strings.HasPrefix(serverOrImage, NPXScheme) {
		return templates.TransportTypeNPX, strings.TrimPrefix(serverOrImage, NPXScheme), nil
	}
	if strings.HasPrefix(serverOrImage, GOScheme) {
		return templates.TransportTypeGO, strings.TrimPrefix(serverOrImage, GOScheme), nil
	}
	return "", "", fmt.Errorf("unsupported protocol scheme: %s", serverOrImage)
}

// createTemplateData creates the template data with optional CA certificate.
func createTemplateData(transportType templates.TransportType, packageName, caCertPath string) (templates.TemplateData, error) {
	// Check if this is a local path (for Go packages only)
	isLocalPath := transportType == templates.TransportTypeGO && isLocalGoPath(packageName)

	templateData := templates.TemplateData{
		MCPPackage:  packageName,
		MCPArgs:     []string{}, // No additional arguments for now
		IsLocalPath: isLocalPath,
	}

	if caCertPath != "" {
		if err := addCACertToTemplate(caCertPath, &templateData); err != nil {
			return templateData, err
		}
	}

	return templateData, nil
}

// addCACertToTemplate reads and validates a CA certificate, adding it to the template data.
func addCACertToTemplate(caCertPath string, templateData *templates.TemplateData) error {
	logger.Debugf("Using custom CA certificate from: %s", caCertPath)

	// Read the CA certificate file
	// #nosec G304 -- This is a user-provided file path that we need to read
	caCertContent, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate file: %w", err)
	}

	// Validate that the file contains a valid PEM certificate
	if err := certs.ValidateCACertificate(caCertContent); err != nil {
		return fmt.Errorf("invalid CA certificate: %w", err)
	}

	// Add the CA certificate content to the template data
	templateData.CACertContent = string(caCertContent)
	logger.Debugf("Successfully validated and loaded CA certificate")
	return nil
}

// buildImageFromTemplate builds a Docker image from the template data.
func buildImageFromTemplate(
	ctx context.Context,
	runtime rt.Runtime,
	transportType templates.TransportType,
	packageName string,
	templateData templates.TemplateData,
) (string, error) {

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

	// If this is a local Go path, copy the source files to the build context
	if templateData.IsLocalPath {
		if err := copyLocalSource(packageName, tempDir); err != nil {
			return "", fmt.Errorf("failed to copy local source: %w", err)
		}
		logger.Debugf("Copied local source from %s to build context", packageName)
	}

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
// For local paths, we clean up the path to make it a valid image name.
func packageNameToImageName(packageName string) string {
	imageName := packageName

	// Handle local paths by cleaning them up
	imageName = strings.TrimPrefix(imageName, "./")
	imageName = strings.TrimPrefix(imageName, "../")

	// Replace problematic characters
	imageName = strings.ReplaceAll(imageName, "/", "-")
	imageName = strings.ReplaceAll(imageName, "@", "-")
	imageName = strings.ReplaceAll(imageName, ".", "-")

	// Ensure the name doesn't start with a dash
	imageName = strings.TrimPrefix(imageName, "-")

	// If the name is empty after cleaning, use a default
	if imageName == "" || imageName == "-" {
		imageName = "toolhive-container"
	}

	return imageName
}

// isLocalGoPath checks if the given path is a local Go path that should be copied into the container.
// Local paths start with "." (relative) or "/" (absolute).
func isLocalGoPath(path string) bool {
	return strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") || strings.HasPrefix(path, "/") || path == "."
}

// copyLocalSource copies the local source directory to the build context directory.
// It recursively copies all files and directories, preserving the structure.
func copyLocalSource(sourcePath, destDir string) error {
	// Get the absolute path of the source
	absSourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for %s: %w", sourcePath, err)
	}

	// Check if the source path exists
	_, err = os.Stat(absSourcePath)
	if err != nil {
		return fmt.Errorf("source path does not exist: %s: %w", absSourcePath, err)
	}

	// Walk through the source directory and copy all files
	return filepath.Walk(absSourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate the relative path from the source root
		relPath, err := filepath.Rel(absSourcePath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Skip the root directory itself
		if relPath == "." {
			return nil
		}

		// Calculate the destination path
		destPath := filepath.Join(destDir, relPath)

		// Skip common directories that shouldn't be copied
		if shouldSkipPath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			// Create directory
			return os.MkdirAll(destPath, info.Mode())
		}
		// Copy file
		return copyFile(path, destPath, info.Mode())
	})
}

// shouldSkipPath determines if a path should be skipped during copying.
// This includes common directories like .git, node_modules, vendor, etc.
func shouldSkipPath(relPath string) bool {
	skipDirs := []string{
		".git",
		".gitignore",
		"node_modules",
		"vendor",
		".DS_Store",
		"Thumbs.db",
		".vscode",
		".idea",
		"*.tmp",
		"*.log",
		".dockerignore",
		"Dockerfile",
		"docker-compose.yml",
		"docker-compose.yaml",
	}

	pathParts := strings.Split(relPath, string(filepath.Separator))
	for _, part := range pathParts {
		for _, skipDir := range skipDirs {
			if part == skipDir || strings.HasPrefix(part, skipDir) {
				return true
			}
		}
	}
	return false
}

// copyFile copies a single file from src to dst with the given mode.
func copyFile(src, dst string, mode os.FileMode) error {
	// Create the destination directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Open source file
	// #nosec G304 -- This is a controlled file copy operation within the build context
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	// Create destination file
	// #nosec G304 -- This is a controlled file copy operation within the build context
	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	// Copy file contents
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	return nil
}

// IsImageProtocolScheme checks if the serverOrImage string contains a protocol scheme (uvx://, npx://, or go://)
func IsImageProtocolScheme(serverOrImage string) bool {
	return strings.HasPrefix(serverOrImage, UVXScheme) ||
		strings.HasPrefix(serverOrImage, NPXScheme) ||
		strings.HasPrefix(serverOrImage, GOScheme)
}

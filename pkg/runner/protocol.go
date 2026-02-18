// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive/pkg/certs"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/secrets"
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
	imageManager images.ImageManager,
	serverOrImage string,
	caCertPath string,
	runtimeOverride *templates.RuntimeConfig,
) (string, error) {
	return BuildFromProtocolSchemeWithName(ctx, imageManager, serverOrImage, caCertPath, "", nil, runtimeOverride, false)
}

// BuildFromProtocolSchemeWithName checks if the serverOrImage string contains a protocol scheme (uvx://, npx://, or go://)
// and builds a Docker image for it if needed with a custom image name.
// If imageName is empty, a default name will be generated.
// buildArgs are baked into the container's ENTRYPOINT at build time (e.g., required subcommands).
// If dryRun is true, returns the Dockerfile content instead of building the image.
// Returns the Docker image name (or Dockerfile content if dryRun) and any error encountered.
func BuildFromProtocolSchemeWithName(
	ctx context.Context,
	imageManager images.ImageManager,
	serverOrImage string,
	caCertPath string,
	imageName string,
	buildArgs []string,
	runtimeOverride *templates.RuntimeConfig,
	dryRun bool,
) (string, error) {
	transportType, packageName, err := ParseProtocolScheme(serverOrImage)
	if err != nil {
		return "", err
	}

	templateData, err := createTemplateData(transportType, packageName, caCertPath, buildArgs, runtimeOverride)
	if err != nil {
		return "", err
	}

	// If dry-run, just return the Dockerfile content
	if dryRun {
		dockerfileContent, err := templates.GetDockerfileTemplate(transportType, templateData)
		if err != nil {
			return "", fmt.Errorf("failed to get Dockerfile template: %w", err)
		}
		return dockerfileContent, nil
	}

	return buildImageFromTemplateWithName(ctx, imageManager, transportType, packageName, templateData, imageName)
}

// ParseProtocolScheme extracts the transport type and package name from the protocol scheme.
func ParseProtocolScheme(serverOrImage string) (templates.TransportType, string, error) {
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

// validateBuildArgs ensures buildArgs don't contain single quotes which would break
// shell quoting in the UVX template. Single quotes cannot be escaped within single-quoted
// strings in shell, making them the only character that can enable command injection.
// NPX and GO use JSON array ENTRYPOINTs without shell interpretation, so they're safe.
func validateBuildArgs(buildArgs []string) error {
	for _, arg := range buildArgs {
		if strings.Contains(arg, "'") {
			return fmt.Errorf("buildArg cannot contain single quotes: %s", arg)
		}
	}
	return nil
}

// createTemplateData creates the template data with optional CA certificate and build arguments.
func createTemplateData(
	transportType templates.TransportType, packageName, caCertPath string, buildArgs []string,
	runtimeOverride *templates.RuntimeConfig,
) (templates.TemplateData, error) {
	// Validate buildArgs to prevent shell injection in templates that use sh -c
	if err := validateBuildArgs(buildArgs); err != nil {
		return templates.TemplateData{}, err
	}

	// Check if this is a local path (for Go packages only)
	isLocalPath := transportType == templates.TransportTypeGO && isLocalGoPath(packageName)

	templateData := templates.TemplateData{
		MCPPackage:  packageName,
		IsLocalPath: isLocalPath,
		BuildArgs:   buildArgs,
	}

	if caCertPath != "" {
		if err := addCACertToTemplate(caCertPath, &templateData); err != nil {
			return templateData, err
		}
	}

	// Load build environment variables from configuration
	if err := addBuildEnvToTemplate(&templateData); err != nil {
		return templateData, err
	}

	// Load build auth files from configuration and secrets
	if err := addBuildAuthFilesToTemplate(&templateData); err != nil {
		return templateData, err
	}

	// Load runtime configuration (base images and packages)
	runtimeConfig := loadRuntimeConfig(transportType, runtimeOverride)
	templateData.RuntimeConfig = runtimeConfig

	return templateData, nil
}

// loadRuntimeConfig loads the runtime configuration for a given transport type.
// Priority order:
// 1. Override provided as parameter
// 2. User configuration from config file
// 3. Default configuration for the transport type
func loadRuntimeConfig(
	transportType templates.TransportType,
	override *templates.RuntimeConfig,
) *templates.RuntimeConfig {
	// If override is provided, use it
	if override != nil {
		return override
	}

	// Try loading from user config
	provider := config.NewProvider()
	if userConfig, err := provider.GetRuntimeConfig(string(transportType)); err == nil && userConfig != nil {
		return userConfig
	}

	// Fall back to defaults
	defaultConfig := templates.GetDefaultRuntimeConfig(transportType)
	return &defaultConfig
}

// addBuildEnvToTemplate loads build environment variables from config and adds them to template data.
// It resolves values from three sources:
// 1. Literal values stored in BuildEnv
// 2. Values from ToolHive secrets (BuildEnvFromSecrets)
// 3. Values from the current shell environment (BuildEnvFromShell)
func addBuildEnvToTemplate(templateData *templates.TemplateData) error {
	provider := config.NewProvider()
	resolvedEnv := make(map[string]string)

	// 1. Add literal values
	literalEnv := provider.GetAllBuildEnv()
	for k, v := range literalEnv {
		resolvedEnv[k] = v
	}

	// 2. Resolve values from secrets
	secretRefs := provider.GetAllBuildEnvFromSecrets()
	if len(secretRefs) > 0 {
		secretValues, err := resolveSecretsForBuildEnv(secretRefs)
		if err != nil {
			return fmt.Errorf("failed to resolve secrets for build env: %w", err)
		}
		for k, v := range secretValues {
			resolvedEnv[k] = v
		}
	}

	// 3. Resolve values from shell environment
	shellRefs := provider.GetAllBuildEnvFromShell()
	for _, key := range shellRefs {
		value := os.Getenv(key)
		if value == "" {
			slog.Warn("Build env variable configured to read from shell, but not set in environment", "key", key)
			continue
		}
		resolvedEnv[key] = value
	}

	if len(resolvedEnv) > 0 {
		templateData.BuildEnv = resolvedEnv
		slog.Debug("Loaded build environment variable(s) (redacted for security)", "value", len(resolvedEnv))
	}

	return nil
}

// addBuildAuthFilesToTemplate loads build auth files from config and secrets, adding them to template data.
func addBuildAuthFilesToTemplate(templateData *templates.TemplateData) error {
	provider := config.NewProvider()
	configuredFiles := provider.GetConfiguredBuildAuthFiles()

	if len(configuredFiles) == 0 {
		return nil
	}

	// Resolve auth file content from secrets
	authFiles, err := resolveBuildAuthFilesFromSecrets(configuredFiles)
	if err != nil {
		return err
	}

	if len(authFiles) > 0 {
		templateData.BuildAuthFiles = authFiles
		slog.Debug("Loaded build auth file(s)", "value", len(authFiles))
	}

	return nil
}

// resolveBuildAuthFilesFromSecrets resolves auth file content from the secrets provider.
func resolveBuildAuthFilesFromSecrets(configuredFiles []string) (map[string]string, error) {
	ctx := context.Background()
	configProvider := config.NewProvider()
	cfg := configProvider.GetConfig()

	// Check if secrets are set up
	if !cfg.Secrets.SetupCompleted {
		return nil, secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider type: %w", err)
	}

	manager, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return nil, fmt.Errorf("failed to create secrets provider: %w", err)
	}

	resolved := make(map[string]string, len(configuredFiles))
	for _, fileType := range configuredFiles {
		secretName := config.BuildAuthFileSecretName(fileType)
		content, err := manager.GetSecret(ctx, secretName)
		if err != nil {
			return nil, fmt.Errorf("failed to get secret '%s' for auth file %s: %w", secretName, fileType, err)
		}
		resolved[fileType] = content
	}

	return resolved, nil
}

// resolveSecretsForBuildEnv resolves secret references to their actual values.
func resolveSecretsForBuildEnv(secretRefs map[string]string) (map[string]string, error) {
	ctx := context.Background()
	configProvider := config.NewProvider()
	cfg := configProvider.GetConfig()

	// Check if secrets are set up
	if !cfg.Secrets.SetupCompleted {
		return nil, secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider type: %w", err)
	}

	manager, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return nil, fmt.Errorf("failed to create secrets provider: %w", err)
	}

	resolved := make(map[string]string, len(secretRefs))
	for key, secretName := range secretRefs {
		value, err := manager.GetSecret(ctx, secretName)
		if err != nil {
			return nil, fmt.Errorf("failed to get secret '%s' for build env variable %s: %w", secretName, key, err)
		}

		// Validate the secret value doesn't contain dangerous characters
		if err := config.ValidateBuildEnvValue(value); err != nil {
			return nil, fmt.Errorf("secret '%s' contains invalid value for build env variable %s: %w", secretName, key, err)
		}

		resolved[key] = value
	}

	return resolved, nil
}

// addCACertToTemplate reads and validates a CA certificate, adding it to the template data.
func addCACertToTemplate(caCertPath string, templateData *templates.TemplateData) error {
	slog.Debug("Using custom CA certificate from", "path", caCertPath)

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
	slog.Debug("Successfully validated and loaded CA certificate")
	return nil
}

// buildContext represents a Docker build context with cleanup functionality.
type buildContext struct {
	Dir            string
	DockerfilePath string
	CleanupFunc    func()
}

// setupBuildContext sets up the appropriate build context directory based on whether
// we're dealing with a local path or remote package.
func setupBuildContext(packageName string, isLocalPath bool) (*buildContext, error) {
	if isLocalPath {
		return setupLocalBuildContext(packageName)
	}
	return setupTempBuildContext()
}

// setupLocalBuildContext sets up a build context using the local directory directly.
func setupLocalBuildContext(packageName string) (*buildContext, error) {
	absPath, err := filepath.Abs(packageName)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for %s: %w", packageName, err)
	}

	// Check if the source path exists
	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("source path does not exist: %s: %w", absPath, err)
	}

	// For Go projects, use the current working directory as the build context
	// to ensure go.mod and the entire project structure is available
	currentDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current working directory: %w", err)
	}

	dockerfilePath := filepath.Join(currentDir, "Dockerfile")

	slog.Debug("Using current working directory as build context", "dir", currentDir)

	return &buildContext{
		Dir:            currentDir,
		DockerfilePath: dockerfilePath,
		CleanupFunc: func() {
			// Clean up the temporary Dockerfile only if we created it
			if _, err := os.Stat(dockerfilePath); err == nil {
				// Check if this is our generated Dockerfile by reading the first few lines
				// #nosec G304 -- This is a controlled file read operation for cleanup verification
				if content, readErr := os.ReadFile(dockerfilePath); readErr == nil {
					if strings.Contains(string(content), "# Generated by ToolHive") {
						if err := os.Remove(dockerfilePath); err != nil {
							slog.Debug("Failed to remove temporary Dockerfile", "error", err)
						}
					}
				}
			}
		},
	}, nil
}

// setupTempBuildContext sets up a temporary build context directory.
func setupTempBuildContext() (*buildContext, error) {
	tempDir, err := os.MkdirTemp("", "toolhive-docker-build-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}

	dockerfilePath := filepath.Join(tempDir, "Dockerfile")

	slog.Debug("Using temporary directory as build context", "dir", tempDir)

	return &buildContext{
		Dir:            tempDir,
		DockerfilePath: dockerfilePath,
		CleanupFunc: func() {
			if err := os.RemoveAll(tempDir); err != nil {
				slog.Debug("Failed to remove temporary directory", "error", err)
			}
		},
	}, nil
}

// writeDockerfile writes the Dockerfile content to the build context.
// For local paths, it checks if a Dockerfile already exists and avoids overwriting it.
func writeDockerfile(dockerfilePath, dockerfileContent string, isLocalPath bool) error {
	if isLocalPath {
		// Check if a Dockerfile already exists
		if _, err := os.Stat(dockerfilePath); err == nil {
			slog.Debug("Dockerfile already exists at , using existing Dockerfile", "dockerfilepath", dockerfilePath)
			return nil // Use the existing Dockerfile
		}
	}

	// Add a comment marker to identify our generated Dockerfile
	markedContent := "# Generated by ToolHive - temporary file\n" + dockerfileContent

	if err := os.WriteFile(dockerfilePath, []byte(markedContent), 0600); err != nil {
		return fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	if isLocalPath {
		slog.Debug("Created temporary Dockerfile at", "dockerfilepath", dockerfilePath)
	}

	return nil
}

// writeCACertificate writes the CA certificate to the build context if provided.
func writeCACertificate(buildContextDir, caCertContent string, isLocalPath bool) (func(), error) {
	if caCertContent == "" {
		return func() {}, nil
	}

	caCertFilePath := filepath.Join(buildContextDir, "ca-cert.crt")
	if err := os.WriteFile(caCertFilePath, []byte(caCertContent), 0600); err != nil {
		return nil, fmt.Errorf("failed to write CA certificate file: %w", err)
	}

	slog.Debug("Added CA certificate to build context", "path", caCertFilePath)

	var cleanupFunc func()
	if isLocalPath {
		// For local paths, clean up the CA certificate file after build
		cleanupFunc = func() {
			if err := os.Remove(caCertFilePath); err != nil {
				slog.Debug("Failed to remove temporary CA certificate", "error", err)
			}
		}
	} else {
		// For temp directories, no specific cleanup needed (handled by build context cleanup)
		cleanupFunc = func() {}
	}

	return cleanupFunc, nil
}

// writeAuthFiles writes auth files to the build context.
// Returns a cleanup function to remove the files after build.
func writeAuthFiles(buildContextDir string, authFiles map[string]string, isLocalPath bool) (func(), error) {
	if len(authFiles) == 0 {
		return func() {}, nil
	}

	// Map of auth file types to their filenames in the build context
	authFileNames := map[string]string{
		"npmrc":  ".npmrc",
		"netrc":  ".netrc",
		"yarnrc": ".yarnrc",
	}

	var writtenFiles []string
	for fileType, content := range authFiles {
		filename, ok := authFileNames[fileType]
		if !ok {
			continue
		}

		filePath := filepath.Join(buildContextDir, filename)
		if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
			// Clean up any files we've written so far
			for _, f := range writtenFiles {
				_ = os.Remove(f)
			}
			return nil, fmt.Errorf("failed to write auth file %s: %w", filename, err)
		}
		writtenFiles = append(writtenFiles, filePath)
		slog.Debug("Added auth file to build context", "path", filePath)
	}

	var cleanupFunc func()
	if isLocalPath {
		cleanupFunc = func() {
			for _, f := range writtenFiles {
				if err := os.Remove(f); err != nil {
					slog.Debug("Failed to remove temporary auth file", "f", f, "s", err)
				}
			}
		}
	} else {
		cleanupFunc = func() {}
	}

	return cleanupFunc, nil
}

// generateImageName generates a unique Docker image name based on the package and transport type.
func generateImageName(transportType templates.TransportType, packageName string) string {
	tag := time.Now().Format("20060102150405")
	return strings.ToLower(fmt.Sprintf("toolhivelocal/%s-%s:%s",
		string(transportType),
		PackageNameToImageName(packageName),
		tag))
}

// buildImageFromTemplateWithName builds a Docker image from the template data with a custom image name.
// If imageName is empty, a default name will be generated.
func buildImageFromTemplateWithName(
	ctx context.Context,
	imageManager images.ImageManager,
	transportType templates.TransportType,
	packageName string,
	templateData templates.TemplateData,
	imageName string,
) (string, error) {

	// Get the Dockerfile content
	dockerfileContent, err := templates.GetDockerfileTemplate(transportType, templateData)
	if err != nil {
		return "", fmt.Errorf("failed to get Dockerfile template: %w", err)
	}

	// Set up the build context
	buildCtx, err := setupBuildContext(packageName, templateData.IsLocalPath)
	if err != nil {
		return "", err
	}
	defer buildCtx.CleanupFunc()

	// Write the Dockerfile
	if err := writeDockerfile(buildCtx.DockerfilePath, dockerfileContent, templateData.IsLocalPath); err != nil {
		return "", err
	}

	// Write CA certificate if provided
	caCertCleanup, err := writeCACertificate(buildCtx.Dir, templateData.CACertContent, templateData.IsLocalPath)
	if err != nil {
		return "", err
	}
	defer caCertCleanup()

	// Write auth files if provided
	authFilesCleanup, err := writeAuthFiles(buildCtx.Dir, templateData.BuildAuthFiles, templateData.IsLocalPath)
	if err != nil {
		return "", err
	}
	defer authFilesCleanup()

	// Use provided image name or generate one
	finalImageName := imageName
	if finalImageName == "" {
		finalImageName = generateImageName(transportType, packageName)
	} else {
		// Validate the provided image name using go-containerregistry
		ref, err := nameref.ParseReference(finalImageName)
		if err != nil {
			return "", fmt.Errorf("invalid image name format '%s': %w", finalImageName, err)
		}
		// Use the normalized reference string
		finalImageName = ref.String()
		slog.Debug("Using validated image name", "image", finalImageName)
	}

	// Log the build process
	slog.Debug("Building Docker image for package", "transporttype", transportType, "s_package", packageName)
	slog.Debug("Using Dockerfile:\n", "dockerfilecontent", dockerfileContent)

	if err := imageManager.BuildImage(ctx, buildCtx.Dir, finalImageName); err != nil {
		return "", fmt.Errorf("failed to build Docker image: %w", err)
	}
	slog.Debug("Successfully built Docker image", "image", finalImageName)

	return finalImageName, nil
}

// PackageNameToImageName replaces slashes with dashes to create a valid Docker image name. If there
// is a version in the package name, the @ is replaced with a dash.
// For local paths, we clean up the path to make it a valid image name.
func PackageNameToImageName(packageName string) string {
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

// IsImageProtocolScheme checks if the serverOrImage string contains a protocol scheme (uvx://, npx://, or go://)
func IsImageProtocolScheme(serverOrImage string) bool {
	return strings.HasPrefix(serverOrImage, UVXScheme) ||
		strings.HasPrefix(serverOrImage, NPXScheme) ||
		strings.HasPrefix(serverOrImage, GOScheme)
}

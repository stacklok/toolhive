package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/permissions"
)

const (
	// registryTypeOCI is the registry type for OCI/Docker images
	registryTypeOCI = "oci"
)

// ConvertUpstreamToToolhive converts an upstream server detail to toolhive format
func ConvertUpstreamToToolhive(upstream *UpstreamServerDetail) (ServerMetadata, error) {
	if upstream == nil {
		return nil, fmt.Errorf("upstream server detail is nil")
	}

	// Extract toolhive-specific metadata from _meta field
	var toolhiveExt *ToolhiveMetadataExtension
	if upstream.Meta != nil {
		toolhiveExt = upstream.Meta.ToolhiveExtension
	}

	// Determine if this is a remote server or container server
	if len(upstream.Remotes) > 0 {
		return convertToRemoteServer(upstream, toolhiveExt), nil
	}

	if len(upstream.Packages) > 0 {
		return convertToImageMetadata(upstream, toolhiveExt)
	}

	return nil, fmt.Errorf("no packages or remotes found for server")
}

// convertToRemoteServer converts upstream format to RemoteServerMetadata
func convertToRemoteServer(upstream *UpstreamServerDetail, toolhiveExt *ToolhiveMetadataExtension) *RemoteServerMetadata {
	// Use the first remote as the primary one
	primaryRemote := upstream.Remotes[0]

	remote := &RemoteServerMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          upstream.Name,
			Description:   upstream.Description,
			Tier:          getStringOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) string { return t.Tier }, "Community"),
			Status:        "active", // Status field removed from new schema
			Transport:     string(primaryRemote.Type),
			Tools:         getSliceOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) []string { return t.Tools }, []string{}),
			Metadata:      getMetadataOrDefault(toolhiveExt),
			RepositoryURL: getRepositoryURL(upstream.Repository),
			Tags:          getSliceOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) []string { return t.Tags }, []string{}),
			CustomMetadata: getMapOrDefault(toolhiveExt,
				func(t *ToolhiveMetadataExtension) map[string]any { return t.CustomMetadata }, nil),
		},
		URL:     primaryRemote.URL,
		Headers: convertHeaders(primaryRemote.Headers),
		EnvVars: convertEnvironmentVariables(getPackageEnvVars(upstream.Packages)),
	}

	return remote
}

// convertToImageMetadata converts upstream format to ImageMetadata
func convertToImageMetadata(upstream *UpstreamServerDetail, toolhiveExt *ToolhiveMetadataExtension) (*ImageMetadata, error) {
	if len(upstream.Packages) == 0 {
		return nil, fmt.Errorf("no packages found for container server")
	}

	// Find the primary package (prefer OCI/Docker, then others)
	var primaryPackage *UpstreamPackage
	for i := range upstream.Packages {
		pkg := &upstream.Packages[i]
		if pkg.RegistryType == registryTypeOCI || pkg.RegistryType == "docker" {
			primaryPackage = pkg
			break
		}
		if primaryPackage == nil {
			primaryPackage = pkg
		}
	}

	// Determine transport from package
	transport := string(primaryPackage.Transport.Type)

	image := &ImageMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          upstream.Name,
			Description:   upstream.Description,
			Tier:          getStringOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) string { return t.Tier }, "Community"),
			Status:        "active", // Status field removed from new schema
			Transport:     transport,
			Tools:         getSliceOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) []string { return t.Tools }, []string{}),
			Metadata:      getMetadataOrDefault(toolhiveExt),
			RepositoryURL: getRepositoryURL(upstream.Repository),
			Tags:          getSliceOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) []string { return t.Tags }, []string{}),
			CustomMetadata: getMapOrDefault(toolhiveExt,
				func(t *ToolhiveMetadataExtension) map[string]any { return t.CustomMetadata }, nil),
		},
		Image:       formatImageName(primaryPackage),
		TargetPort:  getIntOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) int { return t.TargetPort }, 0),
		Permissions: getPermissionsOrDefault(toolhiveExt),
		EnvVars:     convertEnvironmentVariables(primaryPackage.EnvironmentVariables),
		Args:        convertPackageArguments(primaryPackage.PackageArguments),
		DockerTags:  getSliceOrDefault(toolhiveExt, func(t *ToolhiveMetadataExtension) []string { return t.DockerTags }, []string{}),
		Provenance:  getProvenanceOrDefault(toolhiveExt),
	}

	return image, nil
}

// ConvertToolhiveToUpstream converts toolhive format to upstream format
func ConvertToolhiveToUpstream(server ServerMetadata) (*UpstreamServerDetail, error) {
	if server == nil {
		return nil, fmt.Errorf("server metadata is nil")
	}

	upstream := &UpstreamServerDetail{
		Schema:      "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
		Name:        server.GetName(),
		Description: server.GetDescription(),
		Version:     "1.0.0", // Default version, could be extracted from metadata
	}

	// Set repository if available
	if repoURL := server.GetRepositoryURL(); repoURL != "" {
		upstream.Repository = &UpstreamRepository{
			URL:    repoURL,
			Source: extractSourceFromURL(repoURL),
		}
	}

	// Create toolhive extension for _meta
	toolhiveExt := &ToolhiveMetadataExtension{
		Tier:           server.GetTier(),
		Tools:          server.GetTools(),
		Metadata:       server.GetMetadata(),
		Tags:           server.GetTags(),
		CustomMetadata: server.GetCustomMetadata(),
	}

	if server.IsRemote() {
		if remoteServer, ok := server.(*RemoteServerMetadata); ok {
			upstream.Remotes = []UpstreamRemote{
				{
					Type:    convertTransportToUpstream(remoteServer.Transport),
					URL:     remoteServer.URL,
					Headers: convertHeadersToUpstream(remoteServer.Headers),
				},
			}
			// Add environment variables as a package if present
			if len(remoteServer.EnvVars) > 0 {
				upstream.Packages = convertEnvVarsToPackages(remoteServer.EnvVars)
			}
		}
	} else {
		if imageServer, ok := server.(*ImageMetadata); ok {
			toolhiveExt.TargetPort = imageServer.TargetPort
			toolhiveExt.DockerTags = imageServer.DockerTags
			toolhiveExt.Provenance = imageServer.Provenance

			// Determine registry type and format package
			registryType, identifier, version := parseImageString(imageServer.Image)

			upstream.Packages = []UpstreamPackage{
				{
					RegistryType:         registryType,
					Identifier:           identifier,
					Version:              version,
					PackageArguments:     convertArgsToUpstream(imageServer.Args),
					EnvironmentVariables: convertEnvVarsToUpstream(imageServer.EnvVars),
					Transport: UpstreamTransport{
						Type: convertTransportToUpstream(imageServer.Transport),
					},
				},
			}

			// Add permissions to toolhive extension
			if imageServer.Permissions != nil {
				toolhiveExt.CustomMetadata = make(map[string]any)
				toolhiveExt.CustomMetadata["permissions"] = imageServer.Permissions
			}
		}
	}

	// Add toolhive extension to _meta
	upstream.Meta = &UpstreamMeta{
		ToolhiveExtension: toolhiveExt,
	}

	return upstream, nil
}

// Helper functions for conversion

func convertHeaders(headers []UpstreamKeyValueInput) []*Header {
	result := make([]*Header, 0, len(headers))
	for _, h := range headers {
		result = append(result, &Header{
			Name:        h.Name,
			Description: h.Description,
			Required:    h.IsRequired,
			Default:     h.Default,
			Secret:      h.IsSecret,
			Choices:     h.Choices,
		})
	}
	return result
}

func convertHeadersToUpstream(headers []*Header) []UpstreamKeyValueInput {
	result := make([]UpstreamKeyValueInput, 0, len(headers))
	for _, h := range headers {
		result = append(result, UpstreamKeyValueInput{
			Name:        h.Name,
			Description: h.Description,
			IsRequired:  h.Required,
			Default:     h.Default,
			IsSecret:    h.Secret,
			Choices:     h.Choices,
		})
	}
	return result
}

func convertEnvironmentVariables(envVars []UpstreamKeyValueInput) []*EnvVar {
	result := make([]*EnvVar, 0, len(envVars))
	for _, ev := range envVars {
		result = append(result, &EnvVar{
			Name:        ev.Name,
			Description: ev.Description,
			Required:    ev.IsRequired,
			Default:     ev.Default,
			Secret:      ev.IsSecret,
		})
	}
	return result
}

func convertEnvVarsToUpstream(envVars []*EnvVar) []UpstreamKeyValueInput {
	result := make([]UpstreamKeyValueInput, 0, len(envVars))
	for _, ev := range envVars {
		result = append(result, UpstreamKeyValueInput{
			Name:        ev.Name,
			Description: ev.Description,
			IsRequired:  ev.Required,
			Default:     ev.Default,
			IsSecret:    ev.Secret,
		})
	}
	return result
}

func convertPackageArguments(args []UpstreamArgument) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg.Type {
		case UpstreamArgumentTypePositional:
			if arg.Value != "" {
				result = append(result, arg.Value)
			} else if arg.ValueHint != "" {
				result = append(result, arg.ValueHint)
			} else if arg.Default != "" {
				result = append(result, arg.Default)
			}
		case UpstreamArgumentTypeNamed:
			if arg.Value != "" {
				result = append(result, arg.Name, arg.Value)
			} else if arg.Default != "" {
				result = append(result, arg.Name, arg.Default)
			}
		}
	}
	return result
}

func convertArgsToUpstream(args []string) []UpstreamArgument {
	result := make([]UpstreamArgument, 0, len(args))
	for _, arg := range args {
		result = append(result, UpstreamArgument{
			Type:  UpstreamArgumentTypePositional,
			Value: arg,
		})
	}
	return result
}

func convertTransportToUpstream(transport string) UpstreamTransportType {
	switch transport {
	case "sse":
		return UpstreamTransportTypeSSE
	case "streamable-http":
		return UpstreamTransportTypeStreamable
	case "stdio":
		return UpstreamTransportTypeStdio
	default:
		return UpstreamTransportTypeStdio
	}
}

func formatImageName(pkg *UpstreamPackage) string {
	switch pkg.RegistryType {
	case registryTypeOCI, "docker":
		// For OCI/Docker packages, format as name:version
		if pkg.Version != "" {
			return fmt.Sprintf("%s:%s", pkg.Identifier, pkg.Version)
		}
		return pkg.Identifier
	case "npm":
		// For npm packages, use the npx:// protocol scheme that toolhive supports
		if pkg.Version != "" {
			return fmt.Sprintf("npx://%s@%s", pkg.Identifier, pkg.Version)
		}
		return fmt.Sprintf("npx://%s", pkg.Identifier)
	case "pypi":
		// For Python packages, use the uvx:// protocol scheme that toolhive supports
		if pkg.Version != "" {
			return fmt.Sprintf("uvx://%s@%s", pkg.Identifier, pkg.Version)
		}
		return fmt.Sprintf("uvx://%s", pkg.Identifier)
	case "nuget":
		// For NuGet packages, use the dnx:// protocol scheme
		if pkg.Version != "" {
			return fmt.Sprintf("dnx://%s@%s", pkg.Identifier, pkg.Version)
		}
		return fmt.Sprintf("dnx://%s", pkg.Identifier)
	case "mcpb":
		// For MCPB packages, return the URL as-is
		return pkg.Identifier
	default:
		// For unknown registries, return the identifier as-is
		return pkg.Identifier
	}
}

func parseImageString(image string) (registryType, identifier, version string) {
	// Handle special protocol schemes
	if strings.HasPrefix(image, "npx://") {
		registryType = "npm"
		image = strings.TrimPrefix(image, "npx://")
		// For scoped packages like @scope/name@version, find the last @ after the first character
		if strings.HasPrefix(image, "@") && strings.Count(image, "@") > 1 {
			// Scoped package with version: @scope/name@version
			lastAt := strings.LastIndex(image, "@")
			identifier = image[:lastAt]
			version = image[lastAt+1:]
		} else if !strings.HasPrefix(image, "@") && strings.Contains(image, "@") {
			// Unscoped package with version: package@version
			lastAt := strings.LastIndex(image, "@")
			identifier = image[:lastAt]
			version = image[lastAt+1:]
		} else {
			// Package without version (scoped or unscoped)
			identifier = image
		}
		return
	}

	if strings.HasPrefix(image, "uvx://") {
		registryType = "pypi"
		image = strings.TrimPrefix(image, "uvx://")
		lastAt := strings.LastIndex(image, "@")
		if lastAt > 0 {
			identifier = image[:lastAt]
			version = image[lastAt+1:]
		} else {
			identifier = image
		}
		return
	}

	if strings.HasPrefix(image, "dnx://") {
		registryType = "nuget"
		image = strings.TrimPrefix(image, "dnx://")
		lastAt := strings.LastIndex(image, "@")
		if lastAt > 0 {
			identifier = image[:lastAt]
			version = image[lastAt+1:]
		} else {
			identifier = image
		}
		return
	}

	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		registryType = "mcpb"
		identifier = image
		return
	}

	// Default to OCI/Docker
	registryType = registryTypeOCI
	parts := strings.Split(image, ":")
	if len(parts) > 1 {
		identifier = parts[0]
		version = parts[1]
	} else {
		identifier = image
		version = "latest"
	}
	return
}

func getRepositoryURL(repo *UpstreamRepository) string {
	if repo != nil {
		return repo.URL
	}
	return ""
}

func extractSourceFromURL(url string) string {
	if strings.Contains(url, "github.com") {
		return "github"
	}
	if strings.Contains(url, "gitlab.com") {
		return "gitlab"
	}
	return "unknown"
}

func getPackageEnvVars(packages []UpstreamPackage) []UpstreamKeyValueInput {
	for _, pkg := range packages {
		if len(pkg.EnvironmentVariables) > 0 {
			return pkg.EnvironmentVariables
		}
	}
	return nil
}

func convertEnvVarsToPackages(envVars []*EnvVar) []UpstreamPackage {
	if len(envVars) == 0 {
		return nil
	}

	upstreamEnvVars := convertEnvVarsToUpstream(envVars)
	return []UpstreamPackage{
		{
			RegistryType:         "remote",
			Identifier:           "remote-server",
			Version:              "1.0.0",
			EnvironmentVariables: upstreamEnvVars,
			Transport: UpstreamTransport{
				Type: UpstreamTransportTypeStdio,
			},
		},
	}
}

// Helper functions for extracting values with defaults

func getStringOrDefault[T any](ext *T, getter func(*T) string, defaultValue string) string {
	if ext != nil {
		if value := getter(ext); value != "" {
			return value
		}
	}
	return defaultValue
}

func getSliceOrDefault[T any](ext *T, getter func(*T) []string, defaultValue []string) []string {
	if ext != nil {
		if value := getter(ext); len(value) > 0 {
			return value
		}
	}
	return defaultValue
}

func getMapOrDefault[T any](ext *T, getter func(*T) map[string]any, defaultValue map[string]any) map[string]any {
	if ext != nil {
		if value := getter(ext); len(value) > 0 {
			return value
		}
	}
	return defaultValue
}

func getIntOrDefault[T any](ext *T, getter func(*T) int, defaultValue int) int {
	if ext != nil {
		if value := getter(ext); value != 0 {
			return value
		}
	}
	return defaultValue
}

func getMetadataOrDefault(ext *ToolhiveMetadataExtension) *Metadata {
	if ext != nil && ext.Metadata != nil {
		return ext.Metadata
	}
	return &Metadata{
		Stars:       0,
		Pulls:       0,
		LastUpdated: time.Now().Format(time.RFC3339),
	}
}

func getPermissionsOrDefault(ext *ToolhiveMetadataExtension) *permissions.Profile {
	if ext != nil {
		// Check if permissions are stored in custom metadata
		if ext.CustomMetadata != nil {
			if perms, ok := ext.CustomMetadata["permissions"].(*permissions.Profile); ok {
				return perms
			}
		}
	}
	return nil
}

func getProvenanceOrDefault(ext *ToolhiveMetadataExtension) *Provenance {
	if ext != nil && ext.Provenance != nil {
		return ext.Provenance
	}
	return nil
}

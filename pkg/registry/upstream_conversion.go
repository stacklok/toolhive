package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/permissions"
)

// ToolhiveExtensionKey is the key used for ToolHive-specific metadata in the x-publisher field
const ToolhiveExtensionKey = "x-dev.toolhive"

// ToolhivePublisherExtension contains toolhive-specific metadata in the x-publisher field
type ToolhivePublisherExtension struct {
	// Tier represents the tier classification level of the server
	Tier string `json:"tier,omitempty" yaml:"tier,omitempty"`
	// Transport defines the communication protocol for the server
	Transport string `json:"transport,omitempty" yaml:"transport,omitempty"`
	// Tools is a list of tool names provided by this MCP server
	Tools []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	// Metadata contains additional information about the server
	Metadata *Metadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	// Tags are categorization labels for the server
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// CustomMetadata allows for additional user-defined metadata
	CustomMetadata map[string]any `json:"custom_metadata,omitempty" yaml:"custom_metadata,omitempty"`
	// Permissions defines the security profile and access permissions for the server
	Permissions *permissions.Profile `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	// TargetPort is the port for the container to expose (only applicable to SSE and Streamable HTTP transports)
	TargetPort int `json:"target_port,omitempty" yaml:"target_port,omitempty"`
	// DockerTags lists the available Docker tags for this server image
	DockerTags []string `json:"docker_tags,omitempty" yaml:"docker_tags,omitempty"`
	// Provenance contains verification and signing metadata
	Provenance *Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// ConvertUpstreamToToolhive converts an upstream server detail to toolhive format
func ConvertUpstreamToToolhive(upstream *UpstreamServerDetail) (ServerMetadata, error) {
	if upstream == nil {
		return nil, fmt.Errorf("upstream server detail is nil")
	}

	// Extract toolhive-specific metadata from x-publisher field
	var toolhiveExt *ToolhivePublisherExtension
	if upstream.XPublisher != nil {
		toolhiveExt = upstream.XPublisher.XDevToolhive
	}

	// Determine if this is a remote server or container server
	if len(upstream.Server.Remotes) > 0 {
		return convertToRemoteServer(upstream, toolhiveExt), nil
	}

	return convertToImageMetadata(upstream, toolhiveExt)
}

// convertToRemoteServer converts upstream format to RemoteServerMetadata
func convertToRemoteServer(upstream *UpstreamServerDetail, toolhiveExt *ToolhivePublisherExtension) *RemoteServerMetadata {
	remote := &RemoteServerMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          upstream.Server.Name,
			Description:   upstream.Server.Description,
			Tier:          getStringOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) string { return t.Tier }, "Community"),
			Status:        convertStatus(upstream.Server.Status),
			Transport:     convertTransport(upstream.Server.Remotes[0].TransportType),
			Tools:         getSliceOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) []string { return t.Tools }, []string{}),
			Metadata:      getMetadataOrDefault(toolhiveExt),
			RepositoryURL: getRepositoryURL(upstream.Server.Repository),
			Tags:          getSliceOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) []string { return t.Tags }, []string{}),
			CustomMetadata: getMapOrDefault(toolhiveExt,
				func(t *ToolhivePublisherExtension) map[string]any { return t.CustomMetadata }, nil),
		},
		URL:     upstream.Server.Remotes[0].URL,
		Headers: convertHeaders(upstream.Server.Remotes[0].Headers),
		EnvVars: convertEnvironmentVariables(getPackageEnvVars(upstream.Server.Packages)),
	}

	// Override transport if specified in toolhive extension
	if toolhiveExt != nil && toolhiveExt.Transport != "" {
		remote.Transport = toolhiveExt.Transport
	}

	return remote
}

// convertToImageMetadata converts upstream format to ImageMetadata
func convertToImageMetadata(upstream *UpstreamServerDetail, toolhiveExt *ToolhivePublisherExtension) (*ImageMetadata, error) {
	if len(upstream.Server.Packages) == 0 {
		return nil, fmt.Errorf("no packages found for container server")
	}

	// Find the primary package (prefer Docker, then others)
	var primaryPackage *UpstreamPackage
	for i := range upstream.Server.Packages {
		pkg := &upstream.Server.Packages[i]
		if pkg.RegistryName == "docker" {
			primaryPackage = pkg
			break
		}
		if primaryPackage == nil {
			primaryPackage = pkg
		}
	}

	image := &ImageMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          upstream.Server.Name,
			Description:   upstream.Server.Description,
			Tier:          getStringOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) string { return t.Tier }, "Community"),
			Status:        convertStatus(upstream.Server.Status),
			Transport:     getStringOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) string { return t.Transport }, "stdio"),
			Tools:         getSliceOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) []string { return t.Tools }, []string{}),
			Metadata:      getMetadataOrDefault(toolhiveExt),
			RepositoryURL: getRepositoryURL(upstream.Server.Repository),
			Tags:          getSliceOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) []string { return t.Tags }, []string{}),
			CustomMetadata: getMapOrDefault(toolhiveExt,
				func(t *ToolhivePublisherExtension) map[string]any { return t.CustomMetadata }, nil),
		},
		Image:       formatImageName(primaryPackage),
		TargetPort:  getIntOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) int { return t.TargetPort }, 0),
		Permissions: getPermissionsOrDefault(toolhiveExt),
		EnvVars:     convertEnvironmentVariables(primaryPackage.EnvironmentVariables),
		Args:        convertPackageArguments(primaryPackage.PackageArguments),
		DockerTags:  getSliceOrDefault(toolhiveExt, func(t *ToolhivePublisherExtension) []string { return t.DockerTags }, []string{}),
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
		Server: UpstreamServer{
			Name:        server.GetName(),
			Description: server.GetDescription(),
			Status:      convertStatusToUpstream(server.GetStatus()),
			VersionDetail: UpstreamVersionDetail{
				Version: "1.0.0", // Default version, could be extracted from metadata
			},
		},
	}

	// Set repository if available
	if repoURL := server.GetRepositoryURL(); repoURL != "" {
		upstream.Server.Repository = &UpstreamRepository{
			URL:    repoURL,
			Source: extractSourceFromURL(repoURL),
		}
	}

	// Create toolhive extension for x-publisher
	toolhiveExt := &ToolhivePublisherExtension{
		Tier:           server.GetTier(),
		Transport:      server.GetTransport(),
		Tools:          server.GetTools(),
		Metadata:       server.GetMetadata(),
		Tags:           server.GetTags(),
		CustomMetadata: server.GetCustomMetadata(),
	}

	if server.IsRemote() {
		if remoteServer, ok := server.(*RemoteServerMetadata); ok {
			upstream.Server.Remotes = []UpstreamRemote{
				{
					TransportType: convertTransportToUpstream(remoteServer.Transport),
					URL:           remoteServer.URL,
					Headers:       convertHeadersToUpstream(remoteServer.Headers),
				},
			}
			upstream.Server.Packages = convertEnvVarsToPackages(remoteServer.EnvVars)
		}
	} else {
		if imageServer, ok := server.(*ImageMetadata); ok {
			toolhiveExt.TargetPort = imageServer.TargetPort
			toolhiveExt.Permissions = imageServer.Permissions
			toolhiveExt.DockerTags = imageServer.DockerTags
			toolhiveExt.Provenance = imageServer.Provenance

			upstream.Server.Packages = []UpstreamPackage{
				{
					RegistryName:         "docker",
					Name:                 extractImageName(imageServer.Image),
					Version:              extractImageTag(imageServer.Image),
					PackageArguments:     convertArgsToUpstream(imageServer.Args),
					EnvironmentVariables: convertEnvVarsToUpstream(imageServer.EnvVars),
				},
			}
		}
	}

	// Add toolhive extension to x-publisher
	upstream.XPublisher = &UpstreamPublisher{
		XDevToolhive: toolhiveExt,
	}

	return upstream, nil
}

// Helper functions for conversion

func convertStatus(status UpstreamServerStatus) string {
	switch status {
	case UpstreamServerStatusActive:
		return "active"
	case UpstreamServerStatusDeprecated:
		return "deprecated"
	default:
		return "active"
	}
}

func convertStatusToUpstream(status string) UpstreamServerStatus {
	switch status {
	case "deprecated":
		return UpstreamServerStatusDeprecated
	default:
		return UpstreamServerStatusActive
	}
}

func convertTransport(transportType UpstreamTransportType) string {
	switch transportType {
	case UpstreamTransportTypeSSE:
		return "sse"
	case UpstreamTransportTypeStreamable:
		return "streamable-http"
	default:
		return "stdio"
	}
}

func convertTransportToUpstream(transport string) UpstreamTransportType {
	switch transport {
	case "sse":
		return UpstreamTransportTypeSSE
	case "streamable-http":
		return UpstreamTransportTypeStreamable
	default:
		return UpstreamTransportTypeSSE // Default for remote
	}
}

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

func formatImageName(pkg *UpstreamPackage) string {
	switch pkg.RegistryName {
	case "docker":
		return fmt.Sprintf("%s:%s", pkg.Name, pkg.Version)
	case "npm":
		// For npm packages, use the npx:// protocol scheme that toolhive supports
		return fmt.Sprintf("npx://%s@%s", pkg.Name, pkg.Version)
	case "pypi":
		// For Python packages, use the uvx:// protocol scheme that toolhive supports
		return fmt.Sprintf("uvx://%s@%s", pkg.Name, pkg.Version)
	default:
		// For unknown registries, return the package name as-is
		// This might need manual handling or could be a direct image reference
		return pkg.Name
	}
}

func extractImageName(image string) string {
	parts := strings.Split(image, ":")
	return parts[0]
}

func extractImageTag(image string) string {
	parts := strings.Split(image, ":")
	if len(parts) > 1 {
		return parts[1]
	}
	return "latest"
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
			RegistryName:         "remote",
			Name:                 "remote-server",
			Version:              "1.0.0",
			EnvironmentVariables: upstreamEnvVars,
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

func getMetadataOrDefault(ext *ToolhivePublisherExtension) *Metadata {
	if ext != nil && ext.Metadata != nil {
		return ext.Metadata
	}
	return &Metadata{
		Stars:       0,
		Pulls:       0,
		LastUpdated: time.Now().Format(time.RFC3339),
	}
}

func getPermissionsOrDefault(ext *ToolhivePublisherExtension) *permissions.Profile {
	if ext != nil && ext.Permissions != nil {
		return ext.Permissions
	}
	return nil
}

func getProvenanceOrDefault(ext *ToolhivePublisherExtension) *Provenance {
	if ext != nil && ext.Provenance != nil {
		return ext.Provenance
	}
	return nil
}

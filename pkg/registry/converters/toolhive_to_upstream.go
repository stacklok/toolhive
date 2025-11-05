// Package converters provides bidirectional conversion between toolhive registry formats
// and the upstream MCP (Model Context Protocol) ServerJSON format.
//
// The package supports two conversion directions:
//   - toolhive → upstream: ImageMetadata/RemoteServerMetadata → ServerJSON (this file)
//   - upstream → toolhive: ServerJSON → ImageMetadata/RemoteServerMetadata (upstream_to_toolhive.go)
//
// Toolhive-specific fields (permissions, provenance, metadata) are stored in the upstream
// format's publisher extensions under "io.github.stacklok", allowing additional metadata
// while maintaining compatibility with the standard MCP registry format.
package converters

import (
	"fmt"

	upstream "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stacklok/toolhive/pkg/registry/types"
)

// ImageMetadataToServerJSON converts toolhive ImageMetadata to an upstream ServerJSON
// The name parameter should be the simple server name (e.g., "fetch")
func ImageMetadataToServerJSON(name string, imageMetadata *types.ImageMetadata) (*upstream.ServerJSON, error) {
	if imageMetadata == nil {
		return nil, fmt.Errorf("imageMetadata cannot be nil")
	}
	if name == "" {
		return nil, fmt.Errorf("name cannot be empty")
	}

	// Create ServerJSON with basic fields
	serverJSON := &upstream.ServerJSON{
		Schema:      model.CurrentSchemaURL,
		Name:        BuildReverseDNSName(name),
		Title:       imageMetadata.Name,
		Description: imageMetadata.Description,
		Version:     "1.0.0", // TODO: Extract from image tag or metadata
	}

	// Set repository if available
	if imageMetadata.RepositoryURL != "" {
		serverJSON.Repository = &model.Repository{
			URL:    imageMetadata.RepositoryURL,
			Source: "github", // Assume GitHub
		}
	}

	// Create package
	serverJSON.Packages = createPackagesFromImageMetadata(imageMetadata)

	// Create publisher extensions
	serverJSON.Meta = &upstream.ServerMeta{
		PublisherProvided: createImageExtensions(imageMetadata),
	}

	return serverJSON, nil
}

// RemoteServerMetadataToServerJSON converts toolhive RemoteServerMetadata to an upstream ServerJSON
// The name parameter should be the simple server name (e.g., "github-remote")
func RemoteServerMetadataToServerJSON(name string, remoteMetadata *types.RemoteServerMetadata) (*upstream.ServerJSON, error) {
	if remoteMetadata == nil {
		return nil, fmt.Errorf("remoteMetadata cannot be nil")
	}
	if name == "" {
		return nil, fmt.Errorf("name cannot be empty")
	}

	// Create ServerJSON with basic fields
	serverJSON := &upstream.ServerJSON{
		Schema:      model.CurrentSchemaURL,
		Name:        BuildReverseDNSName(name),
		Title:       remoteMetadata.Name,
		Description: remoteMetadata.Description,
		Version:     "1.0.0", // TODO: Version management
	}

	// Set repository if available
	if remoteMetadata.RepositoryURL != "" {
		serverJSON.Repository = &model.Repository{
			URL:    remoteMetadata.RepositoryURL,
			Source: "github", // Assume GitHub
		}
	}

	// Create remote
	serverJSON.Remotes = createRemotesFromRemoteMetadata(remoteMetadata)

	// Create publisher extensions
	serverJSON.Meta = &upstream.ServerMeta{
		PublisherProvided: createRemoteExtensions(remoteMetadata),
	}

	return serverJSON, nil
}

// createPackagesFromImageMetadata creates OCI Package entries from ImageMetadata
func createPackagesFromImageMetadata(imageMetadata *types.ImageMetadata) []model.Package {
	// Convert environment variables
	var envVars []model.KeyValueInput
	for _, envVar := range imageMetadata.EnvVars {
		envVars = append(envVars, model.KeyValueInput{
			Name: envVar.Name,
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Description: envVar.Description,
					IsRequired:  envVar.Required,
					IsSecret:    envVar.Secret,
					Default:     envVar.Default,
				},
			},
		})
	}

	// Determine transport
	transportType := imageMetadata.Transport
	if transportType == "" {
		transportType = model.TransportTypeStdio
	}

	transport := model.Transport{
		Type: transportType,
	}

	// Add URL for non-stdio transports
	// Note: We use localhost as the host because container-based MCP servers run locally
	// and are accessed via port forwarding. The actual container may listen on 0.0.0.0,
	// but clients connect via localhost on the host machine.
	if transportType == model.TransportTypeStreamableHTTP || transportType == model.TransportTypeSSE {
		if imageMetadata.TargetPort > 0 {
			// Include port in URL if explicitly set
			transport.URL = fmt.Sprintf("http://localhost:%d", imageMetadata.TargetPort)
		} else {
			// No port specified - use URL without port (standard HTTP port 80)
			transport.URL = "http://localhost"
		}
	}

	return []model.Package{{
		RegistryType:         model.RegistryTypeOCI,
		Identifier:           imageMetadata.Image,
		EnvironmentVariables: envVars,
		Transport:            transport,
	}}
}

// createRemotesFromRemoteMetadata creates Transport entries from RemoteServerMetadata
func createRemotesFromRemoteMetadata(remoteMetadata *types.RemoteServerMetadata) []model.Transport {
	// Convert headers
	var headers []model.KeyValueInput
	for _, header := range remoteMetadata.Headers {
		headers = append(headers, model.KeyValueInput{
			Name: header.Name,
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Description: header.Description,
					IsRequired:  header.Required,
					IsSecret:    header.Secret,
				},
			},
		})
	}

	return []model.Transport{{
		Type:    remoteMetadata.Transport,
		URL:     remoteMetadata.URL,
		Headers: headers,
	}}
}

// createImageExtensions creates publisher extensions map from ImageMetadata
func createImageExtensions(imageMetadata *types.ImageMetadata) map[string]interface{} {
	extensions := make(map[string]interface{})

	// Always include status
	extensions["status"] = imageMetadata.Status
	if extensions["status"] == "" {
		extensions["status"] = "active"
	}

	// Add tools
	if len(imageMetadata.Tools) > 0 {
		tools := make([]interface{}, len(imageMetadata.Tools))
		for i, tool := range imageMetadata.Tools {
			tools[i] = tool
		}
		extensions["tools"] = tools
	}

	// Add tier
	if imageMetadata.Tier != "" {
		extensions["tier"] = imageMetadata.Tier
	}

	// Add tags
	if len(imageMetadata.Tags) > 0 {
		tags := make([]interface{}, len(imageMetadata.Tags))
		for i, tag := range imageMetadata.Tags {
			tags[i] = tag
		}
		extensions["tags"] = tags
	}

	// Add metadata
	if imageMetadata.Metadata != nil {
		extensions["metadata"] = map[string]interface{}{
			"stars":        float64(imageMetadata.Metadata.Stars),
			"pulls":        float64(imageMetadata.Metadata.Pulls),
			"last_updated": imageMetadata.Metadata.LastUpdated,
		}
	}

	// Add permissions
	if imageMetadata.Permissions != nil {
		extensions["permissions"] = imageMetadata.Permissions
	}

	// Add args (static container arguments)
	if len(imageMetadata.Args) > 0 {
		extensions["args"] = imageMetadata.Args
	}

	// Add provenance
	if imageMetadata.Provenance != nil {
		extensions["provenance"] = imageMetadata.Provenance
	}

	return map[string]interface{}{
		"io.github.stacklok": map[string]interface{}{
			imageMetadata.Image: extensions,
		},
	}
}

// createRemoteExtensions creates publisher extensions map from RemoteServerMetadata
func createRemoteExtensions(remoteMetadata *types.RemoteServerMetadata) map[string]interface{} {
	extensions := make(map[string]interface{})

	// Always include status
	extensions["status"] = remoteMetadata.Status
	if extensions["status"] == "" {
		extensions["status"] = "active"
	}

	// Add tools
	if len(remoteMetadata.Tools) > 0 {
		tools := make([]interface{}, len(remoteMetadata.Tools))
		for i, tool := range remoteMetadata.Tools {
			tools[i] = tool
		}
		extensions["tools"] = tools
	}

	// Add tier
	if remoteMetadata.Tier != "" {
		extensions["tier"] = remoteMetadata.Tier
	}

	// Add tags
	if len(remoteMetadata.Tags) > 0 {
		tags := make([]interface{}, len(remoteMetadata.Tags))
		for i, tag := range remoteMetadata.Tags {
			tags[i] = tag
		}
		extensions["tags"] = tags
	}

	// Add metadata
	if remoteMetadata.Metadata != nil {
		extensions["metadata"] = map[string]interface{}{
			"stars":        float64(remoteMetadata.Metadata.Stars),
			"pulls":        float64(remoteMetadata.Metadata.Pulls),
			"last_updated": remoteMetadata.Metadata.LastUpdated,
		}
	}

	// Add OAuth config
	if remoteMetadata.OAuthConfig != nil {
		extensions["oauth_config"] = remoteMetadata.OAuthConfig
	}

	return map[string]interface{}{
		"io.github.stacklok": map[string]interface{}{
			remoteMetadata.URL: extensions,
		},
	}
}

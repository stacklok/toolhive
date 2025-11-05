// Package converters provides conversion functions from upstream MCP ServerJSON format
// to toolhive ImageMetadata/RemoteServerMetadata formats.
package converters

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	upstream "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry/types"
)

// ServerJSONToImageMetadata converts an upstream ServerJSON (with OCI packages) to toolhive ImageMetadata
// This function only handles OCI packages and will error if there are multiple OCI packages
func ServerJSONToImageMetadata(serverJSON *upstream.ServerJSON) (*types.ImageMetadata, error) {
	if serverJSON == nil {
		return nil, fmt.Errorf("serverJSON cannot be nil")
	}

	pkg, err := extractSingleOCIPackage(serverJSON)
	if err != nil {
		return nil, err
	}

	// Use Title if available, otherwise extract simple name from reverse-DNS Name
	displayName := serverJSON.Title
	if displayName == "" {
		displayName = ExtractServerName(serverJSON.Name)
	}

	imageMetadata := &types.ImageMetadata{
		BaseServerMetadata: types.BaseServerMetadata{
			Name:        displayName,
			Description: serverJSON.Description,
			Transport:   pkg.Transport.Type,
		},
		Image: pkg.Identifier, // OCI packages store full image ref in Identifier
	}

	// Set repository URL
	if serverJSON.Repository != nil && serverJSON.Repository.URL != "" {
		imageMetadata.RepositoryURL = serverJSON.Repository.URL
	}

	// Convert environment variables
	imageMetadata.EnvVars = convertEnvironmentVariables(pkg.EnvironmentVariables)

	// Extract target port from transport URL if present
	imageMetadata.TargetPort = extractTargetPort(pkg.Transport.URL, serverJSON.Name)

	// Convert PackageArguments to simple Args (priority: structured arguments first)
	if len(pkg.PackageArguments) > 0 {
		imageMetadata.Args = flattenPackageArguments(pkg.PackageArguments)
	}

	// Extract publisher-provided extensions (including Args fallback)
	extractImageExtensions(serverJSON, imageMetadata)

	return imageMetadata, nil
}

// extractSingleOCIPackage validates and extracts the single OCI package from ServerJSON
func extractSingleOCIPackage(serverJSON *upstream.ServerJSON) (model.Package, error) {
	if len(serverJSON.Packages) == 0 {
		return model.Package{}, fmt.Errorf("server '%s' has no packages (not a container-based server)", serverJSON.Name)
	}

	// Filter for OCI packages only
	var ociPackages []model.Package
	var packageTypes []string
	for _, pkg := range serverJSON.Packages {
		if pkg.RegistryType == model.RegistryTypeOCI {
			ociPackages = append(ociPackages, pkg)
		}
		packageTypes = append(packageTypes, string(pkg.RegistryType))
	}

	if len(ociPackages) == 0 {
		return model.Package{}, fmt.Errorf("server '%s' has no OCI packages (found: %v)", serverJSON.Name, packageTypes)
	}

	if len(ociPackages) > 1 {
		return model.Package{}, fmt.Errorf("server '%s' has %d OCI packages, expected exactly 1", serverJSON.Name, len(ociPackages))
	}

	return ociPackages[0], nil
}

// convertEnvironmentVariables converts model.KeyValueInput to types.EnvVar
func convertEnvironmentVariables(envVars []model.KeyValueInput) []*types.EnvVar {
	if len(envVars) == 0 {
		return nil
	}

	result := make([]*types.EnvVar, 0, len(envVars))
	for _, envVar := range envVars {
		result = append(result, &types.EnvVar{
			Name:        envVar.Name,
			Description: envVar.Description,
			Required:    envVar.IsRequired,
			Secret:      envVar.IsSecret,
			Default:     envVar.Default,
		})
	}
	return result
}

// extractTargetPort extracts the port number from a transport URL
func extractTargetPort(transportURL, serverName string) int {
	if transportURL == "" {
		return 0
	}

	parsedURL, err := url.Parse(transportURL)
	if err != nil {
		fmt.Printf("⚠️  Failed to parse transport URL '%s' for server '%s': %v\n",
			transportURL, serverName, err)
		return 0
	}

	if parsedURL.Port() == "" {
		return 0
	}

	port, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		fmt.Printf("⚠️  Failed to parse port from URL '%s' for server '%s': %v\n",
			transportURL, serverName, err)
		return 0
	}

	return port
}

// ServerJSONToRemoteServerMetadata converts an upstream ServerJSON (with remotes) to toolhive RemoteServerMetadata
// This function extracts remote server data and reconstructs RemoteServerMetadata format
func ServerJSONToRemoteServerMetadata(serverJSON *upstream.ServerJSON) (*types.RemoteServerMetadata, error) {
	if serverJSON == nil {
		return nil, fmt.Errorf("serverJSON cannot be nil")
	}

	if len(serverJSON.Remotes) == 0 {
		return nil, fmt.Errorf("server '%s' has no remotes (not a remote server)", serverJSON.Name)
	}

	remote := serverJSON.Remotes[0] // Use first remote

	// Use Title if available, otherwise extract simple name from reverse-DNS Name
	displayName := serverJSON.Title
	if displayName == "" {
		displayName = ExtractServerName(serverJSON.Name)
	}

	remoteMetadata := &types.RemoteServerMetadata{
		BaseServerMetadata: types.BaseServerMetadata{
			Name:        displayName,
			Description: serverJSON.Description,
			Transport:   remote.Type,
		},
		URL: remote.URL,
	}

	// Set repository URL
	if serverJSON.Repository != nil && serverJSON.Repository.URL != "" {
		remoteMetadata.RepositoryURL = serverJSON.Repository.URL
	}

	// Convert headers
	if len(remote.Headers) > 0 {
		remoteMetadata.Headers = make([]*types.Header, 0, len(remote.Headers))
		for _, header := range remote.Headers {
			remoteMetadata.Headers = append(remoteMetadata.Headers, &types.Header{
				Name:        header.Name,
				Description: header.Description,
				Required:    header.IsRequired,
				Secret:      header.IsSecret,
			})
		}
	}

	// Extract publisher-provided extensions
	extractRemoteExtensions(serverJSON, remoteMetadata)

	return remoteMetadata, nil
}

// extractImageExtensions extracts publisher-provided extensions into ImageMetadata
func extractImageExtensions(serverJSON *upstream.ServerJSON, imageMetadata *types.ImageMetadata) {
	extensions := getStacklokExtensions(serverJSON)
	if extensions == nil {
		return
	}

	extractBasicImageFields(extensions, imageMetadata)
	extractImageMetadataField(extensions, imageMetadata)
	extractComplexImageFields(extensions, imageMetadata)
}

// getStacklokExtensions retrieves the first stacklok extension data from ServerJSON
func getStacklokExtensions(serverJSON *upstream.ServerJSON) map[string]interface{} {
	if serverJSON.Meta == nil || serverJSON.Meta.PublisherProvided == nil {
		return nil
	}

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Return first extension data (keyed by image reference or URL)
	for _, extensionsData := range stacklokData {
		if extensions, ok := extensionsData.(map[string]interface{}); ok {
			return extensions
		}
	}
	return nil
}

// extractBasicImageFields extracts basic string and slice fields
func extractBasicImageFields(extensions map[string]interface{}, imageMetadata *types.ImageMetadata) {
	if status, ok := extensions["status"].(string); ok {
		imageMetadata.Status = status
	}
	if tier, ok := extensions["tier"].(string); ok {
		imageMetadata.Tier = tier
	}
	if toolsData, ok := extensions["tools"].([]interface{}); ok {
		imageMetadata.Tools = interfaceSliceToStringSlice(toolsData)
	}
	if tagsData, ok := extensions["tags"].([]interface{}); ok {
		imageMetadata.Tags = interfaceSliceToStringSlice(tagsData)
	}
}

// extractImageMetadataField extracts the metadata object (stars, pulls, last_updated)
func extractImageMetadataField(extensions map[string]interface{}, imageMetadata *types.ImageMetadata) {
	metadataData, ok := extensions["metadata"].(map[string]interface{})
	if !ok {
		return
	}

	imageMetadata.Metadata = &types.Metadata{}
	if stars, ok := metadataData["stars"].(float64); ok {
		imageMetadata.Metadata.Stars = int(stars)
	}
	if pulls, ok := metadataData["pulls"].(float64); ok {
		imageMetadata.Metadata.Pulls = int(pulls)
	}
	if lastUpdated, ok := metadataData["last_updated"].(string); ok {
		imageMetadata.Metadata.LastUpdated = lastUpdated
	}
}

// extractComplexImageFields extracts complex fields (args, permissions, provenance)
func extractComplexImageFields(extensions map[string]interface{}, imageMetadata *types.ImageMetadata) {
	// Extract args (fallback if PackageArguments wasn't used)
	if len(imageMetadata.Args) == 0 {
		if argsData, ok := extensions["args"].([]interface{}); ok {
			imageMetadata.Args = interfaceSliceToStringSlice(argsData)
		}
	}

	// Extract permissions using JSON round-trip
	if permsData, ok := extensions["permissions"]; ok {
		imageMetadata.Permissions = remarshalToType[*permissions.Profile](permsData)
	}

	// Extract provenance using JSON round-trip
	if provData, ok := extensions["provenance"]; ok {
		imageMetadata.Provenance = remarshalToType[*types.Provenance](provData)
	}
}

// extractRemoteExtensions extracts publisher-provided extensions into RemoteServerMetadata
func extractRemoteExtensions(serverJSON *upstream.ServerJSON, remoteMetadata *types.RemoteServerMetadata) {
	if serverJSON.Meta == nil || serverJSON.Meta.PublisherProvided == nil {
		return
	}

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	if !ok {
		return
	}

	// Find the extension data (keyed by URL)
	for _, extensionsData := range stacklokData {
		extensions, ok := extensionsData.(map[string]interface{})
		if !ok {
			continue
		}

		// Extract fields
		if status, ok := extensions["status"].(string); ok {
			remoteMetadata.Status = status
		}
		if tier, ok := extensions["tier"].(string); ok {
			remoteMetadata.Tier = tier
		}
		if toolsData, ok := extensions["tools"].([]interface{}); ok {
			remoteMetadata.Tools = interfaceSliceToStringSlice(toolsData)
		}
		if tagsData, ok := extensions["tags"].([]interface{}); ok {
			remoteMetadata.Tags = interfaceSliceToStringSlice(tagsData)
		}
		if metadataData, ok := extensions["metadata"].(map[string]interface{}); ok {
			remoteMetadata.Metadata = &types.Metadata{}
			if stars, ok := metadataData["stars"].(float64); ok {
				remoteMetadata.Metadata.Stars = int(stars)
			}
			if pulls, ok := metadataData["pulls"].(float64); ok {
				remoteMetadata.Metadata.Pulls = int(pulls)
			}
			if lastUpdated, ok := metadataData["last_updated"].(string); ok {
				remoteMetadata.Metadata.LastUpdated = lastUpdated
			}
		}

		// Extract OAuth config using JSON round-trip
		if oauthData, ok := extensions["oauth_config"]; ok {
			remoteMetadata.OAuthConfig = remarshalToType[*types.OAuthConfig](oauthData)
		}

		break // Only process first entry
	}
}

// remarshalToType converts an interface{} value to a specific type using JSON marshaling
// This is useful for deserializing complex nested structures from extensions
func remarshalToType[T any](data interface{}) T {
	var result T

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return result // Return zero value on error
	}

	// Unmarshal into target type
	_ = json.Unmarshal(jsonData, &result) // Ignore error, return zero value if fails

	return result
}

// flattenPackageArguments converts structured PackageArguments to simple string Args
// This provides better interoperability when importing from upstream sources
func flattenPackageArguments(args []model.Argument) []string {
	var result []string
	for _, arg := range args {
		// Add the argument name/flag if present
		if arg.Name != "" {
			result = append(result, arg.Name)
		}
		// Add the value if present (for named args with values or positional args)
		if arg.Value != "" {
			result = append(result, arg.Value)
		}
	}
	return result
}

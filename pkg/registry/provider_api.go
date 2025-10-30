package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"

	"github.com/stacklok/toolhive/pkg/registry/api"
)

// APIRegistryProvider provides registry data from an MCP Registry API endpoint
// It queries the API on-demand for each operation, ensuring fresh data.
type APIRegistryProvider struct {
	*BaseProvider
	apiURL         string
	allowPrivateIp bool
	client         api.Client
}

// NewAPIRegistryProvider creates a new API registry provider
func NewAPIRegistryProvider(apiURL string, allowPrivateIp bool) (*APIRegistryProvider, error) {
	// Create API client
	client, err := api.NewClient(apiURL, allowPrivateIp)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	p := &APIRegistryProvider{
		apiURL:         apiURL,
		allowPrivateIp: allowPrivateIp,
		client:         client,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	// Validate the endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.ValidateEndpoint(ctx); err != nil {
		return nil, fmt.Errorf("invalid MCP Registry API endpoint: %w", err)
	}

	return p, nil
}

// GetRegistry returns the registry data by fetching all servers from the API
// This method queries the API and converts all servers to ToolHive format.
// Note: This can be slow for large registries as it fetches everything.
func (p *APIRegistryProvider) GetRegistry() (*Registry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fetch all servers from the API
	servers, err := p.client.ListServers(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers from API: %w", err)
	}

	// Convert servers to ToolHive format
	serverMetadata, err := ConvertServersToMetadata(servers)
	if err != nil {
		return nil, fmt.Errorf("failed to convert servers to ToolHive format: %w", err)
	}

	// Build Registry structure
	registry := &Registry{
		Version:       "1.0.0",
		LastUpdated:   time.Now().Format(time.RFC3339),
		Servers:       make(map[string]*ImageMetadata),
		RemoteServers: make(map[string]*RemoteServerMetadata),
		Groups:        []*Group{},
	}

	// Separate servers into container and remote
	for _, server := range serverMetadata {
		if server.IsRemote() {
			if remoteServer, ok := server.(*RemoteServerMetadata); ok {
				registry.RemoteServers[remoteServer.Name] = remoteServer
			}
		} else {
			if imageServer, ok := server.(*ImageMetadata); ok {
				registry.Servers[imageServer.Name] = imageServer
			}
		}
	}

	return registry, nil
}

// GetServer returns a specific server by name (queries API directly)
func (p *APIRegistryProvider) GetServer(name string) (ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try to find server by searching (since API uses reverse-DNS names)
	// First try direct lookup by assuming simple name
	servers, err := p.client.SearchServers(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to search for server %s: %w", name, err)
	}

	// Find exact match
	for _, server := range servers {
		// Extract simple name from reverse-DNS format
		simpleName := extractAPIServerName(server.Name)
		if simpleName == name || server.Name == name {
			return ConvertServerJSON(server)
		}
	}

	return nil, fmt.Errorf("server %s not found in API", name)
}

// SearchServers searches for servers matching the query (queries API directly)
func (p *APIRegistryProvider) SearchServers(query string) ([]ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Search via API
	servers, err := p.client.SearchServers(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to search servers: %w", err)
	}

	return ConvertServersToMetadata(servers)
}

// ListServers returns all servers from the API
func (p *APIRegistryProvider) ListServers() ([]ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	servers, err := p.client.ListServers(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	return ConvertServersToMetadata(servers)
}

// extractAPIServerName extracts the simple name from a reverse-DNS format name
// e.g., "io.github.user/weather" -> "weather"
func extractAPIServerName(reverseDNSName string) string {
	// Find the last slash
	idx := strings.LastIndex(reverseDNSName, "/")
	if idx == -1 {
		return reverseDNSName
	}
	return reverseDNSName[idx+1:]
}

// ConvertServerJSON converts an MCP Registry API ServerJSON to ToolHive ServerMetadata
func ConvertServerJSON(serverJSON *v0.ServerJSON) (ServerMetadata, error) {
	if serverJSON == nil {
		return nil, fmt.Errorf("serverJSON is nil")
	}

	// Determine if this is a remote server or container-based server
	// Remote servers have the 'remotes' field populated
	// Container servers have the 'packages' field populated
	if len(serverJSON.Remotes) > 0 {
		return convertAPIToRemoteServer(serverJSON)
	}

	// Check if server has packages
	if len(serverJSON.Packages) == 0 {
		// Skip servers without packages or remotes (incomplete entries)
		return nil, fmt.Errorf("server %s has no packages or remotes, skipping", serverJSON.Name)
	}

	return convertAPIToImageMetadata(serverJSON)
}

// convertAPIToRemoteServer converts a ServerJSON with remotes to RemoteServerMetadata
func convertAPIToRemoteServer(serverJSON *v0.ServerJSON) (*RemoteServerMetadata, error) {
	if len(serverJSON.Remotes) == 0 {
		return nil, fmt.Errorf("no remotes found in server")
	}

	remote := serverJSON.Remotes[0] // Use first remote (Transport type)

	metadata := &RemoteServerMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          extractAPIServerName(serverJSON.Name),
			Description:   serverJSON.Description,
			Tier:          "Community", // Default tier
			Status:        "active",    // Default status
			Transport:     convertAPITransport(remote),
			Tools:         []string{},
			RepositoryURL: getAPIRepositoryURL(serverJSON.Repository),
			Tags:          []string{},
		},
		URL:     remote.URL,
		Headers: convertAPIHeaders(remote.Headers),
	}

	return metadata, nil
}

// convertAPIToImageMetadata converts a ServerJSON with packages to ImageMetadata
func convertAPIToImageMetadata(serverJSON *v0.ServerJSON) (*ImageMetadata, error) {
	if len(serverJSON.Packages) == 0 {
		return nil, fmt.Errorf("no packages found in server")
	}

	// Find OCI package
	var ociPackage *model.Package
	for i := range serverJSON.Packages {
		pkg := &serverJSON.Packages[i]
		if pkg.RegistryType == "oci" || pkg.RegistryType == "docker" {
			ociPackage = pkg
			break
		}
	}

	if ociPackage == nil {
		// Fall back to first package
		ociPackage = &serverJSON.Packages[0]
	}

	image := formatAPIImageName(ociPackage)

	metadata := &ImageMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          extractAPIServerName(serverJSON.Name),
			Description:   serverJSON.Description,
			Tier:          "Community", // Default tier
			Status:        "active",    // Default status
			Transport:     "stdio",     // Default transport for container servers
			Tools:         []string{},
			RepositoryURL: getAPIRepositoryURL(serverJSON.Repository),
			Tags:          []string{},
		},
		Image: image,
	}

	return metadata, nil
}

// ConvertServersToMetadata converts a slice of ServerJSON to a slice of ServerMetadata
// Skips servers that cannot be converted (e.g., incomplete entries)
func ConvertServersToMetadata(servers []*v0.ServerJSON) ([]ServerMetadata, error) {
	result := make([]ServerMetadata, 0, len(servers))

	for _, server := range servers {
		metadata, err := ConvertServerJSON(server)
		if err != nil {
			// Skip servers that can't be converted (e.g., missing packages/remotes)
			// Log the error but continue processing other servers
			continue
		}
		result = append(result, metadata)
	}

	return result, nil
}

// Helper functions for conversion

func convertAPITransport(transport model.Transport) string {
	switch transport.Type {
	case "sse":
		return "sse"
	case "streamable-http":
		return "streamable-http"
	default:
		return "stdio"
	}
}

func getAPIRepositoryURL(repo *model.Repository) string {
	if repo != nil {
		return repo.URL
	}
	return ""
}

func convertAPIHeaders(headers []model.KeyValueInput) []*Header {
	result := make([]*Header, 0, len(headers))
	for _, h := range headers {
		result = append(result, &Header{
			Name:        h.Name,
			Description: h.Description,
			Required:    h.IsRequired,
			Secret:      h.IsSecret,
		})
	}
	return result
}

func formatAPIImageName(pkg *model.Package) string {
	switch pkg.RegistryType {
	case "docker", "oci":
		// For OCI, the Identifier already contains the full image reference
		return pkg.Identifier
	case "npm":
		if pkg.Version != "" {
			return fmt.Sprintf("npx://%s@%s", pkg.Identifier, pkg.Version)
		}
		return fmt.Sprintf("npx://%s", pkg.Identifier)
	case "pypi":
		if pkg.Version != "" {
			return fmt.Sprintf("uvx://%s@%s", pkg.Identifier, pkg.Version)
		}
		return fmt.Sprintf("uvx://%s", pkg.Identifier)
	default:
		return pkg.Identifier
	}
}

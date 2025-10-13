package sources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/httpclient"
	"github.com/stacklok/toolhive/pkg/registry"
)

// APISourceHandler handles registry data from ToolHive Registry API endpoints
// Phase 1: Supports ToolHive API (no pagination, direct ToolHive format)
type APISourceHandler struct {
	httpClient httpclient.Client
	validator  SourceDataValidator
}

// NewAPISourceHandler creates a new API source handler
func NewAPISourceHandler() *APISourceHandler {
	return &APISourceHandler{
		httpClient: httpclient.NewDefaultClient(0), // Use default timeout
		validator:  NewSourceDataValidator(),
	}
}

// Validate validates the API source configuration
func (*APISourceHandler) Validate(source *mcpv1alpha1.MCPRegistrySource) error {
	if source.Type != mcpv1alpha1.RegistrySourceTypeAPI {
		return fmt.Errorf("invalid source type: expected %s, got %s",
			mcpv1alpha1.RegistrySourceTypeAPI, source.Type)
	}

	if source.API == nil {
		return fmt.Errorf("api configuration is required for source type %s",
			mcpv1alpha1.RegistrySourceTypeAPI)
	}

	if source.API.Endpoint == "" {
		return fmt.Errorf("api endpoint cannot be empty")
	}

	// Validate URL format
	_, err := url.Parse(source.API.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}

	return nil
}

// FetchRegistry retrieves registry data from the ToolHive API endpoint
func (h *APISourceHandler) FetchRegistry(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*FetchResult, error) {
	logger := log.FromContext(ctx)

	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return nil, fmt.Errorf("source validation failed: %w", err)
	}

	// Build API URL with format parameter
	apiURL := h.buildAPIURL(mcpRegistry)

	// Fetch data from API
	startTime := time.Now()
	logger.Info("Fetching from ToolHive API",
		"url", apiURL)

	data, err := h.httpClient.Get(ctx, apiURL)
	if err != nil {
		logger.Error(err, "API fetch failed",
			"url", apiURL,
			"duration", time.Since(startTime).String())
		return nil, fmt.Errorf("failed to fetch from API: %w", err)
	}

	logger.Info("API fetch completed",
		"url", apiURL,
		"duration", time.Since(startTime).String(),
		"response_size_bytes", len(data))

	// Parse response
	var listResponse ListServersResponse
	if err := json.Unmarshal(data, &listResponse); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %w", err)
	}

	logger.Info("Parsed API response",
		"total_servers", listResponse.Total,
		"servers_in_response", len(listResponse.Servers))

	// Convert to ToolHive Registry format, fetching details for each server
	toolhiveRegistry, err := h.convertToToolhiveRegistry(ctx, mcpRegistry, &listResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to ToolHive format: %w", err)
	}

	// Calculate hash of the raw data for change detection
	hash := fmt.Sprintf("%x", sha256.Sum256(data))

	// Create and return fetch result
	return NewFetchResult(toolhiveRegistry, hash, mcpv1alpha1.RegistryFormatToolHive), nil
}

// buildAPIURL constructs the API URL with the appropriate path and format parameter
func (*APISourceHandler) buildAPIURL(mcpRegistry *mcpv1alpha1.MCPRegistry) string {
	apiSource := mcpRegistry.Spec.Source.API
	baseURL := apiSource.Endpoint

	// Ensure base URL doesn't end with slash
	if len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}

	// ToolHive API path (MCP Registry API v0 compatible)
	// /v0/servers
	fullURL := baseURL + "/v0/servers"

	parsedURL, err := url.Parse(fullURL)
	if err != nil {
		// Should not happen since we validated earlier
		return fullURL
	}

	// Add format query parameter (required by ToolHive API)
	q := parsedURL.Query()
	q.Set("format", "toolhive")
	parsedURL.RawQuery = q.Encode()

	return parsedURL.String()
}

// convertToToolhiveRegistry converts API response to ToolHive Registry format
// by fetching detailed information for each server
func (h *APISourceHandler) convertToToolhiveRegistry(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	response *ListServersResponse,
) (*registry.Registry, error) {
	logger := log.FromContext(ctx)

	toolhiveRegistry := &registry.Registry{
		Version:       "1.0",
		LastUpdated:   time.Now().Format(time.RFC3339),
		Servers:       make(map[string]*registry.ImageMetadata),
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	// Fetch detailed information for each server
	for _, serverSummary := range response.Servers {
		// Build URL for server details: /v0/servers/{name}
		detailURL := h.buildServerDetailURL(mcpRegistry, serverSummary.Name)

		logger.V(1).Info("Fetching server details",
			"server", serverSummary.Name,
			"url", detailURL)

		// Fetch server details
		detailData, err := h.httpClient.Get(ctx, detailURL)
		if err != nil {
			logger.Error(err, "Failed to fetch server details, using summary only",
				"server", serverSummary.Name)
			// Fall back to summary data
			h.addServerFromSummary(toolhiveRegistry, &serverSummary)
			continue
		}

		// Parse server detail response
		var serverDetail ServerDetailResponse
		if err := json.Unmarshal(detailData, &serverDetail); err != nil {
			logger.Error(err, "Failed to parse server detail response, using summary only",
				"server", serverSummary.Name)
			// Fall back to summary data
			h.addServerFromSummary(toolhiveRegistry, &serverSummary)
			continue
		}

		// Add server with full details
		h.addServerFromDetail(toolhiveRegistry, &serverDetail)
	}

	return toolhiveRegistry, nil
}

// buildServerDetailURL constructs the URL for fetching server details
func (h *APISourceHandler) buildServerDetailURL(mcpRegistry *mcpv1alpha1.MCPRegistry, serverName string) string {
	baseURL := mcpRegistry.Spec.Source.API.Endpoint

	// Remove trailing slash
	if len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}

	// Construct URL: /v0/servers/{name}?format=toolhive
	fullURL := fmt.Sprintf("%s/v0/servers/%s", baseURL, url.PathEscape(serverName))
	parsedURL, err := url.Parse(fullURL)
	if err != nil {
		return fullURL
	}

	// Add format query parameter
	q := parsedURL.Query()
	q.Set("format", "toolhive")
	parsedURL.RawQuery = q.Encode()

	return parsedURL.String()
}

// addServerFromSummary adds a server using only summary data (fallback)
func (*APISourceHandler) addServerFromSummary(reg *registry.Registry, summary *ServerSummaryResponse) {
	imageMetadata := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        summary.Name,
			Description: summary.Description,
			Tier:        summary.Tier,
			Status:      summary.Status,
			Transport:   summary.Transport,
			Tools:       make([]string, 0), // Empty, not available in summary
		},
		Image: "", // Not available in summary
	}
	reg.Servers[summary.Name] = imageMetadata
}

// addServerFromDetail adds a server using full detail data
func (*APISourceHandler) addServerFromDetail(reg *registry.Registry, detail *ServerDetailResponse) {
	imageMetadata := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:          detail.Name,
			Description:   detail.Description,
			Tier:          detail.Tier,
			Status:        detail.Status,
			Transport:     detail.Transport,
			Tools:         detail.Tools,
			RepositoryURL: detail.RepositoryURL,
			Tags:          detail.Tags,
		},
		Image: detail.Image,
		Args:  detail.Args,
		// Note: Permissions are stored in CustomMetadata below since API returns map[string]interface{}
		// and ImageMetadata expects *permissions.Profile. Conversion would be needed for full support.
	}

	// Add environment variables if present
	if len(detail.EnvVars) > 0 {
		imageMetadata.EnvVars = make([]*registry.EnvVar, len(detail.EnvVars))
		for i, envVar := range detail.EnvVars {
			imageMetadata.EnvVars[i] = &registry.EnvVar{
				Name:        envVar.Name,
				Description: envVar.Description,
				Required:    envVar.Required,
				Default:     envVar.Default,
				Secret:      envVar.Secret,
			}
		}
	}

	// Build custom metadata
	customMetadata := make(map[string]interface{})

	// Add all metadata from the detail response
	for k, v := range detail.Metadata {
		customMetadata[k] = v
	}

	// Add permissions to custom metadata if present
	if len(detail.Permissions) > 0 {
		customMetadata["permissions"] = detail.Permissions
	}

	// Add volumes to custom metadata if present
	if len(detail.Volumes) > 0 {
		customMetadata["volumes"] = detail.Volumes
	}

	if len(customMetadata) > 0 {
		imageMetadata.CustomMetadata = customMetadata
	}

	reg.Servers[detail.Name] = imageMetadata
}

// CurrentHash returns the current hash of the API response
//
// TODO: Review hash computation strategy. Currently hashes raw response data which may
// cause unnecessary sync triggers when API response format changes (field order, whitespace).
// Consider alternatives:
//   - Hash normalized/sorted JSON
//   - Hash only semantic content (server names + versions)
//   - Use ETag headers if available from API
//   - Use last_updated timestamp from registry metadata
func (h *APISourceHandler) CurrentHash(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (string, error) {
	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return "", fmt.Errorf("source validation failed: %w", err)
	}

	// Build API URL
	apiURL := h.buildAPIURL(mcpRegistry)

	// Fetch data from API
	data, err := h.httpClient.Get(ctx, apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch from API: %w", err)
	}

	// Compute and return hash
	// NOTE: This is a simple implementation that hashes raw response bytes
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	return hash, nil
}

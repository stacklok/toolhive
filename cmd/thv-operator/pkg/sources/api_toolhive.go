package sources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/httpclient"
	"github.com/stacklok/toolhive/pkg/registry"
)

// ToolHiveAPIHandler handles registry data from ToolHive Registry API endpoints
// API Format: /v0/servers (list), /v0/servers/{name} (detail), /v0/info (metadata)
type ToolHiveAPIHandler struct {
	httpClient httpclient.Client
	validator  SourceDataValidator
}

// NewToolHiveAPIHandler creates a new ToolHive API handler
func NewToolHiveAPIHandler(httpClient httpclient.Client) *ToolHiveAPIHandler {
	return &ToolHiveAPIHandler{
		httpClient: httpClient,
		validator:  NewSourceDataValidator(),
	}
}

// Validate validates that the endpoint is a ToolHive registry
func (h *ToolHiveAPIHandler) Validate(ctx context.Context, endpoint string) error {
	infoURL := endpoint + "/v0/info"

	data, err := h.httpClient.Get(ctx, infoURL)
	if err != nil {
		return fmt.Errorf("failed to fetch /v0/info: %w", err)
	}

	// Parse info response
	var info RegistryInfoResponse
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("failed to parse /v0/info response: %w", err)
	}

	// Validate expected fields
	if info.Version == "" {
		return fmt.Errorf("/v0/info missing 'version' field")
	}

	if info.TotalServers < 0 {
		return fmt.Errorf("/v0/info has invalid 'total_servers' value: %d", info.TotalServers)
	}

	return nil
}

// FetchRegistry retrieves registry data from the ToolHive API endpoint
func (h *ToolHiveAPIHandler) FetchRegistry(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*FetchResult, error) {
	logger := log.FromContext(ctx)
	baseURL := h.getBaseURL(mcpRegistry)

	// Build API URL: /v0/servers?format=toolhive
	apiURL := h.buildServersURL(baseURL)

	// Fetch server list
	startTime := time.Now()
	logger.Info("Fetching from ToolHive API", "url", apiURL)

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
	toolhiveRegistry, err := h.convertToToolhiveRegistry(ctx, baseURL, &listResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to ToolHive format: %w", err)
	}

	// Calculate hash of the raw data for change detection
	hash := fmt.Sprintf("%x", sha256.Sum256(data))

	// Create and return fetch result
	return NewFetchResult(toolhiveRegistry, hash, mcpv1alpha1.RegistryFormatToolHive), nil
}

// CurrentHash returns the current hash of the API response
func (h *ToolHiveAPIHandler) CurrentHash(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (string, error) {
	baseURL := h.getBaseURL(mcpRegistry)
	apiURL := h.buildServersURL(baseURL)

	// Fetch data from API
	data, err := h.httpClient.Get(ctx, apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch from API: %w", err)
	}

	// Compute and return hash
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	return hash, nil
}

// getBaseURL extracts and normalizes the base URL
func (*ToolHiveAPIHandler) getBaseURL(mcpRegistry *mcpv1alpha1.MCPRegistry) string {
	baseURL := mcpRegistry.Spec.Source.API.Endpoint

	// Remove trailing slash
	if len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}

	return baseURL
}

// buildServersURL constructs the URL for listing servers
func (*ToolHiveAPIHandler) buildServersURL(baseURL string) string {
	// ToolHive API path: /v0/servers
	fullURL := baseURL + "/v0/servers"

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

// buildServerDetailURL constructs the URL for fetching server details
func (*ToolHiveAPIHandler) buildServerDetailURL(baseURL, serverName string) string {
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

// convertToToolhiveRegistry converts API response to ToolHive Registry format
// by fetching detailed information for each server
//
//nolint:unparam // error return kept for future extensibility and consistency with interface
func (h *ToolHiveAPIHandler) convertToToolhiveRegistry(
	ctx context.Context,
	baseURL string,
	response *ListServersResponse,
) (*registry.Registry, error) {
	logger := log.FromContext(ctx)

	toolhiveRegistry := &registry.Registry{
		Version:       "1.0",
		LastUpdated:   time.Now().Format(time.RFC3339),
		Servers:       make(map[string]*registry.ImageMetadata),
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	// Fetch detailed information for each server in parallel
	h.fetchServerDetailsParallel(ctx, baseURL, response.Servers, toolhiveRegistry, logger)

	return toolhiveRegistry, nil
}

// fetchServerDetailsParallel fetches server details concurrently with controlled parallelism
func (h *ToolHiveAPIHandler) fetchServerDetailsParallel(
	ctx context.Context,
	baseURL string,
	servers []ServerSummaryResponse,
	toolhiveRegistry *registry.Registry,
	logger logr.Logger,
) {
	// Limit concurrent requests to avoid overwhelming the API
	const maxConcurrency = 10

	// Create a semaphore to limit concurrency
	semaphore := make(chan struct{}, maxConcurrency)

	// Use WaitGroup to wait for all goroutines to complete
	var wg sync.WaitGroup

	// Mutex to protect concurrent writes to the registry
	var mu sync.Mutex

	for _, serverSummary := range servers {
		wg.Add(1)

		// Launch goroutine for each server
		go func(summary ServerSummaryResponse) {
			defer wg.Done()

			// Acquire semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Build URL for server details: /v0/servers/{name}
			detailURL := h.buildServerDetailURL(baseURL, summary.Name)

			logger.V(1).Info("Fetching server details",
				"server", summary.Name,
				"url", detailURL)

			// Fetch server details
			detailData, err := h.httpClient.Get(ctx, detailURL)
			if err != nil {
				logger.Error(err, "Failed to fetch server details, using summary only",
					"server", summary.Name)
				// Fall back to summary data
				mu.Lock()
				h.addServerFromSummary(toolhiveRegistry, &summary)
				mu.Unlock()
				return
			}

			// Parse server detail response
			var serverDetail ServerDetailResponse
			if err := json.Unmarshal(detailData, &serverDetail); err != nil {
				logger.Error(err, "Failed to parse server detail response, using summary only",
					"server", summary.Name)
				// Fall back to summary data
				mu.Lock()
				h.addServerFromSummary(toolhiveRegistry, &summary)
				mu.Unlock()
				return
			}

			// Add server with full details (thread-safe)
			mu.Lock()
			h.addServerFromDetail(toolhiveRegistry, &serverDetail)
			mu.Unlock()
		}(serverSummary)
	}

	// Wait for all goroutines to complete
	wg.Wait()
}

// addServerFromSummary adds a server using only summary data (fallback)
func (*ToolHiveAPIHandler) addServerFromSummary(reg *registry.Registry, summary *ServerSummaryResponse) {
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
func (*ToolHiveAPIHandler) addServerFromDetail(reg *registry.Registry, detail *ServerDetailResponse) {
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

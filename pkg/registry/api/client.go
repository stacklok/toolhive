// Package api provides client functionality for interacting with MCP Registry API endpoints
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/networking"
)

// Client represents an MCP Registry API client
type Client interface {
	// GetServer retrieves a single server by its reverse-DNS name
	GetServer(ctx context.Context, name string) (*v0.ServerJSON, error)

	// ListServers retrieves all servers with automatic pagination handling
	ListServers(ctx context.Context, opts *ListOptions) ([]*v0.ServerJSON, error)

	// SearchServers searches for servers matching the query string
	SearchServers(ctx context.Context, query string) ([]*v0.ServerJSON, error)

	// ValidateEndpoint validates that the endpoint implements the MCP Registry API
	ValidateEndpoint(ctx context.Context) error
}

// ListOptions contains options for listing servers
type ListOptions struct {
	// Limit is the maximum number of servers to retrieve per page (default: 100)
	Limit int

	// UpdatedSince filters servers updated after this RFC3339 timestamp
	UpdatedSince string

	// Version filters servers by version (e.g., "latest")
	Version string
}

// mcpRegistryClient implements the Client interface for MCP Registry v0.1 API
type mcpRegistryClient struct {
	baseURL        string
	httpClient     *http.Client
	allowPrivateIp bool
}

// NewClient creates a new MCP Registry API client
func NewClient(baseURL string, allowPrivateIp bool) (Client, error) {
	// Build HTTP client with security controls
	httpClient, err := networking.NewHttpClientBuilder().
		WithPrivateIPs(allowPrivateIp).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}

	// Ensure base URL doesn't have trailing slash
	if baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}

	return &mcpRegistryClient{
		baseURL:        baseURL,
		httpClient:     httpClient,
		allowPrivateIp: allowPrivateIp,
	}, nil
}

// GetServer retrieves a single server by its reverse-DNS name
func (c *mcpRegistryClient) GetServer(ctx context.Context, name string) (*v0.ServerJSON, error) {
	// URL encode the server name to handle special characters
	encodedName := url.PathEscape(name)
	endpoint := fmt.Sprintf("%s/v0/servers/%s", c.baseURL, encodedName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch server %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var serverResp v0.ServerResponse
	if err := json.NewDecoder(resp.Body).Decode(&serverResp); err != nil {
		return nil, fmt.Errorf("failed to decode server response: %w", err)
	}

	return &serverResp.Server, nil
}

// ListServers retrieves all servers with automatic pagination handling
func (c *mcpRegistryClient) ListServers(ctx context.Context, opts *ListOptions) ([]*v0.ServerJSON, error) {
	if opts == nil {
		opts = &ListOptions{Limit: 100}
	}
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	var allServers []*v0.ServerJSON
	cursor := ""

	// Pagination loop - continue until no more cursors
	for {
		servers, nextCursor, err := c.fetchServersPage(ctx, cursor, opts)
		if err != nil {
			return nil, err
		}

		allServers = append(allServers, servers...)

		// Check if we have more pages
		if nextCursor == "" {
			break
		}

		cursor = nextCursor

		// Safety limit: prevent infinite loops
		if len(allServers) > 10000 {
			return nil, fmt.Errorf("exceeded maximum server limit (10000)")
		}
	}

	return allServers, nil
}

// fetchServersPage fetches a single page of servers
func (c *mcpRegistryClient) fetchServersPage(ctx context.Context, cursor string, opts *ListOptions) ([]*v0.ServerJSON, string, error) {
	endpoint := fmt.Sprintf("%s/v0/servers", c.baseURL)

	// Build query parameters
	params := url.Values{}
	if cursor != "" {
		params.Add("cursor", cursor)
	}
	if opts.Limit > 0 {
		params.Add("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.UpdatedSince != "" {
		params.Add("updated_since", opts.UpdatedSince)
	}
	if opts.Version != "" {
		params.Add("version", opts.Version)
	}

	if len(params) > 0 {
		endpoint = fmt.Sprintf("%s?%s", endpoint, params.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch servers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var listResp v0.ServerListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, "", fmt.Errorf("failed to decode list response: %w", err)
	}

	// Extract ServerJSON from ServerResponse entries
	servers := make([]*v0.ServerJSON, len(listResp.Servers))
	for i, serverResp := range listResp.Servers {
		servers[i] = &serverResp.Server
	}

	return servers, listResp.Metadata.NextCursor, nil
}

// SearchServers searches for servers matching the query string
func (c *mcpRegistryClient) SearchServers(ctx context.Context, query string) ([]*v0.ServerJSON, error) {
	endpoint := fmt.Sprintf("%s/v0/servers?search=%s", c.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search servers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var listResp v0.ServerListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	// Extract ServerJSON from ServerResponse entries
	servers := make([]*v0.ServerJSON, len(listResp.Servers))
	for i, serverResp := range listResp.Servers {
		servers[i] = &serverResp.Server
	}

	return servers, nil
}

// ValidateEndpoint validates that the endpoint implements the MCP Registry API
// by checking for the presence of /openapi.yaml with correct version and description
func (c *mcpRegistryClient) ValidateEndpoint(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/openapi.yaml", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch /openapi.yaml: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/openapi.yaml not found (status %d) - not a valid MCP Registry API", resp.StatusCode)
	}

	// Parse the OpenAPI spec
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read /openapi.yaml: %w", err)
	}

	var openapiSpec map[string]interface{}
	if err := yaml.Unmarshal(data, &openapiSpec); err != nil {
		return fmt.Errorf("failed to parse /openapi.yaml: %w", err)
	}

	// Check for 'info' section
	info, ok := openapiSpec["info"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("/openapi.yaml missing 'info' section")
	}

	// Check version
	version, ok := info["version"].(string)
	if !ok {
		return fmt.Errorf("/openapi.yaml info section missing 'version' field")
	}

	// MCP Registry API should be version 1.0.0
	if version != "1.0.0" {
		return fmt.Errorf("/openapi.yaml version is %s, expected 1.0.0", version)
	}

	// Check description contains GitHub URL
	description, ok := info["description"].(string)
	if !ok {
		return fmt.Errorf("/openapi.yaml info section missing 'description' field")
	}

	expectedGitHubURL := "https://github.com/modelcontextprotocol/registry"
	if !contains(description, expectedGitHubURL) {
		return fmt.Errorf("/openapi.yaml description does not contain expected GitHub URL: %s", expectedGitHubURL)
	}

	return nil
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsRec(s, substr))
}

func containsRec(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

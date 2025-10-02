package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RFC9728AuthInfo represents the OAuth Protected Resource metadata as defined in RFC 9728
type RFC9728AuthInfo struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	JWKSURI                string   `json:"jwks_uri"`
	ScopesSupported        []string `json:"scopes_supported"`
}

// FetchResourceMetadata fetches OAuth Protected Resource metadata as specified in RFC 9728
func FetchResourceMetadata(ctx context.Context, metadataURL string) (*RFC9728AuthInfo, error) {
	if metadataURL == "" {
		return nil, fmt.Errorf("metadata URL is empty")
	}

	// Validate URL
	parsedURL, err := url.Parse(metadataURL)
	if err != nil {
		return nil, fmt.Errorf("invalid metadata URL: %w", err)
	}

	// RFC 9728: Must use HTTPS (except for localhost in development)
	if parsedURL.Scheme != "https" && parsedURL.Hostname() != "localhost" && parsedURL.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("metadata URL must use HTTPS: %s", metadataURL)
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: DefaultHTTPTimeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata request failed with status %d", resp.StatusCode)
	}

	// Check content type
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("unexpected content type: %s", contentType)
	}

	// Parse the metadata
	const maxResponseSize = 1024 * 1024 // 1MB limit
	var metadata RFC9728AuthInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	// RFC 9728 Section 3.3: Validate that the resource value matches
	// For now we just check it's not empty
	if metadata.Resource == "" {
		return nil, fmt.Errorf("metadata missing required 'resource' field")
	}

	return &metadata, nil
}

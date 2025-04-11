package updates

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// VersionClient is an interface for calling the update service API.
type VersionClient interface {
	GetLatestVersion(instanceID string, currentVersion string) (string, error)
}

// NewVersionClient creates a new instance of VersionClient.
func NewVersionClient() VersionClient {
	return &defaultVersionClient{
		versionEndpoint: defaultVersionAPI,
	}
}

type defaultVersionClient struct {
	versionEndpoint string
}

const (
	instanceIDHeader  = "X-Instance-ID"
	userAgentHeader   = "User-Agent"
	defaultVersionAPI = "https://updates.codegate.ai/api/v1/version"
)

// GetLatestVersion sends a GET request to the update API endpoint and returns the version from the response.
// It returns an error if the request fails or if the response status code is not 200.
func (d *defaultVersionClient) GetLatestVersion(instanceID string, currentVersion string) (string, error) {
	// Create a new request
	req, err := http.NewRequest(http.MethodGet, d.versionEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	userAgent := fmt.Sprintf("toolhive/%s", currentVersion)
	// Add `dev` to the user agent for Stacklok devs.
	if os.Getenv("TOOLHIVE_DEV") != "" {
		userAgent += " dev"
	}
	req.Header.Set(instanceIDHeader, instanceID)
	req.Header.Set(userAgentHeader, userAgent)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request to update API: %w", err)
	}
	defer resp.Body.Close()

	// Check if status code is 200
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update API returned non-200 status code: %d", resp.StatusCode)
	}

	// Read and parse the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse JSON response
	var response struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return response.Version, nil
}

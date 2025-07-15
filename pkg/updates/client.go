package updates

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/versions"
)

// VersionClient is an interface for calling the update service API.
type VersionClient interface {
	GetLatestVersion(instanceID string, currentVersion string) (string, error)
}

// NewVersionClient creates a new instance of VersionClient.
func NewVersionClient() VersionClient {
	return NewVersionClientForComponent("CLI", "")
}

// NewVersionClientForComponent creates a new instance of VersionClient for a specific component.
func NewVersionClientForComponent(component, version string) VersionClient {
	return &defaultVersionClient{
		versionEndpoint: defaultVersionAPI,
		component:       component,
		customVersion:   version,
	}
}

type defaultVersionClient struct {
	versionEndpoint string
	component       string
	customVersion   string
}

const (
	instanceIDHeader  = "X-Instance-ID"
	userAgentHeader   = "User-Agent"
	defaultVersionAPI = "https://updates.codegate.ai/api/v1/version"
	defaultTimeout    = 3 * time.Second
)

// GetLatestVersion sends a GET request to the update API endpoint and returns the version from the response.
// It returns an error if the request fails or if the response status code is not 200.
func (d *defaultVersionClient) GetLatestVersion(instanceID string, currentVersion string) (string, error) {
	// Create a new request
	req, err := http.NewRequest(http.MethodGet, d.versionEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Generate user agent in format: toolhive/[component] [vXX] [release/local_build] ([operating_system])

	// Use custom version if set, otherwise use the passed currentVersion
	version := currentVersion
	if d.customVersion != "" {
		version = d.customVersion
	}

	// Format version with 'v' prefix if it doesn't start with 'v'
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	buildType := "local_build"
	if versions.BuildType == "release" {
		buildType = "release"
	}

	// Get platform info as OperatingSystem/Architecture
	platform := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	// Format: toolhive/[component] [vXX] [release/local_build] ([operating_system])
	userAgent := fmt.Sprintf("toolhive/%s %s %s (%s)", d.component, version, buildType, platform)

	req.Header.Set(instanceIDHeader, instanceID)
	req.Header.Set(userAgentHeader, userAgent)

	// Send the request with a reasonable timeout
	client := &http.Client{
		Timeout: defaultTimeout,
	}
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

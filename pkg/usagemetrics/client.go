package usagemetrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/versions"
)

const (
	defaultEndpoint   = "https://updates.stacklok.com/api/v1/toolcount"
	defaultTimeout    = 5 * time.Second
	instanceIDHeader  = "X-Instance-ID"
	anonymousIDHeader = "X-Anonymous-Id"
	userAgentHeader   = "User-Agent"
)

// Client sends usage metrics to the API
type Client struct {
	endpoint string
	client   *http.Client
}

// NewClient creates a new metrics client
func NewClient(endpoint string) *Client {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	return &Client{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// SendMetrics sends the metrics record to the API
func (c *Client) SendMetrics(instanceID string, record MetricRecord) error {
	// Get the anonymous ID from the updates file (shared across all ToolHive components)
	// Use TryGetInstanceID to avoid generating a new ID if it doesn't exist
	anonymousID, err := updates.TryGetAnonymousID()
	if err != nil {
		return fmt.Errorf("failed to get anonymous ID: %w", err)
	}

	// Skip sending if anonymous ID is not initialized yet
	// This is a failsafe - the ID should always exist
	if anonymousID == "" {
		logger.Debugf("Skipping metrics send - anonymous ID not yet initialized")
		return nil
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal metrics record: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(instanceIDHeader, instanceID)   // Proxy instance ID
	req.Header.Set(anonymousIDHeader, anonymousID) // User anonymous ID from updates.json
	req.Header.Set(userAgentHeader, generateUserAgent())

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API returned non-2xx status code: %d", resp.StatusCode)
	}

	return nil
}

// generateUserAgent creates the user agent string
// Format: toolhive/[local|operator] [version] [build_type] (os/arch)
func generateUserAgent() string {
	// Determine component type
	envType := "local"
	if rt.IsKubernetesRuntime() {
		envType = "operator"
	}

	version := versions.GetVersionInfo().Version
	if version != "" && version[0] != 'v' {
		version = "v" + version
	}

	// Get build type, buildType is set at building time
	buildType := versions.BuildType
	if buildType == "" {
		buildType = "local_build"
	}

	platform := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	return fmt.Sprintf("toolhive/%s %s %s (%s)", envType, version, buildType, platform)
}

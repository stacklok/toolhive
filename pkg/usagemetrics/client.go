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
	endpoint    string
	client      *http.Client
	anonymousID string
}

// NewClient creates a new metrics client
func NewClient(endpoint string) *Client {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	// Get anonymous ID once at client creation and cache for process duration
	anonymousID, err := updates.TryGetAnonymousID()
	if err != nil {
		logger.Debugf("Failed to get anonymous ID during client creation: %v", err)
	}

	return &Client{
		endpoint:    endpoint,
		anonymousID: anonymousID,
		client: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// SendMetrics sends the metrics record to the API
func (c *Client) SendMetrics(instanceID string, record MetricRecord) error {
	// Use cached anonymous ID (set at client creation)
	// Skip sending if anonymous ID is not initialized
	if c.anonymousID == "" {
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
	req.Header.Set(instanceIDHeader, instanceID)     // Proxy instance ID
	req.Header.Set(anonymousIDHeader, c.anonymousID) // User anonymous ID
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

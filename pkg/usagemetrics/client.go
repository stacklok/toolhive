// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/stacklok/toolhive-core/env"
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
	// For operator-deployed proxies without filesystem access, anonymous_id will be empty,
	// but we still send metrics with a default value.
	anonymousID := c.anonymousID
	if anonymousID == "" {
		// Only use default for operator-deployed proxies (detected via K8s env vars)
		if rt.IsKubernetesRuntimeWithEnv(&env.OSReader{}) {
			anonymousID = "operator-proxy"
		} else {
			// For local deployments, empty anonymous_id means file doesn't exist yet - skip sending
			return nil
		}
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
	req.Header.Set(anonymousIDHeader, anonymousID) // User anonymous ID (or default for operator)
	req.Header.Set(userAgentHeader, generateUserAgent())

	resp, err := c.client.Do(req) // #nosec G704 -- URL is the hardcoded usage metrics endpoint
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Debugf("Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API returned non-2xx status code: %d", resp.StatusCode)
	}

	return nil
}

// generateUserAgent creates the user agent string using the OS environment
// Format: toolhive/[local|operator] [version] [build_type] (os/arch)
func generateUserAgent() string {
	return generateUserAgentWithEnv(&env.OSReader{})
}

// generateUserAgentWithEnv creates the user agent string using the provided environment reader
// Format: toolhive/[local|operator] [version] [build_type] (os/arch)
func generateUserAgentWithEnv(envReader env.Reader) string {
	// Determine component type
	envType := "local"
	if rt.IsKubernetesRuntimeWithEnv(envReader) {
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

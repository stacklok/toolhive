// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/url"
	"time"
)

const (
	// defaultTimeout is the default HTTP request timeout in seconds.
	defaultTimeout = 30

	// decisionPath is the PDP decision endpoint path.
	decisionPath = "/decision"
)

// DecisionResponse represents the response from the PDP decision endpoint.
type DecisionResponse struct {
	Allow bool `json:"allow"`
}

// Client handles HTTP communication with the PDP server.
type Client struct {
	baseURL    string
	httpClient *nethttp.Client
}

// NewClient creates a new HTTP client for PDP communication.
func NewClient(config *ConnectionConfig) (*Client, error) {
	slog.Debug("Creating new HTTP client", "config", config)

	if config == nil {
		return nil, fmt.Errorf("HTTP configuration is required")
	}

	if config.URL == "" {
		return nil, fmt.Errorf("HTTP URL is required")
	}

	// Validate URL
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("URL scheme must be http or https, got: %s", parsedURL.Scheme)
	}

	// Determine timeout
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	// Create HTTP client
	httpClient := &nethttp.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}

	// Only set custom transport when we need to override TLS settings
	// Otherwise use http.DefaultTransport which includes proxy support and proper defaults
	if config.InsecureSkipVerify {
		// Clone default transport and override TLS config
		transport := nethttp.DefaultTransport.(*nethttp.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // User explicitly requested insecure mode
		}
		httpClient.Transport = transport
	}

	return &Client{
		baseURL:    config.URL,
		httpClient: httpClient,
	}, nil
}

// Authorize sends an authorization request to the PDP server.
// It returns true if the request is authorized, false otherwise.
func (c *Client) Authorize(ctx context.Context, porc PORC, probe bool) (bool, error) {
	// Build the decision URL
	decisionURL, err := url.JoinPath(c.baseURL, decisionPath)
	if err != nil {
		return false, fmt.Errorf("failed to build decision URL: %w", err)
	}

	// Add probe parameter if specified (for PDPs that support debugging mode)
	if probe {
		u, err := url.Parse(decisionURL)
		if err != nil {
			return false, fmt.Errorf("failed to parse decision URL: %w", err)
		}
		q := u.Query()
		q.Set("probe", "true")
		u.RawQuery = q.Encode()
		decisionURL = u.String()
	}

	// Log authorization request without sensitive data
	// PORC can contain sensitive principal attributes and tool arguments, so we only log
	// high-level fields: operation and resource (subject if available)
	logSubject := "unknown"
	if principal, ok := porc["principal"].(map[string]interface{}); ok {
		if sub, ok := principal["sub"].(string); ok {
			logSubject = sub
		}
	}
	//nolint:gosec // G706: authorization metadata is safe for debug logging
	slog.Debug("HTTP PDP authorization",
		"url", decisionURL, "subject", logSubject,
		"operation", porc["operation"], "resource", porc["resource"])

	// Marshal PORC to JSON
	body, err := json.Marshal(porc)
	if err != nil {
		return false, fmt.Errorf("failed to marshal PORC: %w", err)
	}

	// Create HTTP request
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, decisionURL, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Send request
	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: URL is from server configuration, not user input
	if err != nil {
		return false, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check HTTP status
	if resp.StatusCode != nethttp.StatusOK {
		return false, fmt.Errorf("PDP server returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var decision DecisionResponse
	if err := json.Unmarshal(respBody, &decision); err != nil {
		return false, fmt.Errorf("failed to parse decision response: %w", err)
	}

	return decision.Allow, nil
}

// Close closes the HTTP client and releases resources.
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

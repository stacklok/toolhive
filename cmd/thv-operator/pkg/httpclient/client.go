// Package httpclient provides HTTP client functionality for API operations
package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultTimeout is the default timeout for HTTP requests
	DefaultTimeout = 30 * time.Second

	// UserAgent is the user agent string for HTTP requests
	UserAgent = "toolhive-operator/1.0"
)

// Client is an interface for HTTP operations
type Client interface {
	// Get performs an HTTP GET request and returns the response body
	Get(ctx context.Context, url string) ([]byte, error)
}

// DefaultClient is the default HTTP client implementation
type DefaultClient struct {
	client  *http.Client
	timeout time.Duration
}

// NewDefaultClient creates a new default HTTP client with the specified timeout
// If timeout is 0, uses DefaultTimeout
func NewDefaultClient(timeout time.Duration) Client {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &DefaultClient{
		client: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
	}
}

// Get performs an HTTP GET request
func (c *DefaultClient) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")

	// Execute request
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, NewHTTPError(resp.StatusCode, url, resp.Status)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return body, nil
}

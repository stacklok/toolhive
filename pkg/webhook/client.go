// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
)

// Client is an HTTP client for calling webhook endpoints.
type Client struct {
	httpClient  *http.Client
	config      Config
	hmacSecret  []byte
	webhookType Type
}

// NewClient creates a new webhook Client from the given configuration.
// The hmacSecret parameter is the resolved secret bytes for HMAC signing;
// pass nil if signing is not configured.
func NewClient(cfg Config, webhookType Type, hmacSecret []byte) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid webhook config: %w", err)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	transport, err := buildTransport(cfg.TLSConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP transport: %w", err)
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
		config:      cfg,
		hmacSecret:  hmacSecret,
		webhookType: webhookType,
	}, nil
}

// Call sends a request to a validating webhook and returns its response.
func (c *Client) Call(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to marshal request: %w", err))
	}

	respBody, err := c.doHTTPCall(ctx, body)
	if err != nil {
		return nil, err
	}

	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to unmarshal response: %w", err))
	}

	return &resp, nil
}

// CallMutating sends a request to a mutating webhook and returns its response.
func (c *Client) CallMutating(ctx context.Context, req *Request) (*MutatingResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to marshal request: %w", err))
	}

	respBody, err := c.doHTTPCall(ctx, body)
	if err != nil {
		return nil, err
	}

	var resp MutatingResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to unmarshal mutating response: %w", err))
	}

	return &resp, nil
}

// doHTTPCall performs the HTTP POST to the webhook endpoint, handling signing,
// error classification, and response size limiting.
func (c *Client) doHTTPCall(ctx context.Context, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(body))
	if err != nil {
		return nil, NewNetworkError(c.config.Name, fmt.Errorf("failed to create HTTP request: %w", err))
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Apply HMAC signing if configured.
	if len(c.hmacSecret) > 0 {
		timestamp := time.Now().Unix()
		signature := SignPayload(c.hmacSecret, timestamp, body)
		httpReq.Header.Set(SignatureHeader, signature)
		httpReq.Header.Set(TimestampHeader, strconv.FormatInt(timestamp, 10))
	}

	// #nosec G704 -- URL is validated in Config.Validate and we use ValidatingTransport for SSRF protection.
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, classifyError(c.config.Name, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Enforce response size limit.
	limitedReader := io.LimitReader(resp.Body, MaxResponseSize+1)
	respBody, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, NewNetworkError(c.config.Name, fmt.Errorf("failed to read response body: %w", err))
	}
	if int64(len(respBody)) > MaxResponseSize {
		return nil, NewInvalidResponseError(c.config.Name,
			fmt.Errorf("response body exceeds maximum size of %d bytes", MaxResponseSize))
	}

	// 5xx errors indicate webhook operational failures.
	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, NewNetworkError(c.config.Name,
			fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, truncateBody(respBody)))
	}

	// Non-200 responses (excluding 5xx handled above) are treated as invalid.
	if resp.StatusCode != http.StatusOK {
		return nil, NewInvalidResponseError(c.config.Name,
			fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, truncateBody(respBody)))
	}

	return respBody, nil
}

// buildTransport creates an http.RoundTripper with the specified TLS configuration,
// wrapped in a ValidatingTransport for security.
func buildTransport(tlsCfg *TLSConfig) (http.RoundTripper, error) {
	transport := &http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
	}

	if tlsCfg == nil {
		return transport, nil
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load CA bundle if provided.
	if tlsCfg.CABundlePath != "" {
		caCert, err := os.ReadFile(tlsCfg.CABundlePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA bundle: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate bundle")
		}
		tlsConfig.RootCAs = caCertPool
	}

	// Load client certificate for mTLS if provided.
	if tlsCfg.ClientCertPath != "" && tlsCfg.ClientKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(tlsCfg.ClientCertPath, tlsCfg.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if tlsCfg.InsecureSkipVerify {
		//#nosec G402 -- InsecureSkipVerify is intentionally user-configurable for development/testing only.
		tlsConfig.InsecureSkipVerify = true
	}

	transport.TLSClientConfig = tlsConfig
	return &networking.ValidatingTransport{
		Transport:         transport,
		InsecureAllowHTTP: false,
	}, nil
}

// classifyError examines an HTTP client error and returns an appropriately
// typed webhook error (TimeoutError or NetworkError).
func classifyError(webhookName string, err error) error {
	// Check for timeout errors (context deadline, net.Error timeout).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return NewTimeoutError(webhookName, err)
	}
	return NewNetworkError(webhookName, err)
}

// truncateBody returns a preview of the response body for error messages.
func truncateBody(body []byte) string {
	const maxPreview = 256
	if len(body) <= maxPreview {
		return string(body)
	}
	return string(body[:maxPreview]) + "..."
}

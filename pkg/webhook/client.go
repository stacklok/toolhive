// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
)

// allowPrivateIPsForTesting is a test-only escape hatch for the webhook SSRF
// dial-time guard (networking.ProtectedDialerControl, installed via
// networking.HttpClientBuilder.WithPrivateIPs). When true, buildHTTPClient
// allows dials to private, loopback, and link-local addresses so tests can
// talk to httptest servers, which always bind 127.0.0.1.
//
// It is an atomic.Bool so cross-goroutine writes from tests
// (SetAllowPrivateIPsForTesting / SetAllowPrivateIPsForTestMain) and
// cross-goroutine reads from the production client-build path are race-free,
// even if a future test introduces t.Parallel(). Production callers must not
// set this variable.
var allowPrivateIPsForTesting atomic.Bool

// Client is an HTTP client for calling webhook endpoints.
type Client struct {
	httpClient *http.Client
	config     Config
	hmacSecret []byte
	// TODO: webhookType will be used by a future Send() method that dispatches
	// to Call or CallMutating based on type. For now callers pick the method directly.
	webhookType Type
}

// NewClient creates a new webhook Client from the given configuration.
// The hmacSecret parameter is the resolved secret bytes for HMAC signing;
// pass nil if signing is not configured.
func NewClient(cfg Config, webhookType Type, hmacSecret []byte) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid webhook config: %w", err)
	}
	if cfg.HMACSecretRef != "" && len(hmacSecret) == 0 {
		return nil, fmt.Errorf("webhook %q has HMAC configured but resolved secret is empty", cfg.Name)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	httpClient, err := buildHTTPClient(cfg.TLSConfig, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}

	return &Client{
		httpClient:  httpClient,
		config:      cfg,
		hmacSecret:  hmacSecret,
		webhookType: webhookType,
	}, nil
}

// Call sends a request to a validating webhook and returns its response.
func (c *Client) Call(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to marshal request: %w", err), 0)
	}

	respBody, err := c.doHTTPCall(ctx, body)
	if err != nil {
		return nil, err
	}

	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to unmarshal response: %w", err), 0)
	}

	return &resp, nil
}

// CallMutating sends a request to a mutating webhook and returns its response.
func (c *Client) CallMutating(ctx context.Context, req *Request) (*MutatingResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to marshal request: %w", err), 0)
	}

	respBody, err := c.doHTTPCall(ctx, body)
	if err != nil {
		return nil, err
	}

	var resp MutatingResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, NewInvalidResponseError(c.config.Name, fmt.Errorf("failed to unmarshal mutating response: %w", err), 0)
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

	hmacSecret, err := c.hmacSecretForRequest(ctx)
	if err != nil {
		return nil, NewNetworkError(c.config.Name, fmt.Errorf("failed to resolve HMAC secret: %w", err))
	}

	// Apply HMAC signing if configured.
	if len(hmacSecret) > 0 {
		timestamp := time.Now().Unix()
		signature := SignPayload(hmacSecret, timestamp, body)
		httpReq.Header.Set(SignatureHeader, signature)
		httpReq.Header.Set(TimestampHeader, strconv.FormatInt(timestamp, 10))
	}

	// #nosec G704 -- URL is validated in Config.Validate; the inner transport's
	// dialer rejects private/loopback/link-local addresses (SSRF guard), and
	// ValidatingTransport additionally enforces HTTPS unless InsecureAllowHTTP
	// is set for the configured TLS profile.
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
			fmt.Errorf("response body exceeds maximum size of %d bytes", MaxResponseSize), 0)
	}

	// 5xx errors indicate webhook operational failures.
	if resp.StatusCode >= http.StatusInternalServerError {
		// Body preview is logged at debug level so operators can troubleshoot,
		// but is kept out of the returned error chain to avoid surfacing
		// potentially sensitive bytes (e.g. from an internal service reached
		// via a misconfigured URL) into higher-level error logs.
		slog.Debug("webhook returned server error",
			"webhook", c.config.Name,
			"url", c.config.URL,
			"status_code", resp.StatusCode,
			"body_preview", truncateBody(respBody),
		)
		return nil, NewNetworkError(c.config.Name,
			fmt.Errorf("webhook returned HTTP %d", resp.StatusCode))
	}

	// Non-200 responses (excluding 5xx handled above) are treated as invalid.
	// The StatusCode is surfaced so callers can distinguish HTTP 422 (RFC always-deny)
	// from other non-2xx codes that may follow the failure policy.
	if resp.StatusCode != http.StatusOK {
		slog.Debug("webhook returned non-2xx response",
			"webhook", c.config.Name,
			"url", c.config.URL,
			"status_code", resp.StatusCode,
			"body_preview", truncateBody(respBody),
		)
		return nil, NewInvalidResponseError(c.config.Name,
			fmt.Errorf("webhook returned HTTP %d", resp.StatusCode),
			resp.StatusCode)
	}

	return respBody, nil
}

func (c *Client) hmacSecretForRequest(ctx context.Context) ([]byte, error) {
	if c.config.HMACSecretRef == "" {
		return c.hmacSecret, nil
	}
	if !filepath.IsAbs(c.config.HMACSecretRef) {
		return c.hmacSecret, nil
	}

	secret, err := ResolveSecret(ctx, c.config.HMACSecretRef)
	if err != nil {
		return nil, err
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("resolved HMAC secret is empty")
	}
	return secret, nil
}

// buildHTTPClient creates an *http.Client for the given webhook TLS configuration
// and timeout. The dial-time SSRF guard, HTTPS enforcement, CA bundle, and mTLS
// wiring are all delegated to networking.HttpClientBuilder so the webhook client
// does not maintain its own copy of this logic.
func buildHTTPClient(tlsCfg *TLSConfig, timeout time.Duration) (*http.Client, error) {
	builder := networking.NewHttpClientBuilder().
		WithTimeout(timeout).
		WithPrivateIPs(allowPrivateIPsForTesting.Load())

	if tlsCfg != nil {
		if tlsCfg.CABundlePath != "" {
			builder = builder.WithCABundle(tlsCfg.CABundlePath)
		}
		if tlsCfg.ClientCertPath != "" || tlsCfg.ClientKeyPath != "" {
			builder = builder.WithClientCert(tlsCfg.ClientCertPath, tlsCfg.ClientKeyPath)
		}
		if tlsCfg.InsecureSkipVerify {
			// InsecureSkipVerify also allows plaintext HTTP (e.g. for in-cluster
			// endpoints), mirroring the pre-existing webhook behavior.
			builder = builder.WithInsecureSkipVerify(true).WithInsecureAllowHTTP(true)
		}
	}

	return builder.Build()
}

// classifyError examines an HTTP client error and returns an appropriately
// typed webhook error (TimeoutError or NetworkError).
func classifyError(webhookName string, err error) error {
	// Check for context cancellation/deadline first, as these may not wrap net.Error.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return NewTimeoutError(webhookName, err)
	}
	// Check for timeout errors via net.Error interface (e.g., dial timeout).
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

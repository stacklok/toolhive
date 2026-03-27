// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	// healthTimeout is the maximum time to wait for a health check response.
	healthTimeout = 5 * time.Second

	// NonceHeader is the HTTP header used to return the server nonce.
	NonceHeader = "X-Toolhive-Nonce"
)

// CheckHealth verifies that a server at the given URL is healthy and optionally
// matches the expected nonce. It supports http:// and unix:// URL schemes.
func CheckHealth(ctx context.Context, serverURL string, expectedNonce string) error {
	client, requestURL, err := buildHealthClient(serverURL)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create health request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected health status: %d", resp.StatusCode)
	}

	if expectedNonce != "" {
		actualNonce := resp.Header.Get(NonceHeader)
		if subtle.ConstantTimeCompare([]byte(actualNonce), []byte(expectedNonce)) != 1 {
			return fmt.Errorf("nonce mismatch: expected %q, got %q", expectedNonce, actualNonce)
		}
	}

	return nil
}

// buildHealthClient returns an HTTP client and request URL appropriate for
// the given server URL scheme.
func buildHealthClient(serverURL string) (*http.Client, string, error) {
	client, baseURL, err := HTTPClientForURL(serverURL)
	if err != nil {
		return nil, "", err
	}
	healthURL, err := url.JoinPath(baseURL, "health")
	if err != nil {
		return nil, "", fmt.Errorf("failed to build health URL: %w", err)
	}
	return client, healthURL, nil
}

// HTTPClientForURL returns an HTTP client configured for the given server URL
// and the base URL to use for requests. For unix:// URLs it creates a client
// with a Unix socket transport and returns "http://localhost" as the base URL.
// For http:// URLs it validates the host is a loopback address and returns a
// default client. The returned client has no timeout set; callers should apply
// their own timeout via context or client.Timeout.
func HTTPClientForURL(serverURL string) (*http.Client, string, error) {
	switch {
	case strings.HasPrefix(serverURL, "unix://"):
		socketPath, err := ParseUnixSocketPath(serverURL)
		if err != nil {
			return nil, "", err
		}
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		}
		return client, "http://localhost", nil

	case strings.HasPrefix(serverURL, "http://"):
		if err := ValidateLoopbackURL(serverURL); err != nil {
			return nil, "", err
		}
		return &http.Client{}, serverURL, nil

	default:
		return nil, "", fmt.Errorf("unsupported URL scheme: %s", serverURL)
	}
}

// ValidateLoopbackURL checks that an http:// URL points to a loopback address.
func ValidateLoopbackURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Hostname()

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid host in URL: %s", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("refusing health check to non-loopback address: %s", host)
	}
	return nil
}

// ParseUnixSocketPath extracts and validates the socket path from a unix:// URL.
func ParseUnixSocketPath(rawURL string) (string, error) {
	path := strings.TrimPrefix(rawURL, "unix://")
	if path == "" {
		return "", fmt.Errorf("empty unix socket path")
	}

	// Check for traversal before Clean resolves it away
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("unix socket path must not contain '..': %s", path)
	}

	path = filepath.Clean(path)

	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("unix socket path must be absolute: %s", path)
	}

	return path, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

const maxErrorBodySize = 4096

// ErrRegistryUnauthorized is a sentinel error for 401/403 responses from registry APIs.
var ErrRegistryUnauthorized = errors.New("registry authentication failed")

// RegistryHTTPError represents an HTTP error from a registry API endpoint.
type RegistryHTTPError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *RegistryHTTPError) Error() string {
	return fmt.Sprintf("registry API returned status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// Unwrap returns ErrRegistryUnauthorized for 401/403 status codes,
// allowing callers to use errors.Is(err, ErrRegistryUnauthorized).
func (e *RegistryHTTPError) Unwrap() error {
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		return ErrRegistryUnauthorized
	}
	return nil
}

// buildHTTPClient creates an HTTP client with security controls and optional auth.
// If allowPrivateIp is true, HTTP (non-HTTPS) is also allowed for localhost testing.
func buildHTTPClient(allowPrivateIp bool, tokenSource auth.TokenSource) (*http.Client, error) {
	builder := networking.NewHttpClientBuilder().WithPrivateIPs(allowPrivateIp)
	if allowPrivateIp {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	httpClient, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}
	httpClient.Transport = auth.WrapTransport(httpClient.Transport, tokenSource)
	return httpClient, nil
}

// newRegistryHTTPError reads the response body (limited) and returns a RegistryHTTPError.
func newRegistryHTTPError(resp *http.Response) *RegistryHTTPError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	return &RegistryHTTPError{
		StatusCode: resp.StatusCode,
		Body:       string(body),
		URL:        resp.Request.URL.String(),
	}
}

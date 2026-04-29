// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// defaultURLFetchSizeLimit caps the body URL registries will read from a
// remote endpoint. 32 MiB comfortably fits real-world registries while
// preventing a malicious or misconfigured server from exhausting memory.
const defaultURLFetchSizeLimit = 32 * 1024 * 1024

// NewURLRegistry constructs a Registry by fetching an upstream-format JSON
// document from url. The body is read once at construction; subsequent
// remote changes require restart.
//
// httpClient may be nil, in which case http.DefaultClient is used. The
// caller is responsible for setting timeouts via ctx or a custom client.
func NewURLRegistry(ctx context.Context, name, url string, httpClient *http.Client) (Registry, error) {
	if url == "" {
		return nil, fmt.Errorf("url registry %q: url must not be empty", name)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("url registry %q: build request: %w", name, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, &UnavailableError{URL: url, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &UnavailableError{
			URL: url,
			Err: fmt.Errorf("unexpected status %d", resp.StatusCode),
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, defaultURLFetchSizeLimit+1))
	if err != nil {
		return nil, &UnavailableError{URL: url, Err: fmt.Errorf("read body: %w", err)}
	}
	if len(data) > defaultURLFetchSizeLimit {
		return nil, fmt.Errorf("url registry %q: response exceeds %d-byte size limit", name, defaultURLFetchSizeLimit)
	}

	entries, err := parseEntries(data)
	if err != nil {
		return nil, fmt.Errorf("url registry %q: %w", name, err)
	}
	return NewStore(name, entries)
}

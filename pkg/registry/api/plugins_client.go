// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/auth"
	"github.com/stacklok/toolhive/pkg/versions"
)

const pluginsBasePath = "/v0.1/x/dev.toolhive/plugins"

// PluginsListOptions contains options for listing plugins.
type PluginsListOptions struct {
	// Search is an optional search query to filter plugins.
	Search string
	// Limit is the maximum number of plugins per page (default: 100).
	Limit int
	// Cursor is the pagination cursor for fetching the next page.
	Cursor string
}

// PluginsListResult contains a page of plugins and pagination info.
type PluginsListResult struct {
	Plugins    []*thvregistry.Plugin
	NextCursor string
}

// PluginsClient provides access to the ToolHive Plugins extension API.
type PluginsClient interface {
	// GetPlugin retrieves a plugin by namespace and name (latest version).
	GetPlugin(ctx context.Context, namespace, name string) (*thvregistry.Plugin, error)
	// GetPluginVersion retrieves a specific version of a plugin.
	GetPluginVersion(ctx context.Context, namespace, name, version string) (*thvregistry.Plugin, error)
	// ListPlugins retrieves plugins with optional filtering and pagination.
	ListPlugins(ctx context.Context, opts *PluginsListOptions) (*PluginsListResult, error)
	// SearchPlugins searches for plugins matching the query (auto-paginates).
	SearchPlugins(ctx context.Context, query string) (*PluginsListResult, error)
	// ListPluginVersions lists all versions of a specific plugin.
	ListPluginVersions(ctx context.Context, namespace, name string) (*PluginsListResult, error)
}

// NewPluginsClient creates a new ToolHive Plugins extension API client.
// If tokenSource is non-nil, the HTTP client transport will be wrapped to inject
// Bearer tokens into all requests.
func NewPluginsClient(baseURL string, allowPrivateIp bool, tokenSource auth.TokenSource) (PluginsClient, error) {
	httpClient, err := buildHTTPClient(allowPrivateIp, tokenSource)
	if err != nil {
		return nil, err
	}

	// Ensure base URL doesn't have trailing slash
	baseURL = strings.TrimRight(baseURL, "/")

	return &mcpPluginsClient{
		baseURL:    baseURL,
		httpClient: httpClient,
		userAgent:  versions.GetUserAgent(),
	}, nil
}

// GetPlugin retrieves a plugin by namespace and name (latest version).
func (c *mcpPluginsClient) GetPlugin(ctx context.Context, namespace, name string) (*thvregistry.Plugin, error) {
	endpoint, err := url.JoinPath(c.baseURL, pluginsBasePath, url.PathEscape(namespace), url.PathEscape(name))
	if err != nil {
		return nil, fmt.Errorf("failed to build plugins URL: %w", err)
	}

	var plugin thvregistry.Plugin
	if err := c.doPluginsGet(ctx, endpoint, &plugin); err != nil {
		return nil, err
	}
	return &plugin, nil
}

// GetPluginVersion retrieves a specific version of a plugin.
func (c *mcpPluginsClient) GetPluginVersion(ctx context.Context, namespace, name, version string) (*thvregistry.Plugin, error) {
	endpoint, err := url.JoinPath(c.baseURL, pluginsBasePath,
		url.PathEscape(namespace), url.PathEscape(name),
		"versions", url.PathEscape(version))
	if err != nil {
		return nil, fmt.Errorf("failed to build plugins URL: %w", err)
	}

	var plugin thvregistry.Plugin
	if err := c.doPluginsGet(ctx, endpoint, &plugin); err != nil {
		return nil, err
	}
	return &plugin, nil
}

// ListPlugins retrieves plugins with optional filtering and pagination.
// It auto-paginates through all available pages, concatenating results.
func (c *mcpPluginsClient) ListPlugins(ctx context.Context, opts *PluginsListOptions) (*PluginsListResult, error) {
	if opts == nil {
		opts = &PluginsListOptions{}
	}
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	var allPlugins []*thvregistry.Plugin
	cursor := opts.Cursor

	// Pagination loop - continue until no more cursors
	for {
		page, nextCursor, err := c.fetchPluginsPage(ctx, cursor, opts)
		if err != nil {
			return nil, err
		}

		allPlugins = append(allPlugins, page...)

		// Check if we have more pages
		if nextCursor == "" {
			break
		}

		cursor = nextCursor

		// Safety limit: prevent infinite loops
		if len(allPlugins) > 10000 {
			return nil, fmt.Errorf("exceeded maximum plugins limit (10000)")
		}
	}

	return &PluginsListResult{
		Plugins: allPlugins,
	}, nil
}

// SearchPlugins searches for plugins matching the query.
// It auto-paginates through all available pages, concatenating results —
// mirroring ListPlugins. This prevents wrong-publisher installs when
// same-named plugins across namespaces span pages.
func (c *mcpPluginsClient) SearchPlugins(ctx context.Context, query string) (*PluginsListResult, error) {
	opts := &PluginsListOptions{Search: query, Limit: 100}

	var allPlugins []*thvregistry.Plugin
	cursor := ""

	for {
		page, nextCursor, err := c.fetchPluginsPage(ctx, cursor, opts)
		if err != nil {
			return nil, err
		}

		allPlugins = append(allPlugins, page...)

		if nextCursor == "" {
			break
		}

		cursor = nextCursor

		// Safety limit: prevent infinite loops (mirrors ListPlugins).
		if len(allPlugins) > 10000 {
			return nil, fmt.Errorf("exceeded maximum plugins limit (10000)")
		}
	}

	return &PluginsListResult{
		Plugins: allPlugins,
	}, nil
}

// ListPluginVersions lists all versions of a specific plugin.
func (c *mcpPluginsClient) ListPluginVersions(ctx context.Context, namespace, name string) (*PluginsListResult, error) {
	endpoint, err := url.JoinPath(c.baseURL, pluginsBasePath, url.PathEscape(namespace), url.PathEscape(name), "versions")
	if err != nil {
		return nil, fmt.Errorf("failed to build plugins URL: %w", err)
	}

	var listResp pluginsListResponse
	if err := c.doPluginsGet(ctx, endpoint, &listResp); err != nil {
		return nil, err
	}

	return &PluginsListResult{
		Plugins:    listResp.Plugins,
		NextCursor: listResp.Metadata.NextCursor,
	}, nil
}

// mcpPluginsClient implements the PluginsClient interface.
type mcpPluginsClient struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
}

// pluginsListResponse is the wire format for list/search responses.
type pluginsListResponse struct {
	Plugins  []*thvregistry.Plugin `json:"plugins"`
	Metadata struct {
		Count      int    `json:"count"`
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// doPluginsGet performs an HTTP GET request and decodes the JSON response into dest.
func (c *mcpPluginsClient) doPluginsGet(ctx context.Context, endpoint string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: URL from configured registry
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return newRegistryHTTPError(resp)
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

// fetchPluginsPage fetches a single page of plugins.
func (c *mcpPluginsClient) fetchPluginsPage(
	ctx context.Context, cursor string, opts *PluginsListOptions,
) ([]*thvregistry.Plugin, string, error) {
	params := url.Values{}
	if cursor != "" {
		params.Add("cursor", cursor)
	}
	if opts.Limit > 0 {
		params.Add("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Search != "" {
		params.Add("search", opts.Search)
	}

	basePath, err := url.JoinPath(c.baseURL, pluginsBasePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build plugins URL: %w", err)
	}
	endpoint := func() string {
		if len(params) > 0 {
			return basePath + "?" + params.Encode()
		}
		return basePath
	}()

	var listResp pluginsListResponse
	if err := c.doPluginsGet(ctx, endpoint, &listResp); err != nil {
		return nil, "", err
	}

	return listResp.Plugins, listResp.Metadata.NextCursor, nil
}

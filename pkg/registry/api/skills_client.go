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

const skillsBasePath = "/v0.1/x/dev.toolhive/skills"

// SkillsListOptions contains options for listing skills.
type SkillsListOptions struct {
	// Search is an optional search query to filter skills.
	Search string
	// Limit is the maximum number of skills per page (default: 100).
	Limit int
	// Cursor is the pagination cursor for fetching the next page.
	Cursor string
}

// SkillsListResult contains a page of skills and pagination info.
type SkillsListResult struct {
	Skills     []*thvregistry.Skill
	NextCursor string
}

// SkillsClient provides access to the ToolHive Skills extension API.
type SkillsClient interface {
	// GetSkill retrieves a skill by namespace and name (latest version).
	GetSkill(ctx context.Context, namespace, name string) (*thvregistry.Skill, error)
	// GetSkillVersion retrieves a specific version of a skill.
	GetSkillVersion(ctx context.Context, namespace, name, version string) (*thvregistry.Skill, error)
	// ListSkills retrieves skills with optional filtering and pagination.
	ListSkills(ctx context.Context, opts *SkillsListOptions) (*SkillsListResult, error)
	// SearchSkills searches for skills matching the query (single page, no auto-pagination).
	SearchSkills(ctx context.Context, query string) (*SkillsListResult, error)
	// ListSkillVersions lists all versions of a specific skill.
	ListSkillVersions(ctx context.Context, namespace, name string) (*SkillsListResult, error)
}

// NewSkillsClient creates a new ToolHive Skills extension API client.
// If tokenSource is non-nil, the HTTP client transport will be wrapped to inject
// Bearer tokens into all requests.
func NewSkillsClient(baseURL string, allowPrivateIp bool, tokenSource auth.TokenSource) (SkillsClient, error) {
	httpClient, err := buildHTTPClient(allowPrivateIp, tokenSource)
	if err != nil {
		return nil, err
	}

	// Ensure base URL doesn't have trailing slash
	baseURL = strings.TrimRight(baseURL, "/")

	return &mcpSkillsClient{
		baseURL:    baseURL,
		httpClient: httpClient,
		userAgent:  versions.GetUserAgent(),
	}, nil
}

// GetSkill retrieves a skill by namespace and name (latest version).
func (c *mcpSkillsClient) GetSkill(ctx context.Context, namespace, name string) (*thvregistry.Skill, error) {
	endpoint, err := url.JoinPath(c.baseURL, skillsBasePath, url.PathEscape(namespace), url.PathEscape(name))
	if err != nil {
		return nil, fmt.Errorf("failed to build skills URL: %w", err)
	}

	var skill thvregistry.Skill
	if err := c.doSkillsGet(ctx, endpoint, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// GetSkillVersion retrieves a specific version of a skill.
func (c *mcpSkillsClient) GetSkillVersion(ctx context.Context, namespace, name, version string) (*thvregistry.Skill, error) {
	endpoint, err := url.JoinPath(c.baseURL, skillsBasePath,
		url.PathEscape(namespace), url.PathEscape(name),
		"versions", url.PathEscape(version))
	if err != nil {
		return nil, fmt.Errorf("failed to build skills URL: %w", err)
	}

	var skill thvregistry.Skill
	if err := c.doSkillsGet(ctx, endpoint, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// ListSkills retrieves skills with optional filtering and pagination.
// It auto-paginates through all available pages, concatenating results.
func (c *mcpSkillsClient) ListSkills(ctx context.Context, opts *SkillsListOptions) (*SkillsListResult, error) {
	if opts == nil {
		opts = &SkillsListOptions{}
	}
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	var allSkills []*thvregistry.Skill
	cursor := opts.Cursor

	// Pagination loop - continue until no more cursors
	for {
		page, nextCursor, err := c.fetchSkillsPage(ctx, cursor, opts)
		if err != nil {
			return nil, err
		}

		allSkills = append(allSkills, page...)

		// Check if we have more pages
		if nextCursor == "" {
			break
		}

		cursor = nextCursor

		// Safety limit: prevent infinite loops
		if len(allSkills) > 10000 {
			return nil, fmt.Errorf("exceeded maximum skills limit (10000)")
		}
	}

	return &SkillsListResult{
		Skills: allSkills,
	}, nil
}

// SearchSkills searches for skills matching the query.
// Returns a single page of results (no auto-pagination).
func (c *mcpSkillsClient) SearchSkills(ctx context.Context, query string) (*SkillsListResult, error) {
	basePath, err := url.JoinPath(c.baseURL, skillsBasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to build skills URL: %w", err)
	}
	params := url.Values{}
	params.Add("search", query)

	endpoint := basePath + "?" + params.Encode()

	var listResp skillsListResponse
	if err := c.doSkillsGet(ctx, endpoint, &listResp); err != nil {
		return nil, err
	}

	return &SkillsListResult{
		Skills:     listResp.Skills,
		NextCursor: listResp.Metadata.NextCursor,
	}, nil
}

// ListSkillVersions lists all versions of a specific skill.
func (c *mcpSkillsClient) ListSkillVersions(ctx context.Context, namespace, name string) (*SkillsListResult, error) {
	endpoint, err := url.JoinPath(c.baseURL, skillsBasePath, url.PathEscape(namespace), url.PathEscape(name), "versions")
	if err != nil {
		return nil, fmt.Errorf("failed to build skills URL: %w", err)
	}

	var listResp skillsListResponse
	if err := c.doSkillsGet(ctx, endpoint, &listResp); err != nil {
		return nil, err
	}

	return &SkillsListResult{
		Skills:     listResp.Skills,
		NextCursor: listResp.Metadata.NextCursor,
	}, nil
}

// mcpSkillsClient implements the SkillsClient interface.
type mcpSkillsClient struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
}

// skillsListResponse is the wire format for list/search responses.
type skillsListResponse struct {
	Skills   []*thvregistry.Skill `json:"skills"`
	Metadata struct {
		Count      int    `json:"count"`
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// doSkillsGet performs an HTTP GET request and decodes the JSON response into dest.
func (c *mcpSkillsClient) doSkillsGet(ctx context.Context, endpoint string, dest any) error {
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

// fetchSkillsPage fetches a single page of skills.
func (c *mcpSkillsClient) fetchSkillsPage(
	ctx context.Context, cursor string, opts *SkillsListOptions,
) ([]*thvregistry.Skill, string, error) {
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

	basePath, err := url.JoinPath(c.baseURL, skillsBasePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build skills URL: %w", err)
	}
	endpoint := func() string {
		if len(params) > 0 {
			return basePath + "?" + params.Encode()
		}
		return basePath
	}()

	var listResp skillsListResponse
	if err := c.doSkillsGet(ctx, endpoint, &listResp); err != nil {
		return nil, "", err
	}

	return listResp.Skills, listResp.Metadata.NextCursor, nil
}

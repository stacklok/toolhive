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
	path, err := url.JoinPath(skillsBasePath, url.PathEscape(namespace), url.PathEscape(name))
	if err != nil {
		return nil, fmt.Errorf("failed to build skills path: %w", err)
	}

	var skill thvregistry.Skill
	if err := c.doSkillsGet(ctx, path, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// GetSkillVersion retrieves a specific version of a skill.
func (c *mcpSkillsClient) GetSkillVersion(ctx context.Context, namespace, name, version string) (*thvregistry.Skill, error) {
	path, err := url.JoinPath(skillsBasePath,
		url.PathEscape(namespace), url.PathEscape(name),
		"versions", url.PathEscape(version))
	if err != nil {
		return nil, fmt.Errorf("failed to build skills path: %w", err)
	}

	var skill thvregistry.Skill
	if err := c.doSkillsGet(ctx, path, &skill); err != nil {
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
	params := url.Values{}
	params.Add("search", query)

	pathAndQuery := skillsBasePath + "?" + params.Encode()

	var listResp skillsListResponse
	if err := c.doSkillsGet(ctx, pathAndQuery, &listResp); err != nil {
		return nil, err
	}

	return &SkillsListResult{
		Skills:     listResp.Skills,
		NextCursor: listResp.Metadata.NextCursor,
	}, nil
}

// ListSkillVersions lists all versions of a specific skill.
func (c *mcpSkillsClient) ListSkillVersions(ctx context.Context, namespace, name string) (*SkillsListResult, error) {
	path, err := url.JoinPath(skillsBasePath, url.PathEscape(namespace), url.PathEscape(name), "versions")
	if err != nil {
		return nil, fmt.Errorf("failed to build skills path: %w", err)
	}

	var listResp skillsListResponse
	if err := c.doSkillsGet(ctx, path, &listResp); err != nil {
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

// doSkillsGet performs an HTTP GET request to the given path (relative to the
// client's configured baseURL) and decodes the JSON response into dest.
// The path is joined to the trusted baseURL to prevent request forgery.
func (c *mcpSkillsClient) doSkillsGet(ctx context.Context, pathAndQuery string, dest any) error {
	// Parse the trusted base URL and resolve the relative path against it,
	// ensuring the final request URL is always rooted at the configured base.
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %w", c.baseURL, err)
	}
	ref, err := url.Parse(pathAndQuery)
	if err != nil {
		return fmt.Errorf("invalid request path %q: %w", pathAndQuery, err)
	}
	resolved := base.ResolveReference(ref)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
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

	pathAndQuery := func() string {
		if len(params) > 0 {
			return skillsBasePath + "?" + params.Encode()
		}
		return skillsBasePath
	}()

	var listResp skillsListResponse
	if err := c.doSkillsGet(ctx, pathAndQuery, &listResp); err != nil {
		return nil, "", err
	}

	return listResp.Skills, listResp.Metadata.NextCursor, nil
}

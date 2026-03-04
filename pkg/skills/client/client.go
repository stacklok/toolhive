// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides an HTTP client for the ToolHive Skills API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
)

const (
	skillsBasePath   = "/api/v1beta/skills"
	defaultBaseURL   = "http://127.0.0.1:8080"
	defaultTimeout   = 30 * time.Second
	envAPIURL        = "TOOLHIVE_API_URL"
	maxResponseSize  = 1 << 20 // 1 MiB — matches server-side maxRequestBodySize
	maxErrorBodySize = 1 << 16 // 64 KiB — matches auth/token and DCR limits
)

// ErrServerUnreachable is returned when the client cannot connect to the
// ToolHive API server. The most common cause is that "thv serve" is not
// running.
var ErrServerUnreachable = errors.New("could not reach ToolHive API server — is 'thv serve' running?")

// Compile-time interface check.
var _ skills.SkillService = (*Client)(nil)

// Client is an HTTP client for the ToolHive Skills API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithHTTPClient replaces the underlying *http.Client entirely.
// This overrides any previously applied options such as WithTimeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// NewClient creates a new Skills API client with the given base URL.
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewDefaultClient creates a Skills API client using the TOOLHIVE_API_URL
// environment variable, falling back to http://127.0.0.1:8080.
func NewDefaultClient(opts ...Option) *Client {
	return newDefaultClientWithEnv(&env.OSReader{}, opts...)
}

// newDefaultClientWithEnv is the testable core of NewDefaultClient.
func newDefaultClientWithEnv(envReader env.Reader, opts ...Option) *Client {
	base := envReader.Getenv(envAPIURL)
	if base == "" {
		base = defaultBaseURL
	}
	return NewClient(base, opts...)
}

// --- SkillService implementation ---

// List returns all installed skills matching the given options.
func (c *Client) List(ctx context.Context, opts skills.ListOptions) ([]skills.InstalledSkill, error) {
	q := url.Values{}
	if opts.Scope != "" {
		q.Set("scope", string(opts.Scope))
	}
	if opts.ClientApp != "" {
		q.Set("client", opts.ClientApp)
	}
	if opts.ProjectRoot != "" {
		q.Set("project_root", opts.ProjectRoot)
	}

	var resp listResponse
	if err := c.doJSONRequest(ctx, http.MethodGet, "", q, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Skills, nil
}

// Install installs a skill from a remote source.
func (c *Client) Install(ctx context.Context, opts skills.InstallOptions) (*skills.InstallResult, error) {
	body := installRequest{
		Name:        opts.Name,
		Version:     opts.Version,
		Scope:       opts.Scope,
		ProjectRoot: opts.ProjectRoot,
		Client:      opts.Client,
		Force:       opts.Force,
	}

	var resp installResponse
	if err := c.doJSONRequest(ctx, http.MethodPost, "", nil, body, &resp); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Skill: resp.Skill}, nil
}

// Uninstall removes an installed skill.
func (c *Client) Uninstall(ctx context.Context, opts skills.UninstallOptions) error {
	q := url.Values{}
	if opts.Scope != "" {
		q.Set("scope", string(opts.Scope))
	}
	if opts.ProjectRoot != "" {
		q.Set("project_root", opts.ProjectRoot)
	}

	path := "/" + url.PathEscape(opts.Name)
	return c.doJSONRequest(ctx, http.MethodDelete, path, q, nil, nil)
}

// Info returns detailed information about a skill.
func (c *Client) Info(ctx context.Context, opts skills.InfoOptions) (*skills.SkillInfo, error) {
	q := url.Values{}
	if opts.Scope != "" {
		q.Set("scope", string(opts.Scope))
	}
	if opts.ProjectRoot != "" {
		q.Set("project_root", opts.ProjectRoot)
	}

	path := "/" + url.PathEscape(opts.Name)
	var info skills.SkillInfo
	if err := c.doJSONRequest(ctx, http.MethodGet, path, q, nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Validate checks whether a skill definition is valid.
func (c *Client) Validate(ctx context.Context, path string) (*skills.ValidationResult, error) {
	body := validateRequest{Path: path}

	var result skills.ValidationResult
	if err := c.doJSONRequest(ctx, http.MethodPost, "/validate", nil, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Build builds a skill from a local directory into an OCI artifact.
func (c *Client) Build(ctx context.Context, opts skills.BuildOptions) (*skills.BuildResult, error) {
	body := buildRequest{
		Path: opts.Path,
		Tag:  opts.Tag,
	}

	var result skills.BuildResult
	if err := c.doJSONRequest(ctx, http.MethodPost, "/build", nil, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Push pushes a built skill artifact to a remote registry.
func (c *Client) Push(ctx context.Context, opts skills.PushOptions) error {
	body := pushRequest{Reference: opts.Reference}
	return c.doJSONRequest(ctx, http.MethodPost, "/push", nil, body, nil)
}

// --- internal helpers ---

func (c *Client) buildURL(path string, query url.Values) string {
	u := c.baseURL + skillsBasePath + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// doJSONRequest performs the full HTTP request lifecycle: marshal body, build
// URL, create request with context, set headers, execute, check status, and
// decode response or return *APIError.
func (c *Client) doJSONRequest(
	ctx context.Context,
	method, path string,
	query url.Values,
	reqBody any,
	result any,
) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL := c.buildURL(path, query)

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req) // #nosec G704 -- baseURL is a trusted local API server URL
	if err != nil {
		return fmt.Errorf("%w: %w", ErrServerUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return handleErrorResponse(resp)
	}

	if result != nil {
		limited := io.LimitReader(resp.Body, maxResponseSize)
		if err := json.NewDecoder(limited).Decode(result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}

// handleErrorResponse reads the response body and returns an *httperr.CodedError.
func handleErrorResponse(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	if err != nil {
		return httperr.New("failed to read error response body", resp.StatusCode)
	}
	return httperr.New(strings.TrimSpace(string(body)), resp.StatusCode)
}

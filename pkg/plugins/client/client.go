// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides an HTTP client for the ToolHive Plugins API.
package client

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
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/server/discovery"
	"github.com/stacklok/toolhive/pkg/skills/identitytoken"
)

const (
	pluginsBasePath = "/api/v1beta/plugins"
	defaultBaseURL  = "http://127.0.0.1:8080"
	// defaultTimeout is a backstop against a wedged server, not a budget for
	// the operation. Install, build, and push move OCI artifacts of unbounded
	// size over the user's connection, so a timeout tight enough to feel
	// responsive would abort legitimate work — and because giving up cancels
	// the request context, it aborts it server-side too, leaving nothing for
	// a retry to reuse. Callers who want to fail fast can set
	// TOOLHIVE_API_TIMEOUT or pass WithTimeout.
	defaultTimeout   = 10 * time.Minute
	envAPIURL        = "TOOLHIVE_API_URL"
	envAPITimeout    = "TOOLHIVE_API_TIMEOUT"
	maxResponseSize  = 1 << 20 // 1 MiB — defensive response cap; consistent with the skills client
	maxErrorBodySize = 1 << 16 // 64 KiB — matches auth/token and DCR limits
)

// ErrServerUnreachable is returned when the client cannot connect to the
// ToolHive API server. The most common cause is that "thv serve" is not
// running.
var ErrServerUnreachable = errors.New("could not reach ToolHive API server — is 'thv serve' running?")

// ErrRequestTimeout is returned when the server was reached but did not
// respond within the client timeout. This is a distinct condition from
// ErrServerUnreachable: the server is healthy and was working on the request.
// Note that abandoning the request cancels its context server-side, so the
// operation does not continue in the background and retrying restarts it.
var ErrRequestTimeout = errors.New("the ToolHive API server did not respond within the client timeout")

// Compile-time interface check.
var _ plugins.PluginService = (*Client)(nil)

// Client is an HTTP client for the ToolHive Plugins API.
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

// NewClient creates a new Plugins API client with the given base URL.
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

// NewDefaultClient creates a Plugins API client by trying, in order:
//  1. The TOOLHIVE_API_URL environment variable (explicit override)
//  2. The server discovery file (auto-detected running server)
//  3. The default URL http://127.0.0.1:8080
//
// The context is used for the server discovery health check; it is not stored.
func NewDefaultClient(ctx context.Context, opts ...Option) *Client {
	return newDefaultClientWithEnv(ctx, &env.OSReader{}, resolveViaDiscovery, opts...)
}

// discoverFunc resolves a running server's base URL and any transport options
// (e.g. a Unix socket client). It returns an empty base URL when no running
// server is found. resolveViaDiscovery is the production implementation; tests
// inject a stub so the discovery step does not read real local state.
type discoverFunc func(ctx context.Context) (string, []Option)

// newDefaultClientWithEnv is the testable core of NewDefaultClient. The
// envReader and discover dependencies are injected so each resolution step
// can be exercised in isolation.
func newDefaultClientWithEnv(ctx context.Context, envReader env.Reader, discover discoverFunc, opts ...Option) *Client {
	// An env-supplied timeout applies to every resolution path, but ranks
	// below caller-supplied options so a WithTimeout argument still wins.
	withEnvTimeout := func(base []Option) []Option {
		merged := make([]Option, 0, len(base)+len(opts)+1)
		merged = append(merged, base...)
		if d, ok := timeoutFromEnv(envReader); ok {
			merged = append(merged, WithTimeout(d))
		}
		return append(merged, opts...)
	}

	// 1. Explicit env var override always wins.
	if base := envReader.Getenv(envAPIURL); base != "" {
		return NewClient(base, withEnvTimeout(nil)...)
	}

	// 2. Try server discovery.
	if base, httpOpts := discover(ctx); base != "" {
		// Discovery opts go first so caller-supplied opts can override them
		// (e.g. a caller-provided WithTimeout replaces the discovery default).
		return NewClient(base, withEnvTimeout(httpOpts)...)
	}

	// 3. Fall back to the default URL.
	return NewClient(defaultBaseURL, withEnvTimeout(nil)...)
}

// timeoutFromEnv reads TOOLHIVE_API_TIMEOUT as a Go duration (e.g. "45s",
// "5m"). An unset, unparsable, or non-positive value is ignored so a typo
// degrades to the default rather than disabling the timeout entirely.
func timeoutFromEnv(envReader env.Reader) (time.Duration, bool) {
	raw := strings.TrimSpace(envReader.Getenv(envAPITimeout))
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("ignoring invalid "+envAPITimeout, "value", raw, "error", err)
		return 0, false
	}
	return d, true
}

// resolveViaDiscovery attempts to find a running server via the discovery file.
// It returns the base URL and any additional options (e.g. a Unix socket transport).
// On failure it returns empty values and the caller falls back to the default.
func resolveViaDiscovery(ctx context.Context) (string, []Option) {
	result, err := discovery.Discover(ctx)
	if err != nil {
		slog.Debug("server discovery failed", "error", err)
		return "", nil
	}
	if result.State != discovery.StateRunning {
		return "", nil
	}

	client, baseURL, err := discovery.HTTPClientForURL(result.Info.URL)
	if err != nil {
		slog.Debug("invalid URL in discovery file", "url", result.Info.URL, "error", err)
		return "", nil
	}
	client.Timeout = defaultTimeout

	return baseURL, []Option{WithHTTPClient(client)}
}

// --- PluginService implementation ---

// List returns all installed plugins matching the given options.
func (c *Client) List(ctx context.Context, opts plugins.ListOptions) ([]plugins.InstalledPlugin, error) {
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
	if opts.Group != "" {
		q.Set("group", opts.Group)
	}

	var resp listResponse
	if err := c.doJSONRequest(ctx, http.MethodGet, "", q, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Plugins, nil
}

// Install installs a plugin from a remote source.
func (c *Client) Install(ctx context.Context, opts plugins.InstallOptions) (*plugins.InstallResult, error) {
	body := installRequest{
		Name:          opts.Name,
		Version:       opts.Version,
		Scope:         opts.Scope,
		ProjectRoot:   opts.ProjectRoot,
		Clients:       opts.Clients,
		Force:         opts.Force,
		Group:         opts.Group,
		AllowUnsigned: opts.AllowUnsigned,
	}

	var resp installResponse
	if err := c.doJSONRequest(ctx, http.MethodPost, "", nil, body, &resp); err != nil {
		return nil, err
	}
	return &plugins.InstallResult{
		Plugin:     resp.Plugin,
		Provenance: resp.Provenance,
		Unsigned:   resp.Unsigned,
	}, nil
}

// Uninstall removes an installed plugin.
func (c *Client) Uninstall(ctx context.Context, opts plugins.UninstallOptions) error {
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

// Info returns detailed information about a plugin.
func (c *Client) Info(ctx context.Context, opts plugins.InfoOptions) (*plugins.PluginInfo, error) {
	q := url.Values{}
	if opts.Scope != "" {
		q.Set("scope", string(opts.Scope))
	}
	if opts.ProjectRoot != "" {
		q.Set("project_root", opts.ProjectRoot)
	}

	path := "/" + url.PathEscape(opts.Name)
	var info plugins.PluginInfo
	if err := c.doJSONRequest(ctx, http.MethodGet, path, q, nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Validate checks whether a plugin definition is valid.
func (c *Client) Validate(ctx context.Context, path string) (*plugins.ValidationResult, error) {
	body := validateRequest{Path: path}

	var result plugins.ValidationResult
	if err := c.doJSONRequest(ctx, http.MethodPost, "/validate", nil, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Build builds a plugin from a local directory into an OCI artifact.
func (c *Client) Build(ctx context.Context, opts plugins.BuildOptions) (*plugins.BuildResult, error) {
	body := buildRequest{
		Path: opts.Path,
		Tag:  opts.Tag,
	}

	var result plugins.BuildResult
	if err := c.doJSONRequest(ctx, http.MethodPost, "/build", nil, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Push pushes a built plugin artifact to a remote registry.
func (c *Client) Push(ctx context.Context, opts plugins.PushOptions) error {
	// An identity token is a bearer credential redeemable at Fulcio for a
	// signing certificate in the caller's name, so it must not cross a
	// plaintext link to a remote API server. Checked before the body is
	// marshaled: nothing should serialize the token until the destination
	// has been cleared.
	if opts.IdentityToken != "" {
		if err := identitytoken.CheckTransport(c.baseURL); err != nil {
			return err
		}
	}
	body := pushRequest{
		Reference:     opts.Reference,
		IdentityToken: opts.IdentityToken,
		NoSign:        opts.NoSign,
	}
	return c.doJSONRequest(ctx, http.MethodPost, "/push", nil, body, nil)
}

// ListBuilds returns all locally-built OCI plugin artifacts in the local store.
func (c *Client) ListBuilds(ctx context.Context) ([]plugins.LocalBuild, error) {
	var resp listBuildsResponse
	if err := c.doJSONRequest(ctx, http.MethodGet, "/builds", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Builds, nil
}

// DeleteBuild removes a locally-built OCI plugin artifact from the local store.
func (c *Client) DeleteBuild(ctx context.Context, tag string) error {
	return c.doJSONRequest(ctx, http.MethodDelete, "/builds/"+url.PathEscape(tag), nil, nil, nil)
}

// GetContent retrieves the plugin.json body and file listing from an OCI artifact without installing it.
func (c *Client) GetContent(ctx context.Context, opts plugins.ContentOptions) (*plugins.PluginContent, error) {
	q := url.Values{}
	q.Set("ref", opts.Reference)
	var content plugins.PluginContent
	if err := c.doJSONRequest(ctx, http.MethodGet, "/content", q, nil, &content); err != nil {
		return nil, err
	}
	return &content, nil
}

// Sync restores a project's installed plugins to match its lock file.
func (c *Client) Sync(ctx context.Context, opts plugins.SyncOptions) (*plugins.SyncResult, error) {
	body := syncRequest{
		ProjectRoot:   opts.ProjectRoot,
		Clients:       opts.Clients,
		Prune:         opts.Prune,
		Check:         opts.Check,
		Adopt:         opts.Adopt,
		AllowUnsigned: opts.AllowUnsigned,
	}

	var result plugins.SyncResult
	if err := c.doJSONRequest(ctx, http.MethodPost, "/sync", nil, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Upgrade re-resolves a project's lock entries and installs newer content
// where available.
func (c *Client) Upgrade(ctx context.Context, opts plugins.UpgradeOptions) (*plugins.UpgradeResult, error) {
	body := upgradeRequest{
		ProjectRoot:       opts.ProjectRoot,
		Names:             opts.Names,
		Preview:           opts.Preview,
		FailOnChanges:     opts.FailOnChanges,
		AllowRefChange:    opts.AllowRefChange,
		AllowSignerChange: opts.AllowSignerChange,
		Clients:           opts.Clients,
	}

	var result plugins.UpgradeResult
	if err := c.doJSONRequest(ctx, http.MethodPost, "/upgrade", nil, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// --- internal helpers ---

func (c *Client) buildURL(path string, query url.Values) string {
	u := c.baseURL + pluginsBasePath + path
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
		return fmt.Errorf("%w: %w", classifyTransportError(ctx, err), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return handleErrorResponse(resp)
	}

	if result != nil {
		limited := io.LimitReader(resp.Body, maxResponseSize)
		if err := json.NewDecoder(limited).Decode(result); err != nil {
			// Drain before close so the connection can be reused.
			_, _ = io.Copy(io.Discard, limited)
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}

// classifyTransportError decides which sentinel a failed round-trip belongs
// to. Reporting a timeout as an unreachable server sends users to check
// whether "thv serve" is running when it is running and was mid-operation.
func classifyTransportError(ctx context.Context, err error) error {
	// The caller's own context ending takes precedence — neither the server's
	// availability nor the client timeout is why the request stopped.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrRequestTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrRequestTimeout
	}
	return ErrServerUnreachable
}

// handleErrorResponse reads the response body and returns an *httperr.CodedError.
func handleErrorResponse(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	if err != nil {
		return httperr.New("failed to read error response body", resp.StatusCode)
	}
	return httperr.New(strings.TrimSpace(string(body)), resp.StatusCode)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package helpers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// OAuthClient provides helper methods for testing OAuth flows.
type OAuthClient struct {
	tb         testing.TB
	httpClient *http.Client
	baseURL    string
}

// NewOAuthClient creates an HTTP client configured for OAuth testing.
// The client does NOT follow redirects automatically, allowing tests to
// verify redirect behavior.
func NewOAuthClient(tb testing.TB, baseURL string) *OAuthClient {
	tb.Helper()

	return &OAuthClient{
		tb:      tb,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				// Don't follow redirects - we want to inspect them
				return http.ErrUseLastResponse
			},
		},
	}
}

// NewOAuthClientWithRedirects creates an HTTP client that follows redirects.
func NewOAuthClientWithRedirects(tb testing.TB, baseURL string) *OAuthClient {
	tb.Helper()

	return &OAuthClient{
		tb:      tb,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetJWKS fetches the JWKS endpoint and returns the parsed response.
func (c *OAuthClient) GetJWKS() (map[string]interface{}, int) {
	resp, err := c.httpClient.Get(c.baseURL + "/.well-known/jwks.json")
	require.NoError(c.tb, err)
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(c.tb, err)

	var result map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		err = json.Unmarshal(body, &result)
		require.NoError(c.tb, err)
	}

	return result, resp.StatusCode
}

// GetOAuthDiscovery fetches the OAuth Authorization Server Metadata endpoint.
func (c *OAuthClient) GetOAuthDiscovery() (map[string]interface{}, int) {
	resp, err := c.httpClient.Get(c.baseURL + "/.well-known/oauth-authorization-server")
	require.NoError(c.tb, err)
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(c.tb, err)

	var result map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		err = json.Unmarshal(body, &result)
		require.NoError(c.tb, err)
	}

	return result, resp.StatusCode
}

// GetOIDCDiscovery fetches the OIDC Discovery endpoint.
func (c *OAuthClient) GetOIDCDiscovery() (map[string]interface{}, int) {
	resp, err := c.httpClient.Get(c.baseURL + "/.well-known/openid-configuration")
	require.NoError(c.tb, err)
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(c.tb, err)

	var result map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		err = json.Unmarshal(body, &result)
		require.NoError(c.tb, err)
	}

	return result, resp.StatusCode
}

// StartAuthorization initiates the OAuth authorization flow.
// Returns the HTTP response including the redirect location.
func (c *OAuthClient) StartAuthorization(params url.Values) (*http.Response, error) {
	authURL := c.baseURL + "/oauth/authorize?" + params.Encode()
	return c.httpClient.Get(authURL)
}

// ExchangeToken performs a token exchange at the token endpoint.
func (c *OAuthClient) ExchangeToken(params url.Values) (map[string]interface{}, int) {
	resp, err := c.httpClient.PostForm(c.baseURL+"/oauth/token", params)
	require.NoError(c.tb, err)
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(c.tb, err)

	var result map[string]interface{}
	if len(body) > 0 {
		err = json.Unmarshal(body, &result)
		require.NoError(c.tb, err)
	}

	return result, resp.StatusCode
}

// RegisterClient performs dynamic client registration.
func (c *OAuthClient) RegisterClient(clientMetadata map[string]interface{}) (map[string]interface{}, int) {
	body, err := json.Marshal(clientMetadata)
	require.NoError(c.tb, err)

	resp, err := c.httpClient.Post(
		c.baseURL+"/oauth/register",
		"application/json",
		strings.NewReader(string(body)),
	)
	require.NoError(c.tb, err)
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(c.tb, err)

	var result map[string]interface{}
	if len(respBody) > 0 {
		err = json.Unmarshal(respBody, &result)
		require.NoError(c.tb, err)
	}

	return result, resp.StatusCode
}

// Get performs a GET request to the specified path.
func (c *OAuthClient) Get(path string) (*http.Response, error) {
	return c.httpClient.Get(c.baseURL + path)
}

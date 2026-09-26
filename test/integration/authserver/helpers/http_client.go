// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package helpers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// OAuthClient provides helper methods for testing OAuth flows.
type OAuthClient struct {
	httpClient *http.Client
	baseURL    string
	// bindingCookies holds the cookies the auth server set on its redirects to
	// an upstream. /oauth/callback only completes a leg for the browser that
	// started it, so this client plays that browser: it remembers every cookie
	// from StartAuthorization and Callback and sends them on the next Callback.
	// Cookies are kept by name rather than in a cookie jar so an https-issuer
	// Secure cookie still travels to a plain http test server.
	bindingCookies map[string]*http.Cookie
}

// NewOAuthClient creates an HTTP client configured for OAuth testing.
// The client does NOT follow redirects automatically, allowing tests to
// verify redirect behavior.
func NewOAuthClient(baseURL string) *OAuthClient {
	return &OAuthClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				// Don't follow redirects - we want to inspect them
				return http.ErrUseLastResponse
			},
		},
		bindingCookies: make(map[string]*http.Cookie),
	}
}

// GetJWKS fetches the JWKS endpoint and returns the parsed response.
func (c *OAuthClient) GetJWKS() (map[string]interface{}, int, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/.well-known/jwks.json")
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var result map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		if err = json.Unmarshal(body, &result); err != nil {
			return nil, resp.StatusCode, err
		}
	}

	return result, resp.StatusCode, nil
}

// GetOAuthDiscovery fetches the OAuth Authorization Server Metadata endpoint.
func (c *OAuthClient) GetOAuthDiscovery() (map[string]interface{}, int, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/.well-known/oauth-authorization-server")
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var result map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		if err = json.Unmarshal(body, &result); err != nil {
			return nil, resp.StatusCode, err
		}
	}

	return result, resp.StatusCode, nil
}

// GetOIDCDiscovery fetches the OIDC Discovery endpoint.
func (c *OAuthClient) GetOIDCDiscovery() (map[string]interface{}, int, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/.well-known/openid-configuration")
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var result map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		if err = json.Unmarshal(body, &result); err != nil {
			return nil, resp.StatusCode, err
		}
	}

	return result, resp.StatusCode, nil
}

// StartAuthorization initiates the OAuth authorization flow.
// Returns the HTTP response including the redirect location.
func (c *OAuthClient) StartAuthorization(params url.Values) (*http.Response, error) {
	authURL := c.baseURL + "/oauth/authorize?" + params.Encode()
	resp, err := c.httpClient.Get(authURL)
	if err != nil {
		return nil, err
	}
	c.rememberCookies(resp)
	return resp, nil
}

// Callback drives the upstream callback leg: GET /oauth/callback with the given
// code and internal state, carrying the browser-binding cookies remembered from
// the earlier legs. The client does not follow redirects, so the returned
// response exposes what the server issues — an onward redirect to the next
// upstream, or a redirect back to the client with an authorization code.
func (c *OAuthClient) Callback(code, state string) (*http.Response, error) {
	params := url.Values{"code": {code}, "state": {state}}
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/oauth/callback?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	for _, cookie := range c.bindingCookies {
		req.AddCookie(cookie)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	c.rememberCookies(resp)
	return resp, nil
}

// rememberCookies records the cookies set on resp by name.
func (c *OAuthClient) rememberCookies(resp *http.Response) {
	for _, cookie := range resp.Cookies() {
		c.bindingCookies[cookie.Name] = cookie
	}
}

// ExchangeToken performs a token exchange at the token endpoint.
func (c *OAuthClient) ExchangeToken(params url.Values) (map[string]interface{}, int, error) {
	resp, err := c.httpClient.PostForm(c.baseURL+"/oauth/token", params)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var result map[string]interface{}
	if len(body) > 0 {
		if err = json.Unmarshal(body, &result); err != nil {
			return nil, resp.StatusCode, err
		}
	}

	return result, resp.StatusCode, nil
}

// RegisterClient performs dynamic client registration.
func (c *OAuthClient) RegisterClient(clientMetadata map[string]interface{}) (map[string]interface{}, int, error) {
	body, err := json.Marshal(clientMetadata)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/oauth/register",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var result map[string]interface{}
	if len(respBody) > 0 {
		if err = json.Unmarshal(respBody, &result); err != nil {
			return nil, resp.StatusCode, err
		}
	}

	return result, resp.StatusCode, nil
}

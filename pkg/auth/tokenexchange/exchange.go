// Package tokenexchange provides OAuth 2.0 Token Exchange (RFC 8693) support.
package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// grantTypeTokenExchange is the OAuth 2.0 Token Exchange grant type (RFC 8693)
	//nolint:gosec // G101: False positive - these are OAuth2 URN identifiers, not credentials
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"

	// tokenTypeAccessToken indicates an OAuth 2.0 access token
	//nolint:gosec // G101: False positive - these are OAuth2 URN identifiers, not credentials
	tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"

	// defaultHTTPTimeout is the timeout for HTTP requests
	defaultHTTPTimeout = 30 * time.Second

	// maxResponseBodySize is the maximum size for reading response bodies (1 MB)
	maxResponseBodySize = 1 << 20

	// redactedPlaceholder is used to redact sensitive values in string representations
	redactedPlaceholder = "[REDACTED]"

	// emptyPlaceholder is used to indicate empty/missing values in string representations
	emptyPlaceholder = "<empty>"
)

// oAuthError represents an OAuth 2.0 error response as defined in RFC 6749 Section 5.2.
type oAuthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	ErrorURI         string `json:"error_uri,omitempty"`
	StatusCode       int    `json:"-"`
}

func (e *oAuthError) String() string {
	if e.ErrorURI != "" {
		return fmt.Sprintf("OAuth error %q (status %d): see %s", e.Error, e.StatusCode, e.ErrorURI)
	}
	return fmt.Sprintf("OAuth error %q (status %d)", e.Error, e.StatusCode)
}

// parseOAuthError attempts to parse an OAuth error response from the given response body.
func parseOAuthError(statusCode int, body []byte) *oAuthError {
	var oauthErr oAuthError
	if err := json.Unmarshal(body, &oauthErr); err != nil {
		return nil
	}
	if oauthErr.Error == "" {
		return nil
	}
	oauthErr.StatusCode = statusCode
	return &oauthErr
}

// defaultHTTPClient is the default HTTP client used for token exchange requests.
var defaultHTTPClient = &http.Client{
	Timeout: defaultHTTPTimeout,
}

// actingParty represents the acting party in a token exchange delegation scenario.
// When present, it indicates that the actor token holder is acting on behalf of the subject token holder.
type actingParty struct {
	ActorToken     string
	ActorTokenType string
}

// exchangeRequest contains fields necessary to make an OAuth 2.0 token exchange.
// Based on RFC 8693: https://datatracker.ietf.org/doc/html/rfc8693
type exchangeRequest struct {
	// Required fields
	GrantType          string
	SubjectToken       string
	SubjectTokenType   string
	RequestedTokenType string

	// Optional fields
	Resource    string
	Audience    string
	Scope       []string
	ActingParty *actingParty
}

// String implements fmt.Stringer for exchangeRequest, redacting sensitive tokens.
func (r exchangeRequest) String() string {
	subjectToken := redactedPlaceholder
	if r.SubjectToken == "" {
		subjectToken = emptyPlaceholder
	}

	actorToken := "<none>"
	if r.ActingParty != nil {
		actorToken = redactedPlaceholder
		if r.ActingParty.ActorToken == "" {
			actorToken = emptyPlaceholder
		}
	}

	return fmt.Sprintf("exchangeRequest{GrantType: %s, Audience: %s, Scope: %v, SubjectToken: %s, ActorToken: %s}",
		r.GrantType, r.Audience, r.Scope, subjectToken, actorToken)
}

// response is used to decode the remote server response during an OAuth 2.0 token exchange.
type response struct {
	AccessToken     string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int    `json:"expires_in"`
	Scope           string `json:"scope"`
	RefreshToken    string `json:"refresh_token"`
}

// String implements fmt.Stringer for response, redacting sensitive tokens.
func (r response) String() string {
	accessToken := redactedPlaceholder
	if r.AccessToken == "" {
		accessToken = emptyPlaceholder
	}

	refreshToken := redactedPlaceholder
	if r.RefreshToken == "" {
		refreshToken = emptyPlaceholder
	}

	return fmt.Sprintf("response{AccessToken: %s, TokenType: %s, ExpiresIn: %d, RefreshToken: %s}",
		accessToken, r.TokenType, r.ExpiresIn, refreshToken)
}

// clientAuthentication represents OAuth client credentials for token exchange.
type clientAuthentication struct {
	ClientID     string
	ClientSecret string
}

// String implements fmt.Stringer for clientAuthentication, redacting the client secret.
func (c clientAuthentication) String() string {
	clientSecret := redactedPlaceholder
	if c.ClientSecret == "" {
		clientSecret = emptyPlaceholder
	}

	return fmt.Sprintf("clientAuthentication{ClientID: %s, ClientSecret: %s}",
		c.ClientID, clientSecret)
}

// ExchangeConfig holds the configuration for token exchange.
type ExchangeConfig struct {
	// TokenURL is the OAuth 2.0 token endpoint URL
	TokenURL string

	// ClientID is the OAuth 2.0 client identifier
	ClientID string

	// ClientSecret is the OAuth 2.0 client secret
	ClientSecret string

	// Audience is the target audience for the exchanged token (optional per RFC 8693)
	Audience string

	// Scopes is the list of scopes to request (optional per RFC 8693)
	Scopes []string

	// SubjectTokenProvider is a function that returns the subject token to exchange
	// we use a function to allow dynamic retrieval of the token (e.g. from request context)
	// and also to lazy-load the token only when needed, load from dynamic sources, etc.
	SubjectTokenProvider func() (string, error)

	// HTTPClient is the HTTP client to use for token exchange requests.
	// If nil, defaultHTTPClient will be used.
	HTTPClient *http.Client
}

// Validate checks if the ExchangeConfig contains all required fields.
func (c *ExchangeConfig) Validate() error {
	if c.TokenURL == "" {
		return fmt.Errorf("TokenURL is required")
	}

	if c.SubjectTokenProvider == nil {
		return fmt.Errorf("SubjectTokenProvider is required")
	}

	if c.ClientID == "" {
		return fmt.Errorf("ClientID is required")
	}

	// Validate URL format
	_, err := url.Parse(c.TokenURL)
	if err != nil {
		return fmt.Errorf("TokenURL is not a valid URL: %w", err)
	}

	return nil
}

// tokenSource implements oauth2.TokenSource for token exchange.
type tokenSource struct {
	ctx  context.Context
	conf *ExchangeConfig
}

// Token implements oauth2.TokenSource interface.
// It performs the token exchange and returns an oauth2.Token.
func (ts *tokenSource) Token() (*oauth2.Token, error) {
	conf := ts.conf

	// Validate configuration
	if err := conf.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Get the subject token from the provider
	subjectToken, err := conf.SubjectTokenProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get subject token: %w", err)
	}

	// Build the token exchange request
	request := &exchangeRequest{
		GrantType:          grantTypeTokenExchange,
		Audience:           conf.Audience,
		Scope:              conf.Scopes,
		RequestedTokenType: tokenTypeAccessToken,
		SubjectToken:       subjectToken,
		SubjectTokenType:   tokenTypeAccessToken,
	}

	clientAuth := clientAuthentication{
		ClientID:     conf.ClientID,
		ClientSecret: conf.ClientSecret,
	}

	// Perform the exchange
	resp, err := exchangeToken(ts.ctx, conf.TokenURL, request, clientAuth, conf.HTTPClient)
	if err != nil {
		return nil, err
	}

	// Validate required RFC 8693 response fields
	if resp.AccessToken == "" {
		return nil, fmt.Errorf("token exchange: server returned empty access_token")
	}
	if resp.TokenType == "" {
		return nil, fmt.Errorf("token exchange: server returned empty token_type")
	}
	if resp.IssuedTokenType == "" {
		return nil, fmt.Errorf("token exchange: server returned empty issued_token_type (required by RFC 8693)")
	}

	// Build oauth2.Token
	token := &oauth2.Token{
		AccessToken: resp.AccessToken,
		TokenType:   resp.TokenType,
	}

	// Set expiry if provided
	if resp.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}

	if resp.RefreshToken != "" {
		token.RefreshToken = resp.RefreshToken
	}

	return token, nil
}

// TokenSource returns an oauth2.TokenSource that performs token exchange.
func (c *ExchangeConfig) TokenSource(ctx context.Context) oauth2.TokenSource {
	return &tokenSource{
		ctx:  ctx,
		conf: c,
	}
}

// exchangeToken performs the actual HTTP request for token exchange.
// This is the internal implementation used by tokenSource.Token().
func exchangeToken(
	ctx context.Context,
	endpoint string,
	request *exchangeRequest,
	auth clientAuthentication,
	client *http.Client,
) (*response, error) {
	data, err := buildTokenExchangeFormData(request)
	if err != nil {
		return nil, err
	}

	req, err := createTokenExchangeRequest(ctx, endpoint, data, auth)
	if err != nil {
		return nil, err
	}

	if client == nil {
		client = defaultHTTPClient
	}

	body, err := executeTokenExchangeRequest(client, req)
	if err != nil {
		return nil, err
	}

	tokenResp, err := parseTokenExchangeResponse(body)
	if err != nil {
		return nil, err
	}

	return tokenResp, nil
}

// buildTokenExchangeFormData constructs the form data for a token exchange request according to RFC 8693.
func buildTokenExchangeFormData(request *exchangeRequest) (url.Values, error) {
	data := url.Values{}

	// Grant type is always token exchange
	if request.GrantType == "" {
		request.GrantType = grantTypeTokenExchange
	}
	data.Set("grant_type", request.GrantType)

	// Subject token is required
	if request.SubjectToken == "" {
		return nil, fmt.Errorf("subject_token is required")
	}
	data.Set("subject_token", request.SubjectToken)

	// Subject token type defaults to access_token if not specified
	if request.SubjectTokenType == "" {
		request.SubjectTokenType = tokenTypeAccessToken
	}
	data.Set("subject_token_type", request.SubjectTokenType)

	// Requested token type defaults to access_token if not specified
	if request.RequestedTokenType == "" {
		request.RequestedTokenType = tokenTypeAccessToken
	}
	data.Set("requested_token_type", request.RequestedTokenType)

	addOptionalFields(data, request)

	return data, nil
}

// addOptionalFields adds optional RFC 8693 fields to the form data.
func addOptionalFields(data url.Values, request *exchangeRequest) {
	if request.Audience != "" {
		data.Set("audience", request.Audience)
	}
	if len(request.Scope) > 0 {
		data.Set("scope", strings.Join(request.Scope, " "))
	}
	if request.Resource != "" {
		data.Set("resource", request.Resource)
	}

	// Actor token (for delegation scenarios)
	if request.ActingParty != nil && request.ActingParty.ActorToken != "" {
		data.Set("actor_token", request.ActingParty.ActorToken)
		if request.ActingParty.ActorTokenType != "" {
			data.Set("actor_token_type", request.ActingParty.ActorTokenType)
		}
	}
}

// createTokenExchangeRequest creates an HTTP POST request for token exchange.
// Client credentials are sent via HTTP Basic Authentication as recommended by RFC 6749 Section 2.3.1.
func createTokenExchangeRequest(
	ctx context.Context,
	endpoint string,
	data url.Values,
	auth clientAuthentication,
) (*http.Request, error) {
	encodedData := data.Encode()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(encodedData))
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", strconv.Itoa(len(encodedData)))

	// Add client authentication via HTTP Basic Auth per RFC 6749 Section 2.3.1
	// Per RFC 6749 and Go's SetBasicAuth documentation, credentials must be URL-encoded
	// before being passed to SetBasicAuth for OAuth2 compatibility
	if auth.ClientID != "" && auth.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(auth.ClientID), url.QueryEscape(auth.ClientSecret))
	}

	return req, nil
}

// executeTokenExchangeRequest sends the HTTP request and returns the response body.
func executeTokenExchangeRequest(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("failed to read token exchange response: %w", err)
	}

	if err := validateResponseStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	return body, nil
}

// validateResponseStatus checks the HTTP status code and returns an error if not successful.
func validateResponseStatus(statusCode int, body []byte) error {
	if statusCode >= 200 && statusCode <= 299 {
		return nil
	}

	// Try to parse as OAuth error first
	if oauthErr := parseOAuthError(statusCode, body); oauthErr != nil {
		logger.Debugf("Token exchange OAuth error: %s (description: %s)", oauthErr.Error, oauthErr.ErrorDescription)
		return errors.New(oauthErr.String())
	}

	logger.Debugf("Token exchange failed with status %d: %s", statusCode, string(body))
	return fmt.Errorf("token exchange failed with status %d", statusCode)
}

// parseTokenExchangeResponse parses the token exchange response body.
func parseTokenExchangeResponse(body []byte) (*response, error) {
	var tokenResp response
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		logger.Debugf("Failed to parse token exchange response: %v", err)
		return nil, errors.New("failed to parse token exchange response")
	}

	return &tokenResp, nil
}

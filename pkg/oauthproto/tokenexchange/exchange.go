// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokenexchange provides OAuth 2.0 Token Exchange (RFC 8693) support.
package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// NormalizeTokenType converts a short token type name to its full URN.
// Accepts both short forms ("access_token", "id_token", "jwt") and full URNs.
// Returns the full URN or an error if the token type is invalid.
//
// This is primarily intended for CLI/user input processing. Internal APIs
// should use full URNs directly.
func NormalizeTokenType(tokenType string) (string, error) {
	// Empty string is valid (will use default)
	if tokenType == "" {
		return "", nil
	}

	// Check if already a full URN (backward compatibility)
	switch tokenType {
	case oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeIDToken, oauthproto.TokenTypeJWT:
		return tokenType, nil
	}

	// Convert short form to full URN
	switch tokenType {
	case "access_token":
		return oauthproto.TokenTypeAccessToken, nil
	case "id_token":
		return oauthproto.TokenTypeIDToken, nil
	case "jwt":
		return oauthproto.TokenTypeJWT, nil
	default:
		return "", fmt.Errorf("invalid token type %q: must be one of: access_token, id_token, jwt", tokenType)
	}
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

// clientAuthentication represents OAuth client credentials for token exchange.
type clientAuthentication struct {
	ClientID     string
	ClientSecret string
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

	// SubjectTokenType specifies the type of the subject token being exchanged.
	// Common values: oauthproto.TokenTypeAccessToken (default), oauthproto.TokenTypeIDToken, oauthproto.TokenTypeJWT.
	// If empty, defaults to oauthproto.TokenTypeAccessToken.
	SubjectTokenType string

	// RequestedTokenType specifies the desired token type in the exchange response.
	// Defaults to oauthproto.TokenTypeAccessToken per RFC 8693. Set to any RFC 8693
	// token-type URN to request a different issued token type.
	RequestedTokenType string

	// Resource identifies the target resource per RFC 8693 Section 2.1 and RFC 8707.
	// Must be an absolute URI without a fragment. This implementation accepts a
	// single resource indicator; RFC 8707 permits multiple but multi-value support
	// is not yet exposed at this layer.
	Resource string

	// SubjectTokenProvider is a function that returns the subject token to exchange
	// we use a function to allow dynamic retrieval of the token (e.g. from request context)
	// and also to lazy-load the token only when needed, load from dynamic sources, etc.
	SubjectTokenProvider func() (string, error)

	// HTTPClient is the HTTP client to use for token exchange requests.
	// If nil, oauthproto.DefaultHTTPClient() will be used.
	HTTPClient *http.Client
}

// Validate checks if the ExchangeConfig contains all required fields.
//
// Side effect: when SubjectTokenType is provided as a short form
// ("access_token", "id_token", "jwt"), Validate normalizes it to the full
// RFC 8693 URN and writes the result back onto the receiver. Callers in
// pkg/vmcp/auth/strategies/tokenexchange.go and pkg/runner/config_builder.go
// read the normalized value after Validate returns, so this mutation is part
// of the documented contract.
func (c *ExchangeConfig) Validate() error {
	if c.TokenURL == "" {
		return fmt.Errorf("TokenURL is required")
	}

	if c.SubjectTokenProvider == nil {
		return fmt.Errorf("SubjectTokenProvider is required")
	}

	// ClientID is optional - some token exchange endpoints (like Google STS)
	// don't require client credentials and rely on the trust relationship
	// configured in the identity provider (e.g., Workload Identity Federation)

	// Validate and normalize SubjectTokenType if provided
	if c.SubjectTokenType != "" {
		normalized, err := NormalizeTokenType(c.SubjectTokenType)
		if err != nil {
			return fmt.Errorf("invalid SubjectTokenType: %w", err)
		}
		// Update the config with the normalized value
		c.SubjectTokenType = normalized
	}

	// Validate Resource per RFC 8707 Section 2: absolute URI, no fragment.
	if c.Resource != "" {
		u, err := url.Parse(c.Resource)
		if err != nil {
			return fmt.Errorf("invalid Resource URI: %w", err)
		}
		if !u.IsAbs() {
			return fmt.Errorf("invalid Resource: must be an absolute URI (got %q)", c.Resource)
		}
		if u.Fragment != "" {
			return fmt.Errorf("invalid Resource: must not include a fragment (RFC 8707 Section 2)")
		}
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

	// Determine subject token type (default to access_token if not specified)
	subjectTokenType := conf.SubjectTokenType
	if subjectTokenType == "" {
		subjectTokenType = oauthproto.TokenTypeAccessToken
	}

	// Build the token exchange request
	requestedTokenType := conf.RequestedTokenType
	if requestedTokenType == "" {
		requestedTokenType = oauthproto.TokenTypeAccessToken
	}

	request := &exchangeRequest{
		GrantType:          oauthproto.GrantTypeTokenExchange,
		Audience:           conf.Audience,
		Scope:              conf.Scopes,
		RequestedTokenType: requestedTokenType,
		Resource:           conf.Resource,
		SubjectToken:       subjectToken,
		SubjectTokenType:   subjectTokenType,
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

	// RFC 8693 Section 2.2.1 requires token_type in the response. The shared
	// oauthproto.ParseTokenResponse is intentionally permissive on this field
	// (matching x/oauth2); token exchange tightens it back.
	if resp.Token.TokenType == "" {
		return nil, fmt.Errorf("token exchange: server returned empty token_type (required by RFC 8693)")
	}
	// RFC 8693 Section 2.2.1 requires issued_token_type in the response.
	if resp.IssuedTokenType == "" {
		return nil, fmt.Errorf("token exchange: server returned empty issued_token_type (required by RFC 8693)")
	}

	// Surface the RFC 8693 issued_token_type so callers can verify the server
	// returned the type they asked for (e.g., XAA's IdP exchange requires an ID-JAG).
	token := resp.Token.WithExtra(map[string]any{
		"issued_token_type": resp.IssuedTokenType,
	})

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
) (*oauthproto.TokenResponse, error) {
	data, err := buildFormData(request)
	if err != nil {
		return nil, err
	}

	req, err := oauthproto.NewFormRequest(ctx, endpoint, data, auth.ClientID, auth.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("tokenexchange: build request: %w", err)
	}

	resp, err := oauthproto.DoTokenRequest(client, req)
	if err != nil {
		// Preserve the pre-refactor behavior: scrub RetrieveError.Body so raw
		// upstream content cannot leak into error strings via err.Error().
		// pkg/oauthproto deliberately preserves Body for general-purpose
		// callers; tokenexchange opts back into the stricter behavior because
		// its errors propagate through vmcp / runner paths that may log them.
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) {
			retrieveErr.Body = nil
		}
		return nil, err
	}
	return resp, nil
}

// buildFormData constructs the form data for a token exchange request according to RFC 8693.
func buildFormData(request *exchangeRequest) (url.Values, error) {
	data := url.Values{}

	// Grant type is always token exchange
	if request.GrantType == "" {
		request.GrantType = oauthproto.GrantTypeTokenExchange
	}
	data.Set("grant_type", request.GrantType)

	// Subject token is required
	if request.SubjectToken == "" {
		return nil, fmt.Errorf("subject_token is required")
	}
	data.Set("subject_token", request.SubjectToken)

	// Subject token type defaults to access_token if not specified
	if request.SubjectTokenType == "" {
		request.SubjectTokenType = oauthproto.TokenTypeAccessToken
	}
	data.Set("subject_token_type", request.SubjectTokenType)

	// Requested token type defaults to access_token if not specified
	if request.RequestedTokenType == "" {
		request.RequestedTokenType = oauthproto.TokenTypeAccessToken
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

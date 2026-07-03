// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package jwtbearer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Config holds configuration for an OAuth 2.0 JWT Bearer Grant (RFC 7523).
type Config struct {
	// TokenURL is the target authorization server's token endpoint (required).
	TokenURL string

	// ClientID is the OAuth client identifier at the target AS. When both ClientID
	// and ClientSecret are set, the request is authenticated with HTTP Basic per
	// RFC 6749 Section 2.3.1. Public-client identification via a body client_id
	// parameter (RFC 6749 Section 3.2.1) is not supported — the ID-JAG draft
	// (§9.1, Security Considerations) RECOMMENDS (SHOULD) confidential clients,
	// which is the only intended consumer here, and this engine only supports
	// client_secret/HTTP Basic client authentication.
	ClientID string

	// ClientSecret is the OAuth client secret at the target AS.
	ClientSecret string //nolint:gosec // G101: field name, not a credential

	// Scopes are the requested scopes for the access token.
	Scopes []string

	// AssertionProvider returns the JWT assertion (e.g., the ID-JAG from XAA's IdP exchange).
	// Called on each Token() invocation; must not be nil. The returned JWT must
	// satisfy RFC 7523 Section 3 (iss/sub/aud/exp). For an ID-JAG the aud is the
	// Resource AS issuer identifier (per draft-ietf-oauth-identity-assertion-authz-grant),
	// not the token endpoint URL. The provider must be safe for concurrent use —
	// Token() may be called from multiple goroutines (e.g., when wrapped in
	// oauth2.ReuseTokenSource).
	AssertionProvider func() (string, error)

	// HTTPClient is the HTTP client to use. If nil, oauthproto.DefaultHTTPClient()
	// is used.
	HTTPClient *http.Client

	// InsecureAllowHTTP allows plain HTTP for the TokenURL. Only set this for
	// in-cluster HTTP endpoints (e.g., development or testing environments).
	InsecureAllowHTTP bool
}

// ValidateTokenURL checks that a token endpoint URL is syntactically valid: an
// http or https scheme, a host, no fragment, and no embedded credentials. The
// http(s)-only scheme check is enforced unconditionally, even when
// insecureAllowHTTP is true — insecureAllowHTTP only relaxes the HTTPS
// requirement (permitting plain HTTP on any host, not just localhost), it does
// not permit other schemes such as ftp://. label prefixes every error message
// (e.g. "TokenURL", "IDPTokenURL") so call sites can identify which field
// failed. Shared by jwtbearer.Config.Validate and the vMCP XAA strategy
// (pkg/vmcp/auth/strategies), which both validate OAuth token endpoints.
func ValidateTokenURL(label, endpoint string, insecureAllowHTTP bool) error {
	if err := networking.ValidateEndpointURLWithInsecure(endpoint, insecureAllowHTTP); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", label, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s must use http or https scheme", label)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must include a host", label)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", label)
	}
	if u.User != nil {
		return fmt.Errorf("%s must not contain embedded credentials", label)
	}
	return nil
}

// Validate checks that the Config contains all required fields.
func (c *Config) Validate() error {
	if c.TokenURL == "" {
		return fmt.Errorf("TokenURL is required")
	}

	if c.AssertionProvider == nil {
		return fmt.Errorf("AssertionProvider is required")
	}

	return ValidateTokenURL("TokenURL", c.TokenURL, c.InsecureAllowHTTP)
}

// String implements fmt.Stringer for Config, redacting sensitive fields.
func (c *Config) String() string {
	assertion := oauthproto.Redact("")
	if c.AssertionProvider != nil {
		assertion = oauthproto.Redact("set")
	}

	return fmt.Sprintf("Config{TokenURL: %s, ClientID: %s, ClientSecret: %s, Scopes: %v, Assertion: %s}",
		c.TokenURL, c.ClientID, oauthproto.Redact(c.ClientSecret), c.Scopes, assertion)
}

// Compile-time assertion that *tokenSource implements oauth2.TokenSource.
var _ oauth2.TokenSource = (*tokenSource)(nil)

// TokenSource returns an oauth2.TokenSource that performs the JWT Bearer grant.
func (c *Config) TokenSource(ctx context.Context) oauth2.TokenSource {
	return &tokenSource{
		ctx:  ctx,
		conf: c,
	}
}

// tokenSource implements oauth2.TokenSource for the JWT Bearer grant.
type tokenSource struct {
	ctx  context.Context
	conf *Config
}

// Token implements the oauth2.TokenSource interface.
// It performs the JWT Bearer grant and returns an oauth2.Token.
func (ts *tokenSource) Token() (*oauth2.Token, error) {
	conf := ts.conf

	if err := conf.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	assertion, err := conf.AssertionProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get assertion: %w", err)
	}

	data := buildFormData(assertion, conf.Scopes)

	req, err := oauthproto.NewFormRequest(ts.ctx, conf.TokenURL, data, conf.ClientID, conf.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("jwtbearer: build request: %w", err)
	}

	resp, err := oauthproto.DoTokenRequest(conf.HTTPClient, req)
	if err != nil {
		// Scrub RetrieveError.Body so raw upstream content cannot leak into
		// error strings via err.Error(). pkg/oauthproto deliberately preserves
		// Body for general-purpose callers; jwtbearer opts back into the
		// stricter behavior because its errors propagate through vmcp / runner
		// paths that may log them. Matches pkg/oauthproto/tokenexchange.
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) {
			retrieveErr.Body = nil
		}
		return nil, err
	}

	// RFC 6749 Section 5.1 requires token_type in the response. The shared
	// oauthproto.ParseTokenResponse is intentionally permissive on this field
	// (matching x/oauth2); the JWT Bearer grant tightens it back.
	if resp.Token.TokenType == "" {
		return nil, fmt.Errorf("jwtbearer: server returned empty token_type (required by RFC 6749 Section 5.1)")
	}

	return resp.Token, nil
}

// buildFormData constructs the form data for a JWT Bearer grant request.
func buildFormData(assertion string, scopes []string) url.Values {
	data := url.Values{}
	data.Set("grant_type", oauthproto.GrantTypeJWTBearer)
	data.Set("assertion", assertion)

	if len(scopes) > 0 {
		data.Set("scope", strings.Join(scopes, " "))
	}

	return data
}

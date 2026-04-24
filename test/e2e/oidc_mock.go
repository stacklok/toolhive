// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	fositeoauth2 "github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/storage"
	"github.com/ory/fosite/token/jwt"
	"golang.org/x/crypto/bcrypt"
)

// OIDCMockServer represents a lightweight OIDC server using Ory Fosite
type OIDCMockServer struct {
	server   *http.Server
	provider fosite.OAuth2Provider
	store    *storage.MemoryStore
	port     int
	rsaKey   *rsa.PrivateKey

	// Channel to capture OAuth requests for auto-completion
	authRequestChan chan *AuthRequest
	autoComplete    bool
}

// AuthRequest contains the parameters from an OAuth authorization request
type AuthRequest struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	ResponseType  string
	Scope         string
}

// jwtKeyID is the key ID used in both the JWKS response and the JWT header.
// pkg/auth's token validator requires the kid claim to look up the signing key.
const jwtKeyID = "test-key-1"

// OIDCMockOption is a unified option type for configuring the OIDC mock server.
// Use WithClientAudience for client-registration settings and WithAccessTokenLifespan
// (or other fosite-level helpers) for token-lifecycle settings. A single constructor
// accepts both, so tests needing both a custom token lifetime and a specific audience
// no longer require separate constructors.
type OIDCMockOption struct {
	fositeOpt func(*fosite.Config)
	clientOpt func(*fosite.DefaultClient)
}

// WithClientAudience sets the allowed audience(s) on the registered test client.
// Use this when the vMCP OIDC config requires a specific audience claim in tokens.
func WithClientAudience(audiences ...string) OIDCMockOption {
	return OIDCMockOption{clientOpt: func(c *fosite.DefaultClient) {
		c.Audience = audiences
	}}
}

// NewOIDCMockServer creates a new OIDC mock server using Ory Fosite.
// Use WithClientAudience to set client-level options and WithAccessTokenLifespan
// for Fosite-level settings. Both option kinds may be mixed in a single call.
func NewOIDCMockServer(port int, clientID, clientSecret string, opts ...OIDCMockOption) (*OIDCMockServer, error) {
	config := defaultFositeConfig(port)
	for _, opt := range opts {
		if opt.fositeOpt != nil {
			opt.fositeOpt(config)
		}
	}
	return newOIDCMockServer(port, clientID, clientSecret, config, opts...)
}

// defaultFositeConfig returns the standard Fosite config for the mock server.
func defaultFositeConfig(port int) *fosite.Config {
	issuer := fmt.Sprintf("http://localhost:%d", port)
	return &fosite.Config{
		AccessTokenLifespan:   time.Hour,
		RefreshTokenLifespan:  time.Hour * 24,
		AuthorizeCodeLifespan: time.Minute * 10,
		IDTokenLifespan:       time.Hour,
		IDTokenIssuer:         issuer,
		AccessTokenIssuer:     issuer,
		HashCost:              12,
	}
}

// newOIDCMockServer is the shared implementation for NewOIDCMockServer.
func newOIDCMockServer(
	port int, clientID, clientSecret string, config *fosite.Config, opts ...OIDCMockOption,
) (*OIDCMockServer, error) {
	// Generate RSA key for JWT signing
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	// Hash the client secret — Fosite's DefaultClientAuthenticationStrategy uses
	// BCryptHasher.Compare, so the stored secret must be bcrypt-hashed.
	// Use the same cost as the Fosite config to keep them consistent.
	hashedSecret, err := bcrypt.GenerateFromPassword([]byte(clientSecret), config.HashCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash client secret: %w", err)
	}

	// Create memory store and register the test client.
	store := storage.NewMemoryStore()
	client := &fosite.DefaultClient{
		ID:            clientID,
		Secret:        hashedSecret,
		RedirectURIs:  []string{"http://localhost:8080/callback", "http://127.0.0.1:8080/callback"},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token", "client_credentials"},
		Scopes:        []string{"openid", "profile", "email"},
	}
	for _, opt := range opts {
		if opt.clientOpt != nil {
			opt.clientOpt(client)
		}
	}
	store.Clients[clientID] = client

	// Create JWT strategy
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) {
			return key, nil
		},
		compose.NewOAuth2HMACStrategy(config),
		config,
	)

	// Create OpenID Connect strategy
	oidcStrategy := compose.NewOpenIDConnectStrategy(
		func(_ context.Context) (interface{}, error) {
			return key, nil
		},
		config,
	)

	// Create OAuth2 provider with OpenID Connect support
	provider := compose.Compose(
		config,
		store,
		&compose.CommonStrategy{
			CoreStrategy:               jwtStrategy,
			OpenIDConnectTokenStrategy: oidcStrategy,
		},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2ClientCredentialsGrantFactory,
		compose.OpenIDConnectExplicitFactory,
		compose.OAuth2TokenIntrospectionFactory,
	)

	mockServer := &OIDCMockServer{
		provider: provider,
		store:    store,
		port:     port,
		rsaKey:   key,
	}

	// Create HTTP server with routes
	mux := http.NewServeMux()
	mockServer.setupRoutes(mux)

	mockServer.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	return mockServer, nil
}

// setupRoutes configures the HTTP routes for the OIDC server
func (m *OIDCMockServer) setupRoutes(mux *http.ServeMux) {
	// OIDC Discovery endpoint
	mux.HandleFunc("/.well-known/openid-configuration", m.handleDiscovery)

	// OAuth2/OIDC endpoints
	mux.HandleFunc("/auth", m.handleAuthorize)
	mux.HandleFunc("/token", m.handleToken)
	mux.HandleFunc("/userinfo", m.handleUserInfo)
	mux.HandleFunc("/jwks", m.handleJWKS)

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
}

// handleDiscovery serves the OIDC discovery document
func (m *OIDCMockServer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	discovery := map[string]interface{}{
		"issuer":                                fmt.Sprintf("http://localhost:%d", m.port),
		"authorization_endpoint":                fmt.Sprintf("http://localhost:%d/auth", m.port),
		"token_endpoint":                        fmt.Sprintf("http://localhost:%d/token", m.port),
		"userinfo_endpoint":                     fmt.Sprintf("http://localhost:%d/userinfo", m.port),
		"jwks_uri":                              fmt.Sprintf("http://localhost:%d/jwks", m.port),
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(discovery); err != nil {
		http.Error(w, "Failed to encode discovery document", http.StatusInternalServerError)
	}
}

// handleAuthorize handles OAuth2 authorization requests
func (m *OIDCMockServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Capture auth request parameters if auto-complete is enabled
	if m.autoComplete && m.authRequestChan != nil {
		authReq := &AuthRequest{
			ClientID:      r.URL.Query().Get("client_id"),
			RedirectURI:   r.URL.Query().Get("redirect_uri"),
			State:         r.URL.Query().Get("state"),
			CodeChallenge: r.URL.Query().Get("code_challenge"),
			ResponseType:  r.URL.Query().Get("response_type"),
			Scope:         r.URL.Query().Get("scope"),
		}

		// Send to channel (non-blocking)
		select {
		case m.authRequestChan <- authReq:
		default:
			// Channel full, ignore
		}
	}

	// Check for auto-complete parameter for testing
	if r.URL.Query().Get("auto_complete") == "true" {
		// For testing: automatically redirect to callback with success
		redirectURI := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		if redirectURI != "" {
			callbackURL := fmt.Sprintf("%s?code=test-auth-code&state=%s", redirectURI, state)
			http.Redirect(w, r, callbackURL, http.StatusFound)
			return
		}
	}

	// Create authorization request
	ar, err := m.provider.NewAuthorizeRequest(ctx, r)
	if err != nil {
		m.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}

	// Check if client ID is valid (for testing invalid credentials)
	clientID := ar.GetClient().GetID()
	if clientID == "invalid-client" {
		// Return unauthorized error for invalid client
		err := fosite.ErrInvalidClient.WithHint("Client authentication failed")
		m.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}

	// For testing purposes, auto-approve the request
	// In a real server, this would involve user authentication and consent
	session := &openid.DefaultSession{
		Claims: &jwt.IDTokenClaims{
			Subject:   "test-user",
			Issuer:    fmt.Sprintf("http://localhost:%d", m.port),
			Audience:  []string{ar.GetClient().GetID()},
			ExpiresAt: time.Now().Add(time.Hour),
			IssuedAt:  time.Now(),
		},
		Headers: &jwt.Headers{},
		Subject: "test-user",
	}

	// Grant all requested scopes
	for _, scope := range ar.GetRequestedScopes() {
		ar.GrantScope(scope)
	}

	// Create authorization response
	response, err := m.provider.NewAuthorizeResponse(ctx, ar, session)
	if err != nil {
		m.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}

	m.provider.WriteAuthorizeResponse(ctx, w, ar, response)
}

// handleToken handles OAuth2 token requests
func (m *OIDCMockServer) handleToken(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Check for test auth code from auto-complete flow
	if r.FormValue("code") == "test-auth-code" { //nolint:gosec // G120 - test-only mock server
		// Return a test token directly for auto-complete flow
		tokenResponse := map[string]interface{}{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        "openid profile email",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse)
		return
	}

	// Create token request.
	// Use JWTSession so DefaultJWTStrategy can populate JWT claims;
	// openid.DefaultSession does not implement JWTSessionContainer and causes
	// a 500 for client_credentials flows.
	accessRequest, err := m.provider.NewAccessRequest(ctx, r, &fositeoauth2.JWTSession{})
	if err != nil {
		m.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// For client_credentials, grant requested scopes and audiences so they appear
	// in the issued token's scp/aud claims. Other grant types handle this during
	// the authorization step, but client_credentials has no authorization step.
	// Also set the kid in the JWT header so pkg/auth's token validator can look
	// up the signing key in the JWKS by key ID — it rejects tokens without a kid.
	if accessRequest.GetGrantTypes().ExactOne("client_credentials") {
		for _, scope := range accessRequest.GetRequestedScopes() {
			accessRequest.GrantScope(scope)
		}
		for _, aud := range accessRequest.GetRequestedAudience() {
			accessRequest.GrantAudience(aud)
		}
		if jwtSess, ok := accessRequest.GetSession().(*fositeoauth2.JWTSession); ok {
			jwtSess.GetJWTHeader().Add("kid", jwtKeyID)
			// Set subject to the client ID — OIDC Core § 5.1 requires a non-empty
			// sub claim and pkg/auth rejects tokens without one.
			if jwtClaims, ok := jwtSess.GetJWTClaims().(*jwt.JWTClaims); ok {
				jwtClaims.Subject = accessRequest.GetClient().GetID()
			}
		}
	}

	// Create token response
	response, err := m.provider.NewAccessResponse(ctx, accessRequest)
	if err != nil {
		m.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	m.provider.WriteAccessResponse(ctx, w, accessRequest, response)
}

// handleUserInfo handles userinfo requests
func (m *OIDCMockServer) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// Validate access token
	_, _, err := m.provider.IntrospectToken(ctx, fosite.AccessTokenFromRequest(r), fosite.AccessToken, &openid.DefaultSession{})
	if err != nil {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	// Return mock user info
	userInfo := map[string]interface{}{
		"sub":   "test-user",
		"name":  "Test User",
		"email": "test@example.com",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userInfo)
}

// handleJWKS handles JWKS requests
func (m *OIDCMockServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	// Extract public key components
	publicKey := m.rsaKey.Public().(*rsa.PublicKey)
	n := publicKey.N
	e := publicKey.E

	// Convert to base64url format
	nBytes := n.Bytes()
	eBytes := make([]byte, 4)
	eBytes[0] = byte(e >> 24) //nolint:gosec // G115: RSA exponent fits in 4 bytes
	eBytes[1] = byte(e >> 16) //nolint:gosec // G115: RSA exponent fits in 4 bytes
	eBytes[2] = byte(e >> 8)  //nolint:gosec // G115: RSA exponent fits in 4 bytes
	eBytes[3] = byte(e)       //nolint:gosec // G115: RSA exponent fits in 4 bytes

	// Trim leading zeros from exponent
	eStart := 0
	for eStart < len(eBytes) && eBytes[eStart] == 0 {
		eStart++
	}
	eBytes = eBytes[eStart:]

	nB64 := base64.RawURLEncoding.EncodeToString(nBytes)
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"use": "sig",
				"kid": jwtKeyID,
				"alg": "RS256",
				"n":   nB64,
				"e":   eB64,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jwks)
}

// Start starts the OIDC mock server
func (m *OIDCMockServer) Start() error {
	go func() {
		if err := m.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("OIDC mock server error: %v\n", err)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)
	return nil
}

// Stop stops the OIDC mock server
func (m *OIDCMockServer) Stop() error {
	if m.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.server.Shutdown(ctx)
	}
	return nil
}

// GetBaseURL returns the base URL of the mock server
func (m *OIDCMockServer) GetBaseURL() string {
	return fmt.Sprintf("http://localhost:%d", m.port)
}

// EnableAutoComplete enables automatic OAuth flow completion for testing
func (m *OIDCMockServer) EnableAutoComplete() {
	m.autoComplete = true
	m.authRequestChan = make(chan *AuthRequest, 1)
}

// WaitForAuthRequest waits for an OAuth authorization request and returns its parameters
func (m *OIDCMockServer) WaitForAuthRequest(timeout time.Duration) (*AuthRequest, error) {
	if m.authRequestChan == nil {
		return nil, fmt.Errorf("auto-complete not enabled")
	}

	select {
	case req := <-m.authRequestChan:
		return req, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for auth request")
	}
}

// CompleteAuthRequest automatically completes an OAuth request by making a callback
func (*OIDCMockServer) CompleteAuthRequest(authReq *AuthRequest) error {
	if authReq.RedirectURI == "" {
		return fmt.Errorf("no redirect URI in auth request")
	}

	// Make a request to the callback URL with the authorization code
	callbackURL := fmt.Sprintf("%s?code=test-auth-code&state=%s", authReq.RedirectURI, authReq.State)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(callbackURL)
	if err != nil {
		return fmt.Errorf("failed to complete auth request: %w", err)
	}
	defer func() {
		// Error ignored in test cleanup
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("callback failed with status: %d", resp.StatusCode)
	}

	return nil
}

// WithAccessTokenLifespan sets the lifespan of access tokens for the OIDC mock server.
func WithAccessTokenLifespan(d time.Duration) OIDCMockOption {
	return OIDCMockOption{fositeOpt: func(c *fosite.Config) {
		c.AccessTokenLifespan = d
	}}
}

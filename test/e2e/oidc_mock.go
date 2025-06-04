package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/storage"
	"github.com/ory/fosite/token/jwt"
)

// OIDCMockServer represents a lightweight OIDC server using Ory Fosite
type OIDCMockServer struct {
	server   *http.Server
	provider fosite.OAuth2Provider
	store    *storage.MemoryStore
	port     int

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

// NewOIDCMockServer creates a new OIDC mock server using Ory Fosite
func NewOIDCMockServer(port int, clientID, clientSecret string) (*OIDCMockServer, error) {
	// Generate RSA key for JWT signing
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	// Create memory store
	store := storage.NewMemoryStore()

	// Add test client
	client := &fosite.DefaultClient{
		ID:            clientID,
		Secret:        []byte(clientSecret),
		RedirectURIs:  []string{"http://localhost:8080/callback", "http://127.0.0.1:8080/callback"},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        []string{"openid", "profile", "email"},
	}
	store.Clients[clientID] = client

	// Create Fosite configuration
	config := &fosite.Config{
		AccessTokenLifespan:   time.Hour,
		RefreshTokenLifespan:  time.Hour * 24,
		AuthorizeCodeLifespan: time.Minute * 10,
		IDTokenLifespan:       time.Hour,
		IDTokenIssuer:         fmt.Sprintf("http://localhost:%d", port),
		HashCost:              12,
	}

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
		compose.OpenIDConnectExplicitFactory,
		compose.OAuth2TokenIntrospectionFactory,
	)

	mockServer := &OIDCMockServer{
		provider: provider,
		store:    store,
		port:     port,
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
	if r.FormValue("code") == "test-auth-code" {
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

	// Create token request
	accessRequest, err := m.provider.NewAccessRequest(ctx, r, &openid.DefaultSession{})
	if err != nil {
		m.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
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
func (*OIDCMockServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	// For simplicity, return empty JWKS
	// In a real implementation, you'd return the public keys
	jwks := map[string]interface{}{
		"keys": []interface{}{},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jwks)
}

// Start starts the OIDC mock server
func (m *OIDCMockServer) Start() error {
	go func() {
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("callback failed with status: %d", resp.StatusCode)
	}

	return nil
}

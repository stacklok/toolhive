package authserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	"github.com/stacklok/toolhive/pkg/logger"
)

// HandlerResult contains the handlers and resources created by CreateHandlers.
type HandlerResult struct {
	// OAuthMux handles OAuth endpoints (/oauth/authorize, /oauth/token)
	OAuthMux http.Handler

	// WellKnownMux handles well-known endpoints (/.well-known/*)
	WellKnownMux http.Handler

	// Storage is the storage instance (implements IDPTokenStorage)
	Storage Storage
}

// IDPTokenStorage returns the IDP token storage interface.
// This allows callers to access IDP token storage without coupling to the concrete Storage type.
func (r *HandlerResult) IDPTokenStorage() IDPTokenStorage {
	return r.Storage
}

// CreateHandlers creates auth server HTTP handlers from RunConfig.
// Returns nil handlers if config is nil or not enabled.
// The proxyPort is used to resolve :0 in the issuer URL.
// This is the backward-compatible version that creates its own storage internally.
func CreateHandlers(
	ctx context.Context,
	cfg *RunConfig,
	proxyPort int,
) (oauthMux http.Handler, wellKnownMux http.Handler, err error) {
	result, err := CreateHandlersWithResult(ctx, cfg, proxyPort)
	if err != nil {
		return nil, nil, err
	}
	if result == nil {
		return nil, nil, nil
	}
	return result.OAuthMux, result.WellKnownMux, nil
}

// CreateHandlersWithResult creates auth server HTTP handlers and returns a HandlerResult
// that includes access to the storage for sharing with middleware.
// Returns nil if config is nil or not enabled.
func CreateHandlersWithResult(
	ctx context.Context,
	cfg *RunConfig,
	proxyPort int,
) (*HandlerResult, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	// Create default storage
	storage, err := NewStorage(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	return CreateHandlersWithStorage(ctx, cfg, proxyPort, storage)
}

// CreateHandlersWithStorage creates OAuth and well-known handlers using provided storage.
// This allows sharing storage between auth server and other components like middleware.
// Returns nil if config is nil or not enabled.
func CreateHandlersWithStorage(
	ctx context.Context,
	cfg *RunConfig,
	proxyPort int,
	storage Storage,
) (*HandlerResult, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	if storage == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid auth server config: %w", err)
	}

	// Resolve issuer URL - replace :0 with actual port if needed
	issuer := resolveIssuer(cfg.Issuer, proxyPort)

	// Load signing key from file
	rsaKey, err := LoadSigningKey(cfg.SigningKeyPath)
	if err != nil {
		return nil, err
	}

	// Build internal config from RunConfig
	internalConfig, err := cfg.toInternalConfig(issuer, rsaKey)
	if err != nil {
		return nil, err
	}

	// Use existing package functions to create components
	oauth2Config, err := NewOAuth2Config(internalConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 config: %w", err)
	}

	// Cast storage to *MemoryStorage for registerClients and createProvider
	memStorage, ok := storage.(*MemoryStorage)
	if !ok {
		return nil, fmt.Errorf("storage must be *MemoryStorage (other implementations not yet supported)")
	}

	registerClients(memStorage, cfg.Clients)

	provider := createProvider(oauth2Config, memStorage)

	// Create router with optional upstream
	routerOpts, err := createRouterOpts(ctx, cfg.Upstream, issuer)
	if err != nil {
		return nil, err
	}

	router := NewRouter(slog.Default(), provider, oauth2Config, memStorage, routerOpts...)

	// Create and populate muxes
	oauthServeMux := http.NewServeMux()
	wellKnownServeMux := http.NewServeMux()
	router.OAuthRoutes(oauthServeMux)
	router.WellKnownRoutes(wellKnownServeMux)

	logger.Infof("Embedded OAuth authorization server configured with issuer: %s", issuer)

	return &HandlerResult{
		OAuthMux:     oauthServeMux,
		WellKnownMux: wellKnownServeMux,
		Storage:      storage,
	}, nil
}

// LoadSigningKey loads an RSA private key from a PEM file.
// Supports both PKCS1 and PKCS8 formats.
func LoadSigningKey(keyPath string) (*rsa.PrivateKey, error) {
	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 - keyPath is provided by user via CLI flag or config
	if err != nil {
		return nil, fmt.Errorf("failed to read signing key: %w", err)
	}

	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from signing key")
	}

	// Try PKCS1 first
	if rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return rsaKey, nil
	}

	// Try PKCS8
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse signing key: %w", err)
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is not an RSA key")
	}

	return rsaKey, nil
}

// toInternalConfig converts RunConfig to the internal Config struct.
func (c *RunConfig) toInternalConfig(issuer string, rsaKey *rsa.PrivateKey) (*Config, error) {
	accessTokenLifespan := c.AccessTokenLifespan
	if accessTokenLifespan == 0 {
		accessTokenLifespan = time.Hour
	}

	refreshTokenLifespan := c.RefreshTokenLifespan
	if refreshTokenLifespan == 0 {
		refreshTokenLifespan = 24 * time.Hour
	}

	// Generate random HMAC secret at startup. This is safe because:
	// 1. Authorization codes are stored in memory anyway (no persistence across restarts)
	// 2. The HMAC secret only needs to be consistent within a single instance
	// 3. Random generation provides better security than a static value
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("failed to generate HMAC secret: %w", err)
	}

	config := &Config{
		Issuer:               issuer,
		AccessTokenLifespan:  accessTokenLifespan,
		RefreshTokenLifespan: refreshTokenLifespan,
		AuthCodeLifespan:     10 * time.Minute,
		Secret:               secret,
		PrivateKeys: []PrivateKey{{
			KeyID:     "key-1",
			Algorithm: "RS256",
			Key:       rsaKey,
		}},
	}

	// Configure upstream if present
	if c.Upstream != nil && c.Upstream.Issuer != "" {
		clientSecret, err := c.Upstream.resolveClientSecret()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve upstream client secret: %w", err)
		}

		config.Upstream = UpstreamConfig{
			Issuer:       c.Upstream.Issuer,
			ClientID:     c.Upstream.ClientID,
			ClientSecret: clientSecret,
			Scopes:       c.Upstream.Scopes,
			RedirectURI:  issuer + "/oauth/callback",
		}
	}

	return config, nil
}

// resolveClientSecret returns the client secret using the following order of precedence:
// 1. ClientSecret (direct config value)
// 2. ClientSecretFile (read from file)
// 3. UpstreamClientSecretEnvVar environment variable (fallback)
func (c *RunUpstreamConfig) resolveClientSecret() (string, error) {
	// 1. Direct config value takes precedence
	if c.ClientSecret != "" {
		return c.ClientSecret, nil
	}

	// 2. Read from file if specified
	if c.ClientSecretFile != "" {
		data, err := os.ReadFile(c.ClientSecretFile) // #nosec G304 - file path is provided by user via config
		if err != nil {
			return "", fmt.Errorf("failed to read client secret file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	// 3. Fallback to environment variable
	if envSecret := os.Getenv(UpstreamClientSecretEnvVar); envSecret != "" {
		logger.Debug("Using upstream client secret from environment variable")
		return envSecret, nil
	}

	return "", nil
}

// resolveIssuer replaces :0 in issuer with actual port.
func resolveIssuer(issuer string, proxyPort int) string {
	if proxyPort > 0 && strings.Contains(issuer, ":0") {
		return strings.Replace(issuer, ":0", fmt.Sprintf(":%d", proxyPort), 1)
	}
	return issuer
}

// registerClients adds clients from config to storage.
// Public clients are wrapped in LoopbackClient to support RFC 8252 Section 7.3
// compliant loopback redirect URI matching for native OAuth clients.
func registerClients(storage *MemoryStorage, clients []RunClientConfig) {
	for _, c := range clients {
		defaultClient := &fosite.DefaultClient{
			ID:            c.ID,
			RedirectURIs:  c.RedirectURIs,
			ResponseTypes: []string{"code"},
			GrantTypes:    []string{"authorization_code", "refresh_token"},
			Scopes:        []string{"openid", "profile", "email"},
			Public:        c.Public,
		}
		if !c.Public && c.Secret != "" {
			defaultClient.Secret = []byte(c.Secret)
		}

		// Use LoopbackClient for public clients to support RFC 8252 Section 7.3
		// dynamic port matching for native app loopback redirect URIs.
		var client fosite.Client
		if c.Public {
			client = NewLoopbackClient(defaultClient)
		} else {
			client = defaultClient
		}
		storage.RegisterClient(client)
	}
}

// createProvider creates a fosite provider with JWT strategy.
func createProvider(oauth2Config *OAuth2Config, storage *MemoryStorage) fosite.OAuth2Provider {
	// Convert v4 JWK to v3 JWK for fosite compatibility.
	// Fosite v0.49.0 uses go-jose/v3, not v4.
	// This ensures the kid is included in the JWT header.
	signingKeyV4 := oauth2Config.SigningKey
	signingKeyV3 := &josev3.JSONWebKey{
		Key:       signingKeyV4.Key,
		KeyID:     signingKeyV4.KeyID,
		Algorithm: signingKeyV4.Algorithm,
		Use:       signingKeyV4.Use,
	}

	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) { return signingKeyV3, nil },
		compose.NewOAuth2HMACStrategy(oauth2Config.Config),
		oauth2Config.Config,
	)

	return compose.Compose(
		oauth2Config.Config,
		storage,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
	)
}

// createRouterOpts creates router options, including upstream provider if configured.
func createRouterOpts(ctx context.Context, upstream *RunUpstreamConfig, issuer string) ([]RouterOption, error) {
	if upstream == nil || upstream.Issuer == "" {
		return nil, nil
	}

	clientSecret, err := upstream.resolveClientSecret()
	if err != nil {
		return nil, err
	}

	upstreamCfg := UpstreamConfig{
		Issuer:       upstream.Issuer,
		ClientID:     upstream.ClientID,
		ClientSecret: clientSecret,
		Scopes:       upstream.Scopes,
		RedirectURI:  issuer + "/oauth/callback",
	}

	upstreamProvider, err := NewOIDCUpstreamProvider(ctx, upstreamCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create upstream provider: %w", err)
	}

	return []RouterOption{WithUpstreamProvider(upstreamProvider)}, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runner provides integration between the proxy runner and the auth server.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/logger"
)

// EmbeddedAuthServer wraps the authorization server for integration with the proxy runner.
// It handles configuration transformation from authserver.RunConfig to authserver.Config,
// manages resource lifecycle, and provides HTTP handlers for OAuth/OIDC endpoints.
type EmbeddedAuthServer struct {
	server  authserver.Server
	storage storage.Storage
}

// NewEmbeddedAuthServer creates an EmbeddedAuthServer from authserver.RunConfig.
// It loads signing keys from files, reads HMAC secrets from files,
// resolves the upstream client secret from file or environment variable, and initializes
// all auth server components.
//
// The cfg parameter contains file paths and environment variable names that are
// resolved at runtime to build the underlying authserver.Config.
func NewEmbeddedAuthServer(ctx context.Context, cfg *authserver.RunConfig) (*EmbeddedAuthServer, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	// 1. Create key provider from RunConfig.SigningKeyConfig
	keyProvider, err := createKeyProvider(cfg.SigningKeyConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create key provider: %w", err)
	}

	// 2. Load HMAC secrets from files
	hmacSecrets, err := loadHMACSecrets(cfg.HMACSecretFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to load HMAC secrets: %w", err)
	}

	// 3. Parse token lifespans
	accessLifespan, refreshLifespan, authCodeLifespan, err := parseTokenLifespans(cfg.TokenLifespans)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token lifespans: %w", err)
	}

	// 4. Build upstream configurations
	upstreams, err := buildUpstreamConfigs(ctx, cfg.Upstreams)
	if err != nil {
		return nil, fmt.Errorf("failed to build upstream configs: %w", err)
	}

	// 5. Build the resolved Config
	resolvedCfg := authserver.Config{
		Issuer:               cfg.Issuer,
		KeyProvider:          keyProvider,
		HMACSecrets:          hmacSecrets,
		AccessTokenLifespan:  accessLifespan,
		RefreshTokenLifespan: refreshLifespan,
		AuthCodeLifespan:     authCodeLifespan,
		Upstreams:            upstreams,
		ScopesSupported:      cfg.ScopesSupported,
		AllowedAudiences:     cfg.AllowedAudiences,
	}

	// 6. Create storage (in-memory for single-instance deployments)
	stor := storage.NewMemoryStorage()

	// 7. Create the auth server
	server, err := authserver.New(ctx, resolvedCfg, stor)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth server: %w", err)
	}

	return &EmbeddedAuthServer{
		server:  server,
		storage: stor,
	}, nil
}

// Handler returns the HTTP handler for OAuth/OIDC endpoints.
// The handler uses internal chi routing and serves all endpoints:
//   - /oauth/authorize, /oauth/callback, /oauth/token, /oauth/register
//   - /.well-known/jwks.json, /.well-known/oauth-authorization-server, /.well-known/openid-configuration
func (e *EmbeddedAuthServer) Handler() http.Handler {
	return e.server.Handler()
}

// Close releases resources held by the EmbeddedAuthServer.
// Should be called during runner shutdown.
func (e *EmbeddedAuthServer) Close() error {
	return e.server.Close()
}

// createKeyProvider creates a KeyProvider from SigningKeyRunConfig.
// Returns a GeneratingProvider if config is nil or empty (development mode).
func createKeyProvider(cfg *authserver.SigningKeyRunConfig) (keys.KeyProvider, error) {
	if cfg == nil || cfg.SigningKeyFile == "" {
		// Development mode: use ephemeral key
		return keys.NewGeneratingProvider(keys.DefaultAlgorithm), nil
	}

	keyCfg := keys.Config{
		KeyDir:           cfg.KeyDir,
		SigningKeyFile:   cfg.SigningKeyFile,
		FallbackKeyFiles: cfg.FallbackKeyFiles,
	}

	return keys.NewFileProvider(keyCfg)
}

// loadHMACSecrets reads HMAC secrets from files.
// Returns nil if no files are configured (development mode - authserver will generate ephemeral secret).
func loadHMACSecrets(files []string) (*servercrypto.HMACSecrets, error) {
	if len(files) == 0 {
		// Development mode: let authserver generate ephemeral secret
		return nil, nil
	}

	// Read current (first) secret
	current, err := os.ReadFile(files[0])
	if err != nil {
		return nil, fmt.Errorf("failed to read HMAC secret from %s: %w", files[0], err)
	}

	// Trim whitespace (Kubernetes Secret mounts may include trailing newlines)
	current = bytes.TrimSpace(current)

	secrets := &servercrypto.HMACSecrets{
		Current: current,
	}

	// Read rotated secrets (remaining files)
	for _, file := range files[1:] {
		if file == "" {
			continue // Skip empty paths
		}
		// #nosec G304 - file path is from configuration, not user input
		secret, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read rotated HMAC secret from %s: %w", file, err)
		}
		secrets.Rotated = append(secrets.Rotated, bytes.TrimSpace(secret))
	}

	return secrets, nil
}

// parseTokenLifespans parses duration strings from TokenLifespanRunConfig.
// Returns zero values for unset durations (defaults applied by authserver).
func parseTokenLifespans(cfg *authserver.TokenLifespanRunConfig) (access, refresh, authCode time.Duration, err error) {
	if cfg == nil {
		return 0, 0, 0, nil
	}

	if cfg.AccessTokenLifespan != "" {
		access, err = time.ParseDuration(cfg.AccessTokenLifespan)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid access token lifespan: %w", err)
		}
	}

	if cfg.RefreshTokenLifespan != "" {
		refresh, err = time.ParseDuration(cfg.RefreshTokenLifespan)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid refresh token lifespan: %w", err)
		}
	}

	if cfg.AuthCodeLifespan != "" {
		authCode, err = time.ParseDuration(cfg.AuthCodeLifespan)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid auth code lifespan: %w", err)
		}
	}

	return access, refresh, authCode, nil
}

// buildUpstreamConfigs converts UpstreamRunConfig slice to UpstreamConfig slice.
func buildUpstreamConfigs(ctx context.Context, runConfigs []authserver.UpstreamRunConfig) ([]authserver.UpstreamConfig, error) {
	configs := make([]authserver.UpstreamConfig, 0, len(runConfigs))

	for _, rc := range runConfigs {
		oauthCfg, err := buildOAuth2Config(ctx, &rc)
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", rc.Name, err)
		}

		configs = append(configs, authserver.UpstreamConfig{
			Name:   rc.Name,
			Config: oauthCfg,
		})
	}

	return configs, nil
}

// buildOAuth2Config builds an upstream.OAuth2Config from UpstreamRunConfig.
func buildOAuth2Config(ctx context.Context, rc *authserver.UpstreamRunConfig) (*upstream.OAuth2Config, error) {
	switch rc.Type {
	case authserver.UpstreamProviderTypeOIDC:
		return buildOIDCConfig(ctx, rc)
	case authserver.UpstreamProviderTypeOAuth2:
		return buildPureOAuth2Config(rc)
	default:
		return nil, fmt.Errorf("unsupported upstream type: %s", rc.Type)
	}
}

// buildOIDCConfig builds an upstream.OAuth2Config for an OIDC provider.
// It performs OIDC discovery to resolve the authorization and token endpoints.
func buildOIDCConfig(ctx context.Context, rc *authserver.UpstreamRunConfig) (*upstream.OAuth2Config, error) {
	if rc.OIDCConfig == nil {
		return nil, fmt.Errorf("oidc_config required for OIDC provider")
	}

	oidc := rc.OIDCConfig
	clientSecret, err := resolveSecret(oidc.ClientSecretFile, oidc.ClientSecretEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OIDC client secret: %w", err)
	}

	// Perform OIDC discovery to get the actual endpoints
	discoveryDoc, err := oauth.DiscoverOIDCEndpoints(ctx, oidc.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for %s: %w", oidc.IssuerURL, err)
	}

	// Build UserInfo config - use override if provided, otherwise use discovered endpoint
	userInfoCfg := convertUserInfoConfig(oidc.UserInfoOverride)
	if userInfoCfg == nil && discoveryDoc.UserinfoEndpoint != "" {
		userInfoCfg = &upstream.UserInfoConfig{
			EndpointURL: discoveryDoc.UserinfoEndpoint,
		}
	}

	return &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:     oidc.ClientID,
			ClientSecret: clientSecret,
			RedirectURI:  oidc.RedirectURI,
			Scopes:       oidc.Scopes,
		},
		AuthorizationEndpoint: discoveryDoc.AuthorizationEndpoint,
		TokenEndpoint:         discoveryDoc.TokenEndpoint,
		UserInfo:              userInfoCfg,
	}, nil
}

// buildPureOAuth2Config builds an upstream.OAuth2Config for a pure OAuth2 provider.
func buildPureOAuth2Config(rc *authserver.UpstreamRunConfig) (*upstream.OAuth2Config, error) {
	if rc.OAuth2Config == nil {
		return nil, fmt.Errorf("oauth2_config required for OAuth2 provider")
	}

	oauth2 := rc.OAuth2Config
	clientSecret, err := resolveSecret(oauth2.ClientSecretFile, oauth2.ClientSecretEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OAuth2 client secret: %w", err)
	}

	return &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:     oauth2.ClientID,
			ClientSecret: clientSecret,
			RedirectURI:  oauth2.RedirectURI,
			Scopes:       oauth2.Scopes,
		},
		AuthorizationEndpoint: oauth2.AuthorizationEndpoint,
		TokenEndpoint:         oauth2.TokenEndpoint,
		UserInfo:              convertUserInfoConfig(oauth2.UserInfo),
	}, nil
}

// resolveSecret reads a secret from file or environment variable.
// File takes precedence over env var. Returns an error if file is specified but
// unreadable, or if envVar is specified but not set. Returns empty string with
// no error if neither file nor envVar is specified.
func resolveSecret(file, envVar string) (string, error) {
	if file != "" {
		// #nosec G304 - file path is from configuration, not user input
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("failed to read secret file %q: %w", file, err)
		}
		return string(bytes.TrimSpace(data)), nil
	}
	if envVar != "" {
		value := os.Getenv(envVar)
		if value == "" {
			return "", fmt.Errorf("environment variable %q is not set", envVar)
		}
		return value, nil
	}
	logger.Infof("No client secret configured (neither file nor env var specified)")
	return "", nil
}

// convertUserInfoConfig converts UserInfoRunConfig to upstream.UserInfoConfig.
func convertUserInfoConfig(rc *authserver.UserInfoRunConfig) *upstream.UserInfoConfig {
	if rc == nil {
		return nil
	}
	return &upstream.UserInfoConfig{
		EndpointURL:       rc.EndpointURL,
		HTTPMethod:        rc.HTTPMethod,
		AdditionalHeaders: rc.AdditionalHeaders,
		FieldMapping:      convertFieldMapping(rc.FieldMapping),
	}
}

// convertFieldMapping converts UserInfoFieldMappingRunConfig to upstream.UserInfoFieldMapping.
func convertFieldMapping(rc *authserver.UserInfoFieldMappingRunConfig) *upstream.UserInfoFieldMapping {
	if rc == nil {
		return nil
	}
	return &upstream.UserInfoFieldMapping{
		SubjectFields: rc.SubjectFields,
		NameFields:    rc.NameFields,
		EmailFields:   rc.EmailFields,
	}
}

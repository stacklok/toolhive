// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runner provides integration between the proxy runner and the auth server.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// Redis ACL credential environment variable names.
// These are set by the operator when Redis storage is configured.
const (
	// RedisUsernameEnvVar is the environment variable for the Redis ACL username.
	// #nosec G101 -- This is an environment variable name, not a hardcoded credential
	RedisUsernameEnvVar = "TOOLHIVE_AUTH_SERVER_REDIS_USERNAME"

	// RedisPasswordEnvVar is the environment variable for the Redis ACL password.
	// #nosec G101 -- This is an environment variable name, not a hardcoded credential
	RedisPasswordEnvVar = "TOOLHIVE_AUTH_SERVER_REDIS_PASSWORD"
)

// EmbeddedAuthServer wraps the authorization server for integration with the proxy runner.
// It handles configuration transformation from authserver.RunConfig to authserver.Config,
// manages resource lifecycle, and provides HTTP handlers for OAuth/OIDC endpoints.
type EmbeddedAuthServer struct {
	server    authserver.Server
	closeOnce sync.Once
	closeErr  error
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

	// 6. Create storage backend based on configuration
	stor, err := createStorage(ctx, cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	// 7. Create the auth server
	server, err := authserver.New(ctx, resolvedCfg, stor)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth server: %w", err)
	}

	return &EmbeddedAuthServer{
		server: server,
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
// This method is idempotent - subsequent calls after the first will return
// the same error (if any) without attempting to close resources again.
// Should be called during runner shutdown.
func (e *EmbeddedAuthServer) Close() error {
	e.closeOnce.Do(func() {
		e.closeErr = e.server.Close()
	})
	return e.closeErr
}

// IDPTokenStorage returns storage for upstream IDP tokens.
// Returns nil if no upstream IDP is configured.
// This is used by the upstream swap middleware to exchange ToolHive JWTs
// for upstream IDP tokens.
func (e *EmbeddedAuthServer) IDPTokenStorage() storage.UpstreamTokenStorage {
	return e.server.IDPTokenStorage()
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
	// #nosec G304 - file path is from configuration, not user input
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
// It preserves the provider type so the factory can create the correct provider
// (OIDCProviderImpl for OIDC, BaseOAuth2Provider for OAuth2).
func buildUpstreamConfigs(_ context.Context, runConfigs []authserver.UpstreamRunConfig) ([]authserver.UpstreamConfig, error) {
	configs := make([]authserver.UpstreamConfig, 0, len(runConfigs))

	for _, rc := range runConfigs {
		cfg, err := buildUpstreamConfig(&rc)
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", rc.Name, err)
		}
		configs = append(configs, *cfg)
	}

	return configs, nil
}

// buildUpstreamConfig builds an authserver.UpstreamConfig from UpstreamRunConfig.
// It preserves the provider type and builds the appropriate config.
func buildUpstreamConfig(rc *authserver.UpstreamRunConfig) (*authserver.UpstreamConfig, error) {
	switch rc.Type {
	case authserver.UpstreamProviderTypeOIDC:
		oidcCfg, err := buildOIDCConfig(rc)
		if err != nil {
			return nil, err
		}
		return &authserver.UpstreamConfig{
			Name:       rc.Name,
			Type:       authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: oidcCfg,
		}, nil

	case authserver.UpstreamProviderTypeOAuth2:
		oauth2Cfg, err := buildPureOAuth2Config(rc)
		if err != nil {
			return nil, err
		}
		return &authserver.UpstreamConfig{
			Name:         rc.Name,
			Type:         authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: oauth2Cfg,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported upstream type: %s", rc.Type)
	}
}

// buildOIDCConfig builds an upstream.OIDCConfig for an OIDC provider.
// Discovery is deferred to the provider factory - we only resolve secrets here.
//
// Note: OIDCUpstreamRunConfig.UserInfoOverride is intentionally NOT propagated.
// OIDC providers resolve user identity from the ID token's "sub" claim (validated
// by OIDCProviderImpl.ExchangeCodeForIdentity), not from the UserInfo endpoint.
// The UserInfo endpoint may still be discovered via OIDC discovery for other
// purposes, but it is not used for identity resolution.
func buildOIDCConfig(rc *authserver.UpstreamRunConfig) (*upstream.OIDCConfig, error) {
	if rc.OIDCConfig == nil {
		return nil, fmt.Errorf("oidc_config required for OIDC provider")
	}

	oidc := rc.OIDCConfig

	// Warn if UserInfoOverride is configured but won't be used
	if oidc.UserInfoOverride != nil {
		slog.Warn("userinfo_override is configured for OIDC provider but will not be used; "+
			"OIDC providers resolve identity from the ID token, not the UserInfo endpoint",
			"upstream", rc.Name,
		)
	}

	clientSecret, err := resolveSecret(oidc.ClientSecretFile, oidc.ClientSecretEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OIDC client secret: %w", err)
	}

	// Default scopes if not specified
	scopes := oidc.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "offline_access"}
	}

	return &upstream.OIDCConfig{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:     oidc.ClientID,
			ClientSecret: clientSecret,
			RedirectURI:  oidc.RedirectURI,
			Scopes:       scopes,
		},
		Issuer: oidc.IssuerURL,
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
	slog.Debug("no client secret configured (neither file nor env var specified)")
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

// createStorage creates the appropriate storage backend based on configuration.
func createStorage(ctx context.Context, cfg *storage.RunConfig) (storage.Storage, error) {
	if cfg == nil || cfg.Type == "" || cfg.Type == string(storage.TypeMemory) {
		return storage.NewMemoryStorage(), nil
	}
	if cfg.Type == string(storage.TypeRedis) {
		redisCfg, err := convertRedisRunConfig(cfg.RedisConfig)
		if err != nil {
			return nil, fmt.Errorf("invalid Redis config: %w", err)
		}
		return storage.NewRedisStorage(ctx, *redisCfg)
	}
	return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
}

// convertRedisRunConfig converts a serializable RedisRunConfig to the runtime RedisConfig.
// It resolves credentials from environment variables and parses duration strings.
func convertRedisRunConfig(rc *storage.RedisRunConfig) (*storage.RedisConfig, error) {
	if rc == nil {
		return nil, fmt.Errorf("redis config is required when storage type is redis")
	}

	cfg := &storage.RedisConfig{
		KeyPrefix: rc.KeyPrefix,
	}

	// Convert Sentinel config
	if rc.SentinelConfig == nil {
		return nil, fmt.Errorf("sentinel config is required")
	}
	cfg.SentinelConfig = &storage.SentinelConfig{
		MasterName:    rc.SentinelConfig.MasterName,
		SentinelAddrs: rc.SentinelConfig.SentinelAddrs,
		DB:            rc.SentinelConfig.DB,
	}

	// Resolve ACL credentials from environment variables
	if rc.ACLUserConfig == nil {
		return nil, fmt.Errorf("ACL user config is required")
	}
	username, err := resolveEnvVar(rc.ACLUserConfig.UsernameEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve Redis username: %w", err)
	}
	password, err := resolveEnvVar(rc.ACLUserConfig.PasswordEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve Redis password: %w", err)
	}
	cfg.ACLUserConfig = &storage.ACLUserConfig{
		Username: username,
		Password: password,
	}

	// Parse optional timeouts
	if rc.DialTimeout != "" {
		d, err := time.ParseDuration(rc.DialTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid dial timeout: %w", err)
		}
		cfg.DialTimeout = d
	}
	if rc.ReadTimeout != "" {
		d, err := time.ParseDuration(rc.ReadTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid read timeout: %w", err)
		}
		cfg.ReadTimeout = d
	}
	if rc.WriteTimeout != "" {
		d, err := time.ParseDuration(rc.WriteTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid write timeout: %w", err)
		}
		cfg.WriteTimeout = d
	}

	return cfg, nil
}

// resolveEnvVar reads a value from the named environment variable.
func resolveEnvVar(envVar string) (string, error) {
	if envVar == "" {
		return "", fmt.Errorf("environment variable name is empty")
	}
	value := os.Getenv(envVar)
	if value == "" {
		return "", fmt.Errorf("environment variable %q is not set", envVar)
	}
	return value, nil
}

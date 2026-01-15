// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"crypto"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

// Token lifespan bounds for validation.
const (
	// MinAccessTokenLifespan is the minimum allowed access token lifetime.
	MinAccessTokenLifespan = 1 * time.Minute
	// MaxAccessTokenLifespan is the maximum allowed access token lifetime.
	MaxAccessTokenLifespan = 24 * time.Hour
	// MinRefreshTokenLifespan is the minimum allowed refresh token lifetime.
	MinRefreshTokenLifespan = 1 * time.Hour
	// MaxRefreshTokenLifespan is the maximum allowed refresh token lifetime (30 days).
	MaxRefreshTokenLifespan = 30 * 24 * time.Hour
	// MinAuthCodeLifespan is the minimum allowed authorization code lifetime.
	MinAuthCodeLifespan = 30 * time.Second
	// MaxAuthCodeLifespan is the maximum allowed authorization code lifetime (RFC 6749 recommends 10 min max).
	MaxAuthCodeLifespan = 10 * time.Minute
)

// AuthorizationServerConfig wraps fosite.Config with additional configuration
// for JWT signing and other extensions.
type AuthorizationServerConfig struct {
	*fosite.Config
	SigningKey  *jose.JSONWebKey
	SigningJWKS *jose.JSONWebKeySet
}

// Factory is a constructor which is used to create an OAuth2 endpoint handler.
// NewAuthorizationServer handles consuming the new struct and attaching it
// to the parts of the config that it implements.
//
// The strategy parameter is typed as any because fosite uses different strategy
// interfaces for different flows (e.g., oauth2.CoreStrategy, openid.OpenIDConnectTokenStrategy)
// that do not share a common base interface.
type Factory func(config *AuthorizationServerConfig, storage fosite.Storage, strategy any) any

// AuthorizationServerParams contains the configuration needed to create an AuthorizationServerConfig.
// This is a minimal subset of the authserver.Config fields needed for OAuth2.
type AuthorizationServerParams struct {
	Issuer               string
	AccessTokenLifespan  time.Duration
	RefreshTokenLifespan time.Duration
	AuthCodeLifespan     time.Duration
	HMACSecrets          *servercrypto.HMACSecrets
	SigningKeyID         string
	SigningKeyAlgorithm  string
	SigningKey           crypto.Signer
}

// validateIssuerURL validates that the issuer is a valid URL with http or https scheme
// and no trailing slash. Following the pattern from pkg/config/validation.go.
func validateIssuerURL(issuer string) error {
	if issuer == "" {
		return fmt.Errorf("issuer is required")
	}

	parsedURL, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("issuer is not a valid URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("issuer must use http or https scheme")
	}

	if parsedURL.Host == "" {
		return fmt.Errorf("issuer must have a host")
	}

	if strings.HasSuffix(issuer, "/") {
		return fmt.Errorf("issuer must not have a trailing slash")
	}

	return nil
}

// validateHMACSecrets validates that all HMAC secrets meet the minimum length requirement.
func validateHMACSecrets(secrets *servercrypto.HMACSecrets) error {
	if secrets == nil {
		return fmt.Errorf("HMAC secrets are required")
	}
	if len(secrets.Current) < servercrypto.MinSecretLength {
		return fmt.Errorf("current HMAC secret must be at least %d bytes", servercrypto.MinSecretLength)
	}
	for i, rotated := range secrets.Rotated {
		if len(rotated) < servercrypto.MinSecretLength {
			return fmt.Errorf("rotated HMAC secret [%d] must be at least %d bytes", i, servercrypto.MinSecretLength)
		}
	}
	return nil
}

// NewAuthorizationServerConfig creates an AuthorizationServerConfig from the provided configuration.
func NewAuthorizationServerConfig(cfg *AuthorizationServerParams) (*AuthorizationServerConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := validateIssuerURL(cfg.Issuer); err != nil {
		return nil, err
	}
	if cfg.SigningKeyID == "" {
		return nil, fmt.Errorf("signing key ID is required")
	}
	if cfg.SigningKeyAlgorithm == "" {
		return nil, fmt.Errorf("signing key algorithm is required")
	}
	if cfg.SigningKey == nil {
		return nil, fmt.Errorf("signing key is required")
	}
	if err := validateHMACSecrets(cfg.HMACSecrets); err != nil {
		return nil, err
	}

	// Validate algorithm matches key type
	if err := servercrypto.ValidateAlgorithmForKey(cfg.SigningKeyAlgorithm, cfg.SigningKey); err != nil {
		return nil, fmt.Errorf("invalid signing configuration: %w", err)
	}

	// Validate token lifespans are within reasonable bounds
	if cfg.AccessTokenLifespan < MinAccessTokenLifespan || cfg.AccessTokenLifespan > MaxAccessTokenLifespan {
		return nil, fmt.Errorf("access token lifespan must be between %v and %v", MinAccessTokenLifespan, MaxAccessTokenLifespan)
	}
	if cfg.RefreshTokenLifespan < MinRefreshTokenLifespan || cfg.RefreshTokenLifespan > MaxRefreshTokenLifespan {
		return nil, fmt.Errorf("refresh token lifespan must be between %v and %v", MinRefreshTokenLifespan, MaxRefreshTokenLifespan)
	}
	if cfg.AuthCodeLifespan < MinAuthCodeLifespan || cfg.AuthCodeLifespan > MaxAuthCodeLifespan {
		return nil, fmt.Errorf("authorization code lifespan must be between %v and %v", MinAuthCodeLifespan, MaxAuthCodeLifespan)
	}

	// Build JWK from signing key
	jwk := jose.JSONWebKey{
		Key:       cfg.SigningKey,
		KeyID:     cfg.SigningKeyID,
		Algorithm: cfg.SigningKeyAlgorithm,
		Use:       "sig",
	}

	fositeConfig := &fosite.Config{
		AccessTokenIssuer:              cfg.Issuer,
		AccessTokenLifespan:            cfg.AccessTokenLifespan,
		RefreshTokenLifespan:           cfg.RefreshTokenLifespan,
		AuthorizeCodeLifespan:          cfg.AuthCodeLifespan,
		GlobalSecret:                   cfg.HMACSecrets.Current,
		RotatedGlobalSecrets:           cfg.HMACSecrets.Rotated,
		TokenURL:                       cfg.Issuer + "/oauth2/token",
		EnforcePKCE:                    true,
		EnablePKCEPlainChallengeMethod: false, // Only allow S256 per MCP specification
	}

	return &AuthorizationServerConfig{
		Config:      fositeConfig,
		SigningKey:  &jwk,
		SigningJWKS: &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}},
	}, nil
}

// NewAuthorizationServer creates a new fosite OAuth2Provider with the given configuration,
// storage, strategy, and endpoint handler factories.
func NewAuthorizationServer(
	config *AuthorizationServerConfig,
	storage fosite.Storage,
	strategy any,
	factories ...Factory,
) fosite.OAuth2Provider {
	fositeConfig := config.Config
	provider := fosite.NewOAuth2Provider(storage, fositeConfig)

	for _, factory := range factories {
		result := factory(config, storage, strategy)

		if ah, ok := result.(fosite.AuthorizeEndpointHandler); ok {
			fositeConfig.AuthorizeEndpointHandlers.Append(ah)
		}

		if th, ok := result.(fosite.TokenEndpointHandler); ok {
			fositeConfig.TokenEndpointHandlers.Append(th)
		}

		if ti, ok := result.(fosite.TokenIntrospector); ok {
			fositeConfig.TokenIntrospectionHandlers.Append(ti)
		}

		if rh, ok := result.(fosite.RevocationHandler); ok {
			fositeConfig.RevocationHandlers.Append(rh)
		}

		if ph, ok := result.(fosite.PushedAuthorizeEndpointHandler); ok {
			fositeConfig.PushedAuthorizeEndpointHandlers.Append(ph)
		}
	}

	return provider
}

// GetSigningKey returns the config's signing key.
func (c *AuthorizationServerConfig) GetSigningKey(_ context.Context) *jose.JSONWebKey {
	return c.SigningKey
}

// GetPrivateSigningJWKS returns the config's signing JWKS containing private keys.
//
// WARNING: This JWKS contains PRIVATE key material and MUST NOT be exposed publicly.
// Use PublicJWKS() for the /.well-known/jwks.json endpoint.
func (c *AuthorizationServerConfig) GetPrivateSigningJWKS(_ context.Context) *jose.JSONWebKeySet {
	return c.SigningJWKS
}

// PublicJWKS returns a copy of the JWKS containing only public keys.
func (c *AuthorizationServerConfig) PublicJWKS() *jose.JSONWebKeySet {
	if c.SigningJWKS == nil {
		return nil
	}

	publicJWKS := &jose.JSONWebKeySet{
		Keys: make([]jose.JSONWebKey, 0, len(c.SigningJWKS.Keys)),
	}

	for _, key := range c.SigningJWKS.Keys {
		publicKey := key.Public()
		publicJWKS.Keys = append(publicJWKS.Keys, publicKey)
	}

	return publicJWKS
}

// GetAccessTokenIssuer returns the issuer URL for access tokens.
// This is an adapter method that wraps the embedded fosite.Config method.
func (c *AuthorizationServerConfig) GetAccessTokenIssuer() string {
	return c.AccessTokenIssuer
}

// GetAuthorizeCodeLifespan returns the lifetime for authorization codes.
// This is an adapter method that wraps the embedded fosite.Config method.
func (c *AuthorizationServerConfig) GetAuthorizeCodeLifespan() time.Duration {
	return c.AuthorizeCodeLifespan
}

// GetAccessTokenLifespan returns the lifetime for access tokens.
// This is an adapter method that wraps the embedded fosite.Config method.
func (c *AuthorizationServerConfig) GetAccessTokenLifespan() time.Duration {
	return c.AccessTokenLifespan
}

// GetRefreshTokenLifespan returns the lifetime for refresh tokens.
// This is an adapter method that wraps the embedded fosite.Config method.
func (c *AuthorizationServerConfig) GetRefreshTokenLifespan() time.Duration {
	return c.RefreshTokenLifespan
}

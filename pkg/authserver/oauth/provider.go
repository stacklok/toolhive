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

package oauth

import (
	"context"
	"crypto"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
)

// MinSecretLength is the minimum required length for the HMAC secret in bytes.
// 32 bytes (256 bits) is required per OWASP/NIST security guidelines.
const MinSecretLength = 32

// OAuth2Config wraps fosite.Config with additional configuration
// for JWT signing and other extensions.
type OAuth2Config struct {
	*fosite.Config
	SigningKey  *jose.JSONWebKey
	SigningJWKS *jose.JSONWebKeySet
}

// Factory is a constructor which is used to create an OAuth2 endpoint handler.
// NewOAuth2Provider handles consuming the new struct and attaching it
// to the parts of the config that it implements.
//
// The strategy parameter is typed as any because fosite uses different strategy
// interfaces for different flows (e.g., oauth2.CoreStrategy, openid.OpenIDConnectTokenStrategy)
// that do not share a common base interface.
type Factory func(config *OAuth2Config, storage fosite.Storage, strategy any) any

// AuthServerConfig contains the configuration needed to create an OAuth2Config.
// This is a minimal subset of the authserver.Config fields needed for OAuth2.
type AuthServerConfig struct {
	Issuer               string
	AccessTokenLifespan  time.Duration
	RefreshTokenLifespan time.Duration
	AuthCodeLifespan     time.Duration
	HMACSecret           []byte
	SigningKeyID         string
	SigningKeyAlgorithm  string
	SigningKey           crypto.Signer
}

// NewOAuth2ConfigFromAuthServerConfig creates an OAuth2Config from the provided configuration.
func NewOAuth2ConfigFromAuthServerConfig(cfg *AuthServerConfig) (*OAuth2Config, error) {
	if cfg.SigningKeyID == "" {
		return nil, fmt.Errorf("signing key ID is required")
	}
	if cfg.SigningKeyAlgorithm == "" {
		return nil, fmt.Errorf("signing key algorithm is required")
	}
	if cfg.SigningKey == nil {
		return nil, fmt.Errorf("signing key is required")
	}

	// Build JWK from signing key
	jwk := jose.JSONWebKey{
		Key:       cfg.SigningKey,
		KeyID:     cfg.SigningKeyID,
		Algorithm: cfg.SigningKeyAlgorithm,
		Use:       "sig",
	}

	fositeConfig := &fosite.Config{
		AccessTokenIssuer:     cfg.Issuer,
		AccessTokenLifespan:   cfg.AccessTokenLifespan,
		RefreshTokenLifespan:  cfg.RefreshTokenLifespan,
		AuthorizeCodeLifespan: cfg.AuthCodeLifespan,
		GlobalSecret:          cfg.HMACSecret,
		TokenURL:              cfg.Issuer + "/oauth2/token",
	}

	return &OAuth2Config{
		Config:      fositeConfig,
		SigningKey:  &jwk,
		SigningJWKS: &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}},
	}, nil
}

// NewOAuth2Provider creates a new fosite OAuth2Provider with the given configuration,
// storage, strategy, and endpoint handler factories.
func NewOAuth2Provider(config *OAuth2Config, storage fosite.Storage, strategy any, factories ...Factory) fosite.OAuth2Provider {
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
func (c *OAuth2Config) GetSigningKey(_ context.Context) *jose.JSONWebKey {
	return c.SigningKey
}

// GetSigningJWKS returns the config's signing JWKS. This includes private keys.
func (c *OAuth2Config) GetSigningJWKS(_ context.Context) *jose.JSONWebKeySet {
	return c.SigningJWKS
}

// PublicJWKS returns a copy of the JWKS containing only public keys.
func (c *OAuth2Config) PublicJWKS() *jose.JSONWebKeySet {
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

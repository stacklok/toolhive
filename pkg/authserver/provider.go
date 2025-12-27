package authserver

import (
	"context"
	"fmt"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
)

// ErrInvalidKey is returned when a key is invalid or cannot be parsed.
var ErrInvalidKey = fmt.Errorf("invalid key")

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

// NewOAuth2Config creates an OAuth2Config from the provided Config.
func NewOAuth2Config(config *Config) (*OAuth2Config, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	signingKey, jwks, err := buildJWKS(config.PrivateKeys)
	if err != nil {
		return nil, fmt.Errorf("failed to build JWKS: %w", err)
	}

	fositeConfig := &fosite.Config{
		AccessTokenIssuer:     config.Issuer,
		AccessTokenLifespan:   config.AccessTokenLifespan,
		RefreshTokenLifespan:  config.RefreshTokenLifespan,
		AuthorizeCodeLifespan: config.AuthCodeLifespan,
		GlobalSecret:          config.Secret,
		TokenURL:              config.Issuer + "/oauth2/token",
	}

	return &OAuth2Config{
		Config:      fositeConfig,
		SigningKey:  signingKey,
		SigningJWKS: jwks,
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

// buildJWKS builds a JSON Web Key Set from the provided private keys.
func buildJWKS(keys []PrivateKey) (*jose.JSONWebKey, *jose.JSONWebKeySet, error) {
	if len(keys) == 0 {
		return nil, nil, fmt.Errorf("%w: no private keys provided", ErrInvalidKey)
	}

	jwks := &jose.JSONWebKeySet{
		Keys: make([]jose.JSONWebKey, 0, len(keys)),
	}

	var signingKey *jose.JSONWebKey

	for i, pk := range keys {
		jwk := jose.JSONWebKey{
			Key:       pk.Key,
			KeyID:     pk.KeyID,
			Algorithm: pk.Algorithm,
			Use:       "sig",
		}

		if i == 0 {
			signingKey = &jwk
		}

		jwks.Keys = append(jwks.Keys, jwk)
	}

	return signingKey, jwks, nil
}

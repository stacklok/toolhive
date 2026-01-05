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

package authserver

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"fmt"
	"time"
)

// MinRSAKeyBits is the minimum required size for RSA keys in bits.
// 2048 bits is required per NIST SP 800-57 recommendations.
const MinRSAKeyBits = 2048

// Config is the pure configuration for the OAuth authorization server.
// All values must be fully resolved (no file paths, no env vars).
// This is the interface that consumers should use to configure the server.
type Config struct {
	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	Issuer string

	// SigningKey is the key used for signing JWT tokens.
	SigningKey SigningKey

	// HMACSecret is the symmetric secret used for signing authorization codes
	// and refresh tokens (opaque tokens). Unlike the asymmetric SigningKey which
	// signs JWTs for distributed verification, this secret is used internally
	// by the authorization server only.
	// Must be at least 32 bytes and cryptographically random.
	// Must be consistent across all replicas in multi-instance deployments.
	HMACSecret []byte

	// AccessTokenLifespan is the duration that access tokens are valid.
	// If zero, defaults to 1 hour.
	AccessTokenLifespan time.Duration

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	// If zero, defaults to 7 days.
	RefreshTokenLifespan time.Duration

	// AuthCodeLifespan is the duration that authorization codes are valid.
	// If zero, defaults to 10 minutes.
	AuthCodeLifespan time.Duration

	// Clients is the list of pre-registered OAuth clients.
	Clients []ClientConfig

	// Upstream contains configuration for connecting to an upstream IDP.
	// If nil, no upstream IDP is configured and the server operates in standalone mode.
	Upstream *UpstreamConfig
}

// SigningKey represents a key used for signing JWT tokens.
type SigningKey struct {
	// KeyID is the unique identifier for this key, used in the JWT "kid" header.
	KeyID string

	// Algorithm specifies the signing algorithm (e.g., "RS256", "ES256").
	Algorithm string

	// Key is the actual private key. Must implement crypto.Signer.
	Key crypto.Signer
}

// ClientConfig defines a pre-registered OAuth client.
type ClientConfig struct {
	// ID is the unique identifier for this client.
	ID string

	// Secret is the client secret. Required for confidential clients.
	// For public clients, this should be empty.
	Secret string

	// RedirectURIs is the list of allowed redirect URIs for this client.
	RedirectURIs []string

	// Public indicates whether this is a public client (e.g., native app, SPA).
	// Public clients do not have a secret.
	Public bool
}

// UpstreamConfig contains configuration for connecting to an upstream
// Identity Provider (e.g., Google, Okta, Auth0).
type UpstreamConfig struct {
	// Issuer is the URL of the upstream IDP (e.g., https://accounts.google.com).
	Issuer string

	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	ClientSecret string

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string

	// RedirectURI is the callback URL where the upstream IDP will redirect
	// after authentication. This should be our authorization server's callback endpoint.
	RedirectURI string
}

// MinSecretLength is the minimum required length for the HMAC secret in bytes.
// 32 bytes (256 bits) is required per OWASP/NIST security guidelines.
const MinSecretLength = 32

// Validate checks that the Config is valid.
func (c *Config) Validate() error {
	if c.Issuer == "" {
		return fmt.Errorf("issuer is required")
	}

	if err := c.SigningKey.Validate(); err != nil {
		return fmt.Errorf("signing key: %w", err)
	}

	if len(c.HMACSecret) < MinSecretLength {
		return fmt.Errorf("HMAC secret must be at least %d bytes", MinSecretLength)
	}

	for i, client := range c.Clients {
		if err := client.Validate(); err != nil {
			return fmt.Errorf("client %d: %w", i, err)
		}
	}

	if c.Upstream != nil {
		if err := c.Upstream.Validate(); err != nil {
			return fmt.Errorf("upstream config: %w", err)
		}
	}

	return nil
}

// Validate checks that the SigningKey configuration is valid.
func (k *SigningKey) Validate() error {
	if k.KeyID == "" {
		return fmt.Errorf("key ID is required")
	}
	if k.Algorithm == "" {
		return fmt.Errorf("algorithm is required")
	}
	if k.Key == nil {
		return fmt.Errorf("key is required")
	}

	switch k.Algorithm {
	case "RS256", "RS384", "RS512":
		rsaKey, ok := k.Key.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("RSA algorithm requires *rsa.PrivateKey, got %T", k.Key)
		}
		if rsaKey.N.BitLen() < MinRSAKeyBits {
			return fmt.Errorf("RSA key must be at least %d bits, got %d", MinRSAKeyBits, rsaKey.N.BitLen())
		}
	case "ES256", "ES384", "ES512":
		ecdsaKey, ok := k.Key.(*ecdsa.PrivateKey)
		if !ok {
			return fmt.Errorf("ECDSA algorithm requires *ecdsa.PrivateKey, got %T", k.Key)
		}
		expectedCurves := map[string]string{
			"ES256": "P-256",
			"ES384": "P-384",
			"ES512": "P-521",
		}
		expectedCurve := expectedCurves[k.Algorithm]
		if ecdsaKey.Curve.Params().Name != expectedCurve {
			return fmt.Errorf("algorithm %s requires curve %s, got %s",
				k.Algorithm, expectedCurve, ecdsaKey.Curve.Params().Name)
		}
	default:
		return fmt.Errorf("unsupported algorithm: %s", k.Algorithm)
	}

	return nil
}

// Validate checks that the ClientConfig is valid.
func (c *ClientConfig) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("client id is required")
	}

	if len(c.RedirectURIs) == 0 {
		return fmt.Errorf("at least one redirect_uri is required")
	}

	if !c.Public && c.Secret == "" {
		return fmt.Errorf("secret is required for confidential clients")
	}

	return nil
}

// Validate checks that the UpstreamConfig is valid.
func (c *UpstreamConfig) Validate() error {
	if c.Issuer == "" {
		return fmt.Errorf("upstream issuer is required")
	}
	if c.ClientID == "" {
		return fmt.Errorf("upstream client_id is required")
	}
	if c.ClientSecret == "" {
		return fmt.Errorf("upstream client_secret is required")
	}
	if c.RedirectURI == "" {
		return fmt.Errorf("upstream redirect_uri is required")
	}
	return nil
}

// applyDefaults applies default values to the config where not set.
func (c *Config) applyDefaults() {
	if c.AccessTokenLifespan == 0 {
		c.AccessTokenLifespan = time.Hour
	}
	if c.RefreshTokenLifespan == 0 {
		c.RefreshTokenLifespan = 24 * time.Hour * 7 // 7 days
	}
	if c.AuthCodeLifespan == 0 {
		c.AuthCodeLifespan = 10 * time.Minute
	}
}

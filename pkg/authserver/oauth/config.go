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
	"crypto/ecdsa"
	"crypto/rsa"
	"fmt"
	"time"
)

// MinSecretLength is the minimum required length for the HMAC secret in bytes.
// 32 bytes (256 bits) is required per OWASP/NIST security guidelines.
const MinSecretLength = 32

// MinRSAKeyBits is the minimum required size for RSA keys in bits.
// 2048 bits is required per NIST SP 800-57 recommendations.
const MinRSAKeyBits = 2048

// Config represents the configuration for the authorization server.
type Config struct {
	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	Issuer string

	// AccessTokenLifespan is the duration that access tokens are valid.
	AccessTokenLifespan time.Duration

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	RefreshTokenLifespan time.Duration

	// AuthCodeLifespan is the duration that authorization codes are valid.
	AuthCodeLifespan time.Duration

	// Secret is the HMAC secret used for opaque tokens.
	Secret []byte

	// PrivateKeys contains the signing keys for JWT tokens.
	// The first key in the slice is used for signing new tokens.
	PrivateKeys []PrivateKey

	// Upstream contains the configuration for the upstream Identity Provider.
	Upstream UpstreamConfig
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

// PrivateKey represents a private key used for signing JWT tokens.
type PrivateKey struct {
	// KeyID is the unique identifier for this key, used in the JWT "kid" header.
	KeyID string

	// Algorithm specifies the signing algorithm (e.g., "RS256", "ES256").
	Algorithm string

	// Key is the actual private key. It must be either *rsa.PrivateKey or *ecdsa.PrivateKey.
	Key any
}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	if c.Issuer == "" {
		return fmt.Errorf("issuer is required")
	}
	if c.AccessTokenLifespan <= 0 {
		return fmt.Errorf("access token lifespan must be positive")
	}
	if c.RefreshTokenLifespan <= 0 {
		return fmt.Errorf("refresh token lifespan must be positive")
	}
	if c.AuthCodeLifespan <= 0 {
		return fmt.Errorf("auth code lifespan must be positive")
	}
	if len(c.Secret) < MinSecretLength {
		return fmt.Errorf("secret must be at least %d bytes", MinSecretLength)
	}
	if len(c.PrivateKeys) == 0 {
		return fmt.Errorf("at least one private key is required")
	}

	for i, pk := range c.PrivateKeys {
		if err := pk.Validate(); err != nil {
			return fmt.Errorf("private key %d: %w", i, err)
		}
	}

	// Only validate upstream if it's configured (non-empty issuer indicates configuration)
	if c.Upstream.Issuer != "" {
		if err := c.Upstream.Validate(); err != nil {
			return fmt.Errorf("upstream config: %w", err)
		}
	}

	return nil
}

// Validate checks that the private key configuration is valid.
func (pk *PrivateKey) Validate() error {
	if pk.KeyID == "" {
		return fmt.Errorf("key ID is required")
	}
	if pk.Algorithm == "" {
		return fmt.Errorf("algorithm is required")
	}
	if pk.Key == nil {
		return fmt.Errorf("key is required")
	}

	switch pk.Algorithm {
	case "RS256", "RS384", "RS512":
		rsaKey, ok := pk.Key.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("RSA algorithm requires *rsa.PrivateKey, got %T", pk.Key)
		}
		if rsaKey.N.BitLen() < MinRSAKeyBits {
			return fmt.Errorf("RSA key must be at least %d bits, got %d", MinRSAKeyBits, rsaKey.N.BitLen())
		}
	case "ES256", "ES384", "ES512":
		ecdsaKey, ok := pk.Key.(*ecdsa.PrivateKey)
		if !ok {
			return fmt.Errorf("ECDSA algorithm requires *ecdsa.PrivateKey, got %T", pk.Key)
		}
		expectedCurves := map[string]string{
			"ES256": "P-256",
			"ES384": "P-384",
			"ES512": "P-521",
		}
		expectedCurve := expectedCurves[pk.Algorithm]
		if ecdsaKey.Curve.Params().Name != expectedCurve {
			return fmt.Errorf("algorithm %s requires curve %s, got %s",
				pk.Algorithm, expectedCurve, ecdsaKey.Curve.Params().Name)
		}
	default:
		return fmt.Errorf("unsupported algorithm: %s", pk.Algorithm)
	}

	return nil
}

// Validate checks that the upstream configuration is valid.
func (uc *UpstreamConfig) Validate() error {
	if uc.Issuer == "" {
		return fmt.Errorf("upstream issuer is required")
	}
	if uc.ClientID == "" {
		return fmt.Errorf("upstream client ID is required")
	}
	if uc.ClientSecret == "" {
		return fmt.Errorf("upstream client secret is required")
	}
	if uc.RedirectURI == "" {
		return fmt.Errorf("upstream redirect URI is required")
	}
	return nil
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24 * 7, // 7 days
		AuthCodeLifespan:     time.Minute * 10,
	}
}

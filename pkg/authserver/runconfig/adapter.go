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

package runconfig

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/idp"
	"github.com/stacklok/toolhive/pkg/authserver/oauth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/logger"
)

// MinClientSecretLength is the minimum required length for client secrets.
// OAuth 2.0 best practice recommends at least 256 bits (32 bytes) of entropy.
const MinClientSecretLength = 32

// BuildConfig converts ToolHive's RunConfig to generic authserver.Config.
// Handles:
//   - Loading signing key from SigningKeyPath
//   - Loading HMAC secret from HMACSecretPath
//   - Resolving client secret (file -> env)
//   - Port substitution in issuer URL (:0 -> actual port)
func BuildConfig(cfg *RunConfig, proxyPort int) (*authserver.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("RunConfig is nil")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid run config: %w", err)
	}

	// Resolve issuer URL - replace :0 with actual port if needed
	issuer, err := resolveIssuer(cfg.Issuer, proxyPort)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve issuer URL: %w", err)
	}

	// Load signing key from file
	rsaKey, err := oauth.LoadSigningKey(cfg.SigningKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load signing key: %w", err)
	}

	// Load HMAC secret from file (required)
	if cfg.HMACSecretPath == "" {
		return nil, fmt.Errorf("hmac_secret_path is required when auth server is enabled")
	}
	hmacSecret, err := oauth.LoadHMACSecret(cfg.HMACSecretPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load HMAC secret: %w", err)
	}

	// Build generic config
	genericCfg := &authserver.Config{
		Issuer: issuer,
		SigningKey: authserver.SigningKey{
			KeyID:     cfg.SigningKeyID,
			Algorithm: cfg.SigningKeyAlgorithm,
			Key:       rsaKey,
		},
		HMACSecret:           hmacSecret,
		AccessTokenLifespan:  cfg.AccessTokenLifespan,
		RefreshTokenLifespan: cfg.RefreshTokenLifespan,
	}

	// Convert client configs
	clients, err := buildClientConfigs(cfg.Clients)
	if err != nil {
		return nil, fmt.Errorf("failed to build client configs: %w", err)
	}
	genericCfg.Clients = clients

	// Convert upstream config if present
	if cfg.Upstream != nil && cfg.Upstream.Issuer != "" {
		upstreamCfg, err := buildUpstreamConfig(cfg.Upstream, issuer)
		if err != nil {
			return nil, err
		}
		genericCfg.Upstream = upstreamCfg
	}

	return genericCfg, nil
}

// BuildStorageConfig converts StorageConfig to storage.RunConfig.
func BuildStorageConfig(cfg *StorageConfig) *storage.RunConfig {
	if cfg == nil {
		return nil
	}
	return &storage.RunConfig{
		Type:              cfg.Type,
		RedisURL:          cfg.RedisURL,
		RedisPasswordFile: cfg.RedisPasswordFile,
		KeyPrefix:         cfg.KeyPrefix,
	}
}

// resolveIssuer replaces port 0 in the issuer URL with the actual proxy port.
// Only replaces the port if it's exactly "0" in the URL's host portion.
func resolveIssuer(issuer string, proxyPort int) (string, error) {
	if proxyPort <= 0 {
		return issuer, nil
	}

	parsed, err := url.Parse(issuer)
	if err != nil {
		return "", fmt.Errorf("invalid issuer URL: %w", err)
	}

	// If there's no port in the URL, nothing to replace
	if parsed.Port() == "" {
		return issuer, nil
	}

	// Only replace if the port is exactly "0"
	if parsed.Port() != "0" {
		return issuer, nil
	}

	// Extract the host (without port) and reconstruct with actual port
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return "", fmt.Errorf("failed to parse host:port from issuer URL: %w", err)
	}

	parsed.Host = net.JoinHostPort(host, fmt.Sprintf("%d", proxyPort))
	return parsed.String(), nil
}

// buildClientConfigs converts RunConfig clients to generic ClientConfig.
func buildClientConfigs(clients []ClientConfig) ([]authserver.ClientConfig, error) {
	result := make([]authserver.ClientConfig, len(clients))
	for i, c := range clients {
		secret, err := resolveClientSecretFromFile(c.SecretFile, c.Public)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve secret for client %s: %w", c.ID, err)
		}
		result[i] = authserver.ClientConfig{
			ID:           c.ID,
			Secret:       secret,
			RedirectURIs: c.RedirectURIs,
			Public:       c.Public,
		}
	}
	return result, nil
}

// resolveClientSecretFromFile reads the client secret from a file.
// For public clients, returns empty string without error.
func resolveClientSecretFromFile(secretFile string, isPublic bool) (string, error) {
	if isPublic {
		return "", nil
	}

	if secretFile == "" {
		return "", fmt.Errorf("secret_file is required for confidential clients")
	}

	data, err := os.ReadFile(secretFile) // #nosec G304 - file path is provided by user via config
	if err != nil {
		return "", fmt.Errorf("failed to read client secret file: %w", err)
	}

	secret := strings.TrimSpace(string(data))
	if len(secret) < MinClientSecretLength {
		return "", fmt.Errorf("client secret must be at least %d characters, got %d", MinClientSecretLength, len(secret))
	}

	return secret, nil
}

// buildUpstreamConfig converts RunConfig upstream to generic UpstreamConfig.
func buildUpstreamConfig(upstream *UpstreamConfig, issuer string) (*idp.UpstreamConfig, error) {
	clientSecret, err := resolveClientSecret(upstream)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve upstream client secret: %w", err)
	}

	return &idp.UpstreamConfig{
		Issuer:       upstream.Issuer,
		ClientID:     upstream.ClientID,
		ClientSecret: clientSecret,
		Scopes:       upstream.Scopes,
		RedirectURI:  issuer + "/oauth/callback",
	}, nil
}

// resolveClientSecret returns the client secret using the following order of precedence:
// 1. ClientSecretFile (read from file)
// 2. UpstreamClientSecretEnvVar environment variable (fallback)
func resolveClientSecret(c *UpstreamConfig) (string, error) {
	var secret string

	// 1. Read from file if specified
	if c.ClientSecretFile != "" {
		data, err := os.ReadFile(c.ClientSecretFile) // #nosec G304 - file path is provided by user via config
		if err != nil {
			return "", fmt.Errorf("failed to read client secret file: %w", err)
		}
		secret = strings.TrimSpace(string(data))
	} else if envSecret := os.Getenv(UpstreamClientSecretEnvVar); envSecret != "" {
		// 2. Fallback to environment variable
		logger.Debug("Using upstream client secret from environment variable")
		secret = envSecret
	} else {
		return "", fmt.Errorf("no client secret found: set client_secret_file or %s env var", UpstreamClientSecretEnvVar)
	}

	// Validate minimum length
	if len(secret) < MinClientSecretLength {
		return "", fmt.Errorf("upstream client secret must be at least %d characters, got %d", MinClientSecretLength, len(secret))
	}

	return secret, nil
}

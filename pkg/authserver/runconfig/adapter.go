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
	"os"
	"strings"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/oauth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/logger"
)

// BuildConfig converts ToolHive's RunConfig to generic authserver.Config.
// Handles:
//   - Loading signing key from SigningKeyPath
//   - Loading HMAC secret from HMACSecretPath
//   - Resolving client secret (file -> env -> direct)
//   - Port substitution in issuer URL (:0 -> actual port)
func BuildConfig(cfg *RunConfig, proxyPort int) (*authserver.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("RunConfig is nil")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid run config: %w", err)
	}

	// Resolve issuer URL - replace :0 with actual port if needed
	issuer := resolveIssuer(cfg.Issuer, proxyPort)

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
			KeyID:     "key-1",
			Algorithm: "RS256",
			Key:       rsaKey,
		},
		HMACSecret:           hmacSecret,
		AccessTokenLifespan:  cfg.AccessTokenLifespan,
		RefreshTokenLifespan: cfg.RefreshTokenLifespan,
	}

	// Convert client configs
	genericCfg.Clients = buildClientConfigs(cfg.Clients)

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
		RedisPassword:     cfg.RedisPassword,
		RedisPasswordFile: cfg.RedisPasswordFile,
		KeyPrefix:         cfg.KeyPrefix,
	}
}

// resolveIssuer replaces :0 in issuer with actual port.
func resolveIssuer(issuer string, proxyPort int) string {
	if proxyPort > 0 && strings.Contains(issuer, ":0") {
		return strings.Replace(issuer, ":0", fmt.Sprintf(":%d", proxyPort), 1)
	}
	return issuer
}

// buildClientConfigs converts RunConfig clients to generic ClientConfig.
func buildClientConfigs(clients []ClientConfig) []authserver.ClientConfig {
	result := make([]authserver.ClientConfig, len(clients))
	for i, c := range clients {
		result[i] = authserver.ClientConfig{
			ID:           c.ID,
			Secret:       c.Secret,
			RedirectURIs: c.RedirectURIs,
			Public:       c.Public,
		}
	}
	return result
}

// buildUpstreamConfig converts RunConfig upstream to generic UpstreamConfig.
func buildUpstreamConfig(upstream *UpstreamConfig, issuer string) (*authserver.UpstreamConfig, error) {
	clientSecret, err := resolveClientSecret(upstream)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve upstream client secret: %w", err)
	}

	return &authserver.UpstreamConfig{
		Issuer:       upstream.Issuer,
		ClientID:     upstream.ClientID,
		ClientSecret: clientSecret,
		Scopes:       upstream.Scopes,
		RedirectURI:  issuer + "/oauth/callback",
	}, nil
}

// resolveClientSecret returns the client secret using the following order of precedence:
// 1. ClientSecret (direct config value)
// 2. ClientSecretFile (read from file)
// 3. UpstreamClientSecretEnvVar environment variable (fallback)
func resolveClientSecret(c *UpstreamConfig) (string, error) {
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

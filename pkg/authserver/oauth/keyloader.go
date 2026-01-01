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
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

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

// LoadHMACSecret loads an HMAC secret from a file.
// Returns nil if path is empty (triggers random generation in toInternalConfig).
// The secret must be at least 32 bytes after trimming whitespace.
func LoadHMACSecret(secretPath string) ([]byte, error) {
	if secretPath == "" {
		return nil, nil
	}

	data, err := os.ReadFile(secretPath) // #nosec G304 - secretPath is provided by user via CLI flag or config
	if err != nil {
		return nil, fmt.Errorf("failed to read HMAC secret file: %w", err)
	}

	// Trim whitespace (common in Kubernetes Secret mounts which often add trailing newlines)
	secret := []byte(strings.TrimSpace(string(data)))

	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("HMAC secret must be at least %d bytes, got %d bytes", MinSecretLength, len(secret))
	}

	return secret, nil
}

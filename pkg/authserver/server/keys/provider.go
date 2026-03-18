// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package keys

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=provider.go KeyProvider

// KeyProvider provides signing keys for JWT operations.
// Implementations handle key sourcing (file, memory, generation).
type KeyProvider interface {
	// SigningKey returns the current signing key.
	// Returns ErrNoSigningKey if no key is available.
	SigningKey(ctx context.Context) (*SigningKeyData, error)

	// PublicKeys returns all public keys for the JWKS endpoint.
	// May return multiple keys during rotation periods.
	PublicKeys(ctx context.Context) ([]*PublicKeyData, error)
}

// FileProvider loads signing keys from PEM files in a directory.
// The signing key is used for signing new tokens.
// All keys (signing + fallback) are exposed via PublicKeys() for JWKS.
// Keys are loaded once at construction time; changes require restart.
type FileProvider struct {
	signingKey *SigningKeyData
	allKeys    []*SigningKeyData
}

// NewFileProvider creates a provider that loads keys from a directory.
// Config.SigningKeyFile is the primary key used for signing new tokens.
// Config.FallbackKeyFiles are loaded for JWKS verification (for key rotation).
// All keys are loaded immediately and validated.
// Supports RSA (PKCS1/PKCS8), ECDSA (SEC1/PKCS8), and Ed25519 keys.
func NewFileProvider(cfg Config) (*FileProvider, error) {
	if cfg.SigningKeyFile == "" {
		return nil, fmt.Errorf("signing key file is required")
	}

	// Load the primary signing key
	signingKeyPath := filepath.Join(cfg.KeyDir, cfg.SigningKeyFile)
	signingKey, err := loadKeyFromFile(signingKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load signing key: %w", err)
	}

	// Start with the signing key in allKeys
	allKeys := []*SigningKeyData{signingKey}

	// Load fallback keys for JWKS verification
	for _, filename := range cfg.FallbackKeyFiles {
		keyPath := filepath.Join(cfg.KeyDir, filename)
		key, err := loadKeyFromFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load fallback key %s: %w", filename, err)
		}
		allKeys = append(allKeys, key)
	}

	return &FileProvider{
		signingKey: signingKey,
		allKeys:    allKeys,
	}, nil
}

// loadKeyFromFile loads a single key from a PEM file.
func loadKeyFromFile(keyPath string) (*SigningKeyData, error) {
	signer, err := servercrypto.LoadSigningKey(keyPath)
	if err != nil {
		return nil, err
	}

	params, err := servercrypto.DeriveSigningKeyParams(signer, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to derive key parameters: %w", err)
	}

	return &SigningKeyData{
		KeyID:     params.KeyID,
		Algorithm: params.Algorithm,
		Key:       params.Key,
		CreatedAt: time.Now(),
	}, nil
}

// SigningKey returns the primary signing key used for signing new tokens.
// Returns a copy to prevent external mutation of internal state.
func (p *FileProvider) SigningKey(_ context.Context) (*SigningKeyData, error) {
	return &SigningKeyData{
		KeyID:     p.signingKey.KeyID,
		Algorithm: p.signingKey.Algorithm,
		Key:       p.signingKey.Key,
		CreatedAt: p.signingKey.CreatedAt,
	}, nil
}

// PublicKeys returns public keys for all loaded keys (signing + additional).
// This enables verification of tokens signed with any of the loaded keys,
// supporting key rotation scenarios where old keys must remain valid.
func (p *FileProvider) PublicKeys(_ context.Context) ([]*PublicKeyData, error) {
	pubKeys := make([]*PublicKeyData, 0, len(p.allKeys))
	for _, key := range p.allKeys {
		pubKeys = append(pubKeys, &PublicKeyData{
			KeyID:     key.KeyID,
			Algorithm: key.Algorithm,
			PublicKey: key.Key.Public(),
			CreatedAt: key.CreatedAt,
		})
	}
	return pubKeys, nil
}

// GeneratingProvider generates an ephemeral key on first access.
// Suitable for development but NOT recommended for production.
// Generated keys are lost on restart, invalidating all issued tokens.
type GeneratingProvider struct {
	algorithm string
	mu        sync.Mutex
	key       *SigningKeyData
}

// NewGeneratingProvider creates a provider that generates an ephemeral key.
// The key is generated lazily on first SigningKey() call.
// If algorithm is empty, DefaultAlgorithm (ES256) is used.
func NewGeneratingProvider(algorithm string) *GeneratingProvider {
	if algorithm == "" {
		algorithm = DefaultAlgorithm
	}
	return &GeneratingProvider{algorithm: algorithm}
}

// SigningKey returns the signing key, generating one if needed.
// Thread-safe: uses mutex to ensure only one key is generated.
// Returns a copy to prevent external mutation of internal state.
func (p *GeneratingProvider) SigningKey(_ context.Context) (*SigningKeyData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.key != nil {
		return &SigningKeyData{
			KeyID:     p.key.KeyID,
			Algorithm: p.key.Algorithm,
			Key:       p.key.Key,
			CreatedAt: p.key.CreatedAt,
		}, nil
	}

	key, err := p.generateKey()
	if err != nil {
		return nil, err
	}

	slog.Warn("generated ephemeral signing key - tokens will be invalid after restart",
		"algorithm", key.Algorithm,
		"key_id", key.KeyID,
	)

	p.key = key
	return &SigningKeyData{
		KeyID:     p.key.KeyID,
		Algorithm: p.key.Algorithm,
		Key:       p.key.Key,
		CreatedAt: p.key.CreatedAt,
	}, nil
}

// PublicKeys returns the public key for JWKS.
// Generates the signing key if it hasn't been generated yet.
func (p *GeneratingProvider) PublicKeys(ctx context.Context) ([]*PublicKeyData, error) {
	key, err := p.SigningKey(ctx)
	if err != nil {
		return nil, err
	}
	return []*PublicKeyData{{
		KeyID:     key.KeyID,
		Algorithm: key.Algorithm,
		PublicKey: key.Key.Public(),
		CreatedAt: key.CreatedAt,
	}}, nil
}

func (p *GeneratingProvider) generateKey() (*SigningKeyData, error) {
	privateKey, err := generatePrivateKey(p.algorithm)
	if err != nil {
		return nil, fmt.Errorf("failed to generate signing key: %w", err)
	}

	keyID, err := servercrypto.DeriveKeyID(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive key ID: %w", err)
	}

	return &SigningKeyData{
		KeyID:     keyID,
		Algorithm: p.algorithm,
		Key:       privateKey,
		CreatedAt: time.Now(),
	}, nil
}

// generatePrivateKey creates a new private key for the specified algorithm.
func generatePrivateKey(algorithm string) (crypto.Signer, error) {
	switch algorithm {
	case "ES256":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "ES384":
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "ES512":
		return ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	default:
		return nil, fmt.Errorf("unsupported algorithm for key generation: %s", algorithm)
	}
}

// Compile-time interface checks.
var (
	_ KeyProvider = (*FileProvider)(nil)
	_ KeyProvider = (*GeneratingProvider)(nil)
)

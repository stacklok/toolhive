// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package keys

// Config holds configuration for creating a KeyProvider.
// The caller is responsible for populating this from their own config source
// (environment variables, YAML files, flags, etc.).
type Config struct {
	// KeyDir is the directory containing PEM-encoded private key files.
	// All key filenames are relative to this directory.
	//
	// In Kubernetes deployments, this is typically a mounted Secret volume:
	//
	//	volumeMounts:
	//	- name: signing-keys
	//	  mountPath: /etc/toolhive/keys
	KeyDir string

	// SigningKeyFile is the filename of the primary signing key (relative to KeyDir).
	// This key is used for signing new tokens.
	// If empty with KeyDir set, NewProviderFromConfig returns an error.
	// If both KeyDir and SigningKeyFile are empty, an ephemeral key is generated.
	SigningKeyFile string

	// FallbackKeyFiles are filenames of additional keys for verification (relative to KeyDir).
	// These keys are included in the JWKS endpoint for token verification but are NOT
	// used for signing new tokens.
	//
	// Key rotation (single replica): update SigningKeyFile to the new key and move
	// the old filename here. Tokens signed with old keys remain verifiable until
	// they expire.
	//
	// Key rotation (multiple replicas): to avoid a window where one replica signs
	// with a key not yet advertised by another replica's JWKS endpoint:
	//  1. Add the new key to FallbackKeyFiles and roll out to all replicas.
	//  2. Promote it to SigningKeyFile, move the old key to FallbackKeyFiles, roll out.
	//  3. Remove the old key from FallbackKeyFiles after its tokens have expired.
	FallbackKeyFiles []string
}

// NewProviderFromConfig creates a KeyProvider based on the configuration.
//
// Behavior:
//   - If KeyDir and SigningKeyFile are set: load keys from directory
//   - If both are empty: return GeneratingProvider (ephemeral key for development)
//   - If KeyDir is set but SigningKeyFile is empty: returns an error
func NewProviderFromConfig(cfg Config) (KeyProvider, error) {
	if cfg.KeyDir != "" {
		return NewFileProvider(cfg)
	}

	// Generate ephemeral key (development only)
	return NewGeneratingProvider(DefaultAlgorithm), nil
}

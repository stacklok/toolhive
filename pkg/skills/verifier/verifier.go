// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package verifier verifies Sigstore signatures for skill artifacts.
package verifier

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

//go:generate mockgen -destination=mocks/mock_verifier.go -package=mocks -source=verifier.go Verifier

// Verifier verifies Sigstore signatures for OCI and git skill artifacts.
type Verifier interface {
	// VerifyOCI discovers and verifies the Sigstore signature for an OCI artifact.
	VerifyOCI(ctx context.Context, imageRef, digest string) (*Result, error)
	// VerifyGit verifies a gitsign-style commit signature.
	VerifyGit(ctx context.Context, commitSignature, commitHash string) (*Result, error)
	// VerifyBundleOffline re-verifies a stored bundle without network access.
	VerifyBundleOffline(bundle []byte, digest string, expected *lockfile.Provenance) error
	// ResultFromBundle verifies a stored bundle and returns the observed identity.
	ResultFromBundle(bundle []byte, digest string) (*Result, error)
}

// Default implements Verifier using sigstore-go.
type Default struct {
	keychain authn.Keychain
}

// NewDefault creates a verifier that uses the given registry auth keychain.
func NewDefault(keychain authn.Keychain) *Default {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &Default{keychain: keychain}
}

var _ Verifier = (*Default)(nil)

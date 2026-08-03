// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package verifier verifies Sigstore signatures on skill artifacts.
//
// It is a thin wrapper over toolhive-core's container/verifier exports: all
// cryptographic verification — including binding an expected identity into
// the Sigstore policy — happens in core. This package adds the
// skills-domain vocabulary: lock file provenance conversion, the
// unsigned/invalid/mismatch error taxonomy, and the trust-on-first-use flow
// (nil expected identity verifies the chain of trust only; the caller
// records the observed identity).
//
// Verification uses the trusted root embedded in toolhive-core — hermetic,
// no TUF fetch — so results are reproducible offline at the cost of
// snapshot freshness (see core's OfflineTrustedMaterial).
package verifier

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

//go:generate mockgen -destination=mocks/mock_verifier.go -package=mocks -source=verifier.go Verifier

// Verifier verifies Sigstore signatures for skill artifacts.
type Verifier interface {
	// VerifyOCI discovers the Sigstore signature material attached to the
	// OCI artifact and verifies it (keyless/Fulcio flow). A non-nil
	// expected identity is enforced inside the Sigstore verification
	// policy; nil expected is the trust-on-first-use case and verifies the
	// chain of trust only. Returns ErrUnsigned when the artifact carries
	// no signature material.
	VerifyOCI(ctx context.Context, imageRef, digest string, expected *lockfile.Provenance) (*Result, error)

	// VerifyOCIWithKey discovers the signature material and verifies it
	// against the given PEM public key (the cosign key-pair flow).
	// Key-signed bundles carry no certificate identity: trust is the key.
	VerifyOCIWithKey(ctx context.Context, imageRef, digest string, pubKeyPEM []byte) (*Result, error)

	// VerifyGit cryptographically verifies a gitsign commit signature over
	// the commit payload against the embedded Fulcio roots. A non-nil
	// expected identity must match the certificate identity; nil expected
	// is the trust-on-first-use case. Returns ErrUnsigned for an empty
	// signature.
	VerifyGit(ctx context.Context, payload, signature []byte, expected *lockfile.Provenance) (*Result, error)

	// VerifyBundleOffline re-verifies a stored bundle against the artifact
	// digest ("sha256:<hex>") without network access, enforcing expected
	// like VerifyOCI.
	VerifyBundleOffline(bundle []byte, digest string, expected *lockfile.Provenance) error

	// VerifyBundleOfflineWithKey re-verifies a stored key-signed bundle
	// against the signer's PEM public key without network access — the
	// offline counterpart of VerifyOCIWithKey. imageRef and digest
	// reconstruct the signed payload, binding the check to the artifact.
	VerifyBundleOfflineWithKey(bundle []byte, imageRef, digest string, pubKeyPEM []byte) error

	// ResultFromBundle verifies a stored bundle offline (chain of trust
	// only) and returns the observed identity — used to back-fill
	// provenance for adopted skills.
	ResultFromBundle(bundle []byte, digest string) (*Result, error)
}

// Default implements Verifier on toolhive-core's Sigstore exports.
type Default struct {
	keychain authn.Keychain
}

var _ Verifier = (*Default)(nil)

// NewDefault creates a verifier using the given registry auth keychain for
// bundle retrieval. A nil keychain falls back to the default keychain.
func NewDefault(keychain authn.Keychain) *Default {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &Default{keychain: keychain}
}

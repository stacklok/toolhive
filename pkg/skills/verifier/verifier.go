// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package verifier verifies Sigstore signatures on skill artifacts.
//
// It is a thin wrapper over toolhive-core's container/verifier exports. Lock
// identities are bound into core's Sigstore policy; independently optional
// catalog constraints are matched against the observed certificate inside
// each bundle-verification attempt. This package adds the
// skills-domain vocabulary: lock file provenance conversion, the
// unsigned/invalid/mismatch error taxonomy, and the trust-on-first-use flow
// (nil expected identity verifies the chain of trust only; the caller
// records the observed identity).
//
// It also enforces the two pinned certificate fields core's Identity cannot
// express — the signing workflow's git ref and runner class — against the
// certificate a successful policy verification produced.
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

//go:generate mockgen -destination=mocks/mock_verifier.go -package=mocks -source=verifier.go Verifier,OCISnapshotVerifier,OCISnapshot

// Verifier verifies Sigstore signatures for skill artifacts.
type Verifier interface {
	// VerifyOCI discovers the Sigstore signature material attached to the
	// OCI artifact and verifies it (keyless/Fulcio flow). A non-nil
	// lock expectation is enforced inside the Sigstore verification policy;
	// catalog fields are checked independently against each verified bundle.
	// nil expected is the trust-on-first-use case and verifies the chain of
	// trust only.
	// Returns ErrUnsigned when the artifact carries no signature material.
	VerifyOCI(ctx context.Context, imageRef, digest string, expected *ProvenanceExpectation) (*Result, error)

	// VerifyOCIWithKey discovers the signature material and verifies it
	// against the given PEM public key (the cosign key-pair flow).
	// Key-signed bundles carry no certificate identity: trust is the key.
	VerifyOCIWithKey(ctx context.Context, imageRef, digest string, pubKeyPEM []byte) (*Result, error)

	// VerifyGit cryptographically verifies a gitsign commit signature over
	// the commit payload against the embedded Fulcio roots. A non-nil
	// expectation must match the certificate identity according to its lock
	// or catalog semantics; nil expected is the trust-on-first-use case.
	// Returns ErrUnsigned for an empty signature.
	VerifyGit(ctx context.Context, payload, signature []byte, expected *ProvenanceExpectation) (*Result, error)

	// VerifyBundleOffline re-verifies a stored bundle against the artifact
	// digest ("sha256:<hex>") without network access, enforcing expected
	// like VerifyOCI.
	VerifyBundleOffline(bundle []byte, digest string, expected *lockfile.Provenance) error

	// VerifyBundleOfflineWithKey re-verifies a stored key-signed bundle
	// against the signer's PEM public key without network access — the
	// offline counterpart of VerifyOCIWithKey. digest is the artifact's own
	// manifest digest, as for VerifyBundleOffline: the stored bundle carries
	// whatever the signature actually covers, so no caller reconstructs it.
	VerifyBundleOfflineWithKey(bundle []byte, digest string, pubKeyPEM []byte) error

	// ResultFromBundle verifies a stored bundle offline (chain of trust
	// only) and returns the observed identity — used to back-fill
	// provenance for adopted skills.
	ResultFromBundle(bundle []byte, digest string) (*Result, error)
}

// OCISnapshotRetriever discovers the complete bounded set of Sigstore
// signature material attached to a digest-pinned OCI artifact. The returned
// snapshot can evaluate multiple candidate trust anchors without another
// registry request.
type OCISnapshotRetriever interface {
	RetrieveOCISnapshot(ctx context.Context, imageRef, digest string) (OCISnapshot, error)
}

// OCISnapshotVerifier combines the legacy verification surface with strict
// OCI snapshot retrieval for callers that need to evaluate multiple trust
// anchors against one immutable bundle set.
type OCISnapshotVerifier interface {
	Verifier
	OCISnapshotRetriever
}

// OCISnapshot is a completely retrieved set of signature material for one
// digest-pinned OCI artifact. Its verification methods are offline and reuse
// the exact bundle set retrieved by OCISnapshotRetriever.RetrieveOCISnapshot.
type OCISnapshot interface {
	// VerifyKeyless verifies a certificate-bearing bundle against expected.
	// nil expected verifies the chain of trust only and returns the observed
	// identity.
	VerifyKeyless(expected *ProvenanceExpectation) (*Result, error)
	// VerifyWithKey verifies a key-pair-signed bundle against pubKeyPEM.
	VerifyWithKey(pubKeyPEM []byte) (*Result, error)
}

// Default implements OCISnapshotVerifier on toolhive-core's Sigstore exports.
type Default struct {
	keychain authn.Keychain
}

var _ OCISnapshotVerifier = (*Default)(nil)

// NewDefault creates a verifier using the given registry auth keychain for
// bundle retrieval. A nil keychain falls back to the default keychain.
func NewDefault(keychain authn.Keychain) *Default {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &Default{keychain: keychain}
}

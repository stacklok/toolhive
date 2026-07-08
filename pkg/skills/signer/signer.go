// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package signer signs skill OCI artifacts with Sigstore.
package signer

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	verifyBundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore/pkg/signature"
)

var (
	// ErrKeyRequired indicates keyless signing is not configured and --key is required.
	ErrKeyRequired = errors.New("signing key required: pass --key or configure keyless OIDC")
	// ErrSkipSigning indicates signing was explicitly skipped.
	ErrSkipSigning = errors.New("signing skipped")
)

// Options configures OCI signing.
type Options struct {
	// Key is the path to a cosign private key file. Empty means keyless (when supported).
	Key string
	// SkipSigning skips signing entirely.
	SkipSigning bool
}

// Signer signs and attaches Sigstore bundles to OCI artifacts.
type Signer interface {
	SignOCI(ctx context.Context, ref, digest string, opts Options) ([]byte, error)
}

// Default implements Signer.
type Default struct {
	keychain authn.Keychain
}

// NewDefault creates a signer using the given registry auth keychain.
func NewDefault(keychain authn.Keychain) *Default {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &Default{keychain: keychain}
}

// SignOCI signs the artifact and attaches the bundle as a cosign signature manifest.
func (d *Default) SignOCI(ctx context.Context, ref, digest string, opts Options) ([]byte, error) {
	if opts.SkipSigning {
		return nil, ErrSkipSigning
	}
	if opts.Key == "" {
		return nil, ErrKeyRequired
	}
	if _, err := os.Stat(opts.Key); err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}

	keypair, err := loadKeypair(opts.Key)
	if err != nil {
		return nil, err
	}
	digestBytes, err := digestBytesFromString(digest)
	if err != nil {
		return nil, err
	}
	sig, err := signDigestWithKeypair(ctx, keypair, digestBytes)
	if err != nil {
		return nil, fmt.Errorf("signing digest: %w", err)
	}
	if err := attachCosignSignature(ctx, d.keychain, ref, digest, sig); err != nil {
		return nil, fmt.Errorf("attaching signature manifest: %w", err)
	}

	return bundleBytesForDigest(keypair, digestBytes)
}

func bundleBytesForDigest(keypair sign.Keypair, digestBytes []byte) ([]byte, error) {
	pubKey := keypair.GetPublicKey()
	trusted := trustedPublicKeyMaterial(pubKey)
	pb, err := sign.Bundle(&sign.PlainData{Data: digestBytes}, keypair, sign.BundleOptions{
		TrustedRoot: trusted,
	})
	if err != nil {
		return nil, fmt.Errorf("building sigstore bundle: %w", err)
	}
	bun, err := verifyBundle.NewBundle(pb)
	if err != nil {
		return nil, err
	}
	return bun.MarshalJSON()
}

type nonExpiringVerifier struct {
	signature.Verifier
}

func (*nonExpiringVerifier) ValidAtTime(_ time.Time) bool {
	return true
}

func trustedPublicKeyMaterial(pk crypto.PublicKey) root.TrustedMaterial {
	return root.NewTrustedPublicKeyMaterial(func(string) (root.TimeConstrainedVerifier, error) {
		verifier, err := signature.LoadVerifier(pk, crypto.SHA256)
		if err != nil {
			return nil, err
		}
		return &nonExpiringVerifier{Verifier: verifier}, nil
	})
}

var _ Signer = (*Default)(nil)

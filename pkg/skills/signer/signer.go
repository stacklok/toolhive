// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package signer signs skill OCI artifacts with Sigstore, following the
// cosign key-pair convention: a "simple signing" payload binding the
// artifact's manifest digest is signed with the user's key, attached to the
// registry as a cosign signature manifest (the "sha256-<hex>.sig" tag), and
// returned as a serialized Sigstore bundle for durable storage and offline
// re-verification.
package signer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	verifybundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/sign"
)

//go:generate mockgen -destination=mocks/mock_signer.go -package=mocks -source=signer.go Signer

// ErrKeyRequired indicates no signing key was provided. Keyless (OIDC)
// signing is not implemented yet, so a cosign private key is the only
// supported signing method.
var ErrKeyRequired = errors.New("signing key required: pass --key with a cosign private key")

// Options configures OCI signing.
type Options struct {
	// Key is the path to a cosign PEM-encoded private key file, including
	// the format `cosign generate-key-pair` writes. An encrypted key is
	// decrypted with the COSIGN_PASSWORD environment variable of the
	// process doing the signing — for ToolHive that is the API server, not
	// the CLI invoking it.
	Key string
}

// Signer signs skill OCI artifacts and attaches the signature to the
// registry.
type Signer interface {
	// SignOCI signs the artifact at ref pinned to the given manifest digest
	// ("sha256:..."), attaches the signature as a cosign signature manifest
	// next to the artifact, and returns the serialized Sigstore bundle for
	// storage.
	SignOCI(ctx context.Context, ref, digest string, opts Options) ([]byte, error)
}

// Default implements Signer with file-based cosign keys.
type Default struct {
	keychain authn.Keychain
}

var _ Signer = (*Default)(nil)

// NewDefault creates a signer using the given registry auth keychain for
// pushing the signature manifest. A nil keychain falls back to the default
// keychain.
func NewDefault(keychain authn.Keychain) *Default {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &Default{keychain: keychain}
}

// SignOCI signs the artifact following the cosign convention: the signature
// is computed over the simple-signing payload (which embeds the manifest
// digest, binding the signature to the artifact), the SAME signature is
// attached to the registry as a cosign signature manifest, and the returned
// bundle carries it with the payload's digest as the signed message. A
// verifier reconstructing the bundle from the registry manifest (or
// re-verifying the stored bundle offline) therefore checks exactly the
// signature that was attached — one signature, two representations.
func (d *Default) SignOCI(ctx context.Context, ref, digestStr string, opts Options) ([]byte, error) {
	if opts.Key == "" {
		return nil, ErrKeyRequired
	}
	keypair, err := loadKeypair(opts.Key)
	if err != nil {
		return nil, err
	}

	payload, err := SimpleSigningPayload(ref, digestStr)
	if err != nil {
		return nil, err
	}

	// sign.Bundle invokes the keypair's SignData over the payload and
	// records both the signature and the payload digest in the bundle.
	pb, err := sign.Bundle(&sign.PlainData{Data: payload}, keypair, sign.BundleOptions{Context: ctx})
	if err != nil {
		return nil, fmt.Errorf("building sigstore bundle: %w", err)
	}
	msgSig := pb.GetMessageSignature()
	if msgSig == nil || len(msgSig.GetSignature()) == 0 {
		return nil, errors.New("signing produced no message signature")
	}

	if err := attachCosignSignature(ctx, d.keychain, ref, digestStr, payload, msgSig.GetSignature()); err != nil {
		return nil, fmt.Errorf("attaching signature manifest: %w", err)
	}

	bun, err := verifybundle.NewBundle(pb)
	if err != nil {
		return nil, fmt.Errorf("finalizing sigstore bundle: %w", err)
	}
	raw, err := bun.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("serializing sigstore bundle: %w", err)
	}
	return raw, nil
}

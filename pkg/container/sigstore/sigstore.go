// Package sigstore provides a client for verifying artifacts using sigstore
package sigstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/stacklok/toolhive/pkg/logger"
)

//go:embed tufroots
var embeddedTufRoots embed.FS

const (
	// TrustedRootSigstoreGitHub is the GitHub trusted root repository for sigstore
	TrustedRootSigstoreGitHub = "tuf-repo.github.com"
	// TrustedRootSigstorePublicGoodInstance is the public trusted root repository for sigstore
	TrustedRootSigstorePublicGoodInstance = "tuf-repo-cdn.sigstore.dev"
	// RootTUFPath is the path to the root.json file inside an embedded TUF repository
	rootTUFPath = "root.json"
)

// Sigstore is the sigstore verifier
type Sigstore struct {
	verifier *verify.SignedEntityVerifier
	authOpts []AuthMethod
}

// New creates a new Sigstore verifier
func New(authOpts ...AuthMethod) (*Sigstore, error) {
	sigstoreTUFRepoURL := TrustedRootSigstorePublicGoodInstance
	// Get the sigstore options for the TUF client and the verifier
	tufOpts, opts, err := getSigstoreOptions(sigstoreTUFRepoURL)
	if err != nil {
		return nil, err
	}

	// Get the trusted material - sigstore's trusted_root.json
	trustedMaterial, err := root.FetchTrustedRootWithOptions(tufOpts)
	if err != nil {
		return nil, err
	}

	sev, err := verify.NewSignedEntityVerifier(trustedMaterial, opts...)
	if err != nil {
		return nil, err
	}

	// return the verifier
	return &Sigstore{
		verifier: sev,
		authOpts: authOpts,
	}, nil
}

func parseImageRef(imageRef string) (string, string, string, error) {
	// Parse the image reference
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", "", "", fmt.Errorf("error parsing image reference: %w", err)
	}
	switch r := ref.(type) {
	case name.Tag:
		// 0. Get the auth options
		opts := []remote.Option{}

		// 2. Get the image descriptor
		desc, err := remote.Get(ref, opts...)
		if err != nil {
			return "", "", "", fmt.Errorf("error getting image descriptor: %w", err)
		}

		// 3. Get the digest
		digest := ref.Context().Digest(desc.Digest.String())
		owner := strings.Split(r.Context().Name(), "/")[0]
		artifact := strings.Split(r.Context().Name(), "/")[1]
		checksum := digest.Identifier()
		return owner, artifact, checksum, nil
	case name.Digest:
		// strip the reference into 2 parts - the registry and everything else
		parts := strings.SplitN(r.Context().Name(), "/", 2)
		if len(parts) != 2 {
			return "", "", "", fmt.Errorf("error: invalid image reference")
		}
		owner := parts[0]
		artifact := strings.Split(strings.Join(parts[1:], "/"), "@")[0]
		checksum := ref.Identifier()
		return owner, artifact, checksum, nil
	default:
		return "", "", "", fmt.Errorf("error: unknown image reference type")
	}
}

// Verify verifies a container artifact using sigstore
func (s *Sigstore) Verify(
	ctx context.Context,
	imageRef string,
) ([]Result, error) {
	// Construct the bundle(s) - OCI image or GitHub's attestation endpoint
	bundles, err := getSigstoreBundles(ctx, imageRef)
	if err != nil && !errors.Is(err, ErrProvenanceNotFoundOrIncomplete) {
		// We got some other unexpected error prior to querying for the signature/attestation
		return nil, err
	}
	logger.Debugf("Number of sigstore bundles we managed to construct is %d", len(bundles))

	// Exit early if we don't have any bundles to verify. We've tried building a bundle from the OCI image/the GitHub
	// attestation endpoint and failed. This means there's most probably no available provenance information about
	// this artifact, or it's incomplete.
	if len(bundles) == 0 || errors.Is(err, ErrProvenanceNotFoundOrIncomplete) {
		return []Result{{
			IsSigned:   false,
			IsVerified: false,
		}}, nil
	}

	// Construct the verification result for each bundle we managed to generate.
	return getVerifiedResults(ctx, s.verifier, bundles), nil
}

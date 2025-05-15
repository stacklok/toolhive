// Package sigstore provides a client for verifying artifacts using sigstore
package sigstore

import (
	"context"
	"embed"
	"errors"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/stacklok/toolhive/pkg/logger"
)

//go:embed tufroots
var embeddedTufRoots embed.FS

const (
	// GitHubSigstoreTrustedRootRepo is the GitHub trusted root repository for sigstore
	GitHubSigstoreTrustedRootRepo = "tuf-repo.github.com"
	// SigstorePublicTrustedRootRepo is the public trusted root repository for sigstore
	SigstorePublicTrustedRootRepo = "tuf-repo-cdn.sigstore.dev"
	// RootTUFPath is the path to the root.json file inside an embedded TUF repository
	rootTUFPath = "root.json"
)

// Sigstore is the sigstore verifier
type Sigstore struct {
	verifier *verify.SignedEntityVerifier
	authOpts []AuthMethod
}

// New creates a new Sigstore verifier
func New(sigstoreTUFRepoURL string, authOpts ...AuthMethod) (*Sigstore, error) {
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

// Verify verifies a container artifact using sigstore
func (s *Sigstore) Verify(
	ctx context.Context,
	sev *verify.SignedEntityVerifier,
	owner, artifact, checksumref string,
) ([]Result, error) {
	// Sanitize the input
	sanitizeInput(&owner)

	cauth := newContainerAuth(s.authOpts...)

	// Construct the bundle(s) - OCI image or GitHub's attestation endpoint
	bundles, err := getSigstoreBundles(ctx, owner, artifact, checksumref, cauth)
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
	return getVerifiedResults(ctx, sev, bundles), nil
}

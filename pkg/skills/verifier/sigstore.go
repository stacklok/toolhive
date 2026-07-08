// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"fmt"
	"net/url"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const trustedRootSigstorePublicGood = "tuf-repo-cdn.sigstore.dev"

func newSigstoreVerifier() (*verify.Verifier, error) {
	tufOpts, err := tufOptions(trustedRootSigstorePublicGood)
	if err != nil {
		return nil, err
	}
	trustedMaterial, err := root.FetchTrustedRootWithOptions(tufOpts)
	if err != nil {
		return nil, err
	}
	opts := []verify.VerifierOption{
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	}
	return verify.NewVerifier(trustedMaterial, opts...)
}

func tufOptions(sigstoreTUFRepoURL string) (*tuf.Options, error) {
	tufOpts := tuf.DefaultOptions()
	tufOpts.DisableLocalCache = true
	tufURL, err := url.Parse(sigstoreTUFRepoURL)
	if err != nil {
		return nil, fmt.Errorf("parsing sigstore TUF repo URL: %w", err)
	}
	if tufURL.Scheme == "" {
		tufURL.Scheme = "https"
	}
	tufOpts.RepositoryBaseURL = tufURL.String()
	return tufOpts, nil
}

func verifyPolicy(digestAlgo string, digestBytes []byte) verify.PolicyBuilder {
	return verify.NewPolicy(
		verify.WithArtifactDigest(digestAlgo, digestBytes),
		verify.WithoutIdentitiesUnsafe(),
	)
}

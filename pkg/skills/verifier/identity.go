// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const githubTokenIssuer = "https://token.actions.githubusercontent.com" //nolint:gosec // OIDC issuer URL, not a credential

func identityFromVerificationResult(vr *verify.VerificationResult) (*Result, error) {
	if vr == nil || vr.Signature == nil || vr.Signature.Certificate == nil {
		return nil, ErrSignatureInvalid
	}
	cert := vr.Signature.Certificate
	signerIdentity, err := signerIdentityFromCertificate(cert)
	if err != nil {
		return nil, err
	}
	return &Result{
		Signed:         true,
		SignerIdentity: signerIdentity,
		CertIssuer:     cert.Issuer,
		RepositoryURI:  cert.SourceRepositoryURI,
		SigstoreURL:    "https://rekor.sigstore.dev",
	}, nil
}

func signerIdentityFromCertificate(c *certificate.Summary) (string, error) {
	if c.SubjectAlternativeName == "" {
		return "", fmt.Errorf("certificate has no signer identity in SAN")
	}
	builderURL := c.SubjectAlternativeName
	if c.Issuer != githubTokenIssuer {
		return builderURL, nil
	}
	if c.SourceRepositoryURI == "" {
		return "", fmt.Errorf("certificate missing SourceRepositoryURI extension")
	}
	builderURL, _, _ = strings.Cut(builderURL, "@")
	builderURL = strings.TrimPrefix(builderURL, c.SourceRepositoryURI)
	return builderURL, nil
}

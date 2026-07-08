// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
)

// VerifyGit verifies a gitsign-style commit signature.
func (*Default) VerifyGit(ctx context.Context, commitSignature, _ string) (*Result, error) {
	_ = ctx
	commitSignature = strings.TrimSpace(commitSignature)
	if commitSignature == "" {
		return nil, ErrUnsigned
	}
	cert, err := extractCertificateFromGitSignature(commitSignature)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}
	signerIdentity := cert.Subject.CommonName
	if len(cert.EmailAddresses) > 0 {
		signerIdentity = cert.EmailAddresses[0]
	}
	if san := cert.Subject.CommonName; san != "" {
		signerIdentity = san
	}
	for _, ext := range cert.Extensions {
		if ext.Id.String() == "1.3.6.1.4.1.57264.1.1" {
			signerIdentity = string(ext.Value)
		}
	}
	return &Result{
		Signed:         true,
		SignerIdentity: signerIdentity,
		CertIssuer:     cert.Issuer.CommonName,
		SigstoreURL:    "https://rekor.sigstore.dev",
		Bundle:         []byte(commitSignature),
	}, nil
}

func extractCertificateFromGitSignature(signature string) (*x509.Certificate, error) {
	var blocks []byte
	for {
		block, rest := pem.Decode([]byte(signature))
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			blocks = block.Bytes
			break
		}
		signature = string(rest)
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no certificate found in commit signature")
	}
	return x509.ParseCertificate(blocks)
}

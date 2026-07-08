// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/signer"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

func (s *service) artifactVerifier() verifier.Verifier {
	if s.sigVerifier != nil {
		return s.sigVerifier
	}
	return verifier.NewDefault(images.NewCompositeKeychain())
}

func (s *service) artifactSigner() signer.Signer {
	if s.sigSigner != nil {
		return s.sigSigner
	}
	return signer.NewDefault(images.NewCompositeKeychain())
}

type provenanceDecision struct {
	provenance *skills.ProvenanceInfo
	unsigned   bool
	bundle     []byte
}

func (s *service) verifyOCIInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName, ref, digest string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	if expectUnsigned {
		if !opts.AllowUnsigned {
			return nil, httperr.WithCode(errors.New("locked skill is marked unsigned; pass --allow-unsigned"), http.StatusForbidden)
		}
		return &provenanceDecision{unsigned: true}, nil
	}

	v := s.artifactVerifier()
	result, verifyErr := v.VerifyOCI(ctx, ref, digest)
	if verifyErr != nil {
		if errors.Is(verifyErr, verifier.ErrUnsigned) {
			if !opts.AllowUnsigned {
				return nil, httperr.WithCode(
					fmt.Errorf("unsigned skill %q rejected; pass --allow-unsigned to record an exception", skillName),
					http.StatusForbidden,
				)
			}
			return &provenanceDecision{unsigned: true}, nil
		}
		return nil, httperr.WithCode(
			fmt.Errorf("signature verification failed for %q: %w", skillName, verifyErr),
			http.StatusForbidden,
		)
	}
	if err := matchExpectedIdentity(result, expected); err != nil {
		if errors.Is(err, verifier.ErrSignerMismatch) {
			return nil, httperr.WithCode(
				fmt.Errorf("signer identity mismatch for %q: locked %q got %q",
					skillName, expected.SignerIdentity, result.SignerIdentity),
				http.StatusForbidden,
			)
		}
		return nil, httperr.WithCode(err, http.StatusForbidden)
	}
	return &provenanceDecision{
		provenance: provenanceInfoFromResult(result),
		bundle:     result.Bundle,
	}, nil
}

func (s *service) verifyGitInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName, commitHash, commitSignature string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	if expectUnsigned {
		if !opts.AllowUnsigned {
			return nil, httperr.WithCode(errors.New("locked skill is marked unsigned; pass --allow-unsigned"), http.StatusForbidden)
		}
		return &provenanceDecision{unsigned: true}, nil
	}

	v := s.artifactVerifier()
	result, verifyErr := v.VerifyGit(ctx, commitSignature, commitHash)
	if verifyErr != nil {
		if errors.Is(verifyErr, verifier.ErrUnsigned) {
			if !opts.AllowUnsigned {
				return nil, httperr.WithCode(
					fmt.Errorf("unsigned git commit for %q rejected; pass --allow-unsigned", skillName),
					http.StatusForbidden,
				)
			}
			return &provenanceDecision{unsigned: true}, nil
		}
		return nil, httperr.WithCode(
			fmt.Errorf("git signature verification failed for %q: %w", skillName, verifyErr),
			http.StatusForbidden,
		)
	}
	if err := matchExpectedIdentity(result, expected); err != nil {
		return nil, httperr.WithCode(err, http.StatusForbidden)
	}
	return &provenanceDecision{
		provenance: provenanceInfoFromResult(result),
		bundle:     result.Bundle,
	}, nil
}

func verifyLocalInstall(opts skills.InstallOptions, skillName string) (*provenanceDecision, error) {
	if opts.Scope != skills.ScopeProject || opts.ProjectRoot == "" {
		return &provenanceDecision{}, nil
	}
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	if expectUnsigned {
		return &provenanceDecision{unsigned: true}, nil
	}
	if expected != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("local build for %q cannot satisfy locked signer identity", skillName),
			http.StatusForbidden,
		)
	}
	if !opts.AllowUnsigned {
		return nil, httperr.WithCode(
			fmt.Errorf("local build for %q is unsigned; pass --allow-unsigned for project scope", skillName),
			http.StatusForbidden,
		)
	}
	return &provenanceDecision{unsigned: true}, nil
}

func expectedLockTrust(projectRoot, skillName string) (*skills.ProvenanceInfo, bool, error) {
	if projectRoot == "" {
		return nil, false, nil
	}
	lf, err := lockfile.Load(projectRoot)
	if err != nil {
		return nil, false, err
	}
	entry, ok := lf.Get(skillName)
	if !ok {
		return nil, false, nil
	}
	if entry.Unsigned {
		return nil, true, nil
	}
	return provenanceInfoFromLock(entry.Provenance), false, nil
}

func matchExpectedIdentity(result *verifier.Result, expected *skills.ProvenanceInfo) error {
	if expected == nil {
		return nil
	}
	return verifier.MatchIdentity(result, provenanceInfoToLock(expected))
}

func classifySignatureError(err error) skills.FailureReason {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, verifier.ErrSignerMismatch):
		return skills.FailureReasonSignerMismatch
	case errors.Is(err, verifier.ErrUnsigned):
		return skills.FailureReasonUnsignedRejected
	case errors.Is(err, verifier.ErrSignatureInvalid), errors.Is(err, verifier.ErrBundleNotFound):
		return skills.FailureReasonSignatureInvalid
	default:
		return skills.FailureReasonUnknown
	}
}

func applyDecisionToOpts(opts *skills.InstallOptions, decision *provenanceDecision) {
	if decision == nil {
		return
	}
	opts.Provenance = decision.provenance
	opts.Unsigned = decision.unsigned
	opts.SigstoreBundle = decision.bundle
}

func provenanceInfoToLock(p *skills.ProvenanceInfo) *lockfile.Provenance {
	if p == nil {
		return nil
	}
	return &lockfile.Provenance{
		SignerIdentity: p.SignerIdentity,
		CertIssuer:     p.CertIssuer,
		RepositoryURI:  p.RepositoryURI,
		SigstoreURL:    p.SigstoreURL,
	}
}

func provenanceInfoFromLock(p *lockfile.Provenance) *skills.ProvenanceInfo {
	if p == nil {
		return nil
	}
	return &skills.ProvenanceInfo{
		SignerIdentity: p.SignerIdentity,
		CertIssuer:     p.CertIssuer,
		RepositoryURI:  p.RepositoryURI,
		SigstoreURL:    p.SigstoreURL,
	}
}

func provenanceInfoFromResult(r *verifier.Result) *skills.ProvenanceInfo {
	if r == nil || !r.Signed {
		return nil
	}
	return &skills.ProvenanceInfo{
		SignerIdentity: r.SignerIdentity,
		CertIssuer:     r.CertIssuer,
		RepositoryURI:  r.RepositoryURI,
		SigstoreURL:    r.SigstoreURL,
	}
}

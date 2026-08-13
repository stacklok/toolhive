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
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

// artifactVerifier returns the configured signature verifier, defaulting to
// the Sigstore verifier with the composite registry keychain.
func (s *service) artifactVerifier() verifier.Verifier {
	if s.sigVerifier != nil {
		return s.sigVerifier
	}
	return verifier.NewDefault(images.NewCompositeKeychain())
}

// shouldVerifyInstall reports whether install-time signature verification
// applies: project-scope installs. The lock file is where trust decisions
// are recorded, so verification is scoped to it.
func shouldVerifyInstall(opts skills.InstallOptions, scope skills.Scope) bool {
	return scope == skills.ScopeProject && opts.ProjectRoot != ""
}

// provenanceDecision is the outcome of install-time verification: either a
// verified identity (with the bundle backing it) or an explicit unsigned
// exception.
type provenanceDecision struct {
	provenance *skills.ProvenanceInfo
	unsigned   bool
	bundle     []byte
}

// applyDecisionToOpts records the verification outcome on the install
// options, from where it flows into the installed-skill record and the lock
// entry.
func applyDecisionToOpts(opts *skills.InstallOptions, decision *provenanceDecision) {
	if decision == nil {
		return
	}
	opts.Provenance = decision.provenance
	opts.Unsigned = decision.unsigned
	opts.SigstoreBundle = decision.bundle
}

// verifyOCIInstall verifies the signature of the OCI artifact at ref/digest
// before anything is extracted or recorded. The identity expected by the
// lock file (if any) is enforced inside the verifier's Sigstore policy;
// trust on first use records whatever identity verification observes.
func (s *service) verifyOCIInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName, ref, digest string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	if opts.AllowSignerChange {
		// The signer-change guard was explicitly overridden: verify the
		// chain of trust only and re-record whatever identity is observed.
		expected, expectUnsigned = nil, false
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, skillName)
	}

	result, verifyErr := s.artifactVerifier().VerifyOCI(ctx, ref, digest, expected)
	if verifyErr != nil {
		if isAllowedUnsigned(verifyErr, opts, expected) {
			return &provenanceDecision{unsigned: true}, nil
		}
		return nil, classifyInstallVerifyError(verifyErr, skillName, expected)
	}
	return &provenanceDecision{
		provenance: provenanceInfoFromResult(result),
		bundle:     result.Bundle,
	}, nil
}

// verifyGitInstall verifies the gitsign signature on the resolved commit
// before anything is written or recorded.
func (s *service) verifyGitInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName string,
	payload []byte,
	signature string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	if opts.AllowSignerChange {
		expected, expectUnsigned = nil, false
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, skillName)
	}

	result, verifyErr := s.artifactVerifier().VerifyGit(ctx, payload, []byte(signature), expected)
	if verifyErr != nil {
		if isAllowedUnsigned(verifyErr, opts, expected) {
			return &provenanceDecision{unsigned: true}, nil
		}
		return nil, classifyInstallVerifyError(verifyErr, skillName, expected)
	}
	return &provenanceDecision{
		provenance: provenanceInfoFromResult(result),
		bundle:     result.Bundle,
	}, nil
}

// verifyLocalInstall handles installs sourced from the local OCI store or
// raw layer data: there is no registry signature to verify, so the install
// is an unsigned trust decision. An entry already locked to a signer
// identity refuses a local replacement outright — swapping a verified
// artifact for a local build is exactly the substitution the lock exists to
// catch.
func verifyLocalInstall(opts skills.InstallOptions, skillName string) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	if expected != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("skill %q is locked to signer %q; a local build cannot satisfy it",
				skillName, expected.SignerIdentity),
			http.StatusForbidden,
		)
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, skillName)
	}
	if !opts.AllowUnsigned {
		return nil, httperr.WithCode(
			fmt.Errorf("local build for %q is unsigned; set allow_unsigned (--allow-unsigned) to record an exception",
				skillName),
			http.StatusForbidden,
		)
	}
	return &provenanceDecision{unsigned: true}, nil
}

// unsignedLockedDecision handles installs of entries the lock file already
// marks unsigned. A lock-driven operation honors the recorded decision (the
// lock file IS the policy it restores); a fresh user-driven install must
// repeat the explicit exception.
//
// SECURITY: this early return means an entry marked unsigned is installed
// without consulting the verifier at all — by design, but it makes a lock
// diff that converts `provenance:` to `unsigned: true` a trust DOWNGRADE
// that sync will honor silently. That conversion is exactly what lock file
// review must catch; it cannot happen without a lock file edit.
func unsignedLockedDecision(opts skills.InstallOptions, skillName string) (*provenanceDecision, error) {
	if opts.AllowUnsigned || lockDrivenInstall(opts) {
		return &provenanceDecision{unsigned: true}, nil
	}
	return nil, httperr.WithCode(
		fmt.Errorf("skill %q is locked as unsigned; set allow_unsigned (--allow-unsigned) to reinstall it", skillName),
		http.StatusForbidden,
	)
}

// expectedLockTrust reads the trust state recorded in projectRoot's lock
// file for skillName: the expected signer identity (nil on first use — the
// TOFU case), or that the entry was recorded unsigned.
func expectedLockTrust(projectRoot, skillName string) (*lockfile.Provenance, bool, error) {
	if projectRoot == "" {
		return nil, false, nil
	}
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, false, err
	}
	lf, err := lockfile.Load(root)
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
	return entry.Provenance, false, nil
}

// isAllowedUnsigned reports whether a verification failure is the unsigned
// case AND the caller may proceed: either the explicit --allow-unsigned
// exception, or a sync restore of an entry with no recorded trust state
// (entries created before verification existed) — a restore materializes
// what install once accepted, and the outcome is recorded as unsigned so
// the trust state stops being ambiguous. An entry locked to a signer
// identity is never replaceable by an unsigned artifact.
func isAllowedUnsigned(verifyErr error, opts skills.InstallOptions, expected *lockfile.Provenance) bool {
	if !errors.Is(verifyErr, verifier.ErrUnsigned) || expected != nil {
		return false
	}
	return opts.AllowUnsigned || lockDrivenInstall(opts)
}

// lockDrivenInstall reports whether this install materializes an existing
// lock entry rather than making a new trust decision: sync restores and
// upgrade re-pins (both set internal-only options). Such operations honor
// the trust state the entry already records.
func lockDrivenInstall(opts skills.InstallOptions) bool {
	return opts.SyncRestore || opts.LockResolvedReference != ""
}

// classifyInstallVerifyError maps a verifier failure to the HTTP-coded
// error surfaced by the install API — always a 403; the allowed-unsigned
// path is handled before this is called.
func classifyInstallVerifyError(
	verifyErr error,
	skillName string,
	expected *lockfile.Provenance,
) error {
	switch {
	case errors.Is(verifyErr, verifier.ErrUnsigned):
		if expected != nil {
			return httperr.WithCode(
				fmt.Errorf("skill %q is locked to signer %q but the artifact is unsigned",
					skillName, expected.SignerIdentity),
				http.StatusForbidden,
			)
		}
		return httperr.WithCode(
			fmt.Errorf("unsigned skill %q rejected; set allow_unsigned (--allow-unsigned) to record an exception",
				skillName),
			http.StatusForbidden,
		)
	case errors.Is(verifyErr, verifier.ErrSignerMismatch):
		return httperr.WithCode(
			fmt.Errorf("signer identity mismatch for %q: %w"+
				" (if the signer change is intended, remove the skill's lock entry"+
				" and reinstall, or upgrade with allow_signer_change)", skillName, verifyErr),
			http.StatusForbidden,
		)
	default:
		return httperr.WithCode(
			fmt.Errorf("signature verification failed for %q: %w", skillName, verifyErr),
			http.StatusForbidden,
		)
	}
}

// classifySignatureError maps verifier sentinels to typed failure reasons
// for sync/upgrade results. Returns "" when err is not a signature failure.
func classifySignatureError(err error) skills.FailureReason {
	switch {
	case errors.Is(err, verifier.ErrSignerMismatch):
		return skills.FailureReasonSignerMismatch
	case errors.Is(err, verifier.ErrUnsigned):
		return skills.FailureReasonUnsignedRejected
	case errors.Is(err, verifier.ErrSignatureInvalid):
		return skills.FailureReasonSignatureInvalid
	default:
		return ""
	}
}

// provenanceInfoFromLock converts a lock provenance block to the API shape.
func provenanceInfoFromLock(p *lockfile.Provenance) *skills.ProvenanceInfo {
	if p == nil {
		return nil
	}
	return &skills.ProvenanceInfo{
		SignerIdentity:    p.SignerIdentity,
		CertIssuer:        p.CertIssuer,
		RepositoryURI:     p.RepositoryURI,
		RepositoryRef:     p.RepositoryRef,
		RunnerEnvironment: p.RunnerEnvironment,
		SigstoreURL:       p.SigstoreURL,
		Provisional:       p.Provisional,
	}
}

// provenanceInfoToLock converts the internal provenance shape to the lock
// file's.
func provenanceInfoToLock(p *skills.ProvenanceInfo) *lockfile.Provenance {
	if p == nil {
		return nil
	}
	return &lockfile.Provenance{
		SignerIdentity:    p.SignerIdentity,
		CertIssuer:        p.CertIssuer,
		RepositoryURI:     p.RepositoryURI,
		RepositoryRef:     p.RepositoryRef,
		RunnerEnvironment: p.RunnerEnvironment,
		SigstoreURL:       p.SigstoreURL,
		Provisional:       p.Provisional,
	}
}

// provenanceInfoFromResult converts a verification result into the internal
// provenance shape recorded on install options.
func provenanceInfoFromResult(r *verifier.Result) *skills.ProvenanceInfo {
	if r == nil || !r.Signed || r.SignerIdentity == "" {
		return nil
	}
	return &skills.ProvenanceInfo{
		SignerIdentity: r.SignerIdentity,
		CertIssuer:     r.CertIssuer,
		RepositoryURI:  r.RepositoryURI,
		SigstoreURL:    r.SigstoreURL,
		Provisional:    r.Provisional,
	}
}

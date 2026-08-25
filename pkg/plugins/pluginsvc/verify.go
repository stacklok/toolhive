// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

// maxSignatureBlobSize bounds every attacker-influenced blob install-time
// verification captures: the Sigstore bundle persisted with the install
// record, and the commit payload and gitsign signature a git install verifies.
// None is bounded at its source — a registry serves whatever bundle it likes,
// and git imposes no length limit on a commit message — so without a ceiling a
// hostile plugin source can push a multi-MB blob into SQLite on every install
// and make every sync read it back (#6396 review). A real keyless bundle or
// gitsign signature is a few KB (a certificate chain, a signature, an
// inclusion proof), so 1 MiB is orders of magnitude above any legitimate
// value while still refusing the abuse. Over-limit material is rejected, not
// truncated: a truncated bundle would fail to verify later and be
// indistinguishable from tampering.
const maxSignatureBlobSize = 1 << 20 // 1 MiB

// artifactVerifier returns the configured signature verifier, defaulting to
// the Sigstore verifier with the composite registry keychain. Plugins reuse
// the skills verifier wholesale: the Sigstore policy, the trust-on-first-use
// contract, and the pinned ref/runner enforcement are the same trust model
// applied to a different artifact type.
func (s *service) artifactVerifier() verifier.Verifier {
	if s.sigVerifier != nil {
		return s.sigVerifier
	}
	return verifier.NewDefault(images.NewCompositeKeychain())
}

// shouldVerifyInstall reports whether install-time signature verification
// applies: project-scope installs that record lock state. The lock file is
// where trust decisions live, so verification is scoped to exactly the
// installs that write one — including the feature gate, since a disabled
// lock file has nowhere to anchor trust on first use.
func shouldVerifyInstall(opts plugins.InstallOptions, scope plugins.Scope) bool {
	return scope == plugins.ScopeProject && opts.ProjectRoot != "" && plugins.LockFileFeatureEnabled()
}

// provenanceDecision is the outcome of install-time verification: either a
// verified identity (with the bundle backing it) or an explicit unsigned
// exception.
type provenanceDecision struct {
	provenance *lockfile.Provenance
	unsigned   bool
	bundle     []byte
}

// applyDecisionToOpts records the verification outcome on the install
// options, from where it flows into the installed-plugin record and the lock
// entry.
func applyDecisionToOpts(opts *plugins.InstallOptions, decision *provenanceDecision) {
	if decision == nil {
		return
	}
	opts.Provenance = decision.provenance
	opts.Unsigned = decision.unsigned
	opts.SigstoreBundle = decision.bundle
}

// rejectOversizedBlob rejects signature material over maxSignatureBlobSize.
// Reported as unprocessable rather than as a signature failure: the artifact
// may well be correctly signed, but its material is not something ToolHive
// will store.
func rejectOversizedBlob(kind, pluginName string, size int) error {
	if size <= maxSignatureBlobSize {
		return nil
	}
	return httperr.WithCode(
		fmt.Errorf("%s for plugin %q is %d bytes, over the %d-byte limit",
			kind, pluginName, size, maxSignatureBlobSize),
		http.StatusUnprocessableEntity,
	)
}

// signedDecision records a successful verification, refusing an oversized
// bundle before it reaches InstallOptions and the store.
func signedDecision(result *verifier.Result, pluginName string) (*provenanceDecision, error) {
	if err := rejectOversizedBlob("sigstore bundle", pluginName, len(result.Bundle)); err != nil {
		return nil, err
	}
	return &provenanceDecision{provenance: result.ToLockProvenance(), bundle: result.Bundle}, nil
}

// verifyOCIInstall verifies the signature of the OCI artifact at ref/digest
// before anything is extracted or recorded. The identity expected by the
// lock file (if any) is enforced inside the verifier's Sigstore policy;
// trust on first use records whatever identity verification observes.
func (s *service) verifyOCIInstall(
	ctx context.Context,
	opts plugins.InstallOptions,
	pluginName, ref, digest string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, pluginName)
	if err != nil {
		return nil, err
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, pluginName)
	}

	// The verifier takes a ProvenanceExpectation so it can distinguish a
	// strict lock pin from the independently-optional catalog constraints
	// added in #6420. Plugins only ever present a lock expectation today;
	// NewLockExpectation(nil) is nil, preserving the TOFU case.
	result, verifyErr := s.artifactVerifier().VerifyOCI(ctx, ref, digest, verifier.NewLockExpectation(expected))
	if verifyErr != nil {
		if isAllowedUnsigned(verifyErr, opts, expected) {
			return &provenanceDecision{unsigned: true}, nil
		}
		return nil, classifyInstallVerifyError(verifyErr, pluginName, expected)
	}
	return signedDecision(result, pluginName)
}

// verifyGitInstall verifies the gitsign signature on the resolved commit
// before anything is written or recorded.
func (s *service) verifyGitInstall(
	ctx context.Context,
	opts plugins.InstallOptions,
	pluginName string,
	payload []byte,
	signature string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, pluginName)
	if err != nil {
		return nil, err
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, pluginName)
	}
	// Checked before verification: both come straight off a remote commit,
	// so refusing them early keeps a hostile repo from spending our CPU.
	if err := rejectOversizedBlob("commit payload", pluginName, len(payload)); err != nil {
		return nil, err
	}
	if err := rejectOversizedBlob("commit signature", pluginName, len(signature)); err != nil {
		return nil, err
	}

	result, verifyErr := s.artifactVerifier().VerifyGit(
		ctx, payload, []byte(signature), verifier.NewLockExpectation(expected))
	if verifyErr != nil {
		if isAllowedUnsigned(verifyErr, opts, expected) {
			return &provenanceDecision{unsigned: true}, nil
		}
		return nil, classifyInstallVerifyError(verifyErr, pluginName, expected)
	}
	return signedDecision(result, pluginName)
}

// verifyLocalInstall handles installs sourced from the local OCI store or
// raw layer data: there is no registry signature to verify, so the install
// is an unsigned trust decision. An entry already locked to a signer
// identity refuses a local replacement outright — swapping a verified
// artifact for a local build is exactly the substitution the lock exists to
// catch.
func verifyLocalInstall(opts plugins.InstallOptions, pluginName string) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, pluginName)
	if err != nil {
		return nil, err
	}
	if expected != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin %q is locked to signer %q; a local build cannot satisfy it",
				pluginName, expected.SignerIdentity),
			http.StatusForbidden,
		)
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, pluginName)
	}
	// A lock-driven restore of an entry with no recorded trust state
	// materializes what install once accepted, so it records unsigned
	// rather than demanding a flag the operation has no way to pass —
	// the same allowance isAllowedUnsigned makes for OCI and git.
	if !opts.AllowUnsigned && !lockDrivenInstall(opts) {
		return nil, httperr.WithCode(
			fmt.Errorf("local build for %q is unsigned; set allow_unsigned (--allow-unsigned) to record an exception",
				pluginName),
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
// that sync will honor silently. For plugins the blast radius is the whole
// client: an unsigned plugin can ship hooks and MCP servers the client
// loads. That conversion is exactly what lock file review must catch; it
// cannot happen without a lock file edit.
func unsignedLockedDecision(opts plugins.InstallOptions, pluginName string) (*provenanceDecision, error) {
	if opts.AllowUnsigned || lockDrivenInstall(opts) {
		return &provenanceDecision{unsigned: true}, nil
	}
	return nil, httperr.WithCode(
		fmt.Errorf("plugin %q is locked as unsigned; set allow_unsigned (--allow-unsigned) to reinstall it", pluginName),
		http.StatusForbidden,
	)
}

// expectedLockTrust reads the trust state recorded in projectRoot's lock
// file for pluginName: the expected signer identity (nil on first use — the
// TOFU case), or that the entry was recorded unsigned.
func expectedLockTrust(projectRoot, pluginName string) (*lockfile.Provenance, bool, error) {
	if projectRoot == "" {
		return nil, false, nil
	}
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, false, fmt.Errorf("reading lock trust state for %q: %w", pluginName, err)
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, false, fmt.Errorf("reading lock trust state for %q: %w", pluginName, err)
	}
	entry, ok := lf.GetPlugin(pluginName)
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
func isAllowedUnsigned(verifyErr error, opts plugins.InstallOptions, expected *lockfile.Provenance) bool {
	if !errors.Is(verifyErr, verifier.ErrUnsigned) || expected != nil {
		return false
	}
	return opts.AllowUnsigned || lockDrivenInstall(opts)
}

// lockDrivenInstall reports whether this install materializes an existing
// lock entry rather than making a new trust decision: sync restores and
// upgrade re-pins (all set internal-only options no HTTP caller can reach).
// Such operations honor the trust state the entry already records.
//
// ExpectedCanonicalName is part of the test where skills gets by with the
// other two: a plugin upgrade off the local store deliberately clears
// LockResolvedReference so sync restores by digest, so those two markers
// alone would misread it as a fresh user install and demand a flag the
// upgrade API has no way to pass.
func lockDrivenInstall(opts plugins.InstallOptions) bool {
	return opts.SyncRestore || opts.LockResolvedReference != "" || opts.ExpectedCanonicalName != ""
}

// classifyInstallVerifyError maps a verifier failure to the HTTP-coded
// error surfaced by the install API — always a 403; the allowed-unsigned
// path is handled before this is called.
func classifyInstallVerifyError(
	verifyErr error,
	pluginName string,
	expected *lockfile.Provenance,
) error {
	switch {
	case errors.Is(verifyErr, verifier.ErrUnsigned):
		if expected != nil {
			return httperr.WithCode(
				fmt.Errorf("plugin %q is locked to signer %q but the artifact is unsigned",
					pluginName, expected.SignerIdentity),
				http.StatusForbidden,
			)
		}
		return httperr.WithCode(
			fmt.Errorf("unsigned plugin %q rejected; set allow_unsigned (--allow-unsigned) to record an exception",
				pluginName),
			http.StatusForbidden,
		)
	// Checked before the broader ErrSignerMismatch case: pinnedFieldMismatch
	// wraps both, so a ref/runner-only mismatch would otherwise be
	// misreported as a signer-identity change below.
	case errors.Is(verifyErr, verifier.ErrProvenanceFieldMismatch):
		return httperr.WithCode(
			fmt.Errorf("plugin %q's certificate no longer matches its pinned provenance: %w", pluginName, verifyErr),
			http.StatusForbidden,
		)
	case errors.Is(verifyErr, verifier.ErrSignerMismatch):
		return httperr.WithCode(
			fmt.Errorf("signer identity mismatch for %q: %w"+
				" (if the signer change is intended, remove the plugin's lock entry and reinstall)",
				pluginName, verifyErr),
			http.StatusForbidden,
		)
	default:
		return httperr.WithCode(
			fmt.Errorf("signature verification failed for %q: %w", pluginName, verifyErr),
			http.StatusForbidden,
		)
	}
}

// classifySignatureError maps verifier sentinels to typed failure reasons
// for sync/upgrade results. Returns "" when err is not a signature failure.
func classifySignatureError(err error) plugins.FailureReason {
	switch {
	// Checked before the broader ErrSignerMismatch case for the same reason
	// as classifyInstallVerifyError above.
	case errors.Is(err, verifier.ErrProvenanceFieldMismatch):
		return plugins.FailureReasonProvenanceFieldMismatch
	case errors.Is(err, verifier.ErrSignerMismatch):
		return plugins.FailureReasonSignerMismatch
	case errors.Is(err, verifier.ErrUnsigned):
		return plugins.FailureReasonUnsignedRejected
	case errors.Is(err, verifier.ErrSignatureInvalid):
		return plugins.FailureReasonSignatureInvalid
	default:
		return ""
	}
}

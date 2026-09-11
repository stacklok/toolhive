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
// installs that write one.
func shouldVerifyInstall(opts plugins.InstallOptions, scope plugins.Scope) bool {
	return scope == plugins.ScopeProject && opts.ProjectRoot != ""
}

// validateInstallPublicKey rejects a supplied public key before any resolve
// or fetch work begins. Both checks exist so the key is never accepted and
// then quietly unused: an install that does not verify would drop it on the
// floor, and a malformed one would otherwise surface as a verification
// failure deep in the install, long after the input that caused it.
func validateInstallPublicKey(opts plugins.InstallOptions, scope plugins.Scope) error {
	if opts.PublicKey == "" {
		return nil
	}
	if !shouldVerifyInstall(opts, scope) {
		return httperr.WithCode(
			errors.New("public_key (--public-key) applies to project-scoped installs, which are the ones"+
				" whose trust anchor a lock file records; this install would verify nothing"),
			http.StatusBadRequest,
		)
	}
	if _, err := verifier.DecodePublicKey(opts.PublicKey); err != nil {
		return httperr.WithCode(fmt.Errorf("public_key: %w", err), http.StatusBadRequest)
	}
	return nil
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
//
// An entry pinned to a cosign public key, or a first install that supplies
// one, takes the key path instead. Which path runs is decided by the lock
// file and the caller, never by what the artifact turns out to carry: letting
// the artifact select its own verification policy would let a republished
// key-signed artifact walk out of the identity its entry is pinned to.
func (s *service) verifyOCIInstall(
	ctx context.Context,
	opts plugins.InstallOptions,
	pluginName, ref, digest string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, err := expectedLockTrust(opts.ProjectRoot, pluginName)
	if err != nil {
		return nil, err
	}
	keyAnchor, err := resolveKeyAnchor(opts, pluginName, expected, expectUnsigned)
	if err != nil {
		return nil, err
	}
	if keyAnchor != "" {
		return s.verifyOCIInstallWithKey(ctx, keyAnchor, pluginName, ref, digest)
	}
	if opts.AllowSignerChange {
		// The signer-change guard was explicitly overridden: verify the
		// chain of trust only and re-record whatever identity is observed.
		expected, expectUnsigned = nil, false
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
		return nil, classifyInstallVerifyError(verifyErr, pluginName, expected, opts)
	}
	return signedDecision(result, pluginName)
}

// resolveKeyAnchor decides which cosign public key, if any, this install
// verifies against, returning "" for the ordinary keyless path.
//
// Dispatch is lock-first: a key-pinned entry selects the key path using the
// key the LOCK records, so a supplied key can confirm that pin but never
// replace it. A supplied key is itself the anchor only on true first use,
// where nothing is recorded yet and the key is the only thing that can supply
// one.
//
// Every disagreement between the supplied key and the recorded trust state is
// an error rather than a precedence rule. Silently preferring one of two
// conflicting anchors is how a mistyped --public-key installs as though it had
// been honored — and a caller who names a trust anchor has said they want it
// enforced, so the honest answer to "that is not the anchor here" is to stop.
//
// Unlike skills there is no catalog arm: a plugin install presents no
// catalog-declared provenance expectation, so first use has nothing but the
// supplied key to weigh.
func resolveKeyAnchor(
	opts plugins.InstallOptions,
	pluginName string,
	expected *lockfile.Provenance,
	expectUnsigned bool,
) (string, error) {
	supplied := opts.PublicKey
	locked := ""
	if expected != nil {
		locked = expected.PublicKey
	}

	if opts.AllowSignerChange {
		// The override re-verifies from scratch and re-records what it
		// observes. For a key there is nothing to observe — a key-pair bundle
		// carries no identity — so honoring a key here would mean re-anchoring
		// to whatever key the caller named, on the strength of the caller
		// having named it. That is the in-place re-anchor v1 deliberately does
		// not offer. Without a key the override drops the recorded one and
		// takes the keyless path, which is the supported key-to-keyless move.
		if supplied != "" {
			return "", httperr.WithCode(
				fmt.Errorf("plugin %q: a public key cannot be combined with allow_signer_change;"+
					" re-anchoring an entry to a different key is not supported —"+
					" uninstall the plugin and reinstall it with the new key", pluginName),
				http.StatusBadRequest,
			)
		}
		return "", nil
	}

	switch {
	case locked != "" && supplied != "" && supplied != locked:
		return "", keyAnchorConflict(pluginName,
			"is pinned to a different cosign public key than the one supplied")
	case locked != "":
		return locked, nil
	case supplied == "":
		return "", nil
	// A key was supplied and the entry is not key-pinned. Each remaining case
	// already records an anchor the key would have to displace.
	case expected != nil:
		return "", keyAnchorConflict(pluginName,
			fmt.Sprintf("is pinned to signer %q, and a cosign key pair carries no certificate identity"+
				" that could satisfy it", expected.SignerIdentity))
	case expectUnsigned:
		return "", keyAnchorConflict(pluginName,
			"is recorded as an explicit unsigned exception, which a public key cannot upgrade in place")
	default:
		return supplied, nil
	}
}

// keyAnchorConflict reports a supplied public key that contradicts the trust
// state the lock file already records. The remedy is the same for all of them
// — v1 has no in-place re-anchor path — so it is stated once here.
func keyAnchorConflict(pluginName, problem string) error {
	return httperr.WithCode(
		fmt.Errorf("plugin %q %s; to install it under a different trust anchor,"+
			" uninstall the plugin and reinstall it", pluginName, problem),
		http.StatusForbidden,
	)
}

// lockedAnchorDescription names the trust anchor an entry records, for error
// messages that report an artifact failing to satisfy it. A key-pinned entry
// has no signer identity, and rendering it as one would print an empty pin
// and read as a bug in the lock file rather than a refusal.
func lockedAnchorDescription(expected *lockfile.Provenance) string {
	if expected.PublicKey != "" {
		return "a cosign public key"
	}
	return fmt.Sprintf("signer %q", expected.SignerIdentity)
}

// verifyOCIInstallWithKey verifies the artifact against a cosign public key
// and records that key as the entry's trust anchor.
//
// Unlike the keyless path there is nothing to observe: a key-pair bundle
// carries no certificate, so the provenance recorded is the key that was
// checked rather than an identity read off the artifact. That makes this a
// weaker claim than keyless provenance — it says the holder of this key signed
// this artifact, and nothing about who that holder is — which is why the key
// has to come from outside the artifact every time.
func (s *service) verifyOCIInstallWithKey(
	ctx context.Context,
	encodedKey, pluginName, ref, digest string,
) (*provenanceDecision, error) {
	// Re-decoded rather than carried down from validateInstallPublicKey: this
	// key may instead have come from the lock file, and internal callers reach
	// the install path without passing that entry check at all.
	pubKeyPEM, err := verifier.DecodePublicKey(encodedKey)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin %q: pinned %w", pluginName, err),
			http.StatusUnprocessableEntity,
		)
	}
	result, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, ref, digest, pubKeyPEM)
	if verifyErr != nil {
		return nil, classifyKeyVerifyError(verifyErr, pluginName)
	}
	if err := rejectOversizedBlob("sigstore bundle", pluginName, len(result.Bundle)); err != nil {
		return nil, err
	}
	return &provenanceDecision{
		provenance: &lockfile.Provenance{PublicKey: encodedKey},
		bundle:     result.Bundle,
	}, nil
}

// classifyKeyVerifyError maps a key-pair verification failure to the 403 the
// install API surfaces. allow_unsigned is deliberately not consulted on any
// arm: an install that named a public key asked for that key to be enforced,
// and the unsigned exception answers a different question.
func classifyKeyVerifyError(verifyErr error, pluginName string) error {
	switch {
	case errors.Is(verifyErr, verifier.ErrUnsigned):
		return httperr.WithCode(
			fmt.Errorf("plugin %q must verify against a cosign public key, but the artifact"+
				" carries no signature material at all", pluginName),
			http.StatusForbidden,
		)
	case errors.Is(verifyErr, verifier.ErrKeylessSigned):
		return httperr.WithCode(
			fmt.Errorf("plugin %q: %w; install it without a public key so its certificate identity"+
				" is verified and pinned instead", pluginName, verifyErr),
			http.StatusForbidden,
		)
	default:
		// The wrong key and a corrupt signature are indistinguishable here,
		// and saying so is more useful than picking one: the bundle records no
		// key of its own, so the only fact available is that this key does not
		// verify this signature.
		return httperr.WithCode(
			fmt.Errorf("plugin %q does not verify against the cosign public key it is checked against"+
				" — either the key is not the one that signed it, or the signature is damaged: %w",
				pluginName, verifyErr),
			http.StatusForbidden,
		)
	}
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
	// Refused rather than ignored, for the same reason the lock file refuses
	// to store a key on a git entry: a commit signature is made with a Fulcio
	// certificate, so there is no operation here a public key could take part
	// in.
	if opts.PublicKey != "" {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin %q is installed from git, whose commit signature is verified against a"+
				" certificate; a cosign public key cannot verify it", pluginName),
			http.StatusBadRequest,
		)
	}
	if opts.AllowSignerChange {
		expected, expectUnsigned = nil, false
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
		return nil, classifyInstallVerifyError(verifyErr, pluginName, expected, opts)
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
	// A local build has no registry signature material at all, so a public key
	// would have nothing to check. Saying so beats accepting the key and then
	// recording the install as unsigned anyway.
	if opts.PublicKey != "" {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin %q is a local build, which carries no registry signature for a"+
				" cosign public key to verify", pluginName),
			http.StatusBadRequest,
		)
	}
	if expected != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin %q is locked to %s; a local build cannot satisfy it",
				pluginName, lockedAnchorDescription(expected)),
			http.StatusForbidden,
		)
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, pluginName)
	}
	// An entry with no recorded trust state fails closed here too, including
	// under a lock-driven restore — see isAllowedUnsigned. Repairing it
	// automatically would record an unsigned exception nobody chose.
	if !opts.AllowUnsigned {
		return nil, httperr.WithCode(unrecordedTrustError(opts, pluginName,
			fmt.Sprintf("local build for %q is unsigned", pluginName)), http.StatusForbidden)
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
	provenance, unsigned, _, err := lockTrustState(projectRoot, pluginName)
	return provenance, unsigned, err
}

// lockTrustState is expectedLockTrust plus whether the lock file has an entry
// for pluginName at all. The verify paths do not need that distinction — an
// absent entry and one recording no decision both mean "no trust to enforce" —
// but Info does: an entry recording nothing is drift sync can repair, while no
// entry means nothing is pinning the plugin, and the two must not render
// alike. Kept as one read so both callers share a single lock file load.
func lockTrustState(
	projectRoot, pluginName string,
) (provenance *lockfile.Provenance, unsigned, found bool, err error) {
	if projectRoot == "" {
		return nil, false, false, nil
	}
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, false, false, fmt.Errorf("reading lock trust state for %q: %w", pluginName, err)
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, false, false, fmt.Errorf("reading lock trust state for %q: %w", pluginName, err)
	}
	entry, ok := lf.GetPlugin(pluginName)
	if !ok {
		return nil, false, false, nil
	}
	if entry.Unsigned {
		return nil, true, true, nil
	}
	return entry.Provenance, false, true, nil
}

// provenanceInfoFromLock converts a lock file's provenance block to the
// API-facing shape, so Info and Install can report the recorded trust state.
// Mirror skillsvc.provenanceInfoFromLock.
func provenanceInfoFromLock(p *lockfile.Provenance) *plugins.ProvenanceInfo {
	if p == nil {
		return nil
	}
	return &plugins.ProvenanceInfo{
		SignerIdentity:    p.SignerIdentity,
		CertIssuer:        p.CertIssuer,
		RepositoryURI:     p.RepositoryURI,
		RepositoryRef:     p.RepositoryRef,
		RunnerEnvironment: p.RunnerEnvironment,
		SigstoreURL:       p.SigstoreURL,
		PublicKey:         p.PublicKey,
		Provisional:       p.Provisional,
	}
}

// isAllowedUnsigned reports whether a verification failure is the unsigned
// case AND the caller may proceed. Only the explicit --allow-unsigned
// exception qualifies: recording "this is unsigned and I accept that" is a
// trust decision, and nothing may make it on the user's behalf.
//
// A lock-driven restore (sync, upgrade re-pin) of an entry recording no trust
// decision deliberately does NOT qualify, even though such an entry is
// reported as drift and repaired by exactly that path. Allowing it would turn
// "no decision" into "unsigned: true" automatically, laundering
// pre-verification content into a standing exception the user never agreed
// to — the implicit trust decision the lock file exists to prevent. So the
// migration repairs an entry automatically only when a signature actually
// verifies; unsigned content fails closed and needs --allow-unsigned to
// record the exception on purpose.
//
// An entry that already records unsigned is a different case, honored
// earlier by unsignedLockedDecision — restoring a decision the lock file
// states is not the same as inventing one. An entry locked to a signer is
// never replaceable by an unsigned artifact.
func isAllowedUnsigned(verifyErr error, opts plugins.InstallOptions, expected *lockfile.Provenance) bool {
	if !errors.Is(verifyErr, verifier.ErrUnsigned) || expected != nil {
		return false
	}
	return opts.AllowUnsigned
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
	opts plugins.InstallOptions,
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
		return httperr.WithCode(unrecordedTrustError(opts, pluginName,
			fmt.Sprintf("unsigned plugin %q rejected", pluginName)), http.StatusForbidden)
	// Checked before the broader ErrSignerMismatch case: pinnedFieldMismatch
	// wraps both, so a ref/runner-only mismatch would otherwise be
	// misreported as a signer-identity change below.
	case errors.Is(verifyErr, verifier.ErrProvenanceFieldMismatch):
		return httperr.WithCode(
			fmt.Errorf("plugin %q's certificate no longer matches its pinned provenance: %w"+
				" (if this is an expected publisher-side change, upgrade with allow_signer_change)",
				pluginName, verifyErr),
			http.StatusForbidden,
		)
	case errors.Is(verifyErr, verifier.ErrSignerMismatch):
		return httperr.WithCode(
			fmt.Errorf("signer identity mismatch for %q: %w"+
				" (if the signer change is intended, remove the plugin's lock entry"+
				" and reinstall, or upgrade with allow_signer_change)",
				pluginName, verifyErr),
			http.StatusForbidden,
		)
	// Reported before the default arm so a key-signed artifact is named as
	// such rather than as a failed verification. allow_unsigned is
	// deliberately no remedy here: the artifact IS signed, and recording it
	// as an unsigned exception would file a false trust decision in the lock.
	case errors.Is(verifyErr, verifier.ErrKeySigned):
		return keySignedInstallError(pluginName, verifyErr, expected)
	default:
		return httperr.WithCode(
			fmt.Errorf("signature verification failed for %q: %w", pluginName, verifyErr),
			http.StatusForbidden,
		)
	}
}

// keySignedInstallError reports a key-signed artifact that the keyless path
// could not verify. The remedy depends on what the entry already pins, so it
// is chosen from that rather than stated generically.
//
// With no anchor recorded, this is a first install of a key-signed artifact
// and --public-key is exactly the missing input. With a keyless identity
// pinned, --public-key is NOT the remedy: resolveKeyAnchor rejects a key
// against a certificate-pinned entry, because a key pair carries no identity
// that could satisfy it. Naming the flag there would send the caller into a
// conflict error one step later — the most likely way to reach this arm is
// also the one where the obvious advice is wrong.
//
// Either way allow_unsigned is no way out: the artifact IS signed, and
// recording it as an unsigned exception would file a false trust decision in
// the lock.
func keySignedInstallError(pluginName string, verifyErr error, expected *lockfile.Provenance) error {
	if expected != nil {
		return httperr.WithCode(
			fmt.Errorf("plugin %q: %w, but its lock entry is pinned to keyless signer %q;"+
				" a key pair carries no certificate identity that could satisfy that pin, so"+
				" supplying a public key is refused rather than allowed to displace it."+
				" Remove the lock entry and reinstall with `thv ai-plugin install --public-key`"+
				" to anchor it to the key"+
				" (allow_unsigned does not apply — the artifact is signed)",
				pluginName, verifyErr, expected.SignerIdentity),
			http.StatusForbidden,
		)
	}
	return httperr.WithCode(
		fmt.Errorf("plugin %q: %w; re-run `thv ai-plugin install --public-key` pointing at the cosign"+
			" public key it was signed with, and that key is pinned in the lock file for"+
			" subsequent installs (allow_unsigned does not apply — the artifact is signed)",
			pluginName, verifyErr),
		http.StatusForbidden,
	)
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
	case errors.Is(err, verifier.ErrKeySigned):
		return plugins.FailureReasonKeySigned
	case errors.Is(err, verifier.ErrSignatureInvalid):
		return plugins.FailureReasonSignatureInvalid
	default:
		return ""
	}
}

// unrecordedTrustError explains how to record the missing trust decision, in
// terms of the operation the caller actually ran. A lock-driven repair (sync,
// upgrade re-pin) reaches here for an entry that records no decision at all,
// and `thv ai-plugin install --allow-unsigned` is not the remedy for that —
// sync is, because it is the operation that owns the lock entry. Upgrade has
// no allow-unsigned flag of its own, so naming sync is the only actionable
// answer it can give.
//
// Wraps verifier.ErrUnsigned so classifySignatureError reports the refusal as
// FailureReasonUnsignedRejected. Sync reaches this path only now that a
// lock-driven repair of an unrecorded entry fails closed; before that the
// implicit exception meant no error was ever classified, and an unwrapped
// message here would surface the typed reason as "unknown".
func unrecordedTrustError(opts plugins.InstallOptions, pluginName, what string) error {
	if lockDrivenInstall(opts) {
		return fmt.Errorf("%s and the lock entry for %q records no trust decision (%w); "+
			"run `thv ai-plugin sync --allow-unsigned` to record the unsigned exception explicitly, "+
			"or reinstall from a signed artifact to record its signer",
			what, pluginName, verifier.ErrUnsigned)
	}
	return fmt.Errorf("%s (%w); set allow_unsigned (--allow-unsigned) to record an exception",
		what, verifier.ErrUnsigned)
}

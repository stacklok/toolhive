// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
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

// validateInstallPublicKey rejects a supplied public key before any resolve
// or fetch work begins. Both checks exist so the key is never accepted and
// then quietly unused: an install that does not verify would drop it on the
// floor, and a malformed one would otherwise surface as a verification
// failure deep in the install, long after the input that caused it.
func validateInstallPublicKey(opts skills.InstallOptions, scope skills.Scope) error {
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
// lock file (if any) is enforced inside the verifier's Sigstore policy; on
// true first use (no lock entry at all), a catalog-declared expectation
// takes its place if the install resolved from a registry entry that
// declared one (RFC THV-0080 follow-up #6310) — otherwise trust on first
// use records whatever identity verification observes.
//
// An entry pinned to a cosign public key, or a first install that supplies
// one, takes the key path instead. Which path runs is decided by the lock
// file and the caller, never by what the artifact turns out to carry: letting
// the artifact select its own verification policy would let a republished
// key-signed artifact walk out of the identity its entry is pinned to.
func (s *service) verifyOCIInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName, ref, digest string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, lockEntryExists, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	var catalogExpected *regtypes.Provenance
	verifierExpected := verifier.NewLockExpectation(expected)
	if !lockEntryExists {
		if err := validateCatalogProvenance(opts.CatalogProvenance); err != nil {
			return nil, err
		}
		catalogExpected = normalizeCatalogProvenance(opts.CatalogProvenance)
		verifierExpected = verifier.NewCatalogExpectation(catalogExpected)
	}
	keyAnchor, err := resolveKeyAnchor(opts, skillName, expected, expectUnsigned, catalogExpected)
	if err != nil {
		return nil, err
	}
	if keyAnchor != "" {
		return s.verifyOCIInstallWithKey(ctx, keyAnchor, skillName, ref, digest)
	}
	if opts.AllowSignerChange {
		// The signer-change guard was explicitly overridden: verify the
		// chain of trust only and re-record whatever identity is observed.
		expected, expectUnsigned = nil, false
		catalogExpected = nil
		verifierExpected = nil
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, skillName)
	}

	result, verifyErr := s.artifactVerifier().VerifyOCI(ctx, ref, digest, verifierExpected)
	if verifyErr != nil {
		if catalogExpected != nil {
			return nil, classifyCatalogVerifyError(verifyErr, skillName)
		}
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
func resolveKeyAnchor(
	opts skills.InstallOptions,
	skillName string,
	expected *lockfile.Provenance,
	expectUnsigned bool,
	catalogExpected *regtypes.Provenance,
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
				fmt.Errorf("skill %q: a public key cannot be combined with allow_signer_change;"+
					" re-anchoring an entry to a different key is not supported —"+
					" uninstall the skill and reinstall it with the new key", skillName),
				http.StatusBadRequest,
			)
		}
		return "", nil
	}

	switch {
	case locked != "" && supplied != "" && supplied != locked:
		return "", keyAnchorConflict(skillName,
			"is pinned to a different cosign public key than the one supplied")
	case locked != "":
		return locked, nil
	case supplied == "":
		return "", nil
	// A key was supplied and the entry is not key-pinned. Each remaining case
	// already records an anchor the key would have to displace.
	case expected != nil:
		return "", keyAnchorConflict(skillName,
			fmt.Sprintf("is pinned to signer %q, and a cosign key pair carries no certificate identity"+
				" that could satisfy it", expected.SignerIdentity))
	case expectUnsigned:
		return "", keyAnchorConflict(skillName,
			"is recorded as an explicit unsigned exception, which a public key cannot upgrade in place")
	case catalogExpected != nil:
		return "", httperr.WithCode(
			fmt.Errorf("skill %q: its catalog entry declares a certificate identity, which a"+
				" cosign key-pair signature carries none of; refusing to install under a public key"+
				" and silently drop that constraint", skillName),
			http.StatusForbidden,
		)
	default:
		return supplied, nil
	}
}

// keyAnchorConflict reports a supplied public key that contradicts the trust
// state the lock file already records. The remedy is the same for all of them
// — v1 has no in-place re-anchor path — so it is stated once here.
func keyAnchorConflict(skillName, problem string) error {
	return httperr.WithCode(
		fmt.Errorf("skill %q %s; to install it under a different trust anchor,"+
			" uninstall the skill and reinstall it", skillName, problem),
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
	encodedKey, skillName, ref, digest string,
) (*provenanceDecision, error) {
	// Re-decoded rather than carried down from validateInstallPublicKey: this
	// key may instead have come from the lock file, and internal callers reach
	// the install path without passing that entry check at all.
	pubKeyPEM, err := verifier.DecodePublicKey(encodedKey)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("skill %q: pinned %w", skillName, err),
			http.StatusUnprocessableEntity,
		)
	}
	result, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, ref, digest, pubKeyPEM)
	if verifyErr != nil {
		return nil, classifyKeyVerifyError(verifyErr, skillName)
	}
	return &provenanceDecision{
		provenance: &skills.ProvenanceInfo{PublicKey: encodedKey},
		bundle:     result.Bundle,
	}, nil
}

// classifyKeyVerifyError maps a key-pair verification failure to the 403 the
// install API surfaces. allow_unsigned is deliberately not consulted on any
// arm: an install that named a public key asked for that key to be enforced,
// and the unsigned exception answers a different question.
func classifyKeyVerifyError(verifyErr error, skillName string) error {
	switch {
	case errors.Is(verifyErr, verifier.ErrUnsigned):
		return httperr.WithCode(
			fmt.Errorf("skill %q must verify against a cosign public key, but the artifact"+
				" carries no signature material at all", skillName),
			http.StatusForbidden,
		)
	case errors.Is(verifyErr, verifier.ErrKeylessSigned):
		return httperr.WithCode(
			fmt.Errorf("skill %q: %w; install it without a public key so its certificate identity"+
				" is verified and pinned instead", skillName, verifyErr),
			http.StatusForbidden,
		)
	default:
		// The wrong key and a corrupt signature are indistinguishable here,
		// and saying so is more useful than picking one: the bundle records no
		// key of its own, so the only fact available is that this key does not
		// verify this signature.
		return httperr.WithCode(
			fmt.Errorf("skill %q does not verify against the cosign public key it is checked against"+
				" — either the key is not the one that signed it, or the signature is damaged: %w",
				skillName, verifyErr),
			http.StatusForbidden,
		)
	}
}

// verifyGitInstall verifies the gitsign signature on the resolved commit
// before anything is written or recorded. See verifyOCIInstall for the
// catalog-provenance fallback on true first use.
func (s *service) verifyGitInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName string,
	payload []byte,
	signature string,
) (*provenanceDecision, error) {
	expected, expectUnsigned, lockEntryExists, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	// Refused rather than ignored, for the same reason the lock file refuses
	// to store a key on a git entry: a commit signature is made with a Fulcio
	// certificate, so there is no operation here a public key could take part
	// in.
	if opts.PublicKey != "" {
		return nil, httperr.WithCode(
			fmt.Errorf("skill %q is installed from git, whose commit signature is verified against a"+
				" certificate; a cosign public key cannot verify it", skillName),
			http.StatusBadRequest,
		)
	}
	var catalogExpected *regtypes.Provenance
	verifierExpected := verifier.NewLockExpectation(expected)
	if !lockEntryExists {
		if err := validateCatalogProvenance(opts.CatalogProvenance); err != nil {
			return nil, err
		}
		catalogExpected = normalizeCatalogProvenance(opts.CatalogProvenance)
		verifierExpected = verifier.NewCatalogExpectation(catalogExpected)
	}
	if opts.AllowSignerChange {
		expected, expectUnsigned = nil, false
		catalogExpected = nil
		verifierExpected = nil
	}
	if expectUnsigned {
		return unsignedLockedDecision(opts, skillName)
	}

	result, verifyErr := s.artifactVerifier().VerifyGit(ctx, payload, []byte(signature), verifierExpected)
	if verifyErr != nil {
		if catalogExpected != nil {
			return nil, classifyCatalogVerifyError(verifyErr, skillName)
		}
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
	expected, expectUnsigned, _, err := expectedLockTrust(opts.ProjectRoot, skillName)
	if err != nil {
		return nil, err
	}
	// A local build has no registry signature material at all, so a public key
	// would have nothing to check. Saying so beats accepting the key and then
	// recording the install as unsigned anyway.
	if opts.PublicKey != "" {
		return nil, httperr.WithCode(
			fmt.Errorf("skill %q is a local build, which carries no registry signature for a"+
				" cosign public key to verify", skillName),
			http.StatusBadRequest,
		)
	}
	if expected != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("skill %q is locked to %s; a local build cannot satisfy it",
				skillName, lockedAnchorDescription(expected)),
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
// file for skillName: the expected signer identity, whether the entry was
// recorded unsigned, and whether any entry exists. The third result
// distinguishes true first use from legacy entries that predate trust state.
func expectedLockTrust(projectRoot, skillName string) (*lockfile.Provenance, bool, bool, error) {
	if projectRoot == "" {
		return nil, false, false, nil
	}
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, false, false, err
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, false, false, err
	}
	entry, ok := lf.Get(skillName)
	if !ok {
		return nil, false, false, nil
	}
	if entry.Unsigned {
		return nil, true, true, nil
	}
	return entry.Provenance, false, true, nil
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
	// Checked before the broader ErrSignerMismatch case: pinnedFieldMismatch
	// wraps both, so a ref/runner-only mismatch would otherwise be
	// misreported as a signer-identity change below.
	case errors.Is(verifyErr, verifier.ErrProvenanceFieldMismatch):
		return httperr.WithCode(
			fmt.Errorf("skill %q's certificate no longer matches its pinned provenance: %w"+
				" (if this is an expected publisher-side change, upgrade with allow_signer_change)",
				skillName, verifyErr),
			http.StatusForbidden,
		)
	case errors.Is(verifyErr, verifier.ErrSignerMismatch):
		return httperr.WithCode(
			fmt.Errorf("signer identity mismatch for %q: %w"+
				" (if the signer change is intended, remove the skill's lock entry"+
				" and reinstall, or upgrade with allow_signer_change)", skillName, verifyErr),
			http.StatusForbidden,
		)
	// Reported before the default arm so a key-signed artifact is named as
	// such rather than as a failed verification. allow_unsigned is
	// deliberately no remedy here: the artifact IS signed, and recording it
	// as an unsigned exception would file a false trust decision in the lock.
	case errors.Is(verifyErr, verifier.ErrKeySigned):
		return keySignedInstallError(skillName, verifyErr, expected)
	default:
		return httperr.WithCode(
			fmt.Errorf("signature verification failed for %q: %w", skillName, verifyErr),
			http.StatusForbidden,
		)
	}
}

// classifyCatalogVerifyError reports a first-install artifact that does not
// satisfy the provenance constraints declared by its catalog entry.
func classifyCatalogVerifyError(verifyErr error, skillName string) error {
	if errors.Is(verifyErr, verifier.ErrUnsigned) {
		return httperr.WithCode(
			fmt.Errorf("skill %q is unsigned but its catalog entry requires verified provenance", skillName),
			http.StatusForbidden,
		)
	}
	// A key-signed artifact is not a provenance mismatch — nothing was
	// compared, because the keyless policy cannot check a key-pair signature
	// at all. The catalog constraint is beside the point, so this reports the
	// same diagnosis and remedy the non-catalog route does.
	if errors.Is(verifyErr, verifier.ErrKeySigned) {
		return keySignedInstallError(skillName, verifyErr, nil)
	}
	return httperr.WithCode(
		fmt.Errorf("skill %q does not match its catalog-declared provenance: %w", skillName, verifyErr),
		http.StatusForbidden,
	)
}

// keySignedInstallError reports a key-signed artifact that the keyless path
// could not verify. A lock-constrained install and a catalog-constrained
// first install both land here, but the remedy is not the same, so it is
// chosen from what the entry already pins rather than stated generically.
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
func keySignedInstallError(skillName string, verifyErr error, expected *lockfile.Provenance) error {
	if expected != nil {
		return httperr.WithCode(
			fmt.Errorf("skill %q: %w, but its lock entry is pinned to keyless signer %q;"+
				" a key pair carries no certificate identity that could satisfy that pin, so"+
				" supplying a public key is refused rather than allowed to displace it."+
				" Remove the lock entry and reinstall with --public-key to anchor it to the key"+
				" (allow_unsigned does not apply — the artifact is signed)",
				skillName, verifyErr, expected.SignerIdentity),
			http.StatusForbidden,
		)
	}
	return httperr.WithCode(
		fmt.Errorf("skill %q: %w; re-run the install with --public-key pointing at the cosign"+
			" public key it was signed with, and that key is pinned in the lock file for"+
			" subsequent installs (allow_unsigned does not apply — the artifact is signed)",
			skillName, verifyErr),
		http.StatusForbidden,
	)
}

// classifySignatureError maps verifier sentinels to typed failure reasons
// for sync/upgrade results. Returns "" when err is not a signature failure.
func classifySignatureError(err error) skills.FailureReason {
	switch {
	// Checked before the broader ErrSignerMismatch case for the same reason
	// as classifyInstallVerifyError above.
	case errors.Is(err, verifier.ErrProvenanceFieldMismatch):
		return skills.FailureReasonProvenanceFieldMismatch
	case errors.Is(err, verifier.ErrSignerMismatch):
		return skills.FailureReasonSignerMismatch
	case errors.Is(err, verifier.ErrUnsigned):
		return skills.FailureReasonUnsignedRejected
	case errors.Is(err, verifier.ErrKeySigned):
		return skills.FailureReasonKeySigned
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
		PublicKey:         p.PublicKey,
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
		PublicKey:         p.PublicKey,
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
		SignerIdentity:    r.SignerIdentity,
		CertIssuer:        r.CertIssuer,
		RepositoryURI:     r.RepositoryURI,
		RepositoryRef:     r.RepositoryRef,
		RunnerEnvironment: r.RunnerEnvironment,
		SigstoreURL:       r.SigstoreURL,
		Provisional:       r.Provisional,
	}
}

// validateCatalogProvenance rejects catalog constraints the skills verifier
// cannot currently enforce. It runs only for project-scope true first use,
// after lock precedence has been established.
func validateCatalogProvenance(p *regtypes.Provenance) error {
	if p == nil {
		return nil
	}
	if p.SigstoreURL != "" {
		return httperr.WithCode(
			errors.New("catalog declares a sigstore_url constraint for this skill,"+
				" which toolhive cannot yet enforce; refusing to silently ignore it"),
			http.StatusUnprocessableEntity,
		)
	}
	if p.Attestation != nil {
		return httperr.WithCode(
			errors.New("catalog declares an attestation constraint for this skill,"+
				" which toolhive cannot yet enforce; refusing to silently ignore it"),
			http.StatusUnprocessableEntity,
		)
	}
	return nil
}

// normalizeCatalogProvenance treats an empty supported constraint as absent.
// Catalog fields constrain independently, so an object containing no
// supported values must preserve the ordinary unconstrained TOFU behavior,
// including the explicit allow-unsigned path.
func normalizeCatalogProvenance(p *regtypes.Provenance) *regtypes.Provenance {
	if p == nil || (p.SignerIdentity == "" &&
		p.CertIssuer == "" &&
		p.RepositoryURI == "" &&
		p.RepositoryRef == "" &&
		p.RunnerEnvironment == "") {
		return nil
	}
	return p
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	"github.com/stacklok/toolhive/pkg/storage"
)

// var _ ensures *service continues to satisfy the full lock service surface
// now that both Sync (PR4) and Upgrade exist.
var _ skills.SkillLockService = (*service)(nil)

// trustOnlyRollbackTimeout bounds compensation after the lock-file half of a
// trust-only update fails. Compensation is detached from caller cancellation
// so a timed-out request cannot strand the durable store at the new bundle.
const trustOnlyRollbackTimeout = 5 * time.Second

// Upgrade re-resolves each targeted lock entry's Source and, when its digest
// or resolved reference has changed, installs the resolved candidate and
// rewrites the entry (Source itself is never rewritten — see RFC THV-0080).
// Full git commit sources are not upgradable. An immutable OCI digest has no
// newer content, but its separately attached signatures are still evaluated
// when an explicitly supplied replacement key requests a trust update.
func (s *service) Upgrade(ctx context.Context, opts skills.UpgradeOptions) (*skills.UpgradeResult, error) {
	if err := validateUpgradePublicKey(opts); err != nil {
		return nil, err
	}

	_, projectRoot, err := normalizeProjectRoot(skills.ScopeProject, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	opts.ProjectRoot = projectRoot

	var result *skills.UpgradeResult
	err = s.projectTx.run(ctx, projectRoot, func() error {
		root, openErr := lockfile.OpenRoot(projectRoot)
		if openErr != nil {
			return openErr
		}
		lf, loadErr := lockfile.Load(root)
		if loadErr != nil {
			return loadErr
		}

		targets, selectErr := selectUpgradeTargets(lf, opts.Names)
		if selectErr != nil {
			return selectErr
		}

		result = &skills.UpgradeResult{Outcomes: make([]skills.UpgradeOutcome, 0, len(targets))}
		for _, target := range targets {
			result.Outcomes = append(result.Outcomes, s.upgradeOne(ctx, opts, target.Name))
		}
		return nil
	})
	return result, err
}

func validateUpgradePublicKey(opts skills.UpgradeOptions) error {
	if opts.PublicKey == "" {
		return nil
	}
	if !opts.AllowSignerChange {
		return httperr.WithCode(
			errors.New("public_key (--public-key) requires allow_signer_change (--allow-signer-change)"),
			http.StatusBadRequest,
		)
	}
	if _, err := verifier.DecodePublicKey(opts.PublicKey); err != nil {
		return httperr.WithCode(fmt.Errorf("public_key: %w", err), http.StatusBadRequest)
	}
	return nil
}

// selectUpgradeTargets returns the lock entries to upgrade: every entry when
// names is empty, or the named subset in the order requested. An unknown
// name is an error — it is almost always a typo, and silently skipping it
// would make a scripted "upgrade these specific skills" call falsely report
// success.
func selectUpgradeTargets(lf *lockfile.Lockfile, names []string) ([]lockfile.Entry, error) {
	if len(names) == 0 {
		return lf.Skills, nil
	}
	targets := make([]lockfile.Entry, 0, len(names))
	for _, name := range names {
		entry, ok := lf.Get(name)
		if !ok {
			return nil, httperr.WithCode(
				fmt.Errorf("skill %q is not present in the lock file", name),
				http.StatusNotFound,
			)
		}
		targets = append(targets, entry)
	}
	return targets, nil
}

// upgradeOne reloads the named lock entry under the held project
// transaction, then plans and applies against that fresh snapshot. Planning
// every entry first and applying later would let a concurrent uninstall be
// resurrected, or a newer install be overwritten by this older plan.
//
// FailOnChanges is a CI freshness gate: it reports the planned outcome and
// never applies. Exit-code mapping happens in the CLI from these outcomes.
func (s *service) upgradeOne(
	ctx context.Context, opts skills.UpgradeOptions, name string,
) skills.UpgradeOutcome {
	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		return skills.UpgradeOutcome{
			Name: name, Status: skills.UpgradeStatusFailed,
			Reason: classifySyncFailure(err), Error: err.Error(),
		}
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return skills.UpgradeOutcome{
			Name: name, Status: skills.UpgradeStatusFailed,
			Reason: classifySyncFailure(err), Error: err.Error(),
		}
	}
	entry, ok := lf.Get(name)
	if !ok {
		return skills.UpgradeOutcome{
			Name:   name,
			Status: skills.UpgradeStatusFailed,
			Reason: skills.FailureReasonUnknown,
			Error:  fmt.Sprintf("skill %q is no longer in the lock file", name),
		}
	}

	plan := s.planUpgrade(ctx, opts, entry)
	if opts.FailOnChanges {
		return plan.outcome
	}
	return s.applyUpgrade(ctx, opts, plan)
}

// upgradePlan is entry's resolved outcome before any install happens: either
// a terminal status (not-upgradable, up-to-date, ref-change-blocked, or a
// resolution failure) that needs no further action, or the pinned reference
// to install when the upgrade is applied.
type upgradePlan struct {
	entry       lockfile.Entry
	outcome     skills.UpgradeOutcome
	pinnedRef   string // set only when the upgrade needs installing
	resolvedRef string // the resolved reference to record as ResolvedReference; set alongside pinnedRef
	// allowSignerChange is the project-wide override narrowed to THIS entry:
	// true only when dropping the entry's recorded anchor is justified. See
	// resolveSignerPolicy.
	allowSignerChange bool
	// trustDecision is the exact OCI verification result selected while
	// planning. Content apply consumes it without another registry request;
	// a trust-only plan persists only its bundle and lock trust fields.
	trustDecision *provenanceDecision
}

// planUpgrade resolves entry's current state and determines its outcome,
// without installing anything — this lets Upgrade check --fail-on-changes
// against every target before any of them are applied. When an upgrade is
// warranted, the outcome's digest is pinned into pinnedRef so applyUpgrade
// installs exactly what was resolved here, rather than re-resolving
// entry.Source from scratch (which could pick up a different digest if a
// mutable ref moved between planning and applying).
func (s *service) planUpgrade(ctx context.Context, opts skills.UpgradeOptions, entry lockfile.Entry) upgradePlan {
	outcome := skills.UpgradeOutcome{Name: entry.Name, OldDigest: entry.Digest}

	immutable := isImmutableSource(entry)
	if immutable && (gitresolver.IsGitReference(entry.Source) || opts.PublicKey == "") {
		outcome.Status = skills.UpgradeStatusNotUpgradable
		return upgradePlan{entry: entry, outcome: outcome}
	}

	newRef, newDigest, err := s.resolveUpgradeCandidate(ctx, entry, immutable)
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}
	outcome.NewDigest = newDigest
	digestChanged := newDigest != entry.Digest
	resolvedReferenceChanged := newRef != entry.ResolvedReference

	if repositoryChangeBlocksUpgrade(opts, entry, newRef, &outcome) {
		return upgradePlan{entry: entry, outcome: outcome}
	}

	isOCI := strings.Contains(newDigest, ":")
	candidateChanged := digestChanged || resolvedReferenceChanged
	trustDecision, allowSignerChange, blocked := s.resolvePlannedTrust(
		ctx, opts, entry, newRef, newDigest, candidateChanged, isOCI, &outcome,
	)
	if blocked {
		return upgradePlan{entry: entry, outcome: outcome}
	}

	if !candidateChanged {
		return s.finishUnchangedUpgrade(ctx, opts, entry, immutable, outcome, trustDecision)
	}

	pinnedRef, err := buildPinnedReference(lockfile.Entry{ResolvedReference: newRef, Digest: newDigest})
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = skills.FailureReasonUnknown
		outcome.Error = fmt.Errorf("pinning resolved reference: %w", err).Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}

	outcome.Status = skills.UpgradeStatusUpgraded
	return upgradePlan{
		entry:             entry,
		outcome:           outcome,
		pinnedRef:         pinnedRef,
		resolvedRef:       newRef,
		allowSignerChange: allowSignerChange,
		trustDecision:     trustDecision,
	}
}

func (s *service) resolveUpgradeCandidate(
	ctx context.Context, entry lockfile.Entry, immutable bool,
) (string, string, error) {
	if !immutable {
		return s.resolveLatestState(ctx, entry.Source)
	}
	resolvedRef := entry.ResolvedReference
	if resolvedRef == "" {
		resolvedRef = entry.Source
	}
	return resolvedRef, entry.Digest, nil
}

func repositoryChangeBlocksUpgrade(
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	newRef string,
	outcome *skills.UpgradeOutcome,
) bool {
	if newRef == entry.ResolvedReference {
		return false
	}
	outcome.NewResolvedReference = newRef
	// Only a move to a different repository is a supply-chain event. A tag
	// moving within one repository is how a mutable source advances.
	if repositoryMoved(entry.ResolvedReference, newRef) && !opts.AllowRefChange {
		outcome.Status = skills.UpgradeStatusRefChangeBlocked
		return true
	}
	return false
}

func (s *service) resolvePlannedTrust(
	ctx context.Context,
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	newRef, newDigest string,
	candidateChanged, isOCI bool,
	outcome *skills.UpgradeOutcome,
) (*provenanceDecision, bool, bool) {
	if isOCI && opts.PublicKey != "" {
		decision, blocked := s.resolveOCITrustPolicy(ctx, opts, entry, newRef, newDigest, outcome)
		if !blocked {
			outcome.TrustAnchorChanged = trustDecisionChangesEntry(entry, decision)
		}
		return decision, false, blocked
	}
	if !candidateChanged {
		return nil, false, false
	}
	allowSignerChange, blocked := s.resolveSignerPolicy(ctx, opts, entry, newRef, newDigest, outcome)
	return nil, allowSignerChange, blocked
}

func (s *service) finishUnchangedUpgrade(
	ctx context.Context,
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	immutable bool,
	outcome skills.UpgradeOutcome,
	trustDecision *provenanceDecision,
) upgradePlan {
	trustMaterialChanged, err := s.storedTrustMaterialChanged(
		ctx, opts.ProjectRoot, entry.Name, trustDecision,
	)
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}
	switch {
	case outcome.TrustAnchorChanged || trustMaterialChanged:
		outcome.Status = skills.UpgradeStatusTrustUpdated
	case immutable:
		outcome.Status = skills.UpgradeStatusNotUpgradable
	default:
		outcome.Status = skills.UpgradeStatusUpToDate
	}
	return upgradePlan{entry: entry, outcome: outcome, trustDecision: trustDecision}
}

// resolveOCITrustPolicy retrieves a complete snapshot once for an explicit
// public-key re-anchor, then chooses one verified trust decision from it. A
// key-pinned entry always tries its old key first; only a conclusive miss
// permits trying the caller's replacement key, followed by the existing
// keyless transition. Non-key-pinned entries treat a supplied key
// opportunistically and otherwise retain their recorded policy.
func (s *service) resolveOCITrustPolicy(
	ctx context.Context,
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	newRef, newDigest string,
	outcome *skills.UpgradeOutcome,
) (*provenanceDecision, bool) {
	// An unsigned lock decision is the policy for ordinary lock-driven
	// upgrades and intentionally bypasses signature discovery. A supplied key
	// is the only reason to inspect the bundle set: it may replace the
	// exception if it actually verifies, otherwise the exception is retained.
	if entry.Unsigned && opts.PublicKey == "" {
		return &provenanceDecision{unsigned: true}, false
	}

	retriever, ok := s.artifactVerifier().(verifier.OCISnapshotRetriever)
	if !ok {
		setUpgradeTrustFailure(outcome, errors.New("configured signature verifier does not support OCI snapshot retrieval"))
		return nil, true
	}

	snapshot, err := retriever.RetrieveOCISnapshot(ctx, newRef, newDigest)
	if err != nil {
		if errors.Is(err, verifier.ErrUnsigned) {
			return resolveUnsignedCandidate(entry, outcome)
		}
		setUpgradeTrustFailure(outcome, err)
		return nil, true
	}

	if entry.Provenance != nil && entry.Provenance.PublicKey != "" {
		return resolveKeyPinnedSnapshot(opts, entry, snapshot, outcome)
	}
	return resolveNonKeyPinnedSnapshot(opts, entry, snapshot, outcome)
}

func resolveUnsignedCandidate(
	entry lockfile.Entry, outcome *skills.UpgradeOutcome,
) (*provenanceDecision, bool) {
	if entry.Provenance == nil {
		return &provenanceDecision{unsigned: true}, false
	}

	outcome.Status = skills.UpgradeStatusFailed
	outcome.Reason = skills.FailureReasonUnsignedRejected
	if entry.Provenance.PublicKey != "" {
		outcome.Error = fmt.Sprintf("candidate is unsigned, and this entry is pinned to a cosign public key;"+
			" upgrade has no unsigned-consent flag. To move it to an unsigned artifact, reinstall it: %s",
			projectUnsignedReinstallCommand(entry))
	} else {
		outcome.Error = fmt.Sprintf("candidate is unsigned, and this entry is pinned to signer %q;"+
			" upgrade has no unsigned-consent flag. To move it to an unsigned artifact, reinstall it: %s",
			entry.Provenance.SignerIdentity, projectUnsignedReinstallCommand(entry))
	}
	return nil, true
}

func resolveKeyPinnedSnapshot(
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	snapshot verifier.OCISnapshot,
	outcome *skills.UpgradeOutcome,
) (*provenanceDecision, bool) {
	oldKeyPEM, err := verifier.DecodePublicKey(entry.Provenance.PublicKey)
	if err != nil {
		setUpgradeTrustFailure(outcome, fmt.Errorf("lock entry's pinned %w", err))
		return nil, true
	}
	oldResult, oldErr := snapshot.VerifyWithKey(oldKeyPEM)
	if oldErr == nil {
		return keyTrustDecision(entry.Provenance.PublicKey, oldResult), false
	}
	if !conclusiveKeyedMismatch(oldErr) {
		setUpgradeTrustFailure(outcome, fmt.Errorf("verifying candidate against the pinned cosign public key: %w", oldErr))
		return nil, true
	}

	if opts.PublicKey != "" {
		newKeyPEM, decodeErr := verifier.DecodePublicKey(opts.PublicKey)
		if decodeErr != nil {
			setUpgradeTrustFailure(outcome, fmt.Errorf("public_key: %w", decodeErr))
			return nil, true
		}
		newResult, newErr := snapshot.VerifyWithKey(newKeyPEM)
		if newErr == nil {
			return keyTrustDecision(opts.PublicKey, newResult), false
		}
		if !conclusiveKeyedMismatch(newErr) {
			setUpgradeTrustFailure(outcome, fmt.Errorf("verifying candidate against the supplied cosign public key: %w", newErr))
			return nil, true
		}
	}

	keylessResult, keylessErr := snapshot.VerifyKeyless(nil)
	if keylessErr == nil {
		if !opts.AllowSignerChange {
			outcome.Status = skills.UpgradeStatusSignerChangeBlocked
			outcome.NewSignerIdentity = keylessResult.SignerIdentity
			return nil, true
		}
		return keylessTrustDecision(keylessResult), false
	}

	outcome.Status = skills.UpgradeStatusFailed
	outcome.Reason = keyedFailureReason(oldErr, keylessErr)
	outcome.Error = fmt.Errorf("candidate verifies against neither the recorded cosign key"+
		" nor a permitted replacement trust anchor: %w", keylessErr).Error()
	return nil, true
}

func resolveNonKeyPinnedSnapshot(
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	snapshot verifier.OCISnapshot,
	outcome *skills.UpgradeOutcome,
) (*provenanceDecision, bool) {
	if opts.PublicKey != "" {
		keyPEM, err := verifier.DecodePublicKey(opts.PublicKey)
		if err != nil {
			setUpgradeTrustFailure(outcome, fmt.Errorf("public_key: %w", err))
			return nil, true
		}
		result, verifyErr := snapshot.VerifyWithKey(keyPEM)
		if verifyErr == nil {
			return keyTrustDecision(opts.PublicKey, result), false
		}
		if !conclusiveKeyedMismatch(verifyErr) {
			setUpgradeTrustFailure(outcome, fmt.Errorf("verifying candidate against the supplied cosign public key: %w", verifyErr))
			return nil, true
		}
	}

	// An explicitly unsigned entry remains under that recorded exception
	// unless a supplied key actually verified above. This preserves existing
	// lock-driven upgrade behavior; signatures do not choose their own policy.
	if entry.Unsigned {
		return &provenanceDecision{unsigned: true}, false
	}

	if entry.Provenance == nil {
		result, err := snapshot.VerifyKeyless(nil)
		if err != nil {
			setUpgradeTrustFailure(outcome, err)
			return nil, true
		}
		return keylessTrustDecision(result), false
	}

	result, err := snapshot.VerifyKeyless(verifier.NewLockExpectation(entry.Provenance))
	if err == nil {
		return keylessTrustDecision(result), false
	}
	if !errors.Is(err, verifier.ErrSignerMismatch) &&
		!errors.Is(err, verifier.ErrProvenanceFieldMismatch) {
		setUpgradeTrustFailure(outcome, err)
		return nil, true
	}

	replacement, replacementErr := snapshot.VerifyKeyless(nil)
	if replacementErr != nil {
		setUpgradeTrustFailure(outcome, replacementErr)
		return nil, true
	}
	if !opts.AllowSignerChange {
		outcome.Status = skills.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = replacement.SignerIdentity
		return nil, true
	}
	return keylessTrustDecision(replacement), false
}

func keyTrustDecision(encodedKey string, result *verifier.Result) *provenanceDecision {
	return &provenanceDecision{
		provenance: &skills.ProvenanceInfo{PublicKey: encodedKey},
		bundle:     bytes.Clone(result.Bundle),
	}
}

func keylessTrustDecision(result *verifier.Result) *provenanceDecision {
	return &provenanceDecision{
		provenance: provenanceInfoFromResult(result),
		bundle:     bytes.Clone(result.Bundle),
	}
}

func trustDecisionChangesEntry(entry lockfile.Entry, decision *provenanceDecision) bool {
	if decision == nil || entry.Unsigned != decision.unsigned {
		return decision != nil
	}
	selected := provenanceInfoToLock(decision.provenance)
	if entry.Provenance == nil || selected == nil {
		return entry.Provenance != nil || selected != nil
	}
	return *entry.Provenance != *selected
}

func (s *service) storedTrustMaterialChanged(
	ctx context.Context,
	projectRoot, name string,
	decision *provenanceDecision,
) (bool, error) {
	if decision == nil {
		return false, nil
	}
	installed, err := s.store.Get(ctx, name, skills.ScopeProject, projectRoot)
	if errors.Is(err, storage.ErrNotFound) {
		// Upgrade historically leaves missing installs to sync. There is no
		// persisted bundle to refresh, so absence alone is not a trust change.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("loading installed skill trust material: %w", err)
	}
	return !bytes.Equal(installed.SigstoreBundle, decision.bundle), nil
}

func setUpgradeTrustFailure(outcome *skills.UpgradeOutcome, err error) {
	outcome.Status = skills.UpgradeStatusFailed
	outcome.Reason = classifySignatureError(err)
	if outcome.Reason == "" {
		outcome.Reason = skills.FailureReasonUnknown
	}
	outcome.Error = err.Error()
}

// resolveSignerPolicy applies the signer-change guard to one entry, and
// reports both whether the plan stops here and whether --allow-signer-change
// applies to THIS entry.
//
// The guard runs at plan time so plan-only modes stay install-free, probing
// with a nil expected identity so a differing signer is reported as a change
// rather than a bare failure; blocked plans carry no pinnedRef, exactly like
// ref changes.
//
// The returned flag is not simply opts.AllowSignerChange. The override is
// project-wide but authorizes dropping an anchor the artifact has genuinely
// moved off — not ignoring one it still satisfies. Narrowing it per entry is
// what stops a skill that needs the override from unpinning every key-pinned
// skill beside it: applyUpgrade passes the narrowed flag to the install, and
// resolveKeyAnchor drops a recorded key whenever that flag is set.
func (s *service) resolveSignerPolicy(
	ctx context.Context,
	opts skills.UpgradeOptions,
	entry lockfile.Entry,
	newRef, newDigest string,
	outcome *skills.UpgradeOutcome,
) (allowSignerChange, blocked bool) {
	if entry.Provenance == nil {
		// No recorded signer, so there is no signer change to authorize:
		// forwarding the override would only clear the install's
		// expectations for an entry that has none to clear.
		return false, false
	}
	if entry.Provenance.PublicKey != "" {
		// A keyed entry is measured against its pin whether or not the
		// override was passed; only what a genuine move authorizes differs.
		// Measuring once keeps the two modes from disagreeing about what the
		// candidate is.
		verdict := s.judgeKeyedCandidate(ctx, entry, newRef, newDigest)
		if recordKeyedVerdict(verdict, opts.AllowSignerChange, outcome) {
			return false, true
		}
		return opts.AllowSignerChange && verdict.kind == keyedMovedToKeyless, false
	}
	if s.guardSignerChange(ctx, entry, newRef, newDigest, opts.AllowSignerChange, outcome) {
		return false, true
	}
	return opts.AllowSignerChange, false
}

// guardSignerChange probes the candidate artifact's signer identity and
// fills outcome when the upgrade must not proceed: the candidate is signed
// by a different identity versus the recorded provenance, its signature
// cannot be verified at all, it is unsigned, or its certificate's repository
// ref or runner class differs from what is recorded. Returns true when
// blocked. Only a differing identity or provenance field is reported as a
// signer change, and only that arm is waived by allowSignerChange; the rest
// are failures in both modes, because --allow-signer-change would not
// resolve them — it re-verifies from scratch, which an unsigned or
// unverifiable candidate fails just the same. Running the guard under the
// override is what keeps a project-wide flag from waving those through to
// an install that then either refuses them or, worse, records them.
//
// The repository ref has NO automatic allowance for a tag-shaped rotation.
// An earlier version of this guard let a recorded tag ref rotate to any
// other tag ref, reasoning that a release workflow signs each version on
// its own tag — but that also let a candidate signed from an attacker's OWN
// tag (e.g. "refs/tags/attacker-release") on the SAME repository replace a
// pinned tag, since nothing tied the candidate's tag to the specific
// version actually being upgraded to. Binding it correctly would need the
// resolved release source's own tag, which the git resolver does not
// surface at all (only the resolved commit hash) — so an OCI-only partial
// fix would leave git-sourced skills with the identical hole. Every ref
// change — tag or branch, git or OCI — therefore blocks here exactly like a
// genuine signer-identity change, and needs the same explicit
// --allow-signer-change override. See stacklok/toolhive#6315 review.
func (s *service) guardSignerChange(
	ctx context.Context,
	entry lockfile.Entry,
	newRef, newDigest string,
	allowSignerChange bool,
	outcome *skills.UpgradeOutcome,
) bool {
	probe, probeErr := s.probeCandidateSigner(ctx, newRef, newDigest)
	switch {
	case probeErr != nil && errors.Is(probeErr, verifier.ErrUnsigned):
		// Not a signer change, because the remedy a signer change names
		// cannot resolve it: --allow-signer-change re-verifies from scratch
		// and re-records what it observes, and an unsigned artifact fails
		// that verification exactly as it fails this one. Upgrade has no
		// unsigned-consent flag, so the only way onto an unsigned artifact
		// is the reinstall that records the exception explicitly.
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = skills.FailureReasonUnsignedRejected
		outcome.Error = fmt.Errorf("candidate is unsigned, and this entry is pinned to a signer"+
			" identity: %w. Upgrade has no unsigned-consent flag, and --allow-signer-change is not"+
			" one — it re-verifies from scratch, which an unsigned artifact still fails. To move"+
			" this skill to an unsigned artifact, reinstall it: %s",
			probeErr, projectUnsignedReinstallCommand(entry)).Error()
		return true
	case probeErr != nil:
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySignatureError(probeErr)
		if outcome.Reason == "" {
			outcome.Reason = skills.FailureReasonUnknown
		}
		outcome.Error = probeErr.Error()
		return true
	case probe.SignerIdentity != entry.Provenance.SignerIdentity ||
		probe.CertIssuer != entry.Provenance.CertIssuer ||
		runnerEnvironmentChanged(probe, entry.Provenance) ||
		repositoryRefChanged(probe, entry.Provenance):
		// The one arm the override resolves: it re-verifies from scratch
		// and re-records the identity actually observed.
		if allowSignerChange {
			return false
		}
		outcome.Status = skills.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = probe.SignerIdentity
		return true
	}
	return false
}

// recordKeyedVerdict turns a keyed verdict into a plan outcome and reports
// whether the upgrade stops here.
//
// A genuine key-to-keyless move is the one case the two modes treat
// differently: without the override it is blocked so the caller can decide,
// and with the override it proceeds so the pin can be dropped. Everything
// else is identical in both modes — in particular an undecided candidate
// stops the upgrade either way, so the override can never convert a failed
// measurement into permission to re-anchor.
//
// The identity on a blocked move is load-bearing rather than cosmetic: the
// CLI renders a blocked outcome carrying no NewSignerIdentity as "unsigned",
// so leaving it empty would report a keyless candidate as the one thing the
// measurement just established it is not.
func recordKeyedVerdict(
	verdict keyedVerdict, allowSignerChange bool, outcome *skills.UpgradeOutcome,
) bool {
	switch verdict.kind {
	case keyedPinHolds:
		return false
	case keyedMovedToKeyless:
		if allowSignerChange {
			return false
		}
		outcome.Status = skills.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = verdict.identity
		return true
	case keyedUndecided:
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = verdict.reason
		outcome.Error = verdict.err
		return true
	}
	return false
}

// keyedVerdictKind is what a candidate turned out to be when measured
// against the cosign public key its lock entry pins.
type keyedVerdictKind int

const (
	// keyedPinHolds: the candidate verifies against the pinned key, so the
	// signer demonstrably has not changed.
	keyedPinHolds keyedVerdictKind = iota
	// keyedMovedToKeyless: the pinned key conclusively does not apply AND a
	// keyless signature verifies. This is the only genuine key-to-keyless
	// move, and the only state in which dropping the pin is justified.
	keyedMovedToKeyless
	// keyedUndecided: no conclusion could be reached — the candidate carries
	// nothing, carries something that verifies neither way, or verification
	// could not be completed at all.
	keyedUndecided
)

// keyedVerdict is the measurement of a candidate against a pinned key,
// together with the outcome to record when it is not usable.
//
// One measurement serves both callers because they ask the same question and
// differ only in what a genuine move authorizes: the guard blocks it, and
// --allow-signer-change drops the pin for it. Sharing the verdict is what
// keeps those two from disagreeing about what the candidate is.
type keyedVerdict struct {
	kind     keyedVerdictKind
	identity string               // observed keyless identity, keyedMovedToKeyless only
	reason   skills.FailureReason // keyedUndecided only
	err      string               // keyedUndecided only
}

// judgeKeyedCandidate measures a candidate against the public key its entry
// pins, verifying it keylessly when the key does not apply.
//
// The keyless probe is not only for the all-keyless case. VerifyOCIWithKey
// reports ErrKeylessSigned only when EVERY attached bundle carries a
// certificate, so an artifact mid-migration — a valid keyless bundle beside
// a stale key-pair one — comes back as ErrSignatureInvalid instead. Deciding
// on the keyed error alone would call that a damaged signature, when it is
// the supported transition. Only the probe tells the two apart.
//
// SECURITY: an operational failure is never a conclusion. A registry,
// transport, or context error says nothing about which key signed the
// artifact, so it yields keyedUndecided and the upgrade fails. Treating it
// as "the pin no longer applies" would let a transient network fault stand
// in as evidence that a skill moved off its key — and under a project-wide
// --allow-signer-change that is enough to drop the pin and re-anchor an
// artifact that still carries a perfectly valid signature by the pinned key.
// An anchor may only be dropped on a conclusive mismatch plus a keyless
// signature that actually verifies.
func (s *service) judgeKeyedCandidate(
	ctx context.Context, entry lockfile.Entry, newRef, newDigest string,
) keyedVerdict {
	pubKeyPEM, err := verifier.DecodePublicKey(entry.Provenance.PublicKey)
	if err != nil {
		return keyedVerdict{
			kind:   keyedUndecided,
			reason: skills.FailureReasonUnknown,
			err:    fmt.Errorf("lock entry's pinned %w", err).Error(),
		}
	}

	_, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, newRef, newDigest, pubKeyPEM)
	if verifyErr == nil {
		return keyedVerdict{kind: keyedPinHolds}
	}
	if errors.Is(verifyErr, verifier.ErrUnsigned) {
		// Nothing is attached at all, so there is no keyless bundle to find
		// and no signer change to authorize.
		return keyedVerdict{
			kind:   keyedUndecided,
			reason: skills.FailureReasonUnsignedRejected,
			err: fmt.Errorf("candidate is unsigned, and this entry is pinned to a cosign public"+
				" key: %w. Upgrade has no unsigned-consent flag, and --allow-signer-change is not"+
				" one — it re-verifies from scratch, which an unsigned artifact still fails. To"+
				" move this skill to an unsigned artifact, reinstall it: %s",
				verifyErr, projectUnsignedReinstallCommand(entry)).Error(),
		}
	}

	if !conclusiveKeyedMismatch(verifyErr) {
		// The pin could not be evaluated, so nothing about the candidate has
		// been established — least of all that it moved off the key. Stop
		// before the keyless probe: a candidate carrying BOTH a still-valid
		// pinned-key signature and a valid keyless one would otherwise be
		// re-anchored to keyless on the strength of a transient fault.
		return keyedVerdict{
			kind:   keyedUndecided,
			reason: skills.FailureReasonUnknown,
			err: fmt.Errorf("verifying candidate against the pinned cosign public key: %w",
				verifyErr).Error(),
		}
	}

	probe, probeErr := s.probeCandidateSigner(ctx, newRef, newDigest)
	if probeErr == nil {
		return keyedVerdict{kind: keyedMovedToKeyless, identity: probe.SignerIdentity}
	}
	return keyedVerdict{
		kind:   keyedUndecided,
		reason: keyedFailureReason(verifyErr, probeErr),
		err:    keyedFailureMessage(verifyErr, probeErr),
	}
}

// conclusiveKeyedMismatch reports whether a failed keyed verification
// actually settled that the pinned key does not apply to the candidate.
//
// Only the verifier's own signature verdicts do. ErrKeylessSigned means
// every attached bundle is keyless; ErrSignatureInvalid means a bundle was
// checked against the key and did not pass. A registry, transport, or
// context error means the question was never answered, and must not be read
// as an answer.
func conclusiveKeyedMismatch(verifyErr error) bool {
	return errors.Is(verifyErr, verifier.ErrKeylessSigned) ||
		errors.Is(verifyErr, verifier.ErrSignatureInvalid)
}

// keyedFailureReason classifies a candidate that verifies neither against the
// pinned key nor keylessly. Only conclusive mismatches reach here — an
// operational failure was already reported as undecided, because calling one
// signature-invalid would attach a destructive remedy to a network blip.
func keyedFailureReason(verifyErr, probeErr error) skills.FailureReason {
	if errors.Is(verifyErr, verifier.ErrKeylessSigned) {
		// Every bundle is keyless, so the pinned key never had one to check
		// and the keyless verdict is the whole diagnosis.
		if reason := classifySignatureError(probeErr); reason != "" {
			return reason
		}
		return skills.FailureReasonUnknown
	}
	return skills.FailureReasonSignatureInvalid
}

// keyedFailureMessage explains a candidate that verifies neither way, naming
// a remedy only where one exists.
func keyedFailureMessage(verifyErr, probeErr error) string {
	switch {
	case errors.Is(verifyErr, verifier.ErrKeylessSigned):
		return fmt.Errorf("candidate dropped key-pair signing for keyless, but its keyless"+
			" signature does not verify: %w", probeErr).Error()
	default:
		return fmt.Errorf("candidate does not verify against the cosign public key this entry is"+
			" pinned to — either it was signed with a different key or the signature is damaged:"+
			" %w (to propose a replacement key, use --allow-signer-change --public-key <path>)",
			verifyErr).Error()
	}
}

// projectUnsignedReinstallCommand renders an unsigned reinstall the caller
// can actually run.
//
// Naming the flag alone is not enough to act on. `thv skill install` requires
// the skill argument and defaults to --scope user, where --allow-unsigned
// records no lock decision. So the rendered command includes both.
func projectUnsignedReinstallCommand(entry lockfile.Entry) string {
	source := entry.Source
	if source == "" {
		source = entry.Name
	}
	return fmt.Sprintf("`thv skill uninstall %s --scope project` then"+
		" `thv skill install %s --scope project --allow-unsigned`"+
		" (add --project-root if you are not in the project directory)",
		entry.Name, source)
}

// runnerEnvironmentChanged reports whether the candidate's runner class
// differs from the one recorded. An entry that recorded none is
// unconstrained — lock entries written before the field existed have it
// empty, as do certificates that carry no such extension.
func runnerEnvironmentChanged(probe *verifier.Result, recorded *lockfile.Provenance) bool {
	return recorded.RunnerEnvironment != "" && probe.RunnerEnvironment != recorded.RunnerEnvironment
}

// repositoryRefChanged reports whether the candidate's certificate ref
// differs from the one recorded, with the same absent-means-unconstrained
// rule as runnerEnvironmentChanged. Unlike the runner class, no ref value
// is treated as an automatically allowed rotation — see guardSignerChange's
// doc comment for why.
func repositoryRefChanged(probe *verifier.Result, recorded *lockfile.Provenance) bool {
	return recorded.RepositoryRef != "" && probe.RepositoryRef != recorded.RepositoryRef
}

// probeCandidateSigner verifies the candidate artifact chain-of-trust-only
// (nil expected identity) and returns the observed identity. Git candidates
// are re-resolved at the pinned commit to obtain the signature material.
func (s *service) probeCandidateSigner(ctx context.Context, newRef, newDigest string) (*verifier.Result, error) {
	if strings.Contains(newDigest, ":") {
		return s.artifactVerifier().VerifyOCI(ctx, newRef, newDigest, nil)
	}
	pinned, err := buildPinnedReference(lockfile.Entry{ResolvedReference: newRef, Digest: newDigest})
	if err != nil {
		return nil, err
	}
	gitRef, err := gitresolver.ParseGitReference(pinned)
	if err != nil {
		return nil, err
	}
	resolved, err := s.gitResolver.Resolve(ctx, gitRef)
	if err != nil {
		return nil, err
	}
	return s.artifactVerifier().VerifyGit(ctx, resolved.CommitPayload, []byte(resolved.CommitSignature), nil)
}

// applyUpgrade installs plan's pinned content when the plan calls for it.
// Preview mode reports the plan's outcome without installing anything.
// Assumes the project transaction is already held.
func (s *service) applyUpgrade(ctx context.Context, opts skills.UpgradeOptions, plan upgradePlan) skills.UpgradeOutcome {
	if opts.Preview {
		return plan.outcome
	}
	if plan.outcome.Status == skills.UpgradeStatusTrustUpdated {
		if err := s.persistTrustOnlyUpgrade(ctx, opts.ProjectRoot, plan); err != nil {
			return failedUpgradeOutcome(plan.outcome, err)
		}
		return plan.outcome
	}
	if plan.pinnedRef == "" {
		return plan.outcome
	}

	clients := opts.Clients
	if len(clients) == 0 {
		if existing, err := s.store.Get(ctx, plan.entry.Name, skills.ScopeProject, opts.ProjectRoot); err == nil {
			clients = existing.Clients
		}
	}

	installOpts := skills.InstallOptions{
		Name:                  plan.pinnedRef,
		Scope:                 skills.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               clients,
		LockSource:            plan.entry.Source,
		LockResolvedReference: plan.resolvedRef,
		AllowSignerChange:     plan.allowSignerChange,
		ExpectedCanonicalName: plan.entry.Name,
		RefreshMetadata:       plan.entry.ResolvedReference != plan.resolvedRef,
	}
	constraints := &installConstraints{expectedLockEntry: &plan.entry}
	if plan.trustDecision != nil {
		constraints.preverifiedOCI = &preverifiedOCITrust{
			decision: &provenanceDecision{
				provenance: cloneProvenanceInfo(plan.trustDecision.provenance),
				unsigned:   plan.trustDecision.unsigned,
				bundle:     bytes.Clone(plan.trustDecision.bundle),
			},
			digest: plan.outcome.NewDigest,
		}
	}

	if _, err := s.installLocked(
		ctx, installOpts, plan.pinnedRef, skills.ScopeProject, newDepState(), constraints,
	); err != nil {
		return failedUpgradeOutcome(plan.outcome, err)
	}

	return plan.outcome
}

func failedUpgradeOutcome(outcome skills.UpgradeOutcome, err error) skills.UpgradeOutcome {
	outcome.Status = skills.UpgradeStatusFailed
	outcome.Reason = classifySyncFailure(err)
	outcome.Error = err.Error()
	return outcome
}

func cloneProvenanceInfo(provenance *skills.ProvenanceInfo) *skills.ProvenanceInfo {
	if provenance == nil {
		return nil
	}
	clone := *provenance
	return &clone
}

// persistTrustOnlyUpgrade changes only the stored bundle and the lock entry's
// trust fields. SQLite and the lock file cannot share a transaction, so this
// is rollback-assisted rather than crash-atomic: the durable DB write happens
// first and is restored if the subsequent lock-file write fails.
func (s *service) persistTrustOnlyUpgrade(
	ctx context.Context, projectRoot string, plan upgradePlan,
) error {
	if plan.trustDecision == nil {
		return errors.New("trust-only upgrade has no verified trust decision")
	}

	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return err
	}
	oldSkill, err := s.store.Get(ctx, plan.entry.Name, skills.ScopeProject, projectRoot)
	if err != nil {
		return fmt.Errorf("loading installed skill for trust update: %w", err)
	}
	if oldSkill.Digest != plan.entry.Digest {
		return httperr.WithCode(
			fmt.Errorf("installed skill %q changed while its trust update was being planned; retry the upgrade",
				plan.entry.Name),
			http.StatusConflict,
		)
	}
	oldSkill.SigstoreBundle = bytes.Clone(oldSkill.SigstoreBundle)
	updatedSkill := oldSkill
	updatedSkill.SigstoreBundle = bytes.Clone(plan.trustDecision.bundle)

	updatedEntry := plan.entry
	updatedEntry.Provenance = provenanceInfoToLock(plan.trustDecision.provenance)
	updatedEntry.Unsigned = plan.trustDecision.unsigned

	if err := s.store.Update(ctx, updatedSkill); err != nil {
		return fmt.Errorf("updating stored signature bundle: %w", err)
	}
	if err := lockfile.CompareAndSwapEntry(root, plan.entry, updatedEntry); err != nil {
		lockErr := func() error {
			wrapped := fmt.Errorf("updating lock trust anchor: %w", errors.Join(errLockWrite, err))
			if errors.Is(err, lockfile.ErrEntryChanged) {
				return httperr.WithCode(wrapped, http.StatusConflict)
			}
			return wrapped
		}()
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trustOnlyRollbackTimeout)
		defer cancel()
		if rollbackErr := s.store.Update(rollbackCtx, oldSkill); rollbackErr != nil {
			return errors.Join(lockErr, fmt.Errorf("restoring stored signature bundle: %w", rollbackErr))
		}
		return lockErr
	}
	return nil
}

// resolveLatestState re-resolves source (a lock entry's original Source
// value) to its current resolvedReference and digest, using the same
// dispatch order as Install (git, direct OCI with registry fallback,
// registry name), but stopping short of extraction or any DB/lock write.
// For OCI sources this still pulls the artifact into the local store —
// there is no lighter "digest only" primitive in RegistryClient — matching
// the RFC's "preview is not side-effect-free" note; git sources resolve
// without touching disk.
func (s *service) resolveLatestState(ctx context.Context, source string) (resolvedRef, digestStr string, err error) {
	// resolvedState carries the two return values through the shared
	// source-dispatch skeleton, which routes exactly like Install does
	// (git, direct OCI with registry fallback, plain registry name) so the
	// two can never drift again.
	type resolvedState struct{ ref, digest string }

	state, err := dispatchSource(ctx, s, source, sourceOps[resolvedState]{
		git: func(ctx context.Context, gitURL string) (resolvedState, error) {
			r, d, gitErr := s.resolveGitLatest(ctx, gitURL)
			return resolvedState{r, d}, gitErr
		},
		oci: func(ctx context.Context, ref nameref.Reference) (resolvedState, error) {
			r, d, ociErr := s.resolveOCILatest(ctx, ref)
			return resolvedState{r, d}, ociErr
		},
		registry: func(ctx context.Context, resolved *registryResolveResult) (resolvedState, error) {
			r, d, regErr := s.resolveRegistryLatest(ctx, source, resolved)
			return resolvedState{r, d}, regErr
		},
	})
	return state.ref, state.digest, err
}

// resolveRegistryLatest resolves the latest state of a registry catalogue
// result, dispatching to the OCI or git resolver it points at.
func (s *service) resolveRegistryLatest(
	ctx context.Context, source string, resolved *registryResolveResult,
) (string, string, error) {
	switch {
	case resolved.OCIRef != nil:
		return s.resolveOCILatest(ctx, resolved.OCIRef)
	case resolved.GitURL != "":
		return s.resolveGitLatest(ctx, resolved.GitURL)
	}
	return "", "", httperr.WithCode(
		fmt.Errorf("skill %q resolved from registry but has no installable package", source),
		http.StatusUnprocessableEntity,
	)
}

func (s *service) resolveGitLatest(ctx context.Context, gitURL string) (string, string, error) {
	if s.gitResolver == nil {
		return "", "", httperr.WithCode(errors.New("git resolver is not configured"), http.StatusInternalServerError)
	}
	gitRef, err := gitresolver.ParseGitReference(gitURL)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("invalid git reference: %w", err), http.StatusBadRequest)
	}
	resolved, err := s.gitResolver.Resolve(ctx, gitRef)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("resolving git skill: %w", err), http.StatusBadGateway)
	}
	return gitURL, resolved.CommitHash, nil
}

func (s *service) resolveOCILatest(ctx context.Context, ref nameref.Reference) (string, string, error) {
	if s.registry == nil || s.ociStore == nil {
		return "", "", httperr.WithCode(errors.New("OCI registry is not configured"), http.StatusInternalServerError)
	}
	d, err := s.registry.Pull(ctx, s.ociStore, ref.String())
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("pulling %q: %w", ref.String(), err), classifyPullError(err))
	}
	// qualifiedOCIRef, not ref.String(): install records the qualified form
	// (implicit ":latest" made explicit) in ResolvedReference, and this value
	// is compared against it for the ref-change guard. The unqualified form
	// would misreport every digest change on a tag-less source as a blocked
	// reference change.
	return qualifiedOCIRef(ref), d.String(), nil
}

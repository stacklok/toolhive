// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
)

// var _ ensures *service continues to satisfy the full lock service surface
// now that both Sync (PR4) and Upgrade exist.
var _ skills.SkillLockService = (*service)(nil)

// Upgrade re-resolves each targeted lock entry's Source and, when the
// resolved digest has changed, installs the newer content and rewrites the
// entry (Source itself is never rewritten — see RFC THV-0080). Entries
// pinned to an immutable reference (an OCI digest or a full git commit hash)
// are reported not-upgradable: there is nothing newer to resolve to.
func (s *service) Upgrade(ctx context.Context, opts skills.UpgradeOptions) (*skills.UpgradeResult, error) {

	_, projectRoot, err := normalizeProjectRoot(skills.ScopeProject, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	opts.ProjectRoot = projectRoot

	unlock := s.projectTx.lock(projectRoot)
	defer unlock()

	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, err
	}

	targets, err := selectUpgradeTargets(lf, opts.Names)
	if err != nil {
		return nil, err
	}

	result := &skills.UpgradeResult{Outcomes: make([]skills.UpgradeOutcome, 0, len(targets))}
	for _, target := range targets {
		result.Outcomes = append(result.Outcomes, s.upgradeOne(ctx, opts, target.Name))
	}
	return result, nil
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

	if isImmutableSource(entry) {
		outcome.Status = skills.UpgradeStatusNotUpgradable
		return upgradePlan{entry: entry, outcome: outcome}
	}

	newRef, newDigest, err := s.resolveLatestState(ctx, entry.Source)
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}
	outcome.NewDigest = newDigest

	if newDigest == entry.Digest {
		outcome.Status = skills.UpgradeStatusUpToDate
		return upgradePlan{entry: entry, outcome: outcome}
	}

	if newRef != entry.ResolvedReference {
		outcome.NewResolvedReference = newRef
		// Only a move to a different repository is a supply-chain event. A
		// tag moving within the same repository is how a catalog-sourced
		// skill advances at all, and blocking it would force automation to
		// pass --allow-ref-change on every routine upgrade — which would
		// also disable the repository check this guard exists for.
		if repositoryMoved(entry.ResolvedReference, newRef) && !opts.AllowRefChange {
			outcome.Status = skills.UpgradeStatusRefChangeBlocked
			return upgradePlan{entry: entry, outcome: outcome}
		}
	}

	allowSignerChange, blocked := s.resolveSignerPolicy(ctx, opts, entry, newRef, newDigest, &outcome)
	if blocked {
		return upgradePlan{entry: entry, outcome: outcome}
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
	}
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
		return opts.AllowSignerChange, false
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
	if !opts.AllowSignerChange && s.guardSignerChange(ctx, entry, newRef, newDigest, outcome) {
		return false, true
	}
	return opts.AllowSignerChange, false
}

// guardSignerChange probes the candidate artifact's signer identity and
// fills outcome when the upgrade must not proceed: the candidate is signed
// by a different identity (or unsigned) versus the recorded provenance, its
// signature cannot be verified at all, or its certificate's repository ref
// or runner class differs from what is recorded. Returns true when blocked.
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
	outcome *skills.UpgradeOutcome,
) bool {
	probe, probeErr := s.probeCandidateSigner(ctx, newRef, newDigest)
	switch {
	case probeErr != nil && errors.Is(probeErr, verifier.ErrUnsigned):
		outcome.Status = skills.UpgradeStatusSignerChangeBlocked
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
				verifyErr, projectReinstallCommand(entry, "--allow-unsigned")).Error(),
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
		err:    keyedFailureMessage(entry, verifyErr, probeErr),
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
func keyedFailureMessage(entry lockfile.Entry, verifyErr, probeErr error) string {
	switch {
	case errors.Is(verifyErr, verifier.ErrKeylessSigned):
		return fmt.Errorf("candidate dropped key-pair signing for keyless, but its keyless"+
			" signature does not verify: %w", probeErr).Error()
	default:
		return fmt.Errorf("candidate does not verify against the cosign public key this entry is"+
			" pinned to — either it was signed with a different key or the signature is damaged:"+
			" %w (re-anchoring to a new key is not supported in place; reinstall it: %s)",
			verifyErr, projectReinstallCommand(entry, "--public-key <path>")).Error()
	}
}

// projectReinstallCommand renders a reinstall the caller can actually run.
//
// Naming the flag alone is not enough to act on. `thv skill install` requires
// the skill argument, and it defaults to --scope user, where
// validateInstallPublicKey rejects --public-key outright and --allow-unsigned
// records nothing — a lock entry's trust anchor only exists project-scoped.
// So the bare flag would be refused before it verified anything.
func projectReinstallCommand(entry lockfile.Entry, flag string) string {
	source := entry.Source
	if source == "" {
		source = entry.Name
	}
	return fmt.Sprintf("`thv skill uninstall %s --scope project` then"+
		" `thv skill install %s --scope project %s`"+
		" (add --project-root if you are not in the project directory)",
		entry.Name, source, flag)
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
	if plan.pinnedRef == "" || opts.Preview {
		return plan.outcome
	}

	clients := opts.Clients
	if len(clients) == 0 {
		if existing, err := s.store.Get(ctx, plan.entry.Name, skills.ScopeProject, opts.ProjectRoot); err == nil {
			clients = existing.Clients
		}
	}

	if _, err := s.installLocked(ctx, skills.InstallOptions{
		Name:                  plan.pinnedRef,
		Scope:                 skills.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               clients,
		LockSource:            plan.entry.Source,
		LockResolvedReference: plan.resolvedRef,
		AllowSignerChange:     plan.allowSignerChange,
		ExpectedCanonicalName: plan.entry.Name,
	}, plan.pinnedRef, skills.ScopeProject, newDepState()); err != nil {
		outcome := plan.outcome
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return outcome
	}

	return plan.outcome
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

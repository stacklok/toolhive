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

	// Resolve every target's latest state first, without installing
	// anything. FailOnChanges is a CI freshness gate: it reports the full
	// planned outcome set and never runs the apply pass at all — returning
	// the outcomes (rather than an error that discards them) lets callers
	// see exactly which skills are stale and distinguish "would change"
	// from a genuine resolution failure. Exit-code mapping happens in the
	// CLI from these outcomes, mirroring how sync --check works.
	plans := make([]upgradePlan, len(targets))
	for i, entry := range targets {
		plans[i] = s.planUpgrade(ctx, opts, entry)
	}

	result := &skills.UpgradeResult{Outcomes: make([]skills.UpgradeOutcome, 0, len(plans))}
	for _, p := range plans {
		if opts.FailOnChanges {
			result.Outcomes = append(result.Outcomes, p.outcome)
			continue
		}
		result.Outcomes = append(result.Outcomes, s.applyUpgrade(ctx, opts, p))
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

// upgradePlan is entry's resolved outcome before any install happens: either
// a terminal status (not-upgradable, up-to-date, ref-change-blocked, or a
// resolution failure) that needs no further action, or the pinned reference
// to install when the upgrade is applied.
type upgradePlan struct {
	entry       lockfile.Entry
	outcome     skills.UpgradeOutcome
	pinnedRef   string // set only when the upgrade needs installing
	resolvedRef string // the resolved reference to record as ResolvedReference; set alongside pinnedRef
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

	// Signer-change guard: when the entry records a signer identity, the
	// candidate must verify with the same one before the upgrade is even
	// planned. Verification happens here (plan-only modes stay
	// install-free) with a nil expected identity so a differing signer is
	// reported as a change rather than a bare failure; blocked plans carry
	// no pinnedRef, exactly like ref changes.
	if entry.Provenance != nil && !opts.AllowSignerChange {
		if blocked := s.guardSignerChange(ctx, entry, newRef, newDigest, &outcome); blocked {
			return upgradePlan{entry: entry, outcome: outcome}
		}
	}

	pinnedRef, err := buildPinnedReference(lockfile.Entry{ResolvedReference: newRef, Digest: newDigest})
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = skills.FailureReasonUnknown
		outcome.Error = fmt.Errorf("pinning resolved reference: %w", err).Error()
		return upgradePlan{entry: entry, outcome: outcome}
	}

	outcome.Status = skills.UpgradeStatusUpgraded
	return upgradePlan{entry: entry, outcome: outcome, pinnedRef: pinnedRef, resolvedRef: newRef}
}

// guardSignerChange probes the candidate artifact's signer identity and
// fills outcome when the upgrade must not proceed: the candidate is signed
// by a different identity (or unsigned) versus the recorded provenance, its
// signature cannot be verified at all, or its certificate's repository ref
// does not fit an allowed transition (see refTransitionAllowed) — the ref
// itself is then relaxed for the actual re-verify in applyUpgrade
// (refRelaxedExpectation), but only because THIS check already vetted the
// transition; that relaxation is not a substitute for this one. Returns true
// when blocked.
//
// The runner class IS compared here (unlike the ref): the same workflow
// moving to a different runner class is not part of publishing a new
// version, so it stays exact.
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
		!refTransitionAllowed(entry.Provenance.RepositoryRef, probe.RepositoryRef):
		outcome.Status = skills.UpgradeStatusSignerChangeBlocked
		outcome.NewSignerIdentity = probe.SignerIdentity
		return true
	}
	return false
}

// refTransitionAllowed reports whether a candidate's certificate ref may
// replace the one recorded, WITHOUT an explicit --allow-signer-change. The
// allowed transition depends on the recorded ref's SHAPE, not on whether the
// artifact is git- or OCI-sourced — a tag-triggered release workflow signs
// each version on its own tag regardless of how the resulting artifact is
// delivered, and a git-sourced skill tracking tagged releases (see
// TestUpgrade_RepinsRepositoryRef) rotates refs on every upgrade exactly
// like an OCI one does:
//
//   - An unpinned entry (recorded ref empty — pre-existing entries, or a
//     certificate that carries no ref extension at all) has nothing to
//     transition from, so anything is allowed; enforcement resumes once a
//     ref is recorded.
//   - A recorded tag ref (under "refs/tags/") may rotate to another tag ref
//     — a new release is exactly a new tag.
//   - Anything else (a branch ref, or a non-standard ref) must stay
//     IDENTICAL. A workflow's branch trigger is not expected to move on its
//     own between upgrades; a change here is exactly the "signed by the
//     right workflow, but from a different branch" substitution this field
//     exists to catch — allowing it unconditionally would undo the
//     enforcement PR #6315 added.
//
// Anything outside these cases requires the operator to confirm the change
// explicitly via --allow-signer-change, the same override already used for
// a genuine signer-identity change.
func refTransitionAllowed(recordedRef, candidateRef string) bool {
	if recordedRef == "" {
		return true
	}
	const tagRefPrefix = "refs/tags/"
	if strings.HasPrefix(recordedRef, tagRefPrefix) {
		return strings.HasPrefix(candidateRef, tagRefPrefix)
	}
	return candidateRef == recordedRef
}

// runnerEnvironmentChanged reports whether the candidate's runner class
// differs from the one recorded. An entry that recorded none is
// unconstrained — lock entries written before the field existed have it
// empty, as do certificates that carry no such extension.
func runnerEnvironmentChanged(probe *verifier.Result, recorded *lockfile.Provenance) bool {
	return recorded.RunnerEnvironment != "" && probe.RunnerEnvironment != recorded.RunnerEnvironment
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

	if _, err := s.Install(ctx, skills.InstallOptions{
		Name:                  plan.pinnedRef,
		Scope:                 skills.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               clients,
		LockSource:            plan.entry.Source,
		LockResolvedReference: plan.resolvedRef,
		AllowSignerChange:     opts.AllowSignerChange,
		// A new version is normally signed on a new ref; the plan-time guard
		// above already vetted every other identity field.
		AllowRefRepin: true,
	}); err != nil {
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

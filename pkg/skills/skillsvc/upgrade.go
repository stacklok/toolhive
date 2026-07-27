// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
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
	if !skills.LockFileFeatureEnabled() {
		return nil, errExperimentalLockFeature
	}

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

	if newRef != entry.ResolvedReference && !opts.AllowRefChange {
		outcome.Status = skills.UpgradeStatusRefChangeBlocked
		outcome.NewResolvedReference = newRef
		return upgradePlan{entry: entry, outcome: outcome}
	}
	if newRef != entry.ResolvedReference {
		outcome.NewResolvedReference = newRef
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

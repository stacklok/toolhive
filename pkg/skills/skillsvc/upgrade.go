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
	if entry.Provenance.PublicKey != "" {
		return s.guardKeyedSignerChange(ctx, entry, newRef, newDigest, outcome)
	}
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

// guardKeyedSignerChange is guardSignerChange for an entry pinned to a cosign
// public key. There is no identity to probe for and compare — a key-pair
// bundle carries no certificate — so the pinned key is applied to the
// candidate directly: verifying against it IS the evidence that the signer
// has not changed, and the upgrade then proceeds on the anchor the entry
// already records. No new key is accepted here; the lock supplies it.
//
// The blocked arms are split by whether --allow-signer-change would actually
// help, because the caller is told to use it. It does for a candidate that
// moved to keyless signing or dropped its signature: dropping the recorded
// key and re-verifying is exactly the key-to-keyless move resolveKeyAnchor
// supports. It does NOT for a candidate signed by a different key — that
// needs an in-place re-anchor, which v1 does not offer — so that arm reports
// a failure naming the route that works instead of a remedy that does not.
func (s *service) guardKeyedSignerChange(
	ctx context.Context,
	entry lockfile.Entry,
	newRef, newDigest string,
	outcome *skills.UpgradeOutcome,
) bool {
	pubKeyPEM, err := verifier.DecodePublicKey(entry.Provenance.PublicKey)
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = skills.FailureReasonUnknown
		outcome.Error = fmt.Errorf("lock entry's pinned %w", err).Error()
		return true
	}
	_, verifyErr := s.artifactVerifier().VerifyOCIWithKey(ctx, newRef, newDigest, pubKeyPEM)
	switch {
	case verifyErr == nil:
		return false
	case errors.Is(verifyErr, verifier.ErrKeylessSigned), errors.Is(verifyErr, verifier.ErrUnsigned):
		outcome.Status = skills.UpgradeStatusSignerChangeBlocked
		return true
	default:
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = skills.FailureReasonSignatureInvalid
		outcome.Error = fmt.Errorf("candidate does not verify against the cosign public key this entry"+
			" is pinned to — either it was signed with a different key or the signature is damaged:"+
			" %w (re-anchoring to a new key is not supported in place; uninstall and reinstall with"+
			" --public-key)", verifyErr).Error()
		return true
	}
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
		AllowSignerChange:     opts.AllowSignerChange,
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

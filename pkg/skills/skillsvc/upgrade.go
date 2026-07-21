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

	result := &skills.UpgradeResult{}
	for _, entry := range targets {
		outcome := s.upgradeEntry(ctx, opts, entry)
		result.Outcomes = append(result.Outcomes, outcome)
		if opts.FailOnChanges && outcome.Status != skills.UpgradeStatusUpToDate && outcome.Status != skills.UpgradeStatusNotUpgradable {
			return result, httperr.WithCode(
				fmt.Errorf("skill %q would change (%s); failing due to --fail-on-changes", outcome.Name, outcome.Status),
				http.StatusConflict,
			)
		}
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

// upgradeEntry resolves entry's current state and, if warranted, applies the
// upgrade. It never returns an error: every outcome (including a resolution
// or install failure) is reported as an UpgradeOutcome so a multi-skill
// upgrade can report partial results instead of aborting on the first error.
func (s *service) upgradeEntry(ctx context.Context, opts skills.UpgradeOptions, entry lockfile.Entry) skills.UpgradeOutcome {
	outcome := skills.UpgradeOutcome{Name: entry.Name, OldDigest: entry.Digest}

	if isImmutableSource(entry) {
		outcome.Status = skills.UpgradeStatusNotUpgradable
		return outcome
	}

	newRef, newDigest, err := s.resolveLatestState(ctx, entry.Source)
	if err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return outcome
	}
	outcome.NewDigest = newDigest

	if newDigest == entry.Digest {
		outcome.Status = skills.UpgradeStatusUpToDate
		return outcome
	}

	if newRef != entry.ResolvedReference && !opts.AllowRefChange {
		outcome.Status = skills.UpgradeStatusRefChangeBlocked
		outcome.NewResolvedReference = newRef
		return outcome
	}
	if newRef != entry.ResolvedReference {
		outcome.NewResolvedReference = newRef
	}

	if opts.Preview {
		outcome.Status = skills.UpgradeStatusUpgraded
		return outcome
	}

	if _, err := s.Install(ctx, skills.InstallOptions{
		Name:        entry.Source,
		Scope:       skills.ScopeProject,
		ProjectRoot: opts.ProjectRoot,
		Clients:     opts.Clients,
		LockSource:  entry.Source,
	}); err != nil {
		outcome.Status = skills.UpgradeStatusFailed
		outcome.Reason = classifySyncFailure(err)
		outcome.Error = err.Error()
		return outcome
	}

	outcome.Status = skills.UpgradeStatusUpgraded
	return outcome
}

// resolveLatestState re-resolves source (a lock entry's original Source
// value) to its current resolvedReference and digest, using the same
// dispatch order as Install (git, direct OCI, registry name), but stopping
// short of extraction or any DB/lock write. For OCI sources this still pulls
// the artifact into the local store — there is no lighter "digest only"
// primitive in RegistryClient — matching the RFC's "preview is not
// side-effect-free" note; git sources resolve without touching disk.
func (s *service) resolveLatestState(ctx context.Context, source string) (resolvedRef, digestStr string, err error) {
	if gitresolver.IsGitReference(source) {
		return s.resolveGitLatest(ctx, source)
	}

	ref, isOCI, err := parseOCIReference(source)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("invalid OCI reference %q: %w", source, err), http.StatusBadRequest)
	}
	if isOCI {
		return s.resolveOCILatest(ctx, ref)
	}

	resolved, regErr := s.resolveFromRegistry(source)
	if regErr != nil {
		return "", "", regErr
	}
	if resolved == nil {
		return "", "", httperr.WithCode(fmt.Errorf("skill %q not found in registry", source), http.StatusNotFound)
	}
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
	return ref.String(), d.String(), nil
}

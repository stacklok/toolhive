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

// Upgrade re-resolves each lock file entry's original source and, when the
// resolved digest differs from the pinned one, installs the new content and
// updates the lock file entry.
func (s *service) Upgrade(ctx context.Context, opts skills.UpgradeOptions) (*skills.UpgradeResult, error) {
	projectRoot, err := skills.ValidateProjectRoot(opts.ProjectRoot)
	if err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	preview := opts.Preview || opts.DryRun

	lf, err := lockfile.Load(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("loading lock file: %w", err)
	}

	targets, err := selectUpgradeTargets(lf.Skills, opts.Names)
	if err != nil {
		return nil, err
	}

	result := &skills.UpgradeResult{}
	for _, entry := range targets {
		outcome := s.upgradeEntry(ctx, projectRoot, entry, opts, preview)
		result.Outcomes = append(result.Outcomes, outcome)
	}

	if opts.FailOnChanges {
		for _, o := range result.Outcomes {
			if o.Status == skills.UpgradeStatusUpgraded {
				return result, httperr.WithCode(
					errors.New("upgrade preview found changes"),
					http.StatusConflict,
				)
			}
		}
	}

	return result, nil
}

// selectUpgradeTargets returns the subset of entries named in names, in the
// order requested, or all entries when names is empty.
func selectUpgradeTargets(entries []lockfile.Entry, names []string) ([]lockfile.Entry, error) {
	if len(names) == 0 {
		return entries, nil
	}
	byName := make(map[string]lockfile.Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	targets := make([]lockfile.Entry, 0, len(names))
	for _, name := range names {
		entry, ok := byName[name]
		if !ok {
			return nil, httperr.WithCode(
				fmt.Errorf("skill %q is not present in the lock file", name), http.StatusNotFound)
		}
		targets = append(targets, entry)
	}
	return targets, nil
}

// upgradeEntry attempts to upgrade a single lock entry.
func (s *service) upgradeEntry(
	ctx context.Context, projectRoot string, entry lockfile.Entry, opts skills.UpgradeOptions, preview bool,
) skills.UpgradeOutcome {
	if isImmutableSource(entry.Source) {
		return skills.UpgradeOutcome{Name: entry.Name, Status: skills.UpgradeStatusNotUpgradable, OldDigest: entry.Digest}
	}

	if preview {
		return s.upgradeEntryPreview(ctx, entry)
	}

	installOpts := skills.InstallOptions{
		Name:         entry.Source,
		LockSource:   entry.Source,
		Scope:        skills.ScopeProject,
		ProjectRoot:  projectRoot,
		Clients:      opts.Clients,
		Force:        true,
		Managed:      true,
		ExplicitLock: entry.Explicit,
	}
	result, err := s.installInternal(ctx, installOpts)
	if err != nil {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest,
			Reason: classifyUpgradeError(err), Error: err.Error(),
		}
	}

	if result.Skill.Digest == entry.Digest && result.Skill.Reference == entry.ResolvedReference {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusUpToDate,
			OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
		}
	}

	if result.Skill.Reference != entry.ResolvedReference && !opts.AllowRefChange {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusRefChangeBlocked,
			OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
			NewResolvedReference: result.Skill.Reference,
			Error: fmt.Sprintf("resolvedReference would change from %q to %q; pass --allow-ref-change to proceed",
				entry.ResolvedReference, result.Skill.Reference),
		}
	}

	contentDigest := result.ContentDigest
	if contentDigest == "" {
		cd, cdErr := s.contentDigestForSkill(ctx, installOpts, result.Skill)
		if cdErr != nil {
			return skills.UpgradeOutcome{
				Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest,
				Reason: skills.FailureReasonDigestMissing, Error: cdErr.Error(),
			}
		}
		contentDigest = cd
	}

	newEntry := lockfile.Entry{
		Name:              entry.Name,
		Version:           result.Skill.Metadata.Version,
		Source:            entry.Source,
		ResolvedReference: result.Skill.Reference,
		Digest:            result.Skill.Digest,
		ContentDigest:     contentDigest,
		RequiredBy:        entry.RequiredBy,
		Explicit:          entry.Explicit,
	}
	if lockErr := lockfile.UpsertEntry(projectRoot, newEntry); lockErr != nil {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
			Reason: skills.FailureReasonLockWriteFailed,
			Error:  fmt.Sprintf("skill upgraded but failed to update lock file: %v", lockErr),
		}
	}

	status := skills.UpgradeStatusUpgraded
	if result.Skill.Digest == entry.Digest {
		status = skills.UpgradeStatusUpToDate
	}
	return skills.UpgradeOutcome{
		Name: entry.Name, Status: status,
		OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
		NewResolvedReference: result.Skill.Reference,
	}
}

func (s *service) upgradeEntryPreview(ctx context.Context, entry lockfile.Entry) skills.UpgradeOutcome {
	newDigest, newRef, err := s.resolveLatestDigestAndRef(ctx, entry.Source)
	if err != nil {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest,
			Reason: classifyUpgradeError(err), Error: err.Error(),
		}
	}
	if newRef != entry.ResolvedReference {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusRefChangeBlocked,
			OldDigest: entry.Digest, NewDigest: newDigest,
			NewResolvedReference: newRef,
			Error:                fmt.Sprintf("resolvedReference would change from %q to %q", entry.ResolvedReference, newRef),
		}
	}
	if newDigest == entry.Digest {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusUpToDate, OldDigest: entry.Digest, NewDigest: newDigest,
		}
	}
	return skills.UpgradeOutcome{
		Name: entry.Name, Status: skills.UpgradeStatusUpgraded, OldDigest: entry.Digest, NewDigest: newDigest,
	}
}

func (s *service) resolveLatestDigestAndRef(ctx context.Context, source string) (string, string, error) {
	if gitresolver.IsGitReference(source) {
		if s.gitResolver == nil {
			return "", "", httperr.WithCode(errors.New("git resolver is not configured"), http.StatusInternalServerError)
		}
		gitRef, err := gitresolver.ParseGitReference(source)
		if err != nil {
			return "", "", httperr.WithCode(fmt.Errorf("invalid git reference: %w", err), http.StatusBadRequest)
		}
		resolved, err := s.gitResolver.Resolve(ctx, gitRef)
		if err != nil {
			return "", "", httperr.WithCode(fmt.Errorf("resolving git skill: %w", err), http.StatusBadGateway)
		}
		return resolved.CommitHash, source, nil
	}

	ref, isOCI, err := parseOCIReference(source)
	if err != nil {
		return "", "", httperr.WithCode(fmt.Errorf("invalid OCI reference %q: %w", source, err), http.StatusBadRequest)
	}
	if isOCI {
		d, err := s.resolveOCIDigest(ctx, ref)
		return d, qualifiedOCIRef(ref), err
	}

	resolved, err := s.resolveFromRegistry(source)
	if err != nil {
		return "", "", err
	}
	if resolved == nil {
		return "", "", httperr.WithCode(fmt.Errorf("skill %q not found in registry", source), http.StatusNotFound)
	}
	if resolved.OCIRef != nil {
		d, err := s.resolveOCIDigest(ctx, resolved.OCIRef)
		return d, qualifiedOCIRef(resolved.OCIRef), err
	}
	d, err := s.resolveGitDigest(ctx, resolved.GitURL)
	return d, resolved.GitURL, err
}

func classifyUpgradeError(err error) skills.FailureReason {
	return classifySyncError(err)
}

func (s *service) resolveGitDigest(ctx context.Context, gitURL string) (string, error) {
	if s.gitResolver == nil {
		return "", httperr.WithCode(errors.New("git resolver is not configured"), http.StatusInternalServerError)
	}
	gitRef, err := gitresolver.ParseGitReference(gitURL)
	if err != nil {
		return "", httperr.WithCode(fmt.Errorf("invalid git reference: %w", err), http.StatusBadRequest)
	}
	resolved, err := s.gitResolver.Resolve(ctx, gitRef)
	if err != nil {
		return "", httperr.WithCode(fmt.Errorf("resolving git skill: %w", err), http.StatusBadGateway)
	}
	return resolved.CommitHash, nil
}

func (s *service) resolveOCIDigest(ctx context.Context, ref nameref.Reference) (string, error) {
	if s.registry == nil || s.ociStore == nil {
		return "", httperr.WithCode(errors.New("OCI registry is not configured"), http.StatusInternalServerError)
	}
	ociRef := qualifiedOCIRef(ref)
	pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
	defer cancel()
	d, err := s.registry.Pull(pullCtx, s.ociStore, ociRef)
	if err != nil {
		return "", httperr.WithCode(fmt.Errorf("pulling OCI artifact %q: %w", ociRef, err), classifyPullError(err))
	}
	return d.String(), nil
}

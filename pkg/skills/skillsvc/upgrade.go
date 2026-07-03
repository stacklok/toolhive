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
// updates the lock file entry. Entries pinned to an immutable reference (an
// OCI digest or a full git commit hash) are reported as not upgradable.
func (s *service) Upgrade(ctx context.Context, opts skills.UpgradeOptions) (*skills.UpgradeResult, error) {
	projectRoot, err := skills.ValidateProjectRoot(opts.ProjectRoot)
	if err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

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
		result.Outcomes = append(result.Outcomes, s.upgradeEntry(ctx, projectRoot, entry, opts))
	}
	return result, nil
}

// selectUpgradeTargets returns the subset of entries named in names, in the
// order requested, or all entries when names is empty. Requesting a name
// absent from the lock file is a 404, not a silent skip.
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
	ctx context.Context, projectRoot string, entry lockfile.Entry, opts skills.UpgradeOptions,
) skills.UpgradeOutcome {
	if isImmutableSource(entry.Source) {
		return skills.UpgradeOutcome{Name: entry.Name, Status: skills.UpgradeStatusNotUpgradable, OldDigest: entry.Digest}
	}

	if opts.DryRun {
		return s.upgradeEntryDryRun(ctx, entry)
	}

	// installInternal (not Install) is used deliberately: re-installing from
	// entry.Source must not change the lock entry's Source field, and this
	// function decides for itself whether and how to rewrite the entry below.
	result, err := s.installInternal(ctx, skills.InstallOptions{
		Name:        entry.Source,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     opts.Clients,
		Force:       true,
	})
	if err != nil {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest, Error: err.Error(),
		}
	}

	if result.Skill.Digest == entry.Digest {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusUpToDate, OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
		}
	}

	newEntry := lockfile.Entry{
		Name:              entry.Name,
		Version:           result.Skill.Metadata.Version,
		Source:            entry.Source,
		ResolvedReference: result.Skill.Reference,
		Digest:            result.Skill.Digest,
	}
	if lockErr := lockfile.UpsertEntry(projectRoot, newEntry); lockErr != nil {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
			Error: fmt.Sprintf("skill upgraded but failed to update lock file: %v", lockErr),
		}
	}
	return skills.UpgradeOutcome{
		Name: entry.Name, Status: skills.UpgradeStatusUpgraded, OldDigest: entry.Digest, NewDigest: result.Skill.Digest,
	}
}

// upgradeEntryDryRun reports what an upgrade would do without installing
// anything or modifying the lock file.
func (s *service) upgradeEntryDryRun(ctx context.Context, entry lockfile.Entry) skills.UpgradeOutcome {
	newDigest, err := s.resolveLatestDigest(ctx, entry.Source)
	if err != nil {
		return skills.UpgradeOutcome{
			Name: entry.Name, Status: skills.UpgradeStatusFailed, OldDigest: entry.Digest, Error: err.Error(),
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

// resolveLatestDigest resolves source to the digest or commit hash it
// currently points to, without installing it or writing any files or DB
// records. Used for --dry-run upgrade checks.
//
// There is no lightweight "peek" API for either backend, so this still pulls
// the OCI artifact into the local cache, or clones the git repository — but
// it performs none of the extraction, filesystem, or database writes that a
// real install does.
func (s *service) resolveLatestDigest(ctx context.Context, source string) (string, error) {
	if gitresolver.IsGitReference(source) {
		return s.resolveGitDigest(ctx, source)
	}

	ref, isOCI, err := parseOCIReference(source)
	if err != nil {
		return "", httperr.WithCode(fmt.Errorf("invalid OCI reference %q: %w", source, err), http.StatusBadRequest)
	}
	if isOCI {
		return s.resolveOCIDigest(ctx, ref)
	}

	// Plain registry name — resolve through the catalog to find the current package.
	resolved, err := s.resolveFromRegistry(source)
	if err != nil {
		return "", err
	}
	if resolved == nil {
		return "", httperr.WithCode(fmt.Errorf("skill %q not found in registry", source), http.StatusNotFound)
	}
	if resolved.OCIRef != nil {
		return s.resolveOCIDigest(ctx, resolved.OCIRef)
	}
	return s.resolveGitDigest(ctx, resolved.GitURL)
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

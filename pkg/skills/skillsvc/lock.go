// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// recordLockEntry upserts a project-scope lock file entry after a successful
// install. Returns an error when the lock write fails so callers can fail the
// install command for project scope.
func recordLockEntry(opts skills.InstallOptions, source string, sk skills.InstalledSkill, contentDigest string) error {
	if sk.Scope != skills.ScopeProject || sk.ProjectRoot == "" {
		return nil
	}

	entry := lockfile.Entry{
		Name:              sk.Metadata.Name,
		Version:           sk.Metadata.Version,
		Source:            source,
		ResolvedReference: sk.Reference,
		Digest:            sk.Digest,
		ContentDigest:     contentDigest,
		Explicit:          opts.ExplicitLock,
		Provenance:        provenanceInfoToLock(opts.Provenance),
		Unsigned:          opts.Unsigned,
	}

	if opts.RequiredByParent != "" {
		entry.RequiredBy = []string{opts.RequiredByParent}
	}

	if err := lockfile.UpsertEntry(sk.ProjectRoot, entry); err != nil {
		return fmt.Errorf(
			"skill %q installed but lock NOT updated — do not commit; re-run or fix permissions: %w",
			sk.Metadata.Name, err,
		)
	}
	return nil
}

// recordDepLockEntry merges a transitive dependency into the lock file.
func recordDepLockEntry(
	projectRoot, parent, source string,
	sk skills.InstalledSkill,
	contentDigest string,
	opts skills.InstallOptions,
) error {
	return lockfile.UpdateEntry(projectRoot, func(lf *lockfile.Lockfile) error {
		existing, ok := lf.Get(sk.Metadata.Name)
		if ok {
			existing.RequiredBy = appendUnique(existing.RequiredBy, parent)
			existing.Version = sk.Metadata.Version
			existing.ResolvedReference = sk.Reference
			existing.Digest = sk.Digest
			existing.ContentDigest = contentDigest
			if existing.Source == "" {
				existing.Source = source
			}
			lf.Upsert(existing)
			return nil
		}
		lf.Upsert(lockfile.Entry{
			Name:              sk.Metadata.Name,
			Version:           sk.Metadata.Version,
			Source:            source,
			ResolvedReference: sk.Reference,
			Digest:            sk.Digest,
			ContentDigest:     contentDigest,
			RequiredBy:        []string{parent},
			Provenance:        provenanceInfoToLock(opts.Provenance),
			Unsigned:          opts.Unsigned,
		})
		return nil
	})
}

func appendUnique(slice []string, value string) []string {
	for _, s := range slice {
		if s == value {
			return slice
		}
	}
	return append(slice, value)
}

type depInstallState struct {
	visited map[string]struct{}
	count   int
}

func newDepInstallState() *depInstallState {
	return &depInstallState{visited: make(map[string]struct{})}
}

// materializeDependencies installs toolhive.requires transitively for all scopes.
func (s *service) materializeDependencies(
	ctx context.Context,
	baseOpts skills.InstallOptions,
	scope skills.Scope,
	parentName string,
	requires []skills.Dependency,
	state *depInstallState,
) error {
	for _, dep := range requires {
		if dep.Reference == "" {
			continue
		}
		if err := s.materializeOneDependency(ctx, baseOpts, scope, parentName, dep.Reference, state); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) materializeOneDependency(
	ctx context.Context,
	baseOpts skills.InstallOptions,
	scope skills.Scope,
	parentName string,
	depRef string,
	state *depInstallState,
) error {
	if _, ok := state.visited[depRef]; ok {
		return httperr.WithCode(
			fmt.Errorf("circular dependency detected involving %q", depRef),
			http.StatusUnprocessableEntity,
		)
	}
	state.visited[depRef] = struct{}{}
	state.count++
	if state.count > skills.MaxDependencies {
		return httperr.WithCode(
			fmt.Errorf("too many dependencies: exceeds limit of %d", skills.MaxDependencies),
			http.StatusUnprocessableEntity,
		)
	}

	depOpts := baseOpts
	depOpts.Name = depRef
	depOpts.LockSource = depRef
	depOpts.RequiredByParent = parentName
	depOpts.ExplicitLock = false
	depOpts.Managed = baseOpts.Managed
	depOpts.SkipDependencies = false

	result, err := s.installInternal(ctx, depOpts)
	if err != nil {
		return fmt.Errorf("installing dependency %q required by %q: %w", depRef, parentName, err)
	}

	// Nested requires from the installed dependency artifact.
	if len(result.Requires) > 0 {
		if err := s.materializeDependencies(ctx, baseOpts, scope, result.Skill.Metadata.Name, result.Requires, state); err != nil {
			return err
		}
	}

	if scope != skills.ScopeProject || baseOpts.ProjectRoot == "" {
		return nil
	}

	contentDigest, cdErr := s.contentDigestForSkill(ctx, depOpts, result.Skill)
	if cdErr != nil {
		return cdErr
	}
	return recordDepLockEntry(baseOpts.ProjectRoot, parentName, depRef, result.Skill, contentDigest, depOpts)
}

func (s *service) contentDigestForSkill(
	ctx context.Context,
	opts skills.InstallOptions,
	sk skills.InstalledSkill,
) (string, error) {
	if len(opts.LayerData) > 0 {
		return contentDigestFromLayerData(opts.LayerData)
	}
	if s.pathResolver == nil {
		return "", errors.New("path resolver is required to compute content digest")
	}
	clientTypes := sk.Clients
	if len(clientTypes) == 0 {
		clientTypes = s.pathResolver.ListSkillSupportingClients()
	}
	if len(clientTypes) == 0 {
		return "", errors.New("no client directories available for content digest")
	}
	skillPath, err := s.pathResolver.GetSkillPath(clientTypes[0], sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	if err != nil {
		return "", err
	}
	_ = ctx
	return lockfile.ContentDigestFromDir(skillPath)
}

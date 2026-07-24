// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// errLockWrite marks failures to write the project lock file, so
// classifySyncFailure can map exactly these — and not every HTTP 500 — to
// the lock-write-failed reason automation keys on.
var errLockWrite = errors.New("lock file write failed")

// recordLockState updates opts.ProjectRoot's lock file to reflect a
// just-completed project-scope install: an entry for sk, plus recursively
// materialized entries for any toolhive.requires dependencies declared in
// its SKILL.md. It also marks sk as lock-managed in the store. Callers must
// only invoke this for project-scope installs with the lock file feature
// enabled (see skills.LockFileFeatureEnabled) — sk is returned updated so the
// caller can reflect the Managed flag back to its own result.
func (s *service) recordLockState(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	sk skills.InstalledSkill,
) (skills.InstalledSkill, error) {
	contentDigest, err := computeContentDigest(s.pathResolver, sk)
	if err != nil {
		return sk, fmt.Errorf("computing content digest: %w", err)
	}

	source := opts.LockSource
	if source == "" {
		source = originalName
	}
	resolvedReference := opts.LockResolvedReference
	if resolvedReference == "" {
		resolvedReference = sk.Reference
	}
	if err := recordLockEntry(sk.ProjectRoot, lockEntryInput{
		Name:              sk.Metadata.Name,
		Version:           sk.Metadata.Version,
		Source:            source,
		ResolvedReference: resolvedReference,
		Digest:            sk.Digest,
		ContentDigest:     contentDigest,
		RequiredByParent:  opts.RequiredByParent,
		PreserveExplicit:  opts.SyncRestore,
	}); err != nil {
		return sk, fmt.Errorf("writing lock entry: %w", errors.Join(errLockWrite, err))
	}

	if !sk.Managed {
		sk.Managed = true
		if err := s.store.Update(ctx, sk); err != nil {
			return sk, fmt.Errorf("marking skill as lock-managed: %w", err)
		}
	}

	// A sync restore reinstalls exactly what the lock file already pins:
	// every dependency has its own entry that the sync loop restores
	// directly, so re-materializing toolhive.requires here would re-resolve
	// each dependency from its mutable source string — silently upgrading
	// and re-pinning it, the opposite of a deterministic restore.
	if !opts.SyncRestore {
		if err := s.materializeDependencies(ctx, opts, source, sk); err != nil {
			return sk, fmt.Errorf("materializing dependencies: %w", err)
		}
	}
	return sk, nil
}

// materializeDependencies installs every toolhive.requires dependency
// declared by sk's SKILL.md, recursively (a dependency may itself declare
// further dependencies). A Visited set threaded through opts prevents
// infinite recursion on a requires cycle; skills.MaxDependencies bounds the
// total number of skills materialized across the whole tree, not just the
// direct dependency list of a single skill.
//
// source is the reference sk was installed from (recordLockState's Source,
// after any LockSource override) — the same kind of string a dependency
// edge names in another skill's toolhive.requires. Visited must be keyed
// consistently by that reference form, not by sk's resolved Metadata.Name:
// a requires edge naming sk by a reference string other than its resolved
// name would otherwise bypass the cycle check and re-materialize sk as its
// own dependency, corrupting its RequiredBy list.
func (s *service) materializeDependencies(
	ctx context.Context,
	opts skills.InstallOptions,
	source string,
	sk skills.InstalledSkill,
) error {
	parsed, err := readSkillMD(s.pathResolver, sk)
	if err != nil {
		return err
	}
	if len(parsed.Requires) == 0 {
		return nil
	}

	visited := opts.Visited
	if visited == nil {
		visited = make(map[string]struct{})
	}
	visited[source] = struct{}{}

	for _, dep := range parsed.Requires {
		if _, seen := visited[dep.Reference]; seen {
			continue
		}
		if len(visited) >= skills.MaxDependencies {
			return fmt.Errorf("dependency tree for %q exceeds maximum of %d skills",
				sk.Metadata.Name, skills.MaxDependencies)
		}
		visited[dep.Reference] = struct{}{}

		depOpts := skills.InstallOptions{
			Name:             dep.Reference,
			Scope:            sk.Scope,
			ProjectRoot:      sk.ProjectRoot,
			Clients:          sk.Clients,
			Group:            opts.Group, // deps join the parent's group, not the default one
			RequiredByParent: sk.Metadata.Name,
			Visited:          visited,
		}
		if _, err := s.Install(ctx, depOpts); err != nil {
			return fmt.Errorf("installing dependency %q (required by %q): %w", dep.Reference, sk.Metadata.Name, err)
		}
	}
	return nil
}

// lockEntryInput carries the fields recordLockEntry needs to upsert a lock
// entry, decoupled from skillsvc's own InstallOptions/InstalledSkill shapes.
type lockEntryInput struct {
	Name              string
	Version           string
	Source            string
	ResolvedReference string
	Digest            string
	ContentDigest     string
	// RequiredByParent names the parent skill when this entry is a
	// transitively materialized dependency. Empty means the entry is
	// explicit (a direct, user-requested install).
	RequiredByParent string
	// PreserveExplicit keeps the existing entry's Explicit flag verbatim
	// instead of deriving it from RequiredByParent. Set by sync restores: a
	// restore is not a user install, so it must not promote a non-explicit
	// dependency to explicit — that would permanently exempt it from
	// cascade removal.
	PreserveExplicit bool
}

// recordLockEntry upserts a single entry into projectRoot's lock file. When
// an entry for the same name already exists, its RequiredBy list is merged
// (not overwritten) so a dependency shared by multiple parents keeps every
// parent, and Explicit is sticky once true.
func recordLockEntry(projectRoot string, in lockEntryInput) error {
	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return err
	}
	return lockfile.Update(root, func(lf *lockfile.Lockfile) error {
		entry := lockfile.Entry{
			Name:              in.Name,
			Version:           in.Version,
			Source:            in.Source,
			ResolvedReference: in.ResolvedReference,
			Digest:            in.Digest,
			ContentDigest:     in.ContentDigest,
			Explicit:          in.RequiredByParent == "",
		}
		existing, exists := lf.Get(in.Name)
		if exists {
			entry.RequiredBy = existing.RequiredBy
			entry.Explicit = entry.Explicit || existing.Explicit
		}
		if in.PreserveExplicit {
			entry.Explicit = exists && existing.Explicit
		}
		if in.RequiredByParent != "" {
			entry.RequiredBy = appendUnique(entry.RequiredBy, in.RequiredByParent)
		}
		lf.Upsert(entry)
		return nil
	})
}

func appendUnique(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

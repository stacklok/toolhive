// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// recordLockState updates opts.ProjectRoot's lock file to reflect a
// just-completed project-scope install: a plugins: entry for pl. It also
// marks pl as lock-managed in the store. Callers must only invoke this for
// project-scope installs with the lock file feature enabled (see
// plugins.LockFileFeatureEnabled) — pl is returned updated so the caller
// can reflect the Managed flag back to its own result.
//
// Plugin requires is parsed today but not materialized; requiredBy/explicit
// stay unused until that Phase-3 wave. Every recorded plugin install is
// treated as explicit.
func (s *service) recordLockState(
	ctx context.Context,
	opts plugins.InstallOptions,
	pl plugins.InstalledPlugin,
	contentDigest string,
) (plugins.InstalledPlugin, error) {
	// contentDigest is always populated by installWithExtraction
	// (lockContentDigest gates on the same ScopeProject + feature-flag
	// condition as installAndRegister's lockScoped), and Install sets
	// LockSource before any dispatch can rewrite opts.Name — so neither
	// value needs a fallback here.
	if contentDigest == "" {
		return pl, fmt.Errorf("recording lock state for %q: content digest was not computed", pl.Metadata.Name)
	}
	source := opts.LockSource
	resolvedReference := opts.LockResolvedReference
	if resolvedReference == "" {
		resolvedReference = pl.Reference
	}
	if err := recordLockEntry(pl.ProjectRoot, lockEntryInput{
		Name:              pl.Metadata.Name,
		Version:           pl.Metadata.Version,
		Source:            source,
		ResolvedReference: resolvedReference,
		Digest:            pl.Digest,
		ContentDigest:     contentDigest,
	}); err != nil {
		return pl, fmt.Errorf("writing lock entry: %w", err)
	}

	if !pl.Managed {
		pl.Managed = true
		if err := s.store.Update(ctx, pl); err != nil {
			return pl, fmt.Errorf("marking plugin as lock-managed: %w", err)
		}
	}
	return pl, nil
}

// lockEntryInput carries the fields recordLockEntry needs to upsert a
// plugins: lock entry, decoupled from pluginsvc's own InstallOptions shape.
type lockEntryInput struct {
	Name              string
	Version           string
	Source            string
	ResolvedReference string
	Digest            string
	ContentDigest     string
}

// recordLockEntry upserts a single plugins: entry into projectRoot's lock
// file. When an entry for the same name already exists, Explicit is sticky
// once true so a later reinstall cannot demote it. requiredBy is preserved
// verbatim (v1 does not materialize plugin requires).
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
			Explicit:          true,
		}
		existing, exists := lf.GetPlugin(in.Name)
		if exists {
			entry.RequiredBy = existing.RequiredBy
			entry.Explicit = entry.Explicit || existing.Explicit
		}
		lf.UpsertPlugin(entry)
		return nil
	})
}

// removeLockEntry removes opts.Name's plugins: lock entry. Unlike skills,
// plugin uninstall does not cascade: requires is not materialized in v1.
func removeLockEntry(opts plugins.UninstallOptions) error {
	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		return err
	}
	return lockfile.RemovePluginEntry(root, opts.Name)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Sync restores a project's installed skills to match its lock file: missing
// or drifted entries are reinstalled at their pinned digest (never
// re-resolved from source — see buildPinnedReference), unmanaged installs are
// reported (or adopted with Adopt), and lock-managed installs no longer in
// the lock file are reported (or removed with Prune). Check performs the
// same reconciliation read-only: nothing is installed, written, or removed.
func (s *service) Sync(ctx context.Context, opts skills.SyncOptions) (*skills.SyncResult, error) {
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

	installed, err := s.store.List(ctx, storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	if err != nil {
		return nil, fmt.Errorf("listing installed skills: %w", err)
	}
	installedByName := make(map[string]skills.InstalledSkill, len(installed))
	for _, sk := range installed {
		installedByName[sk.Metadata.Name] = sk
	}

	result := &skills.SyncResult{}
	for _, entry := range lf.Skills {
		sk, dbOK := installedByName[entry.Name]
		s.syncLockedEntry(ctx, opts, entry, sk, dbOK, result)
	}
	for _, sk := range installed {
		if _, ok := lf.Get(sk.Metadata.Name); ok {
			continue // handled by the loop above
		}
		s.syncUnlockedInstall(ctx, opts, sk, result)
	}

	return result, nil
}

// errExperimentalLockFeature is returned by Sync/Upgrade while the lock file
// feature is behind its rollout gate (skills.LockFileFeatureEnabled).
var errExperimentalLockFeature = httperr.WithCode(
	fmt.Errorf("skills lock file support is experimental; set %s=true to use it", skills.LockFileEnvVar),
	http.StatusForbidden,
)

// syncLockedEntry reconciles one lock file entry against installed state,
// appending its outcome to result. Missing (dbOK false) and drifted (digest
// or contentDigest mismatch) entries are reinstalled at the pinned reference
// unless opts.Check is set, in which case nothing is written — drift is
// still reported in result.Drifted, never as a failure.
func (s *service) syncLockedEntry(
	ctx context.Context,
	opts skills.SyncOptions,
	entry lockfile.Entry,
	sk skills.InstalledSkill,
	dbOK bool,
	result *skills.SyncResult,
) {
	if dbOK && entryMatchesInstalled(s.pathResolver, entry, sk) {
		result.AlreadyCurrent = append(result.AlreadyCurrent, entry.Name)
		return
	}
	if dbOK {
		result.Drifted = append(result.Drifted, entry.Name)
	}
	if opts.Check {
		return
	}
	if err := s.reinstallPinned(ctx, opts, entry, sk, dbOK); err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: entry.Name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	result.Installed = append(result.Installed, entry.Name)
}

// entryMatchesInstalled reports whether the installed skill's pinned digest
// and on-disk contentDigest both still match the lock entry.
func entryMatchesInstalled(pathResolver skills.PathResolver, entry lockfile.Entry, sk skills.InstalledSkill) bool {
	if sk.Digest != entry.Digest {
		return false
	}
	contentDigest, err := computeContentDigest(pathResolver, sk)
	return err == nil && contentDigest == entry.ContentDigest
}

// reinstallPinned reinstalls entry at its pinned reference, preserving its
// recorded Source (never re-resolving) and the clients it was previously
// installed for unless the caller overrides them.
func (s *service) reinstallPinned(
	ctx context.Context, opts skills.SyncOptions, entry lockfile.Entry, existing skills.InstalledSkill, dbOK bool,
) error {
	pinnedRef, err := buildPinnedReference(entry)
	if err != nil {
		return fmt.Errorf("pinning %q: %w", entry.Name, err)
	}
	clients := opts.Clients
	if len(clients) == 0 && dbOK {
		clients = existing.Clients
	}
	_, err = s.Install(ctx, skills.InstallOptions{
		Name:        pinnedRef,
		Scope:       skills.ScopeProject,
		ProjectRoot: opts.ProjectRoot,
		Clients:     clients,
		Force:       true, // sync restores exactly the pinned content over any drifted files
		LockSource:  entry.Source,
		SyncRestore: true, // reinstall even though Digest is unchanged — drift happened on disk, not to the pin
	})
	return err
}

// syncUnlockedInstall classifies a project-scope install that has no lock
// entry: NeverManaged (optionally adopted) or RemovedFromLock (optionally
// pruned), appending the outcome to result.
func (s *service) syncUnlockedInstall(
	ctx context.Context, opts skills.SyncOptions, sk skills.InstalledSkill, result *skills.SyncResult,
) {
	if !sk.Managed {
		result.NeverManaged = append(result.NeverManaged, sk.Metadata.Name)
		if opts.Adopt && !opts.Check {
			if err := s.adoptSkill(ctx, sk); err != nil {
				result.Failed = append(result.Failed, skills.SyncFailure{
					Name: sk.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
				})
			}
		}
		return
	}

	result.RemovedFromLock = append(result.RemovedFromLock, sk.Metadata.Name)
	if opts.Prune && !opts.Check {
		if err := s.Uninstall(ctx, skills.UninstallOptions{
			Name: sk.Metadata.Name, Scope: skills.ScopeProject, ProjectRoot: opts.ProjectRoot,
		}); err != nil {
			result.Failed = append(result.Failed, skills.SyncFailure{
				Name: sk.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
			})
			return
		}
		result.Pruned = append(result.Pruned, sk.Metadata.Name)
	}
}

// adoptSkill writes a lock entry for an existing, unmanaged project-scope
// install, pinning its current on-disk state. The install's own Reference is
// used as Source: an adopted install predates (or never went through) lock
// tracking, so the original user-typed request is not recoverable — the
// concrete resolved reference is the closest available fact to pin against.
func (s *service) adoptSkill(ctx context.Context, sk skills.InstalledSkill) error {
	contentDigest, err := computeContentDigest(s.pathResolver, sk)
	if err != nil {
		return fmt.Errorf("computing content digest: %w", err)
	}
	if err := recordLockEntry(sk.ProjectRoot, lockEntryInput{
		Name:              sk.Metadata.Name,
		Version:           sk.Metadata.Version,
		Source:            sk.Reference,
		ResolvedReference: sk.Reference,
		Digest:            sk.Digest,
		ContentDigest:     contentDigest,
	}); err != nil {
		return fmt.Errorf("writing lock entry: %w", err)
	}
	sk.Managed = true
	if err := s.store.Update(ctx, sk); err != nil {
		return fmt.Errorf("marking skill as lock-managed: %w", err)
	}
	return nil
}

// classifySyncFailure maps an error from the install/uninstall path to an
// RFC THV-0080 typed failure reason using its HTTP status code — the
// structured signal those paths already attach via httperr — rather than
// matching on error message text.
func classifySyncFailure(err error) skills.FailureReason {
	switch httperr.Code(err) {
	case http.StatusNotFound:
		return skills.FailureReasonDigestMissing
	case http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return skills.FailureReasonRegistryUnreachable
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict:
		return skills.FailureReasonValidationRejected
	case http.StatusInternalServerError:
		return skills.FailureReasonLockWriteFailed
	default:
		return skills.FailureReasonUnknown
	}
}

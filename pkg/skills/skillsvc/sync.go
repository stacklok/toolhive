// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Sync restores a project's installed skills to match its lock file: missing
// or drifted entries are reinstalled at their pinned digest (never
// re-resolved from source — see buildPinnedReference), unmanaged installs are
// reported (or adopted with Adopt), and lock-managed installs no longer in
// the lock file are reported (or removed with Prune). Check performs the
// same reconciliation read-only: nothing is installed, written, or removed.
func (s *service) Sync(ctx context.Context, opts skills.SyncOptions) (*skills.SyncResult, error) {

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

	installed, err := s.store.List(ctx, storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	if err != nil {
		return nil, fmt.Errorf("listing installed skills: %w", err)
	}

	names := make([]string, 0, len(lf.Skills)+len(installed))
	seen := make(map[string]struct{}, len(lf.Skills)+len(installed))
	for _, entry := range lf.Skills {
		names = append(names, entry.Name)
		seen[entry.Name] = struct{}{}
	}
	for _, sk := range installed {
		if _, ok := seen[sk.Metadata.Name]; ok {
			continue
		}
		names = append(names, sk.Metadata.Name)
	}

	result := &skills.SyncResult{}
	for _, name := range names {
		s.syncOne(ctx, opts, name, result)
	}

	return result, nil
}

// syncOne re-reads the lock entry and DB row under the held project
// transaction, then reconciles that fresh state. The initial Sync snapshot
// is only used to discover names; mutation from a stale view would
// resurrect an uninstall or prune a concurrent install.
func (s *service) syncOne(
	ctx context.Context, opts skills.SyncOptions, name string, result *skills.SyncResult,
) {
	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	entry, hasEntry := lf.Get(name)

	sk, err := s.store.Get(ctx, name, skills.ScopeProject, opts.ProjectRoot)
	dbOK := err == nil
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}

	if hasEntry {
		s.syncLockedEntry(ctx, opts, entry, sk, dbOK, result)
		return
	}
	if !dbOK {
		return
	}
	s.syncUnlockedInstall(ctx, opts, sk, result)
}

// syncLockedEntry reconciles one lock file entry against installed state,
// appending its outcome to result. Missing (dbOK false) and drifted (digest
// or contentDigest mismatch) entries are reinstalled at the pinned reference
// unless opts.Check is set, in which case nothing is written — both states
// are still reported (Missing/Drifted), never as failures. Recording
// Missing before the Check return is what makes --check a real gate on a
// fresh clone or CI runner, where every entry has a lock entry but no
// install record.
func (s *service) syncLockedEntry(
	ctx context.Context,
	opts skills.SyncOptions,
	entry lockfile.Entry,
	sk skills.InstalledSkill,
	dbOK bool,
	result *skills.SyncResult,
) {
	sigOK := true
	if dbOK {
		if sigErr := s.verifyStoredSignature(entry, sk); sigErr != nil {
			// A failed offline re-verification is treated as drift: check
			// mode reports it, apply mode reinstalls from the pinned
			// reference — where install-time verification enforces the
			// locked identity and, on success, heals the stored bundle.
			sigOK = false
			slog.Warn("stored signature failed offline re-verification",
				"skill", entry.Name, "error", sigErr)
		}
	}
	if dbOK && sigOK && entryMatchesInstalled(s.pathResolver, entry, sk, opts.Clients) {
		result.AlreadyCurrent = append(result.AlreadyCurrent, entry.Name)
		return
	}
	if dbOK {
		result.Drifted = append(result.Drifted, entry.Name)
	} else {
		result.Missing = append(result.Missing, entry.Name)
	}
	if opts.Check {
		return
	}
	if err := s.reinstallPinned(ctx, opts, entry); err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: entry.Name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	result.Installed = append(result.Installed, entry.Name)
}

// entryMatchesInstalled reports whether the installed skill's pinned digest
// still matches the lock entry, every expected client is present, and every
// checked client directory's on-disk contentDigest matches. With no
// --clients override, expected clients are every skill-supporting client
// detected on the host so a newly installed client is not treated as current.
func entryMatchesInstalled(
	pathResolver skills.PathResolver, entry lockfile.Entry, sk skills.InstalledSkill, requestedClients []string,
) bool {
	if sk.Digest != entry.Digest {
		return false
	}
	if len(sk.Clients) == 0 {
		return false
	}
	expected := requestedClients
	if len(expected) == 0 && pathResolver != nil {
		expected = pathResolver.ListSkillSupportingClients()
	}
	if len(expected) == 0 || !clientsContainAll(sk.Clients, expected) {
		return false
	}
	for _, client := range mergeClientLists(sk.Clients, expected) {
		dir, err := pathResolver.GetSkillPath(client, sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
		if err != nil {
			return false
		}
		contentDigest, err := lockfile.ContentDigestFromDir(dir)
		if err != nil || contentDigest != entry.ContentDigest {
			return false
		}
	}
	return true
}

// reinstallPinned reinstalls entry at its pinned reference, preserving its
// recorded Source (never re-resolving). Empty opts.Clients keeps Install's
// all-detected default so a newly detected client is materialized.
// Assumes the project transaction is already held.
func (s *service) reinstallPinned(
	ctx context.Context, opts skills.SyncOptions, entry lockfile.Entry,
) error {
	pinnedRef, err := buildPinnedReference(entry)
	if err != nil {
		return fmt.Errorf("pinning %q: %w", entry.Name, err)
	}
	_, err = s.installLocked(ctx, skills.InstallOptions{
		Name:                  pinnedRef,
		Scope:                 skills.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               opts.Clients,
		Force:                 true, // sync restores exactly the pinned content over any drifted files
		LockSource:            entry.Source,
		LockResolvedReference: entry.ResolvedReference, // preserve — pinnedRef is a restore form
		SyncRestore:           true,                    // reinstall despite unchanged Digest — drift is on disk, not the pin
		ExpectedCanonicalName: entry.Name,
	}, pinnedRef, skills.ScopeProject, newDepState())
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
			if err := s.adoptSkill(ctx, opts, sk); err != nil {
				result.Failed = append(result.Failed, skills.SyncFailure{
					Name: sk.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
				})
			}
		}
		return
	}

	result.RemovedFromLock = append(result.RemovedFromLock, sk.Metadata.Name)
	if opts.Prune && !opts.Check {
		if err := s.uninstallLocked(ctx, skills.UninstallOptions{
			Name: sk.Metadata.Name, Scope: skills.ScopeProject, ProjectRoot: opts.ProjectRoot,
		}, skills.ScopeProject); err != nil {
			result.Failed = append(result.Failed, skills.SyncFailure{
				Name: sk.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
			})
			return
		}
		result.Pruned = append(result.Pruned, sk.Metadata.Name)
	}
}

// verifyStoredSignature re-verifies the Sigstore bundle stored with an
// installed skill against the identity its lock entry records — entirely
// offline, via the embedded trust root. Entries recorded unsigned or with
// no provenance have nothing to verify. A recorded identity with no stored
// bundle fails closed for OCI installs (the bundle should exist); git
// installs never store a bundle — their signature lives on the commit and
// is re-verified when content is re-resolved.
func (s *service) verifyStoredSignature(entry lockfile.Entry, sk skills.InstalledSkill) error {
	if entry.Unsigned || entry.Provenance == nil {
		return nil
	}
	if len(sk.SigstoreBundle) == 0 {
		if !strings.Contains(entry.Digest, ":") {
			return nil // git install: no stored bundle by design
		}
		return fmt.Errorf("%w: lock entry records signer %q but no bundle is stored",
			verifier.ErrSignatureInvalid, entry.Provenance.SignerIdentity)
	}
	return s.artifactVerifier().VerifyBundleOffline(sk.SigstoreBundle, entry.Digest, entry.Provenance)
}

// adoptSkill writes a lock entry for an existing, unmanaged project-scope
// install, pinning its current on-disk state. The install's own Reference is
// used as Source: an adopted install predates (or never went through) lock
// tracking, so the original user-typed request is not recoverable — the
// concrete resolved reference is the closest available fact to pin against.
//
// Trust state is back-filled from the stored Sigstore bundle when one
// exists; otherwise adoption is the same trust decision as an unsigned
// install and requires the explicit AllowUnsigned exception.
//
// Assumes the project transaction is already held. If marking the skill
// managed fails after the lock entry is written, the pre-adopt lock entry
// is restored (or the newly created entry is removed).
func (s *service) adoptSkill(ctx context.Context, opts skills.SyncOptions, sk skills.InstalledSkill) error {
	contentDigest, err := computeContentDigest(s.pathResolver, sk)
	if err != nil {
		return fmt.Errorf("computing content digest: %w", err)
	}
	var provenance *lockfile.Provenance
	unsigned := false
	if len(sk.SigstoreBundle) > 0 {
		result, verifyErr := s.artifactVerifier().ResultFromBundle(sk.SigstoreBundle, sk.Digest)
		if verifyErr != nil {
			return fmt.Errorf("verifying stored bundle for adoption: %w", verifyErr)
		}
		provenance = provenanceInfoToLock(provenanceInfoFromResult(result))
	}
	if provenance == nil {
		if !opts.AllowUnsigned {
			return httperr.WithCode(
				fmt.Errorf("%w: adopting %q records it as unsigned; pass --allow-unsigned to accept that",
					verifier.ErrUnsigned, sk.Metadata.Name),
				http.StatusForbidden,
			)
		}
		unsigned = true
	}

	var prevEntry *lockfile.Entry
	if root, rootErr := lockfile.OpenRoot(sk.ProjectRoot); rootErr == nil {
		if lf, loadErr := lockfile.Load(root); loadErr == nil {
			if e, ok := lf.Get(sk.Metadata.Name); ok {
				prev := e
				prevEntry = &prev
			}
		}
	}

	if err := recordLockEntry(sk.ProjectRoot, lockEntryInput{
		Name:              sk.Metadata.Name,
		Version:           sk.Metadata.Version,
		Source:            sk.Reference,
		ResolvedReference: sk.Reference,
		Digest:            sk.Digest,
		ContentDigest:     contentDigest,
		Provenance:        provenance,
		Unsigned:          unsigned,
	}); err != nil {
		return fmt.Errorf("writing lock entry: %w", errors.Join(errLockWrite, err))
	}
	sk.Managed = true
	if err := s.store.Update(ctx, sk); err != nil {
		if restoreErr := restoreAdoptedLockEntry(sk.ProjectRoot, sk.Metadata.Name, prevEntry); restoreErr != nil {
			return errors.Join(
				fmt.Errorf("marking skill as lock-managed: %w", err),
				fmt.Errorf("restoring lock entry after failed adopt: %w", restoreErr),
			)
		}
		return fmt.Errorf("marking skill as lock-managed: %w", err)
	}
	return nil
}

// restoreAdoptedLockEntry undoes adoptSkill's lock write: reinstates the
// entry observed before adoption, or removes the name if none existed.
func restoreAdoptedLockEntry(projectRoot, name string, prevEntry *lockfile.Entry) error {
	if prevEntry != nil {
		root, err := lockfile.OpenRoot(projectRoot)
		if err != nil {
			return err
		}
		return lockfile.UpsertEntry(root, *prevEntry)
	}
	_, err := removeLockEntry(skills.UninstallOptions{
		Name: name, Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	})
	return err
}

// classifySyncFailure maps an error from the install/uninstall path to an
// RFC THV-0080 typed failure reason using structured signals those paths
// already attach — the errLockWrite sentinel and httperr status codes —
// rather than matching on error message text. Lock-write failures are
// identified by the sentinel specifically: mapping every HTTP 500 to
// lock-write-failed would mislabel unrelated internal errors (e.g. a
// missing resolver) for the automation that keys on this reason.
func classifySyncFailure(err error) skills.FailureReason {
	if errors.Is(err, errLockWrite) {
		return skills.FailureReasonLockWriteFailed
	}
	if reason := classifySignatureError(err); reason != "" {
		return reason
	}
	switch httperr.Code(err) {
	case http.StatusNotFound:
		return skills.FailureReasonDigestMissing
	case http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return skills.FailureReasonRegistryUnreachable
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict:
		return skills.FailureReasonValidationRejected
	default:
		return skills.FailureReasonUnknown
	}
}

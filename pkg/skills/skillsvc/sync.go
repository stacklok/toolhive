// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Sync installs the exact name/digest pinned in the project's lock file for
// every entry, restoring skills that are missing or have drifted from their
// pinned contentDigest. Project-scoped skills outside the lock file are
// classified as never-managed or removed-from-lock.
func (s *service) Sync(ctx context.Context, opts skills.SyncOptions) (*skills.SyncResult, error) {
	projectRoot, err := skills.ValidateProjectRoot(opts.ProjectRoot)
	if err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	lf, err := lockfile.Load(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("loading lock file: %w", err)
	}

	result := &skills.SyncResult{}
	locked := make(map[string]struct{}, len(lf.Skills))
	for _, entry := range lf.Skills {
		locked[entry.Name] = struct{}{}
	}

	if opts.Adopt {
		if err := s.syncAdopt(ctx, projectRoot, lf, opts.Clients, result); err != nil {
			return nil, err
		}
		return result, nil
	}

	for _, entry := range lf.Skills {
		s.syncEntry(ctx, projectRoot, entry, opts, result)
	}

	if err := s.syncUnmanaged(ctx, projectRoot, lf, locked, opts.Prune, opts.Check, result); err != nil {
		return nil, err
	}

	// Backward compatibility: populate deprecated Unmanaged field.
	result.Unmanaged = append(append([]string{}, result.NeverManaged...), result.RemovedFromLock...)

	return result, nil
}

func (s *service) syncEntry(
	ctx context.Context, projectRoot string, entry lockfile.Entry, opts skills.SyncOptions, result *skills.SyncResult,
) {
	status, verifyErr := s.verifyLockEntry(ctx, projectRoot, entry, opts.Clients)
	if verifyErr != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: entry.Name, Reason: classifySyncError(verifyErr), Error: verifyErr.Error(),
		})
		return
	}

	if opts.Check {
		switch status {
		case syncStatusUpToDate:
			result.UpToDate = append(result.UpToDate, entry.Name)
		case syncStatusDrifted, syncStatusMissing:
			result.Failed = append(result.Failed, skills.SyncFailure{
				Name: entry.Name, Reason: skills.FailureReasonContentMismatch,
				Error: fmt.Sprintf("on-disk content does not match lock for %q", entry.Name),
			})
		}
		return
	}

	switch status {
	case syncStatusUpToDate:
		result.UpToDate = append(result.UpToDate, entry.Name)
		return
	case syncStatusDrifted:
		result.Drifted = append(result.Drifted, entry.Name)
	case syncStatusMissing:
		// install below
	}

	pinnedRef, err := buildPinnedReference(entry)
	if err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: entry.Name, Reason: skills.FailureReasonValidationRejected, Error: err.Error(),
		})
		return
	}

	installOpts := skills.InstallOptions{
		Name:        pinnedRef,
		LockSource:  entry.Source,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     opts.Clients,
		Force:       true,
		Managed:     true,
	}
	if _, err := s.installInternal(ctx, installOpts); err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{
			Name: entry.Name, Reason: classifySyncError(err), Error: err.Error(),
		})
		return
	}
	result.Installed = append(result.Installed, entry.Name)
}

type syncEntryStatus int

const (
	syncStatusUpToDate syncEntryStatus = iota
	syncStatusDrifted
	syncStatusMissing
)

func (s *service) verifyLockEntry(
	ctx context.Context, projectRoot string, entry lockfile.Entry, clients []string,
) (syncEntryStatus, error) {
	existing, storeErr := s.store.Get(ctx, entry.Name, skills.ScopeProject, projectRoot)
	if storeErr != nil {
		if errors.Is(storeErr, storage.ErrNotFound) {
			return syncStatusMissing, nil
		}
		return syncStatusMissing, storeErr
	}

	if entry.ContentDigest == "" {
		if existing.Digest == entry.Digest {
			return syncStatusUpToDate, nil
		}
		return syncStatusDrifted, nil
	}

	clientTypes, err := s.resolveClientTypes(clients)
	if err != nil {
		return syncStatusMissing, err
	}

	digests, err := s.contentDigestsForClients(entry.Name, skills.ScopeProject, projectRoot, clientTypes)
	if err != nil {
		return syncStatusMissing, err
	}
	if len(digests) == 0 {
		return syncStatusMissing, nil
	}

	first := digests[0]
	for _, d := range digests[1:] {
		if d != first {
			return syncStatusDrifted, fmt.Errorf("client directories disagree on content for %q", entry.Name)
		}
	}
	if first != entry.ContentDigest {
		return syncStatusDrifted, nil
	}
	if existing.Digest != entry.Digest {
		return syncStatusDrifted, nil
	}
	return syncStatusUpToDate, nil
}

func (s *service) contentDigestsForClients(
	skillName string, scope skills.Scope, projectRoot string, clientTypes []string,
) ([]string, error) {
	if s.pathResolver == nil {
		return nil, errors.New("path resolver is not configured")
	}
	var digests []string
	for _, ct := range clientTypes {
		skillPath, err := s.pathResolver.GetSkillPath(ct, skillName, scope, projectRoot)
		if err != nil {
			return nil, err
		}
		d, err := lockfile.ContentDigestFromDir(skillPath)
		if err != nil {
			return nil, err
		}
		digests = append(digests, d)
	}
	return digests, nil
}

func (s *service) syncUnmanaged(
	ctx context.Context,
	projectRoot string,
	lf *lockfile.Lockfile,
	locked map[string]struct{},
	prune bool,
	check bool,
	result *skills.SyncResult,
) error {
	installed, err := s.store.List(ctx, storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	if err != nil {
		return fmt.Errorf("listing installed skills: %w", err)
	}

	for _, sk := range installed {
		name := sk.Metadata.Name
		if _, ok := locked[name]; ok {
			continue
		}
		if isStillRequiredByLockedParent(name, lf, locked) {
			continue
		}

		if sk.Managed {
			result.RemovedFromLock = append(result.RemovedFromLock, name)
			if !prune || check {
				continue
			}
			if err := s.Uninstall(ctx, skills.UninstallOptions{
				Name: name, Scope: skills.ScopeProject, ProjectRoot: projectRoot,
			}); err != nil && !errors.Is(err, storage.ErrNotFound) {
				result.Failed = append(result.Failed, skills.SyncFailure{
					Name: name, Reason: classifySyncError(err), Error: err.Error(),
				})
				continue
			}
			result.Pruned = append(result.Pruned, name)
			continue
		}
		result.NeverManaged = append(result.NeverManaged, name)
	}
	return nil
}

func isStillRequiredByLockedParent(name string, lf *lockfile.Lockfile, locked map[string]struct{}) bool {
	for _, entry := range lf.Skills {
		if entry.Name != name {
			continue
		}
		for _, parent := range entry.RequiredBy {
			if _, ok := locked[parent]; ok {
				return true
			}
		}
	}
	return false
}

func (s *service) syncAdopt(
	ctx context.Context, projectRoot string, lf *lockfile.Lockfile, clients []string, result *skills.SyncResult,
) error {
	installed, err := s.store.List(ctx, storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	if err != nil {
		return fmt.Errorf("listing installed skills: %w", err)
	}

	clientTypes, err := s.resolveClientTypes(clients)
	if err != nil {
		return err
	}

	for _, sk := range installed {
		name := sk.Metadata.Name
		if _, exists := lf.Get(name); exists {
			continue
		}

		digests, digestErr := s.contentDigestsForClients(name, skills.ScopeProject, projectRoot, clientTypes)
		if digestErr != nil {
			result.Failed = append(result.Failed, skills.SyncFailure{
				Name: name, Reason: classifySyncError(digestErr), Error: digestErr.Error(),
			})
			continue
		}
		if len(digests) == 0 {
			result.Failed = append(result.Failed, skills.SyncFailure{
				Name: name, Reason: skills.FailureReasonContentMismatch,
				Error: fmt.Sprintf("no on-disk files found for %q", name),
			})
			continue
		}
		first := digests[0]
		for _, d := range digests[1:] {
			if d != first {
				result.Failed = append(result.Failed, skills.SyncFailure{
					Name: name, Reason: skills.FailureReasonContentMismatch,
					Error: fmt.Sprintf("client directories disagree on content for %q; reinstall to reconcile", name),
				})
				first = ""
				break
			}
		}
		if first == "" {
			continue
		}

		entry := lockfile.Entry{
			Name:              name,
			Version:           sk.Metadata.Version,
			Source:            name,
			ResolvedReference: sk.Reference,
			Digest:            sk.Digest,
			ContentDigest:     first,
		}
		if lockErr := lockfile.UpsertEntry(projectRoot, entry); lockErr != nil {
			result.Failed = append(result.Failed, skills.SyncFailure{
				Name: name, Reason: skills.FailureReasonLockWriteFailed, Error: lockErr.Error(),
			})
			continue
		}
		result.Installed = append(result.Installed, name)
	}
	return nil
}

func (s *service) resolveClientTypes(clients []string) ([]string, error) {
	if len(clients) > 0 {
		return clients, nil
	}
	if s.pathResolver == nil {
		return nil, errors.New("path resolver is not configured")
	}
	return s.pathResolver.ListSkillSupportingClients(), nil
}

func classifySyncError(err error) skills.FailureReason {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "lock NOT updated"), strings.Contains(msg, "lock file"):
		return skills.FailureReasonLockWriteFailed
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "registry"), strings.Contains(msg, "pulling"):
		return skills.FailureReasonRegistryUnreachable
	case strings.Contains(msg, "digest"), strings.Contains(msg, "content"):
		return skills.FailureReasonDigestMissing
	case strings.Contains(msg, "invalid"), strings.Contains(msg, "validation"):
		return skills.FailureReasonValidationRejected
	default:
		return skills.FailureReasonUnknown
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/storage"
)

// installFromGit clones a git repository, extracts the skill, writes files to
// disk, creates a DB record, and completes group/lock bookkeeping while the
// caller-held lock remains held. The digest is the git commit hash, enabling
// same-commit no-op and upgrade detection.
//
// When alreadyLocked is false (user-scope), the per-skill lock is acquired
// after the canonical name is known and held through installAndRegister.
// When alreadyLocked is true (project transaction), no per-skill lock is
// taken — the project tx is the serialization boundary.
func (s *service) installFromGit(
	ctx context.Context,
	opts *skills.InstallOptions,
	scope skills.Scope,
	originalName string,
	deps *depState,
	alreadyLocked bool,
) (*skills.InstallResult, error) {
	if s.gitResolver == nil {
		return nil, httperr.WithCode(
			errors.New("git resolver is not configured"),
			http.StatusInternalServerError,
		)
	}
	if s.pathResolver == nil {
		return nil, httperr.WithCode(
			errors.New("path resolver is required for git installs"),
			http.StatusInternalServerError,
		)
	}

	// Parse the git:// reference.
	gitRef, err := gitresolver.ParseGitReference(opts.Name)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid git reference: %w", err),
			http.StatusBadRequest,
		)
	}

	// Preserve the original git:// URL for provenance tracking.
	gitURL := opts.Name

	// Clone, read SKILL.md, collect files.
	resolved, err := s.gitResolver.Resolve(ctx, gitRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("resolving git skill: %w", err),
			http.StatusBadGateway,
		)
	}

	if err := skills.ValidateSkillName(resolved.SkillConfig.Name); err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("skill contains invalid name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	// Hydrate install options from the git result.
	opts.Name = resolved.SkillConfig.Name
	opts.Reference = gitURL
	opts.Digest = resolved.CommitHash

	if opts.Version == "" && resolved.SkillConfig.Version != "" {
		opts.Version = resolved.SkillConfig.Version
	}

	if err := validateExpectedCanonicalName(*opts); err != nil {
		return nil, err
	}

	if deps != nil {
		if deps.alreadyDone(opts.Name) {
			return s.mergeRequiredByOnly(ctx, *opts, opts.Name, scope)
		}
		if err := deps.enter(opts.Name); err != nil {
			return nil, err
		}
		defer deps.leave(opts.Name)
	}

	if !alreadyLocked {
		unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
		defer unlock()
	}

	// Verify the commit signature before anything is written or recorded.
	// This runs under the held lock so concurrent first installs cannot
	// both read an absent lock entry and race their TOFU anchors.
	if shouldVerifyInstall(*opts, scope) {
		decision, verifyErr := s.verifyGitInstall(
			ctx, *opts, resolved.SkillConfig.Name, resolved.CommitPayload, resolved.CommitSignature,
		)
		if verifyErr != nil {
			return nil, verifyErr
		}
		applyDecisionToOpts(opts, decision)
	}

	clientTypes, clientDirs, err := s.resolveAndValidateClients(*opts, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}

	result, err := s.applyGitInstall(ctx, *opts, scope, clientTypes, clientDirs, resolved.Files)
	if err != nil {
		return nil, err
	}
	return s.installAndRegister(ctx, *opts, originalName, result, opts.Group, result.Skill.Metadata.Name, scope, deps)
}

// applyGitInstall handles the create/upgrade/no-op logic for a git-based skill
// install. It checks the store for an existing record, writes files, and
// persists the result.
func (s *service) applyGitInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientTypes []string,
	clientDirs map[string]string,
	files []gitresolver.FileEntry,
) (*skills.InstallResult, error) {
	existing, storeErr := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	isNotFound := errors.Is(storeErr, storage.ErrNotFound)
	if storeErr != nil && !isNotFound {
		return nil, fmt.Errorf("checking existing skill: %w", storeErr)
	}
	if !isNotFound {
		result, err := s.applyGitInstallExisting(ctx, opts, scope, existing, clientTypes, clientDirs, files)
		if err == nil {
			// Preserve the pre-install record so a later rollback (e.g. a
			// failed dependency materialization) can restore it rather than
			// delete it.
			pre := existing
			result.PreExisting = &pre
		}
		return result, err
	}
	return s.applyGitInstallFresh(ctx, opts, scope, clientTypes, clientDirs, files)
}

func (s *service) applyGitInstallExisting(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	existing skills.InstalledSkill,
	clientTypes []string,
	clientDirs map[string]string,
	files []gitresolver.FileEntry,
) (*skills.InstallResult, error) {
	// SyncRestore forces the same full re-extraction path as a digest
	// change: sync repairs on-disk drift that happened without the pinned
	// digest changing, so the "same digest means content is already
	// correct" no-op/skip branches below must not apply.
	if existing.Digest != opts.Digest || opts.SyncRestore {
		allClients, allDirs, err := s.expandToExistingClients(
			existing.Clients, clientTypes, clientDirs, opts.Name, scope, opts.ProjectRoot)
		if err != nil {
			return nil, err
		}
		// Deduplicate so clients sharing the same directory don't conflict.
		dirsToWrite := uniqueDirClients(allClients, allDirs, nil)
		return s.gitWriteMultiAndPersist(ctx, opts, scope, allClients, allDirs, files,
			dirsToWrite, nil, true, true)
	}
	clientsExplicit := len(opts.Clients) > 0
	if clientsContainAll(existing.Clients, clientTypes) ||
		(len(existing.Clients) == 0 && len(clientTypes) <= 1 && !clientsExplicit) {
		return &skills.InstallResult{Skill: existing}, nil
	}
	toWrite := missingClients(existing.Clients, clientTypes)
	if len(toWrite) == 0 {
		return &skills.InstallResult{Skill: existing}, nil
	}
	// Deduplicate and skip directories already owned by existing clients.
	dirsToWrite := uniqueDirClients(toWrite, clientDirs, existingClientDirs(existing.Clients, clientDirs))
	if len(dirsToWrite) == 0 {
		return s.gitWriteMultiAndPersist(ctx, opts, scope, clientTypes, clientDirs, files,
			nil, existing.Clients, true, false)
	}
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if _, statErr := os.Stat(dir); statErr == nil && !opts.Force { // lgtm[go/path-injection]
			return nil, httperr.WithCode(
				fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", dir),
				http.StatusConflict,
			)
		}
	}
	return s.gitWriteMultiAndPersist(ctx, opts, scope, clientTypes, clientDirs, files,
		dirsToWrite, existing.Clients, true, false)
}

func (s *service) applyGitInstallFresh(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientTypes []string,
	clientDirs map[string]string,
	files []gitresolver.FileEntry,
) (*skills.InstallResult, error) {
	// Deduplicate so clients sharing the same directory don't conflict.
	dirsToCheck := uniqueDirClients(clientTypes, clientDirs, nil)
	for _, ct := range dirsToCheck {
		dir := filepath.Clean(clientDirs[ct])
		if _, statErr := os.Stat(dir); statErr == nil && !opts.Force { // lgtm[go/path-injection]
			return nil, httperr.WithCode(
				fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", dir),
				http.StatusConflict,
			)
		}
	}
	return s.gitWriteMultiAndPersist(ctx, opts, scope, clientTypes, clientDirs, files,
		dirsToCheck, nil, false, false)
}

// gitWriteMultiAndPersist writes git files to the given client directories,
// verifies each tree, then creates or updates the store record. On failure
// after any write, previously written directories in this call are removed.
// When overwriting existing content (upgrade or force), trees are
// snapshotted first so installAndRegister rollback can restore them.
func (s *service) gitWriteMultiAndPersist(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	allRequested []string,
	clientDirs map[string]string,
	files []gitresolver.FileEntry,
	dirsToWrite []string,
	existingClients []string,
	isUpgrade, writeAggressive bool,
) (*skills.InstallResult, error) {
	var snapshotTargets []string
	for _, ct := range dirsToWrite {
		snapshotTargets = append(snapshotTargets, filepath.Clean(clientDirs[ct]))
	}
	var backups map[string]string
	if isUpgrade || opts.Force {
		var snapErr error
		backups, snapErr = snapshotDirs(snapshotTargets)
		if snapErr != nil {
			return nil, fmt.Errorf("snapshotting skill trees before write: %w", snapErr)
		}
	}

	var written []string
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		writeMode := opts.Force
		if writeAggressive {
			writeMode = true
		}
		if writeErr := gitresolver.WriteFiles(files, dir, writeMode); writeErr != nil {
			for _, wct := range written {
				_ = s.installer.Remove(filepath.Clean(clientDirs[wct]))
			}
			_ = restoreDirs(backups)
			return nil, fmt.Errorf("writing git skill: %w", writeErr)
		}
		if checkErr := skills.CheckFilesystem(dir); checkErr != nil {
			_ = s.installer.Remove(dir)
			for _, wct := range written {
				_ = s.installer.Remove(filepath.Clean(clientDirs[wct]))
			}
			_ = restoreDirs(backups)
			return nil, fmt.Errorf("post-extraction verification failed: %w", checkErr)
		}
		written = append(written, ct)
	}

	sk := buildInstalledSkill(opts, scope, allRequested, existingClients)
	if isUpgrade {
		if err := s.store.Update(ctx, sk); err != nil {
			for _, wct := range written {
				_ = s.installer.Remove(filepath.Clean(clientDirs[wct]))
			}
			_ = restoreDirs(backups)
			return nil, err
		}
	} else {
		if err := s.store.Create(ctx, sk); err != nil {
			for _, wct := range written {
				_ = s.installer.Remove(filepath.Clean(clientDirs[wct]))
			}
			_ = restoreDirs(backups)
			return nil, err
		}
	}
	result := &skills.InstallResult{Skill: sk}
	if len(backups) > 0 {
		result.RestoreFiles = func() error { return restoreDirs(backups) }
	}
	return result, nil
}

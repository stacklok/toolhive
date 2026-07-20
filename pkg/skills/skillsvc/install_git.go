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
// disk, and creates a DB record. The digest is the git commit hash, enabling
// same-commit no-op and upgrade detection.
func (s *service) installFromGit(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
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

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	clientTypes, clientDirs, err := s.resolveAndValidateClients(opts, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}

	return s.applyGitInstall(ctx, opts, scope, clientTypes, clientDirs, resolved.Files)
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
		return s.applyGitInstallExisting(ctx, opts, scope, existing, clientTypes, clientDirs, files)
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
	if existing.Digest != opts.Digest {
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
			return nil, fmt.Errorf("writing git skill: %w", writeErr)
		}
		if checkErr := skills.CheckFilesystem(dir); checkErr != nil {
			_ = s.installer.Remove(dir)
			for _, wct := range written {
				_ = s.installer.Remove(filepath.Clean(clientDirs[wct]))
			}
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
			return nil, err
		}
	} else {
		if err := s.store.Create(ctx, sk); err != nil {
			for _, wct := range written {
				_ = s.installer.Remove(filepath.Clean(clientDirs[wct]))
			}
			return nil, err
		}
	}
	return &skills.InstallResult{Skill: sk}, nil
}

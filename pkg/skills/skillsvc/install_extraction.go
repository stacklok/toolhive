// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

// installWithExtraction handles the full install flow: managed/unmanaged
// detection, extraction, and DB record creation or update.
func (s *service) installWithExtraction(
	ctx context.Context, opts skills.InstallOptions, scope skills.Scope,
) (*skills.InstallResult, error) {
	clientTypes, clientDirs, err := s.resolveAndValidateClients(opts, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}

	existing, storeErr := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	isNotFound := errors.Is(storeErr, storage.ErrNotFound)
	if storeErr != nil && !isNotFound {
		return nil, fmt.Errorf("checking existing skill: %w", storeErr)
	}

	result, err := s.dispatchExtraction(ctx, opts, scope, existing, storeErr, clientTypes, clientDirs)
	if err == nil && storeErr == nil {
		// Preserve the pre-install record so a later rollback (e.g. a failed
		// dependency materialization) can restore it rather than delete it.
		pre := existing
		result.PreExisting = &pre
	}
	return result, err
}

// dispatchExtraction routes an extraction-based install to the no-op,
// same-digest, upgrade, or fresh path based on the pre-install store state.
func (s *service) dispatchExtraction(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	existing skills.InstalledSkill,
	storeErr error,
	clientTypes []string,
	clientDirs map[string]string,
) (*skills.InstallResult, error) {
	if !opts.SyncRestore && isExtractionNoOp(existing, storeErr, opts, clientTypes) {
		return &skills.InstallResult{Skill: existing}, nil
	}

	digestMatches := storeErr == nil && existing.Digest == opts.Digest
	if digestMatches && storeErr == nil && !opts.SyncRestore {
		return s.installExtractionSameDigestNewClients(ctx, opts, scope, existing, clientTypes, clientDirs)
	}

	if storeErr == nil {
		return s.installExtractionUpgradeDigest(ctx, opts, scope, existing, clientTypes, clientDirs)
	}

	return s.installExtractionFresh(ctx, opts, scope, clientTypes, clientDirs)
}

// isExtractionNoOp reports whether the install can be short-circuited because
// the same digest and all requested clients are already present. Legacy store
// rows (empty Clients slice) are treated as satisfied only when the user did
// not explicitly specify --clients.
func isExtractionNoOp(existing skills.InstalledSkill, storeErr error, opts skills.InstallOptions, clientTypes []string) bool {
	if storeErr != nil || existing.Digest != opts.Digest {
		return false
	}
	if clientsContainAll(existing.Clients, clientTypes) {
		return true
	}
	return len(existing.Clients) == 0 && len(clientTypes) <= 1 && len(opts.Clients) == 0
}

func (s *service) installExtractionSameDigestNewClients(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	existing skills.InstalledSkill,
	clientTypes []string,
	clientDirs map[string]string,
) (*skills.InstallResult, error) {
	toWrite := missingClients(existing.Clients, clientTypes)
	if len(toWrite) == 0 {
		return &skills.InstallResult{Skill: existing}, nil
	}
	// Deduplicate and skip directories already owned by existing clients.
	dirsToWrite := uniqueDirClients(toWrite, clientDirs, existingClientDirs(existing.Clients, clientDirs))
	if len(dirsToWrite) == 0 {
		// All new clients share directories with existing ones — no-op.
		sk := buildInstalledSkill(opts, scope, clientTypes, existing.Clients)
		if err := s.store.Update(ctx, sk); err != nil {
			return nil, err
		}
		return &skills.InstallResult{Skill: sk}, nil
	}
	targets := cleanedDirs(dirsToWrite, clientDirs)
	backups, snapErr := snapshotTargets(targets)
	if snapErr != nil {
		return nil, fmt.Errorf("snapshotting skill trees before install: %w", snapErr)
	}
	restore := func() error { return restoreTargets(backups) }
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if backups[dir].existed && !opts.Force {
			return nil, errors.Join(
				httperr.WithCode(
					fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", dir),
					http.StatusConflict,
				),
				restore(),
			)
		}
		if _, exErr := s.installer.Extract(opts.LayerData, dir, opts.Force); exErr != nil {
			return nil, errors.Join(fmt.Errorf("extracting skill: %w", exErr), restore())
		}
	}
	sk := buildInstalledSkill(opts, scope, clientTypes, existing.Clients)
	if err := s.store.Update(ctx, sk); err != nil {
		return nil, errors.Join(err, restore())
	}
	return &skills.InstallResult{Skill: sk, RestoreFiles: restore}, nil
}

// cleanedDirs maps client types to their cleaned target directories.
func cleanedDirs(clients []string, clientDirs map[string]string) []string {
	out := make([]string, 0, len(clients))
	for _, ct := range clients {
		out = append(out, filepath.Clean(clientDirs[ct]))
	}
	return out
}

func (s *service) installExtractionUpgradeDigest(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	existing skills.InstalledSkill,
	clientTypes []string,
	clientDirs map[string]string,
) (*skills.InstallResult, error) {
	allClients, allDirs, err := s.expandToExistingClients(
		existing.Clients, clientTypes, clientDirs, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	// Deduplicate so clients sharing the same directory don't conflict.
	dirsToWrite := uniqueDirClients(allClients, allDirs, nil)
	backups, snapErr := snapshotTargets(cleanedDirs(dirsToWrite, allDirs))
	if snapErr != nil {
		return nil, fmt.Errorf("snapshotting skill trees before upgrade: %w", snapErr)
	}
	restore := func() error { return restoreTargets(backups) }
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(allDirs[ct])
		if _, exErr := s.installer.Extract(opts.LayerData, dir, true); exErr != nil {
			return nil, errors.Join(fmt.Errorf("extracting skill upgrade: %w", exErr), restore())
		}
	}
	sk := buildInstalledSkill(opts, scope, allClients, nil)
	if err := s.store.Update(ctx, sk); err != nil {
		return nil, errors.Join(err, restore())
	}
	return &skills.InstallResult{Skill: sk, RestoreFiles: restore}, nil
}

func (s *service) installExtractionFresh(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientTypes []string,
	clientDirs map[string]string,
) (*skills.InstallResult, error) {
	// Deduplicate so clients sharing the same directory don't conflict.
	dirsToWrite := uniqueDirClients(clientTypes, clientDirs, nil)

	backups, snapErr := snapshotTargets(cleanedDirs(dirsToWrite, clientDirs))
	if snapErr != nil {
		return nil, fmt.Errorf("snapshotting skill trees before install: %w", snapErr)
	}
	restore := func() error { return restoreTargets(backups) }
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if backups[dir].existed && !opts.Force {
			return nil, httperr.WithCode(
				fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", dir),
				http.StatusConflict,
			)
		}
	}
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if _, exErr := s.installer.Extract(opts.LayerData, dir, opts.Force); exErr != nil {
			return nil, errors.Join(fmt.Errorf("extracting skill: %w", exErr), restore())
		}
	}
	sk := buildInstalledSkill(opts, scope, clientTypes, nil)
	if err := s.store.Create(ctx, sk); err != nil {
		return nil, errors.Join(err, restore())
	}
	return &skills.InstallResult{Skill: sk, RestoreFiles: restore}, nil
}

// buildInstalledSkill constructs an InstalledSkill from install options.
// requestedClientTypes is the set of clients targeted by this install; they
// are merged with existingClients for the persisted Clients field.
func buildInstalledSkill(
	opts skills.InstallOptions,
	scope skills.Scope,
	requestedClientTypes []string,
	existingClients []string,
) skills.InstalledSkill {
	clients := mergeClientLists(existingClients, requestedClientTypes)

	return skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:    opts.Name,
			Version: opts.Version,
		},
		Scope:          scope,
		ProjectRoot:    opts.ProjectRoot,
		Reference:      opts.Reference,
		Digest:         opts.Digest,
		Status:         skills.InstallStatusInstalled,
		InstalledAt:    time.Now().UTC(),
		Clients:        clients,
		SigstoreBundle: opts.SigstoreBundle,
	}
}

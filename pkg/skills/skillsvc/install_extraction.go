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

	if isExtractionNoOp(existing, storeErr, opts, clientTypes) {
		return &skills.InstallResult{Skill: existing}, nil
	}

	digestMatches := storeErr == nil && existing.Digest == opts.Digest
	if digestMatches && storeErr == nil {
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
	var written []string
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if _, statErr := os.Stat(dir); statErr == nil && !opts.Force { // lgtm[go/path-injection]
			removeSkillDirs(s.installer, clientDirs, written)
			return nil, httperr.WithCode(
				fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", dir),
				http.StatusConflict,
			)
		}
		if _, exErr := s.installer.Extract(opts.LayerData, dir, opts.Force); exErr != nil {
			removeSkillDirs(s.installer, clientDirs, written)
			return nil, fmt.Errorf("extracting skill: %w", exErr)
		}
		written = append(written, ct)
	}
	sk := buildInstalledSkill(opts, scope, clientTypes, existing.Clients)
	if err := s.store.Update(ctx, sk); err != nil {
		removeSkillDirs(s.installer, clientDirs, written)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

func removeSkillDirs(inst skills.Installer, clientDirs map[string]string, clients []string) {
	for _, ct := range clients {
		_ = inst.Remove(filepath.Clean(clientDirs[ct]))
	}
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
	var written []string
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(allDirs[ct])
		if _, exErr := s.installer.Extract(opts.LayerData, dir, true); exErr != nil {
			removeSkillDirs(s.installer, allDirs, written)
			return nil, fmt.Errorf("extracting skill upgrade: %w", exErr)
		}
		written = append(written, ct)
	}
	sk := buildInstalledSkill(opts, scope, allClients, nil)
	if err := s.store.Update(ctx, sk); err != nil {
		removeSkillDirs(s.installer, allDirs, dirsToWrite)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
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

	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if _, statErr := os.Stat(dir); statErr == nil && !opts.Force { // lgtm[go/path-injection]
			return nil, httperr.WithCode(
				fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", dir),
				http.StatusConflict,
			)
		}
	}
	var written []string
	for _, ct := range dirsToWrite {
		dir := filepath.Clean(clientDirs[ct])
		if _, exErr := s.installer.Extract(opts.LayerData, dir, opts.Force); exErr != nil {
			removeSkillDirs(s.installer, clientDirs, written)
			return nil, fmt.Errorf("extracting skill: %w", exErr)
		}
		written = append(written, ct)
	}
	sk := buildInstalledSkill(opts, scope, clientTypes, nil)
	if err := s.store.Create(ctx, sk); err != nil {
		removeSkillDirs(s.installer, clientDirs, dirsToWrite)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
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
		Scope:       scope,
		ProjectRoot: opts.ProjectRoot,
		Reference:   opts.Reference,
		Digest:      opts.Digest,
		Status:      skills.InstallStatusInstalled,
		InstalledAt: time.Now().UTC(),
		Clients:     clients,
	}
}

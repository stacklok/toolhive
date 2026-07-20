// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// Install installs a skill. When the Name field contains an OCI reference
// (detected by the presence of '/', ':', or '@'), the artifact is pulled from
// the registry and extracted. When LayerData is provided, the skill is extracted
// to disk and a full installation record is created. Without LayerData, a
// pending record is created.
func (s *service) Install(ctx context.Context, opts skills.InstallOptions) (*skills.InstallResult, error) {
	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)
	// Canonicalize the project root so that equivalent paths produce
	// the same lock key and DB record.
	opts.ProjectRoot = projectRoot

	// Git references (git://host/owner/repo[@ref][#path]) are dispatched first;
	// the prefix is unambiguous and cannot collide with OCI references.
	if gitresolver.IsGitReference(opts.Name) {
		result, err := s.installFromGit(ctx, opts, scope)
		if err != nil {
			return nil, err
		}
		return s.installAndRegister(ctx, result, opts.Group, result.Skill.Metadata.Name, scope, opts.ProjectRoot)
	}

	// When the caller supplies `version` separately and the name is a tag-less
	// OCI-like reference (contains '/' but no ':' or '@'), splice the version
	// in as the tag. Without this, parseOCIReference + qualifiedOCIRef would
	// default the pull to ":latest" and silently drop opts.Version. An
	// explicit tag in the name still wins (we only splice when none is set).
	if opts.Version != "" &&
		strings.ContainsRune(opts.Name, '/') &&
		!strings.ContainsAny(opts.Name, ":@") {
		opts.Name = opts.Name + ":" + opts.Version
	}

	ref, isOCI, err := parseOCIReference(opts.Name)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid OCI reference %q: %w", opts.Name, err),
			http.StatusBadRequest,
		)
	}
	if isOCI {
		result, ociErr := s.installFromOCI(ctx, opts, scope, ref)
		if ociErr == nil {
			return s.installAndRegister(ctx, result, opts.Group, opts.Name, scope, opts.ProjectRoot)
		}
		// OCI pull failed — fall back to registry lookup for names that look
		// like a qualified "namespace/name". Names that are unambiguously OCI
		// (digest, explicit tag, or multi-segment path) must not trigger a
		// registry search. See isUnambiguousOCIRef for the full rule set.
		if isUnambiguousOCIRef(opts.Name, ref) {
			return nil, ociErr
		}
		slog.Debug("OCI pull failed, attempting registry fallback", "name", opts.Name, "error", ociErr)
		resolved, regErr := s.resolveFromRegistry(opts.Name)
		if regErr != nil {
			return nil, regErr
		}
		if resolved != nil {
			return s.installFromResolvedRegistry(ctx, opts, scope, resolved)
		}
		return nil, ociErr
	}

	// Plain skill name — validate and proceed with existing flow.
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	return s.installByName(ctx, opts, scope)
}

// installByName handles installation for a validated plain skill name. It
// checks the local OCI store and registry before falling back to an error.
func (s *service) installByName(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

	// Without layer data, check the local OCI store for a matching tag,
	// then the registry/index, before returning an error.
	if len(opts.LayerData) == 0 {
		resolved := false
		if s.ociStore != nil {
			var resolveErr error
			// Pass pointer to hydrate opts with layer data, digest, and version.
			resolved, resolveErr = s.resolveFromLocalStore(ctx, &opts)
			if resolveErr != nil {
				return nil, resolveErr
			}
		}
		if !resolved {
			// Release lock before registry lookup -- installFromOCI
			// acquires its own lock on the artifact's skill name, which
			// could be the same key, causing deadlock since sync.Mutex
			// is not re-entrant.
			unlock()
			locked = false

			return s.installFromRegistryLookup(ctx, opts, scope)
		}
		// resolved: opts hydrated, fall through to installWithExtraction
	}

	result, err := s.installWithExtraction(ctx, opts, scope)
	if err != nil {
		return nil, err
	}
	return s.installAndRegister(ctx, result, opts.Group, opts.Name, scope, opts.ProjectRoot)
}

// installFromRegistryLookup resolves a plain skill name via the registry and
// dispatches to the appropriate installer (OCI or git).
func (s *service) installFromRegistryLookup(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	resolved, regErr := s.resolveFromRegistry(opts.Name)
	if regErr != nil {
		return nil, regErr
	}
	if resolved != nil {
		return s.installFromResolvedRegistry(ctx, opts, scope, resolved)
	}

	return nil, httperr.WithCode(
		fmt.Errorf("skill %q not found in local store or registry;"+
			" install by OCI reference:\n  thv skill install ghcr.io/<namespace>/%s:<version>",
			opts.Name, opts.Name),
		http.StatusNotFound,
	)
}

// installFromResolvedRegistry dispatches an install to the appropriate
// backend (OCI or git) based on the result of a registry lookup.
func (s *service) installFromResolvedRegistry(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	resolved *registryResolveResult,
) (*skills.InstallResult, error) {
	switch {
	case resolved.OCIRef != nil:
		slog.Info("resolved skill from registry (OCI)", "name", opts.Name, "oci_reference", resolved.OCIRef.String())
		opts.Name = resolved.OCIRef.String()
		result, ociErr := s.installFromOCI(ctx, opts, scope, resolved.OCIRef)
		if ociErr != nil {
			return nil, ociErr
		}
		// Use the skill name extracted from the artifact, not opts.Name which
		// holds the OCI ref string. installFromOCI mutates its own copy of opts
		// (Go pass-by-value), so the caller never sees the updated name.
		return s.installAndRegister(ctx, result, opts.Group, result.Skill.Metadata.Name, scope, opts.ProjectRoot)
	case resolved.GitURL != "":
		slog.Info("resolved skill from registry (git)", "name", opts.Name, "git_url", resolved.GitURL)
		opts.Name = resolved.GitURL
		result, gitErr := s.installFromGit(ctx, opts, scope)
		if gitErr != nil {
			return nil, gitErr
		}
		return s.installAndRegister(ctx, result, opts.Group, result.Skill.Metadata.Name, scope, opts.ProjectRoot)
	}
	return nil, httperr.WithCode(
		fmt.Errorf("skill %q resolved from registry but has no installable package", opts.Name),
		http.StatusUnprocessableEntity,
	)
}

// registerSkillInGroup adds the skill to the requested group when a group
// manager is configured. When groupName is empty it defaults to the
// "default" group, matching workload behavior.
func (s *service) registerSkillInGroup(ctx context.Context, groupName string, skillName string) error {
	if s.groupManager == nil {
		return nil
	}
	if groupName == "" {
		groupName = groups.DefaultGroup
	}
	return groups.AddSkillToGroup(ctx, s.groupManager, groupName, skillName)
}

// installAndRegister registers the just-installed skill in the target group.
// If group registration fails, the DB record is rolled back so that a retry
// starts fresh rather than leaving the system in an inconsistent state (skill
// installed but not in the expected group).
func (s *service) installAndRegister(
	ctx context.Context,
	result *skills.InstallResult,
	groupName string,
	skillName string,
	scope skills.Scope,
	projectRoot string,
) (*skills.InstallResult, error) {
	if err := s.registerSkillInGroup(ctx, groupName, skillName); err != nil {
		// Best-effort rollback: remove the DB record so retries start fresh.
		// Files on disk are left in place; a fresh install will detect them
		// and either overwrite (force) or return a conflict.
		_ = s.store.Delete(ctx, skillName, scope, projectRoot)
		return nil, fmt.Errorf("registering skill in group: %w", err)
	}
	return result, nil
}

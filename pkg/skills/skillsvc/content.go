// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// GetContent retrieves the SKILL.md body and file listing from a skill artifact
// without installing it. The reference may be:
//   - A local build tag (e.g. "my-skill")
//   - A fully-qualified OCI reference (e.g. "ghcr.io/org/skill:v1")
//   - A git:// reference (e.g. "git://github.com/org/repo#path/to/skill")
//   - An https:// URL (converted to git:// internally)
//
// Resolution order: git (git:// and https://) → OCI (local store, then remote
// pull) → registry catalog lookup.
func (s *service) GetContent(ctx context.Context, opts skills.ContentOptions) (*skills.SkillContent, error) {
	ref := opts.Reference
	if ref == "" {
		return nil, httperr.WithCode(
			errors.New("reference is required"),
			http.StatusBadRequest,
		)
	}

	// Git references (git:// or https://) are dispatched first since their
	// scheme prefix is unambiguous and cannot collide with OCI references.
	if gitresolver.IsGitReference(ref) {
		return s.getContentFromGit(ctx, ref)
	}
	if isHTTPURL(ref) {
		gitURL, err := buildGitReferenceFromRegistryURL(ref)
		if err != nil {
			return nil, httperr.WithCode(
				fmt.Errorf("invalid URL %q: %w", ref, err),
				http.StatusBadRequest,
			)
		}
		return s.getContentFromGit(ctx, gitURL)
	}

	// Try OCI resolution (local store + remote pull). If this succeeds, return.
	content, ociErr := s.getContentFromOCI(ctx, ref)
	if ociErr == nil {
		return content, nil
	}

	// OCI failed. The fallback strategy depends on how the caller referenced
	// the skill:
	//
	//   - Unambiguous OCI refs (tag/digest/multi-segment path) — try a
	//     registry lookup *by OCI identifier* to find the same skill's git
	//     package, so an ephemeral OCI outage can transparently fall back to
	//     git when the registry catalog has both.
	//   - Ambiguous refs (plain skill name) — use the existing name-based
	//     registry resolution which preferred OCI → git.
	if parsedRef, isOCI, parseErr := parseOCIReference(ref); parseErr == nil && isOCI &&
		isUnambiguousOCIRef(ref, parsedRef) {
		if gitURL := s.resolveGitFallbackForOCIRef(parsedRef); gitURL != "" {
			slog.Info(
				"OCI content fetch failed; falling back to git package declared in registry entry",
				"oci_ref", ref,
				"git_url", gitURL,
				"oci_error", ociErr,
			)
			c, gitErr := s.getContentFromGit(ctx, gitURL)
			if gitErr == nil {
				return c, nil
			}
			return nil, fmt.Errorf(
				"OCI pull failed (%w); registry git fallback also failed: %v",
				ociErr, gitErr,
			)
		}
		return nil, ociErr
	}

	resolved, regErr := s.resolveFromRegistry(ref)
	if regErr != nil {
		return nil, regErr
	}
	if resolved != nil {
		switch {
		case resolved.OCIRef != nil:
			return s.getContentFromOCI(ctx, resolved.OCIRef.String())
		case resolved.GitURL != "":
			return s.getContentFromGit(ctx, resolved.GitURL)
		}
	}

	// Nothing matched — return the original OCI error.
	return nil, ociErr
}

// getContentFromGit clones a git repository and extracts the SKILL.md content.
func (s *service) getContentFromGit(ctx context.Context, ref string) (*skills.SkillContent, error) {
	if s.gitResolver == nil {
		return nil, httperr.WithCode(
			errors.New("git resolver is not configured"),
			http.StatusInternalServerError,
		)
	}

	gitRef, err := gitresolver.ParseGitReference(ref)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid git reference: %w", err),
			http.StatusBadRequest,
		)
	}

	resolved, err := s.gitResolver.Resolve(ctx, gitRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("resolving git skill: %w", err),
			http.StatusBadGateway,
		)
	}

	content := &skills.SkillContent{
		Name:        resolved.SkillConfig.Name,
		Description: resolved.SkillConfig.Description,
		Version:     resolved.SkillConfig.Version,
		License:     resolved.SkillConfig.License,
		Body:        string(resolved.SkillConfig.Body),
		Files:       make([]skills.SkillFileEntry, 0, len(resolved.Files)),
	}

	for _, f := range resolved.Files {
		content.Files = append(content.Files, skills.SkillFileEntry{
			Path: f.Path,
			Size: len(f.Content),
		})
	}

	return content, nil
}

// getContentFromOCI resolves a reference from the local OCI store or pulls it
// from a remote registry, then extracts the SKILL.md content.
func (s *service) getContentFromOCI(ctx context.Context, ref string) (*skills.SkillContent, error) {
	if s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI store is not configured"),
			http.StatusInternalServerError,
		)
	}

	// Try the local store first (covers local builds by tag name and
	// previously pulled remote refs tagged by Pull).
	d, resolveErr := s.ociStore.Resolve(ctx, ref)
	if resolveErr != nil {
		if s.registry == nil {
			return nil, httperr.WithCode(
				fmt.Errorf("reference %q not found in local store and OCI registry is not configured", ref),
				http.StatusBadRequest,
			)
		}

		ociRef, isOCI, parseErr := parseOCIReference(ref)
		if parseErr != nil {
			return nil, httperr.WithCode(
				fmt.Errorf("invalid reference %q: %w", ref, parseErr),
				http.StatusBadRequest,
			)
		}
		if !isOCI {
			return nil, httperr.WithCode(
				fmt.Errorf("reference %q not found in local store and is not a valid OCI reference", ref),
				http.StatusBadRequest,
			)
		}

		qualifiedRef := qualifiedOCIRef(ociRef)
		pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
		defer cancel()

		// Content-preview pulls intentionally do NOT carry the local-build
		// marker: Registry.Pull tags by digest, which returns a plain
		// descriptor from the OCI store, so no annotations land on the
		// root-index entry. The pulled blobs stay in the OCI store as a
		// cache, but the tag is invisible to ListBuilds so remote skills
		// browsed via the content API don't pollute the local builds listing.
		var pullErr error
		d, pullErr = s.registry.Pull(pullCtx, s.ociStore, qualifiedRef)
		if pullErr != nil {
			return nil, httperr.WithCode(
				fmt.Errorf("pulling OCI artifact %q: %w", qualifiedRef, pullErr),
				classifyPullError(pullErr),
			)
		}
	}

	layerData, skillConfig, err := s.extractOCIContent(ctx, d)
	if err != nil {
		return nil, err
	}

	entries, err := ociskills.DecompressTar(layerData)
	if err != nil {
		return nil, fmt.Errorf("decompressing skill layer: %w", err)
	}

	content := &skills.SkillContent{
		Name:        skillConfig.Name,
		Description: skillConfig.Description,
		Version:     skillConfig.Version,
		License:     skillConfig.License,
		Files:       make([]skills.SkillFileEntry, 0, len(entries)),
	}

	for _, entry := range entries {
		content.Files = append(content.Files, skills.SkillFileEntry{
			Path: entry.Path,
			Size: len(entry.Content),
		})
		if strings.EqualFold(filepath.Base(entry.Path), "SKILL.md") {
			content.Body = string(entry.Content)
		}
	}

	return content, nil
}

// isHTTPURL returns true if the reference starts with http:// or https://.
func isHTTPURL(ref string) bool {
	return strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://")
}

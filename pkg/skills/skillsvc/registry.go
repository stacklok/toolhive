// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// registryResolveResult holds the outcome of a registry skill name lookup.
// Exactly one of OCIRef or GitURL will be set.
type registryResolveResult struct {
	OCIRef nameref.Reference
	GitURL string // raw git:// URL for installFromGit
}

// resolveFromRegistry attempts to resolve a skill name by querying the
// configured skill registry/index. Accepts either a plain name ("skill-creator")
// or a qualified "namespace/name" ("io.github.stacklok/skill-creator").
// Returns (result, nil) on success, (nil, nil) when no match is found or no
// lookup is configured, or (nil, err) on ambiguity.
func (s *service) resolveFromRegistry(name string) (*registryResolveResult, error) {
	if s.skillLookup == nil {
		return nil, nil
	}

	// Split qualified "namespace/name" if present. Use the last segment as
	// the search query since SearchSkills matches on name substring.
	wantNamespace, searchName := splitQualifiedName(name)

	results, err := s.skillLookup.SearchSkills(searchName)
	if err != nil {
		slog.Warn("registry skill lookup failed, falling back to not-found", "name", name, "error", err)
		return nil, nil
	}

	// Filter for exact match. Case-insensitive because registry data
	// may not be normalized to lowercase even though local skill names are.
	var matches []regtypes.Skill
	for _, sk := range results {
		if !strings.EqualFold(sk.Name, searchName) {
			continue
		}
		if wantNamespace != "" && !strings.EqualFold(sk.Namespace, wantNamespace) {
			continue
		}
		matches = append(matches, sk)
	}

	if len(matches) == 0 {
		return nil, nil
	}

	if len(matches) > 1 {
		const maxCandidates = 5
		var candidates []string
		for _, sk := range matches {
			candidates = append(candidates, sk.Namespace+"/"+sk.Name)
		}
		suffix := ""
		if len(candidates) > maxCandidates {
			suffix = fmt.Sprintf(" and %d more", len(candidates)-maxCandidates)
			candidates = candidates[:maxCandidates]
		}
		return nil, httperr.WithCode(
			fmt.Errorf("ambiguous skill name %q matches multiple registry entries: %s%s; install by full OCI reference instead",
				name, strings.Join(candidates, ", "), suffix),
			http.StatusConflict,
		)
	}

	return resolveRegistryPackages(name, matches[0].Packages)
}

// splitQualifiedName splits "namespace/name" into (namespace, name).
// If the input has no "/" it returns ("", name) unchanged.
func splitQualifiedName(s string) (namespace, name string) {
	idx := strings.LastIndex(s, "/")
	if idx < 0 {
		return "", s
	}
	return s[:idx], s[idx+1:]
}

// resolveGitFallbackForOCIRef attempts to find a git:// URL in the skill
// registry that can serve as a fallback when an OCI pull failed for ref.
//
// The lookup is purely advisory: any error, ambiguity, or missing data is
// treated as "no fallback available" so the caller can simply return the
// original OCI error. Returning "" means "no fallback found".
//
// Matching strategy:
//   - Derive a search term from the ref's tail path segment (e.g.
//     "yara-rule-authoring" from "ghcr.io/stacklok/dockyard/skills/yara-rule-authoring:0.1.0").
//   - Query the registry via SearchSkills — no new interface method required.
//   - Post-filter to registry entries whose OCI packages share the same
//     repository path as ref (ignoring tag/digest, so :0.1.0 and :latest match
//     the same entry).
//   - If exactly one entry matches and it has a git package, build and return
//     its git:// URL. Multiple matches would be ambiguous so we skip the
//     fallback rather than guess.
func (s *service) resolveGitFallbackForOCIRef(ref nameref.Reference) string {
	if s.skillLookup == nil {
		return ""
	}

	repo := ref.Context().RepositoryStr()
	tail := repo
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		tail = repo[idx+1:]
	}
	if tail == "" {
		return ""
	}

	results, err := s.skillLookup.SearchSkills(tail)
	if err != nil {
		slog.Debug("registry lookup for OCI fallback failed, skipping fallback",
			"ref", ref.String(), "error", err)
		return ""
	}

	wantRepo := canonicalOCIRepo(ref)

	var matches []regtypes.Skill
	for _, sk := range results {
		if !skillHasMatchingOCIRepo(sk, wantRepo) {
			continue
		}
		matches = append(matches, sk)
	}

	// Ambiguous: bail out rather than guess. An ambiguous fallback is worse
	// than surfacing the original OCI error because it could silently serve
	// content from the wrong skill.
	if len(matches) != 1 {
		return ""
	}

	return firstGitPackageURL(matches[0].Packages)
}

// canonicalOCIRepo returns the registry+repository portion of ref without a
// tag or digest so two references to the same repository at different versions
// compare equal.
func canonicalOCIRepo(ref nameref.Reference) string {
	ctx := ref.Context()
	return ctx.RegistryStr() + "/" + ctx.RepositoryStr()
}

// skillHasMatchingOCIRepo reports whether sk has any OCI package whose
// identifier refers to the same repository as wantRepo.
func skillHasMatchingOCIRepo(sk regtypes.Skill, wantRepo string) bool {
	for _, pkg := range sk.Packages {
		if pkg.RegistryType != "oci" || pkg.Identifier == "" {
			continue
		}
		parsed, err := nameref.ParseReference(pkg.Identifier)
		if err != nil {
			continue
		}
		if canonicalOCIRepo(parsed) == wantRepo {
			return true
		}
	}
	return false
}

// firstGitPackageURL returns the git:// URL for the first usable git package
// in pkgs, or "" if none is usable. The format follows gitresolver's parser:
//
//	git://host/owner/repo[@ref][#subfolder]
//
// Commit is preferred over Ref for reproducibility; both are optional.
func firstGitPackageURL(pkgs []regtypes.SkillPackage) string {
	for _, pkg := range pkgs {
		if pkg.RegistryType != "git" || pkg.URL == "" {
			continue
		}
		gitURL, err := buildGitReferenceFromRegistryURL(pkg.URL)
		if err != nil {
			continue
		}
		if ref := preferredGitRef(pkg); ref != "" {
			gitURL += "@" + ref
		}
		if pkg.Subfolder != "" {
			gitURL += "#" + pkg.Subfolder
		}
		return gitURL
	}
	return ""
}

// preferredGitRef returns the ref to pin the git fallback to. Commit is
// preferred over branch/tag for reproducibility because the registry records
// both when available.
func preferredGitRef(pkg regtypes.SkillPackage) string {
	if pkg.Commit != "" {
		return pkg.Commit
	}
	return pkg.Ref
}

// resolveRegistryPackages selects the best installable package from a registry
// entry. OCI packages are preferred; git is the fallback.
func resolveRegistryPackages(name string, packages []regtypes.SkillPackage) (*registryResolveResult, error) {
	// Try OCI packages first (preferred).
	for _, pkg := range packages {
		if pkg.RegistryType == "oci" && pkg.Identifier != "" {
			ref, parseErr := nameref.ParseReference(pkg.Identifier)
			if parseErr != nil {
				id := truncate(pkg.Identifier, 256)
				return nil, httperr.WithCode(
					fmt.Errorf("registry skill %q has invalid OCI identifier %q: %w", name, id, parseErr),
					http.StatusUnprocessableEntity,
				)
			}
			return &registryResolveResult{OCIRef: ref}, nil
		}
	}

	// Fallback: look for git packages.
	for _, pkg := range packages {
		if pkg.RegistryType == "git" && pkg.URL != "" {
			gitURL, gitErr := buildGitReferenceFromRegistryURL(pkg.URL)
			if gitErr != nil {
				u := truncate(pkg.URL, 256)
				return nil, httperr.WithCode(
					fmt.Errorf("registry skill %q has invalid git URL %q: %w", name, u, gitErr),
					http.StatusUnprocessableEntity,
				)
			}
			if pkg.Subfolder != "" {
				gitURL += "#" + pkg.Subfolder
			}
			return &registryResolveResult{GitURL: gitURL}, nil
		}
	}

	return nil, httperr.WithCode(
		fmt.Errorf("skill %q found in registry but has no installable package (OCI or git)", name),
		http.StatusUnprocessableEntity,
	)
}

// truncate returns s shortened to maxLen with an ellipsis appended if needed.
func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// buildGitReferenceFromRegistryURL converts a registry URL (typically HTTPS)
// to a git:// scheme reference that ParseGitReference can handle.
func buildGitReferenceFromRegistryURL(rawURL string) (string, error) {
	// The registry may store URLs as "https://github.com/org/repo" or
	// already as "git://github.com/org/repo".
	if gitresolver.IsGitReference(rawURL) {
		// Already a git:// URL — validate it.
		if _, err := gitresolver.ParseGitReference(rawURL); err != nil {
			return "", err
		}
		return rawURL, nil
	}

	// Convert https://host/path → git://host/path
	stripped := strings.TrimPrefix(rawURL, "https://")
	stripped = strings.TrimPrefix(stripped, "http://")
	if stripped == rawURL {
		return "", fmt.Errorf("unsupported URL scheme; expected https:// or git://")
	}
	gitURL := "git://" + stripped

	// Validate the constructed reference.
	if _, err := gitresolver.ParseGitReference(gitURL); err != nil {
		return "", err
	}
	return gitURL, nil
}

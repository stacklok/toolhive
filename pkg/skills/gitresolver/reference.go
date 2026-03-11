// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"fmt"
	"net"
	"path"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

const gitScheme = "git://"

// GitReference represents a parsed git:// skill reference.
type GitReference struct {
	// URL is the HTTPS clone URL (e.g., https://github.com/org/repo)
	URL string
	// Path is the subdirectory within repo (e.g., "path/to/skill"), empty = repo root
	Path string
	// Ref is the git ref: branch, tag, or commit (e.g., "v1.0.0"), empty = default branch
	Ref string
}

// IsGitReference returns true if name starts with "git://".
func IsGitReference(name string) bool {
	return strings.HasPrefix(name, gitScheme)
}

// ParseGitReference parses a git:// skill reference.
//
// Format: git://host/owner/repo[@ref][#path/to/skill]
//
// Examples:
//   - git://github.com/org/repo
//   - git://github.com/org/repo@v1.0.0
//   - git://github.com/org/repo#skills/my-skill
//   - git://github.com/org/repo@main#skills/my-skill
func ParseGitReference(raw string) (*GitReference, error) {
	if !IsGitReference(raw) {
		return nil, fmt.Errorf("not a git reference: must start with %q", gitScheme)
	}

	// Strip scheme
	rest := raw[len(gitScheme):]

	// Split off fragment (#path)
	var skillPath string
	if idx := strings.Index(rest, "#"); idx >= 0 {
		skillPath = rest[idx+1:]
		rest = rest[:idx]
	}

	// Split off ref (@ref)
	var ref string
	if idx := strings.Index(rest, "@"); idx >= 0 {
		ref = rest[idx+1:]
		rest = rest[:idx]
	}

	// rest is now "host/owner/repo" (or "host/owner/repo/...")
	if rest == "" {
		return nil, fmt.Errorf("invalid git reference: empty host/path")
	}

	// Extract host
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return nil, fmt.Errorf("invalid git reference: no repository path after host")
	}
	host := rest[:slashIdx]
	repoPath := rest[slashIdx+1:]

	// Validate host
	if err := validateHost(host); err != nil {
		return nil, fmt.Errorf("invalid git reference: %w", err)
	}

	// Validate repo path has at least owner/repo
	if repoPath == "" || !strings.Contains(repoPath, "/") {
		return nil, fmt.Errorf("invalid git reference: repository path must be at least owner/repo")
	}

	// Validate ref
	if err := validateRef(ref); err != nil {
		return nil, fmt.Errorf("invalid git reference: %w", err)
	}

	// Validate skill path
	if err := validateSkillPath(skillPath); err != nil {
		return nil, fmt.Errorf("invalid git reference: %w", err)
	}

	// Build HTTPS clone URL
	cloneURL := "https://" + host + "/" + repoPath

	return &GitReference{
		URL:  cloneURL,
		Path: skillPath,
		Ref:  ref,
	}, nil
}

// SkillName extracts the expected skill name from the reference.
// Uses the last component of Path if set, otherwise the last component of the repo URL.
func (r *GitReference) SkillName() string {
	if r.Path != "" {
		return path.Base(r.Path)
	}
	// Extract from URL: "https://github.com/org/repo" -> "repo"
	trimmed := strings.TrimSuffix(r.URL, ".git")
	return path.Base(trimmed)
}

// validateHost checks the host is not localhost, a private IP, or empty.
// Reuses pkg/networking SSRF utilities as the single source of truth.
//
// NOTE: This check only validates literal IPs and known localhost strings.
// Hostnames that DNS-resolve to private IPs (DNS rebinding) are NOT caught here
// because go-git does not expose a DialContext hook. A pre-clone DNS resolution
// check could be added as defense-in-depth.
func validateHost(host string) error {
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}

	// Strip port if present
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	// Reject localhost variants using the shared networking utility.
	if networking.IsLocalhost(hostname) {
		return fmt.Errorf("host %q is not allowed: localhost is rejected for SSRF prevention", host)
	}

	// Reject private/loopback IPs using the shared networking utility.
	ip := net.ParseIP(hostname)
	if ip != nil && networking.IsPrivateIP(ip) {
		return fmt.Errorf("host %q is not allowed: private/loopback IPs are rejected for SSRF prevention", host)
	}

	return nil
}

// validateRef checks that the ref doesn't contain shell metacharacters.
func validateRef(ref string) error {
	if ref == "" {
		return nil
	}
	// Reject characters that could be used in shell injection or path traversal
	for _, c := range ref {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '/':
			continue
		default:
			return fmt.Errorf("ref %q contains invalid character %q", ref, c)
		}
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("ref %q must not contain '..' segments", ref)
	}
	return nil
}

// validateSkillPath checks that the path doesn't contain traversal, null bytes,
// absolute paths, or backslashes.
func validateSkillPath(p string) error {
	if p == "" {
		return nil
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("path contains null bytes")
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return fmt.Errorf("path %q must be relative", p)
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("path %q must not contain backslashes", p)
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return fmt.Errorf("path %q must not contain '..' traversal segments", p)
		}
	}
	return nil
}

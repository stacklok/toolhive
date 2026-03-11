// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package gitresolver resolves skill installations from git repositories.
package gitresolver

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"time"

	"github.com/stacklok/toolhive/pkg/git"
	"github.com/stacklok/toolhive/pkg/skills"
)

// cloneTimeout is the maximum time allowed for cloning a git repository.
const cloneTimeout = 2 * time.Minute

// semverLike matches refs that look like semantic version tags (v1.0, v1.2.3, v1.2.3-rc1, etc.).
// Requires at least one dot-separated numeric segment after the major version to avoid matching
// branch names like "v1-beta-branch".
var semverLike = regexp.MustCompile(`^v\d+\.\d+(\.\d+)*(-[a-zA-Z0-9._-]+)?$`)

// Resolver clones a git repository and extracts skill files.
type Resolver interface {
	// Resolve clones the repo, validates the skill, and returns the skill
	// directory contents as files ready for installation.
	Resolve(ctx context.Context, ref *GitReference) (*ResolveResult, error)
}

// ResolveResult contains the outcome of resolving a git skill reference.
type ResolveResult struct {
	// SkillConfig is the parsed SKILL.md
	SkillConfig *skills.ParseResult
	// Files is all files in the skill directory
	Files []FileEntry
	// CommitHash is the git commit hash (for digest/upgrade detection)
	CommitHash string
}

// FileEntry represents a single file from the cloned repository.
type FileEntry struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// ResolverOption configures a defaultResolver.
type ResolverOption func(*defaultResolver)

// WithGitClient sets a fixed git client, bypassing per-clone auth resolution.
// Primarily used for testing with mock clients.
func WithGitClient(client git.Client) ResolverOption {
	return func(r *defaultResolver) {
		r.fixedClient = client
	}
}

// NewResolver creates a new git skill resolver.
func NewResolver(opts ...ResolverOption) Resolver {
	r := &defaultResolver{}
	for _, o := range opts {
		o(r)
	}
	return r
}

type defaultResolver struct {
	// fixedClient, when set, is used for all clones (testing).
	// When nil, a new client is created per-clone with host-scoped auth.
	fixedClient git.Client
}

// clientForURL returns a git client appropriate for the given clone URL.
// If a fixed client was provided (testing), it is returned as-is.
// Otherwise, a new client is created with host-scoped auth from the environment.
func (r *defaultResolver) clientForURL(cloneURL string) git.Client {
	if r.fixedClient != nil {
		return r.fixedClient
	}
	auth := ResolveAuth(cloneURL)
	var opts []git.ClientOption
	if auth != nil {
		opts = append(opts, git.WithAuth(auth))
	}
	return git.NewDefaultGitClient(opts...)
}

// Resolve clones a git repository and extracts skill files from it.
func (r *defaultResolver) Resolve(ctx context.Context, ref *GitReference) (*ResolveResult, error) {
	// Enforce a clone timeout to prevent indefinite hangs from slow/malicious servers.
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	// Build clone config from the git reference
	cloneConfig := &git.CloneConfig{
		URL: ref.URL,
	}
	if ref.Ref != "" {
		switch {
		case len(ref.Ref) == 40 && isHex(ref.Ref):
			// Full commit hash → checkout specific commit
			cloneConfig.Commit = ref.Ref
		case semverLike.MatchString(ref.Ref):
			// Semver-like pattern (v1.0.0) → clone as tag
			cloneConfig.Tag = ref.Ref
		default:
			// Everything else → treat as branch
			cloneConfig.Branch = ref.Ref
		}
	}

	client := r.clientForURL(ref.URL)

	repoInfo, err := client.Clone(ctx, cloneConfig)
	if err != nil {
		return nil, fmt.Errorf("cloning repository: %w", err)
	}
	defer client.Cleanup(ctx, repoInfo) //nolint:errcheck // best-effort cleanup

	// Get commit hash for digest tracking
	commitHash, err := client.HeadCommitHash(repoInfo)
	if err != nil {
		return nil, fmt.Errorf("getting commit hash: %w", err)
	}

	// Read SKILL.md from the skill path
	skillMDPath := path.Join(ref.Path, "SKILL.md")
	if ref.Path == "" {
		skillMDPath = "SKILL.md"
	}

	skillContent, err := client.GetFileContent(repoInfo, skillMDPath)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md at %q: %w", skillMDPath, err)
	}

	// Parse the skill definition
	parsed, err := skills.ParseSkillMD(skillContent)
	if err != nil {
		return nil, fmt.Errorf("parsing SKILL.md: %w", err)
	}

	// Validate skill name
	if err := skills.ValidateSkillName(parsed.Name); err != nil {
		return nil, fmt.Errorf("invalid skill name in SKILL.md: %w", err)
	}

	// Collect all files in the skill directory.
	// For now, we read SKILL.md as the primary file. Additional files in the
	// skill directory are discovered by listing the tree entries.
	files, err := r.collectFiles(repoInfo, ref.Path)
	if err != nil {
		return nil, fmt.Errorf("collecting skill files: %w", err)
	}

	return &ResolveResult{
		SkillConfig: parsed,
		Files:       files,
		CommitHash:  commitHash,
	}, nil
}

// collectFiles reads all files from the given path in the repository.
func (*defaultResolver) collectFiles(repoInfo *git.RepositoryInfo, basePath string) ([]FileEntry, error) {
	ref, err := repoInfo.Repository.Head()
	if err != nil {
		return nil, fmt.Errorf("getting HEAD: %w", err)
	}

	commit, err := repoInfo.Repository.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("getting commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("getting tree: %w", err)
	}

	// Navigate to subdirectory if specified
	if basePath != "" {
		tree, err = tree.Tree(basePath)
		if err != nil {
			return nil, fmt.Errorf("navigating to path %q: %w", basePath, err)
		}
	}

	var files []FileEntry
	for _, entry := range tree.Entries {
		// Skip directories — we only want files at the top level of the skill dir.
		// Nested subdirectories are not part of the skill spec today.
		// WriteFiles includes MkdirAll as defense-in-depth for containment safety.
		if entry.Mode == 0040000 {
			continue
		}

		file, fileErr := tree.File(entry.Name)
		if fileErr != nil {
			return nil, fmt.Errorf("reading file %q: %w", entry.Name, fileErr)
		}

		content, contentErr := file.Contents()
		if contentErr != nil {
			return nil, fmt.Errorf("reading content of %q: %w", entry.Name, contentErr)
		}

		// All files are capped to 0644 by the writer; set a uniform mode here.
		mode := fs.FileMode(0644)

		files = append(files, FileEntry{
			Path:    entry.Name,
			Content: []byte(content),
			Mode:    mode,
		})
	}

	return files, nil
}

// isHex checks if a string is a valid non-empty hexadecimal string.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'f',
			c >= 'A' && c <= 'F':
			continue
		default:
			return false
		}
	}
	return true
}

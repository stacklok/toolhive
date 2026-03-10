// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// ErrNilRepository is returned when a nil repository is passed to an operation that requires one.
var ErrNilRepository = errors.New("repository is nil")

// ErrInvalidFilePath is returned when a file path contains traversal or absolute components.
var ErrInvalidFilePath = errors.New("invalid file path")

// ErrInvalidCloneConfig is returned when CloneConfig has conflicting ref specifications.
var ErrInvalidCloneConfig = errors.New("invalid clone config: at most one of Branch, Tag, or Commit may be specified")

// Client defines the interface for Git operations
type Client interface {
	// Clone clones a repository with the given configuration
	Clone(ctx context.Context, config *CloneConfig) (*RepositoryInfo, error)

	// GetFileContent retrieves the content of a file from the repository
	GetFileContent(repoInfo *RepositoryInfo, path string) ([]byte, error)

	// HeadCommitHash returns the commit hash of the HEAD reference.
	HeadCommitHash(repoInfo *RepositoryInfo) (string, error)

	// Cleanup removes local repository directory
	Cleanup(ctx context.Context, repoInfo *RepositoryInfo) error
}

// DefaultGitClient implements Client using go-git
type DefaultGitClient struct {
	// auth is the optional authentication method for cloning
	auth transport.AuthMethod
}

// ClientOption configures a DefaultGitClient.
type ClientOption func(*DefaultGitClient)

// WithAuth sets the authentication method for git operations.
func WithAuth(auth transport.AuthMethod) ClientOption {
	return func(c *DefaultGitClient) {
		c.auth = auth
	}
}

// NewDefaultGitClient creates a new DefaultGitClient
func NewDefaultGitClient(opts ...ClientOption) *DefaultGitClient {
	c := &DefaultGitClient{}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Clone clones a repository with the given configuration
func (c *DefaultGitClient) Clone(ctx context.Context, config *CloneConfig) (*RepositoryInfo, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	// Prepare clone options
	cloneOptions := &git.CloneOptions{
		URL:  config.URL,
		Auth: c.auth,
	}

	// Set reference if specified (but not for commit-based clones)
	if config.Commit == "" {
		cloneOptions.Depth = 1
		if config.Branch != "" {
			cloneOptions.ReferenceName = plumbing.NewBranchReferenceName(config.Branch)
			cloneOptions.SingleBranch = true
		} else if config.Tag != "" {
			cloneOptions.ReferenceName = plumbing.NewTagReferenceName(config.Tag)
			cloneOptions.SingleBranch = true
		}
	}
	// For commit-based clones, we need the full repository to ensure the commit is available

	// Use in-memory filesystems for the repository and the storer
	// See https://github.com/mindersec/minder/blob/main/internal/providers/git/git.go
	// for more details
	// Clone the repository
	memFS := memfs.New()
	memFS = &LimitedFs{
		Fs:            memFS,
		MaxFiles:      10 * 1000,
		TotalFileSize: 100 * 1024 * 1024,
	}
	// go-git seems to want separate filesystems for the storer and the checked out files
	storerFs := memfs.New()
	storerFs = &LimitedFs{
		Fs:            storerFs,
		MaxFiles:      10 * 1000,
		TotalFileSize: 100 * 1024 * 1024,
	}
	storerCache := cache.NewObjectLRUDefault()
	storer := filesystem.NewStorage(storerFs, storerCache)

	repo, err := git.CloneContext(ctx, storer, memFS, cloneOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	// Get repository information
	repoInfo := &RepositoryInfo{
		Repository:       repo,
		RemoteURL:        config.URL,
		storerFilesystem: storerFs,
		objectCache:      storerCache,
	}

	// If specific commit is requested, checkout that commit
	if config.Commit != "" {
		workTree, err := repo.Worktree()
		if err != nil {
			return nil, fmt.Errorf("failed to get worktree: %w", err)
		}

		hash := plumbing.NewHash(config.Commit)
		err = workTree.Checkout(&git.CheckoutOptions{
			Hash: hash,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to checkout commit %s: %w", config.Commit, err)
		}
	}

	// Update repository info with current state
	if err := c.updateRepositoryInfo(repoInfo); err != nil {
		return nil, fmt.Errorf("failed to update repository info: %w", err)
	}

	return repoInfo, nil
}

// GetFileContent retrieves the content of a file from the repository
func (*DefaultGitClient) GetFileContent(repoInfo *RepositoryInfo, path string) ([]byte, error) {
	if repoInfo == nil || repoInfo.Repository == nil {
		return nil, ErrNilRepository
	}

	// Reject absolute paths, traversal, and null bytes
	if filepath.IsAbs(path) || strings.Contains(path, "..") || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidFilePath, path)
	}

	// Get the HEAD reference
	ref, err := repoInfo.Repository.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	// Get the commit object
	commit, err := repoInfo.Repository.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	// Get the tree
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Get the file
	file, err := tree.File(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get file %s: %w", path, err)
	}

	// Read file contents
	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read file contents: %w", err)
	}

	return []byte(content), nil
}

// Cleanup removes local repository directory
func (*DefaultGitClient) Cleanup(_ context.Context, repoInfo *RepositoryInfo) error {
	if repoInfo == nil || repoInfo.Repository == nil {
		return ErrNilRepository
	}

	// 1. Clear object cache explicitly
	if repoInfo.objectCache != nil {
		slog.Debug("Clearing object cache")
		repoInfo.objectCache.Clear()
	}

	// 2. Clear worktree filesystem
	worktree, err := repoInfo.Repository.Worktree()
	if err == nil && worktree.Filesystem != nil {
		slog.Debug("Clearing worktree filesystem")
		if err := util.RemoveAll(worktree.Filesystem, "/"); err != nil {
			slog.Warn("Failed to clear worktree filesystem", "error", err)
		}
	}

	// 3. Clear storer filesystem (memfs)
	if repoInfo.storerFilesystem != nil {
		slog.Debug("Clearing storer filesystem")
		if err := util.RemoveAll(repoInfo.storerFilesystem, "/"); err != nil {
			slog.Warn("Failed to clear storer filesystem", "error", err)
		}
	}

	// 4. Nil out all references
	repoInfo.objectCache = nil
	repoInfo.storerFilesystem = nil
	repoInfo.Repository = nil

	return nil
}

// updateRepositoryInfo updates the repository info with current state
func (*DefaultGitClient) updateRepositoryInfo(repoInfo *RepositoryInfo) error {
	if repoInfo == nil || repoInfo.Repository == nil {
		return ErrNilRepository
	}

	// Get current branch name
	ref, err := repoInfo.Repository.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	if ref.Name().IsBranch() {
		repoInfo.Branch = ref.Name().Short()
	}

	return nil
}

// HeadCommitHash returns the commit hash of the HEAD reference.
func (*DefaultGitClient) HeadCommitHash(repoInfo *RepositoryInfo) (string, error) {
	if repoInfo == nil || repoInfo.Repository == nil {
		return "", ErrNilRepository
	}

	ref, err := repoInfo.Repository.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	return ref.Hash().String(), nil
}

package git

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Client defines the interface for Git operations
type Client interface {
	// Clone clones a repository with the given configuration
	Clone(ctx context.Context, config *CloneConfig) (*RepositoryInfo, error)

	// GetFileContent retrieves the content of a file from the repository
	GetFileContent(repoInfo *RepositoryInfo, path string) ([]byte, error)

	// Cleanup removes local repository directory
	Cleanup(repoInfo *RepositoryInfo) error
}

// DefaultGitClient implements GitClient using go-git
type DefaultGitClient struct{}

// NewDefaultGitClient creates a new DefaultGitClient
func NewDefaultGitClient() *DefaultGitClient {
	return &DefaultGitClient{}
}

// Clone clones a repository with the given configuration
func (c *DefaultGitClient) Clone(ctx context.Context, config *CloneConfig) (*RepositoryInfo, error) {
	// Prepare clone options (no authentication for initial version)
	cloneOptions := &git.CloneOptions{
		URL: config.URL,
	}

	// Set reference if specified (but not for commit-based clones)
	if config.Commit == "" {
		if config.Branch != "" {
			cloneOptions.ReferenceName = plumbing.NewBranchReferenceName(config.Branch)
			cloneOptions.SingleBranch = true
		} else if config.Tag != "" {
			cloneOptions.ReferenceName = plumbing.NewTagReferenceName(config.Tag)
			cloneOptions.SingleBranch = true
		}
	}
	// For commit-based clones, we need the full repository to ensure the commit is available

	// Clone the repository
	repo, err := git.PlainCloneContext(ctx, config.Directory, false, cloneOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	// Get repository information
	repoInfo := &RepositoryInfo{
		Repository: repo,
		RemoteURL:  config.URL,
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
		return nil, fmt.Errorf("repository is nil")
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
func (*DefaultGitClient) Cleanup(repoInfo *RepositoryInfo) error {
	if repoInfo == nil || repoInfo.Repository == nil {
		return nil
	}

	// Get the repository directory from the worktree
	workTree, err := repoInfo.Repository.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Remove the directory
	return os.RemoveAll(workTree.Filesystem.Root())
}

// updateRepositoryInfo updates the repository info with current state
func (*DefaultGitClient) updateRepositoryInfo(repoInfo *RepositoryInfo) error {
	if repoInfo == nil || repoInfo.Repository == nil {
		return fmt.Errorf("repository is nil")
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

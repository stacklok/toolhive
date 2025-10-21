package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client defines the interface for Git operations
type Client interface {
	// FetchFileSparse fetches a single file using sparse checkout for minimal memory usage
	FetchFileSparse(ctx context.Context, config *CloneConfig, filePath string) ([]byte, error)
}

// DefaultGitClient implements GitClient using go-git
type DefaultGitClient struct{}

// NewDefaultGitClient creates a new DefaultGitClient
func NewDefaultGitClient() *DefaultGitClient {
	return &DefaultGitClient{}
}

// FetchFileSparse fetches a single file using sparse checkout for minimal memory usage
func (c *DefaultGitClient) FetchFileSparse(ctx context.Context, config *CloneConfig, filePath string) ([]byte, error) {
	logger := log.FromContext(ctx)

	// Log memory stats before operation
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	logger.V(1).Info("Git operation starting",
		"url", config.URL,
		"file", filePath,
		"alloc_mb", memBefore.Alloc/(1024*1024))

	repo, err := c.cloneRepositoryForSparseCheckout(ctx, config)
	if err != nil {
		return nil, err
	}

	// Variable to hold repo reference for cleanup
	repoRef := &repo

	// Ensure cleanup happens even on error
	defer func() {
		logger.V(1).Info("Git cleanup starting")

		// Explicitly clear repository reference to help GC
		if repoRef != nil && *repoRef != nil {
			*repoRef = nil
		}

		// Force garbage collection to release memory
		runtime.GC()

		// Log memory stats after cleanup
		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)

		// Calculate delta in MB with proper signed arithmetic
		allocAfterMB := memAfter.Alloc / (1024 * 1024)
		allocBeforeMB := memBefore.Alloc / (1024 * 1024)
		var deltaMB int64
		if allocAfterMB >= allocBeforeMB {
			// #nosec G115 -- Memory delta in MB will never exceed int64 max
			deltaMB = int64(allocAfterMB - allocBeforeMB)
		} else {
			// #nosec G115 -- Memory delta in MB will never exceed int64 max
			deltaMB = -int64(allocBeforeMB - allocAfterMB)
		}

		logger.V(1).Info("Git cleanup completed",
			"alloc_mb", allocAfterMB,
			"delta_mb", deltaMB)
	}()

	// Checkout specific commit if needed
	if config.Commit != "" {
		logger.V(1).Info("Checking out commit", "commit", config.Commit)
		if err := c.checkoutCommit(repo, config.Commit); err != nil {
			return nil, err
		}
	}

	// Perform sparse checkout
	logger.V(1).Info("Performing sparse checkout", "file", filePath)
	if err := c.performSparseCheckout(repo, filePath); err != nil {
		return nil, err
	}

	// Read and return file content with path validation
	content, err := c.readFileSecurely(config.Directory, filePath)
	if err != nil {
		return nil, err
	}

	logger.V(1).Info("File fetched successfully",
		"size_kb", fmt.Sprintf("%.2f", float64(len(content))/1024))
	return content, nil
}

// cloneRepositoryForSparseCheckout clones repository with minimal data transfer
func (*DefaultGitClient) cloneRepositoryForSparseCheckout(ctx context.Context, config *CloneConfig) (*git.Repository, error) {
	logger := log.FromContext(ctx)

	cloneOptions := &git.CloneOptions{
		URL:          config.URL,
		Depth:        1,    // Shallow clone - only latest commit
		SingleBranch: true, // Only fetch the target branch
		NoCheckout:   true, // Don't checkout files yet
	}

	// Set reference based on branch/tag/commit
	if config.Commit == "" {
		if config.Branch != "" {
			cloneOptions.ReferenceName = plumbing.NewBranchReferenceName(config.Branch)
		} else if config.Tag != "" {
			cloneOptions.ReferenceName = plumbing.NewTagReferenceName(config.Tag)
		}
	} else {
		// For commit-based clones, we need full history to find the commit
		// Disable shallow clone and single branch to fetch all refs
		// WARNING: This loads full history into memory - potential memory leak!
		logger.Info("WARNING: Cloning full repository history for commit lookup - this may consume significant memory",
			"commit", config.Commit,
			"url", config.URL)
		cloneOptions.Depth = 0
		cloneOptions.SingleBranch = false
	}

	repo, err := git.PlainCloneContext(ctx, config.Directory, false, cloneOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	return repo, nil
}

// checkoutCommit checks out a specific commit
func (*DefaultGitClient) checkoutCommit(repo *git.Repository, commit string) error {
	workTree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Parse the commit hash
	hash := plumbing.NewHash(commit)

	// Try to get the commit object to verify it exists
	_, err = repo.CommitObject(hash)
	if err != nil {
		return fmt.Errorf("commit %s not found: %w", commit, err)
	}

	// Checkout the commit
	if err := workTree.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
		return fmt.Errorf("failed to checkout commit %s: %w", commit, err)
	}

	return nil
}

// performSparseCheckout performs sparse checkout for a single file
func (*DefaultGitClient) performSparseCheckout(repo *git.Repository, filePath string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Get HEAD reference to checkout
	ref, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	// Determine what to checkout based on file location
	fileDir := filepath.Dir(filePath)

	// For root directory files, we can't use sparse checkout effectively
	// Just do a regular checkout which is still efficient with depth=1
	if fileDir == "." || fileDir == "/" {
		if err := worktree.Checkout(&git.CheckoutOptions{
			Hash:  ref.Hash(),
			Force: true,
		}); err != nil {
			return fmt.Errorf("failed checkout: %w", err)
		}
	} else {
		// For files in subdirectories, use sparse checkout
		if err := worktree.Checkout(&git.CheckoutOptions{
			Hash:                      ref.Hash(),
			Force:                     true,
			SparseCheckoutDirectories: []string{fileDir},
		}); err != nil {
			return fmt.Errorf("failed sparse checkout: %w", err)
		}
	}

	// Clear reference to help GC
	ref = nil

	return nil
}

// readFileSecurely reads a file with path traversal protection
func (*DefaultGitClient) readFileSecurely(baseDir, filePath string) ([]byte, error) {
	// Sanitize the file path to prevent path traversal
	cleanFilePath := filepath.Clean(filePath)
	if cleanFilePath == ".." || strings.HasPrefix(cleanFilePath, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanFilePath) {
		return nil, fmt.Errorf("invalid file path: potential path traversal detected in %s", filePath)
	}

	fullPath := filepath.Join(baseDir, cleanFilePath)

	// Verify the resolved path is within the expected directory
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve file path: %w", err)
	}
	absDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve directory path: %w", err)
	}

	// Ensure paths end with separator for proper prefix matching
	if !strings.HasSuffix(absDir, string(filepath.Separator)) {
		absDir += string(filepath.Separator)
	}
	if !strings.HasPrefix(absFullPath, absDir) {
		return nil, fmt.Errorf("file path escapes repository directory: %s", filePath)
	}

	// #nosec G304 -- Path is sanitized and validated to be within repository directory
	content, err := os.ReadFile(absFullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	return content, nil
}

package sources

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/git"
)

const (
	// DefaultRegistryDataFile is the default file name for the registry data in Git sources
	DefaultRegistryDataFile = "registry.json"
	// WorkspaceDirEnvVar is the environment variable name for the workspace directory
	WorkspaceDirEnvVar = "WORKSPACE_DIR"
)

// getWorkspaceDir returns the workspace directory from env var or empty string for system temp
func getWorkspaceDir() string {
	return os.Getenv(WorkspaceDirEnvVar)
}

// GitSourceHandler handles registry data from Git repositories
type GitSourceHandler struct {
	gitClient git.Client
	validator SourceDataValidator
}

// NewGitSourceHandler creates a new Git source handler
func NewGitSourceHandler() *GitSourceHandler {
	return &GitSourceHandler{
		gitClient: git.NewDefaultGitClient(),
		validator: NewSourceDataValidator(),
	}
}

// Validate validates the Git source configuration
func (*GitSourceHandler) Validate(source *mcpv1alpha1.MCPRegistrySource) error {
	if source.Type != mcpv1alpha1.RegistrySourceTypeGit {
		return fmt.Errorf("invalid source type: expected %s, got %s",
			mcpv1alpha1.RegistrySourceTypeGit, source.Type)
	}

	if source.Git == nil {
		return fmt.Errorf("git configuration is required for source type %s",
			mcpv1alpha1.RegistrySourceTypeGit)
	}

	gitSource := source.Git

	if gitSource.Repository == "" {
		return fmt.Errorf("git repository URL cannot be empty")
	}

	// Validate mutually exclusive branch/tag/commit
	specified := 0
	if gitSource.Branch != "" {
		specified++
	}
	if gitSource.Tag != "" {
		specified++
	}
	if gitSource.Commit != "" {
		specified++
	}

	if specified > 1 {
		return fmt.Errorf("only one of branch, tag, or commit may be specified")
	}

	// Set default path if not specified
	if gitSource.Path == "" {
		gitSource.Path = DefaultRegistryDataFile
	}

	return nil
}

// FetchRegistry retrieves registry data from the Git repository using sparse checkout
func (h *GitSourceHandler) FetchRegistry(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*FetchResult, error) {

	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return nil, fmt.Errorf("source validation failed: %w", err)
	}

	// Create temporary directory for sparse checkout
	tempDir, err := os.MkdirTemp(getWorkspaceDir(), "mcpregistry-git-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Get file path
	filePath := mcpRegistry.Spec.Source.Git.Path
	if filePath == "" {
		filePath = DefaultRegistryDataFile
	}

	// Prepare clone configuration
	cloneConfig := &git.CloneConfig{
		URL:       mcpRegistry.Spec.Source.Git.Repository,
		Branch:    mcpRegistry.Spec.Source.Git.Branch,
		Tag:       mcpRegistry.Spec.Source.Git.Tag,
		Commit:    mcpRegistry.Spec.Source.Git.Commit,
		Directory: tempDir,
	}

	// Fetch file using sparse checkout with timing and metrics
	startTime := time.Now()
	logger := log.FromContext(ctx)
	logger.Info("Starting git sparse checkout",
		"repository", cloneConfig.URL,
		"branch", cloneConfig.Branch,
		"tag", cloneConfig.Tag,
		"commit", cloneConfig.Commit,
		"file", filePath)

	registryData, err := h.gitClient.FetchFileSparse(ctx, cloneConfig, filePath)
	fetchDuration := time.Since(startTime)

	if err != nil {
		logger.Error(err, "Git sparse checkout failed",
			"repository", cloneConfig.URL,
			"file", filePath,
			"duration", fetchDuration.String())
		return nil, fmt.Errorf("failed to fetch file %s from repository: %w", filePath, err)
	}

	// Calculate directory size and object count for metrics
	dirSize, objectCount, sizeErr := calculateDirectoryMetrics(tempDir)
	if sizeErr != nil {
		logger.Error(sizeErr, "Failed to calculate directory metrics")
		dirSize = 0
		objectCount = 0
	}

	logger.Info("Git sparse checkout completed",
		"repository", cloneConfig.URL,
		"file", filePath,
		"duration", fetchDuration.String(),
		"directory_size_mb", fmt.Sprintf("%.2f", float64(dirSize)/(1024*1024)),
		"object_count", objectCount,
		"file_size_kb", fmt.Sprintf("%.2f", float64(len(registryData))/1024))

	// Validate and parse registry data
	reg, err := h.validator.ValidateData(registryData, mcpRegistry.Spec.Source.Format)
	if err != nil {
		return nil, fmt.Errorf("registry data validation failed: %w", err)
	}

	// Calculate hash from the registry data we already fetched (no second clone needed)
	hash := fmt.Sprintf("%x", sha256.Sum256(registryData))

	// Create and return fetch result with pre-calculated hash
	return NewFetchResult(reg, hash, mcpRegistry.Spec.Source.Format), nil
}

// CurrentHash returns the current hash of the source data using sparse checkout
func (h *GitSourceHandler) CurrentHash(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (string, error) {
	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return "", fmt.Errorf("source validation failed: %w", err)
	}

	// Create temporary directory for sparse checkout
	tempDir, err := os.MkdirTemp(getWorkspaceDir(), "mcpregistry-git-hash-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Get file path
	filePath := mcpRegistry.Spec.Source.Git.Path
	if filePath == "" {
		filePath = DefaultRegistryDataFile
	}

	// Prepare clone configuration
	cloneConfig := &git.CloneConfig{
		URL:       mcpRegistry.Spec.Source.Git.Repository,
		Branch:    mcpRegistry.Spec.Source.Git.Branch,
		Tag:       mcpRegistry.Spec.Source.Git.Tag,
		Commit:    mcpRegistry.Spec.Source.Git.Commit,
		Directory: tempDir,
	}

	// Fetch file using sparse checkout
	registryData, err := h.gitClient.FetchFileSparse(ctx, cloneConfig, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to fetch file %s from repository: %w", filePath, err)
	}

	// Compute and return hash of the data
	hash := fmt.Sprintf("%x", sha256.Sum256(registryData))
	return hash, nil
}

// calculateDirectoryMetrics calculates the total size and file count of a directory
func calculateDirectoryMetrics(dirPath string) (totalSize int64, fileCount int, err error) {
	err = filepath.WalkDir(dirPath, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			fileCount++
			info, err := d.Info()
			if err != nil {
				return err
			}
			totalSize += info.Size()
		}

		return nil
	})

	return totalSize, fileCount, err
}

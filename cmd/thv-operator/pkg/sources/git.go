package sources

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/git"
)

const (
	// DefaultRegistryDataFile is the default file name for the registry data in Git sources
	DefaultRegistryDataFile = "registry.json"
)

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

// FetchRegistry retrieves registry data from the Git repository
func (h *GitSourceHandler) FetchRegistry(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*FetchResult, error) {

	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return nil, fmt.Errorf("source validation failed: %w", err)
	}

	// Create temporary directory for cloning
	tempDir, err := os.MkdirTemp("", "mcpregistry-git-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Prepare clone configuration
	cloneConfig := &git.CloneConfig{
		URL:       mcpRegistry.Spec.Source.Git.Repository,
		Branch:    mcpRegistry.Spec.Source.Git.Branch,
		Tag:       mcpRegistry.Spec.Source.Git.Tag,
		Commit:    mcpRegistry.Spec.Source.Git.Commit,
		Directory: tempDir,
	}

	// Clone the repository
	repoInfo, err := h.gitClient.Clone(ctx, cloneConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	// Ensure cleanup
	defer func() {
		if cleanupErr := h.gitClient.Cleanup(repoInfo); cleanupErr != nil {
			// Log error but don't fail the operation
			log.FromContext(ctx).Error(cleanupErr, "Failed to cleanup repository")
		}
	}()

	// Get file content from repository
	filePath := mcpRegistry.Spec.Source.Git.Path
	if filePath == "" {
		filePath = DefaultRegistryDataFile
	}

	registryData, err := h.gitClient.GetFileContent(repoInfo, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file %s from repository: %w", filePath, err)
	}

	// Validate and parse registry data
	reg, err := h.validator.ValidateData(registryData, mcpRegistry.Spec.Source.Format)
	if err != nil {
		return nil, fmt.Errorf("registry data validation failed: %w", err)
	}

	// Calculate hash using the same method as CurrentHash for consistency
	hash, err := h.CurrentHash(ctx, mcpRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate hash: %w", err)
	}

	// Create and return fetch result with pre-calculated hash
	return NewFetchResult(reg, hash, mcpRegistry.Spec.Source.Format), nil
}

// CurrentHash returns the current hash of the source data without performing a full fetch
func (h *GitSourceHandler) CurrentHash(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (string, error) {
	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return "", fmt.Errorf("source validation failed: %w", err)
	}

	// Create temporary directory for cloning
	tempDir, err := os.MkdirTemp("", "mcpregistry-git-hash-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Prepare clone configuration
	cloneConfig := &git.CloneConfig{
		URL:       mcpRegistry.Spec.Source.Git.Repository,
		Branch:    mcpRegistry.Spec.Source.Git.Branch,
		Tag:       mcpRegistry.Spec.Source.Git.Tag,
		Commit:    mcpRegistry.Spec.Source.Git.Commit,
		Directory: tempDir,
	}

	// Clone the repository
	repoInfo, err := h.gitClient.Clone(ctx, cloneConfig)
	if err != nil {
		return "", fmt.Errorf("failed to clone repository: %w", err)
	}

	// Ensure cleanup
	defer func() {
		if cleanupErr := h.gitClient.Cleanup(repoInfo); cleanupErr != nil {
			// Log error but don't fail the operation
			fmt.Printf("Warning: failed to cleanup repository: %v\n", cleanupErr)
		}
	}()

	// Get file content from repository
	filePath := mcpRegistry.Spec.Source.Git.Path
	if filePath == "" {
		filePath = DefaultRegistryDataFile
	}

	registryData, err := h.gitClient.GetFileContent(repoInfo, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to get file %s from repository: %w", filePath, err)
	}

	// Compute and return hash of the data
	hash := fmt.Sprintf("%x", sha256.Sum256(registryData))
	return hash, nil
}

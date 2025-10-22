package sources

import (
	"context"
	"crypto/sha256"
	"fmt"
	"runtime"
	"time"

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
func (h *GitSourceHandler) fetchRegistryData(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) ([]byte, error) {

	// Validate source configuration
	if err := h.Validate(&mcpRegistry.Spec.Source); err != nil {
		return nil, fmt.Errorf("source validation failed: %w", err)
	}

	// Prepare clone configuration
	cloneConfig := &git.CloneConfig{
		URL:    mcpRegistry.Spec.Source.Git.Repository,
		Branch: mcpRegistry.Spec.Source.Git.Branch,
		Tag:    mcpRegistry.Spec.Source.Git.Tag,
		Commit: mcpRegistry.Spec.Source.Git.Commit,
	}

	// Clone the repository with timing and metrics
	logger := log.FromContext(ctx)
	startTime := time.Now()
	logger.Info("Starting git clone",
		"repository", cloneConfig.URL,
		"branch", cloneConfig.Branch,
		"tag", cloneConfig.Tag,
		"commit", cloneConfig.Commit)

	// Capture memory stats before operation
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	repoInfo, err := h.gitClient.Clone(ctx, cloneConfig)
	cloneDuration := time.Since(startTime)

	if err != nil {
		logger.Error(err, "Git clone failed",
			"repository", cloneConfig.URL,
			"duration", cloneDuration.String())
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	logger.Info("Git clone completed",
		"repository", cloneConfig.URL,
		"duration", cloneDuration.String(),
		"branch", repoInfo.Branch)

	// Ensure cleanup
	defer func() {
		if cleanupErr := h.gitClient.Cleanup(ctx, repoInfo); cleanupErr != nil {
			// Log error but don't fail the operation
			log.FromContext(ctx).Error(cleanupErr, "Failed to cleanup repository")
		}
		logMemoryStatsAfterOperation(ctx, &memBefore)
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

	return registryData, nil
}

// FetchRegistry retrieves registry data from the Git repository
func (h *GitSourceHandler) FetchRegistry(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*FetchResult, error) {

	registryData, err := h.fetchRegistryData(ctx, mcpRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry data: %w", err)
	}

	// Validate and parse registry data
	reg, err := h.validator.ValidateData(registryData, mcpRegistry.Spec.Source.Format)
	if err != nil {
		return nil, fmt.Errorf("registry data validation failed: %w", err)
	}

	// Calculate hash using the SHA256 hash of the registry data
	hash := fmt.Sprintf("%x", sha256.Sum256(registryData))

	// Create and return fetch result with pre-calculated hash
	return NewFetchResult(reg, hash, mcpRegistry.Spec.Source.Format), nil
}

// CurrentHash returns the current hash of the source data after fetching the registry data
func (h *GitSourceHandler) CurrentHash(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (string, error) {
	registryData, err := h.fetchRegistryData(ctx, mcpRegistry)
	if err != nil {
		return "", fmt.Errorf("failed to fetch registry data: %w", err)
	}

	// Compute and return hash of the data
	hash := fmt.Sprintf("%x", sha256.Sum256(registryData))
	return hash, nil
}

// logMemoryStatsAfterOperation logs the memory stats after an operation
func logMemoryStatsAfterOperation(ctx context.Context, memBefore *runtime.MemStats) {
	// Log memory stats after cleanup and GC
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

	// Calculate additional memory metrics for container memory debugging
	// These help explain the difference between Go heap and container RSS
	sysMB := memAfter.Sys / (1024 * 1024)                   // Total memory from OS
	heapAllocMB := memAfter.HeapAlloc / (1024 * 1024)       // Bytes allocated on heap
	heapSysMB := memAfter.HeapSys / (1024 * 1024)           // Bytes obtained from OS for heap
	heapReleasedMB := memAfter.HeapReleased / (1024 * 1024) // Bytes returned to OS
	heapIdleMB := memAfter.HeapIdle / (1024 * 1024)         // Bytes in idle spans

	log.FromContext(ctx).Info("Memory stats after operation",
		"alloc_mb", allocAfterMB,
		"delta_mb", deltaMB,
		"sys_mb", sysMB,
		"heap_alloc_mb", heapAllocMB,
		"heap_sys_mb", heapSysMB,
		"heap_idle_mb", heapIdleMB,
		"heap_released_mb", heapReleasedMB)
}

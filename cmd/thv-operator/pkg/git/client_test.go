package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// createTestRepository creates a local Git repository with the given files for testing.
// Returns the repository path and a cleanup function.
func createTestRepository(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()

	// Create temporary directory for the repository
	repoDir, err := os.MkdirTemp("", "test-repo-*")
	if err != nil {
		t.Fatalf("Failed to create test repo dir: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(repoDir)
	}

	// Initialize Git repository
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to init test repository: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		cleanup()
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Create files
	for filePath, content := range files {
		fullPath := filepath.Join(repoDir, filePath)
		dir := filepath.Dir(fullPath)

		// Create parent directories if needed
		if err := os.MkdirAll(dir, 0700); err != nil {
			cleanup()
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}

		// Write file
		if err := os.WriteFile(fullPath, []byte(content), 0600); err != nil {
			cleanup()
			t.Fatalf("Failed to write file %s: %v", filePath, err)
		}

		// Add to Git
		if _, err := worktree.Add(filePath); err != nil {
			cleanup()
			t.Fatalf("Failed to add file %s: %v", filePath, err)
		}
	}

	// Commit files
	_, err = worktree.Commit("Test commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		cleanup()
		t.Fatalf("Failed to commit: %v", err)
	}

	return repoDir, cleanup
}

func TestNewDefaultGitClient(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	if client == nil {
		t.Fatal("NewDefaultGitClient() returned nil")
	}

	// Verify it's the correct type
	if _, ok := any(client).(*DefaultGitClient); !ok {
		t.Fatal("NewDefaultGitClient() did not return *DefaultGitClient")
	}
}

func TestDefaultGitClient_FetchFileSparse_InvalidURL(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       "invalid-url",
		Directory: tempDir,
	}

	content, err := client.FetchFileSparse(ctx, config, "registry.json")
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
	if content != nil {
		t.Error("Expected nil content for invalid URL")
	}
}

func TestDefaultGitClient_FetchFileSparse_NonExistentRepo(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       "https://github.com/nonexistent/nonexistent-repo-12345.git",
		Directory: tempDir,
	}

	content, err := client.FetchFileSparse(ctx, config, "registry.json")
	if err == nil {
		t.Error("Expected error for non-existent repository, got nil")
	}
	if content != nil {
		t.Error("Expected nil content for non-existent repository")
	}
}

func TestDefaultGitClient_FetchFileSparse_NonExistentFile(t *testing.T) {
	t.Parallel()

	// Create a local test repository
	sourceRepoDir, cleanup := createTestRepository(t, map[string]string{
		"existing.txt": "exists",
	})
	defer cleanup()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: tempDir,
	}

	content, err := client.FetchFileSparse(ctx, config, "nonexistent-file.json")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
	if content != nil {
		t.Error("Expected nil content for non-existent file")
	}

	if !strings.Contains(err.Error(), "failed to read file") {
		t.Errorf("Expected 'failed to read file' error, got: %v", err)
	}
}

func TestDefaultGitClient_FetchFileSparse_PathTraversal(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	sourceRepoDir, cleanup := createTestRepository(t, map[string]string{
		"existing.txt": "exists",
	})
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
		cleanup()
	})

	config := &CloneConfig{
		URL:       sourceRepoDir,
		Branch:    "main",
		Directory: tempDir,
	}

	tests := []struct {
		name     string
		filePath string
	}{
		{
			name:     "parent directory traversal",
			filePath: "../../../etc/passwd",
		},
		{
			name:     "current directory reference",
			filePath: "..",
		},
		{
			name:     "absolute path",
			filePath: "/etc/passwd",
		},
		{
			name:     "directory with parent reference",
			filePath: "pkg/../../etc/passwd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, err := client.FetchFileSparse(ctx, config, tt.filePath)
			if err == nil {
				t.Errorf("Expected error for path traversal attempt '%s', got nil", tt.filePath)
			}
			if content != nil {
				t.Errorf("Expected nil content for path traversal attempt '%s'", tt.filePath)
			}
			if err != nil && err.Error() == "" {
				t.Error("Error message should not be empty")
			}
		})
	}
}

func TestDefaultGitClient_FetchFileSparse_Success(t *testing.T) {
	t.Parallel()

	// Create a local test repository
	sourceRepoDir, cleanup := createTestRepository(t, map[string]string{
		"pkg/registry/data/registry.json": `{"name": "test-registry"}`,
	})
	defer cleanup()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: tempDir,
	}

	// Fetch a known file from the repository
	content, err := client.FetchFileSparse(ctx, config, "pkg/registry/data/registry.json")
	if err != nil {
		t.Fatalf("FetchFileSparse() failed: %v", err)
	}

	if len(content) == 0 {
		t.Error("Expected non-empty content")
	}

	// Verify the file content
	if !strings.Contains(string(content), "test-registry") {
		t.Errorf("Expected content to contain 'test-registry', got: %s", string(content))
	}

	// Verify temporary directory exists during operation
	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		t.Error("Temporary directory should exist during operation")
	}

	// Verify the file was checked out in the correct location
	expectedPath := filepath.Join(tempDir, "pkg/registry/data/registry.json")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("Expected file to exist at %s", expectedPath)
	}
}

func TestDefaultGitClient_FetchFileSparse_WithBranch(t *testing.T) {
	t.Parallel()

	// Create a local test repository
	sourceRepoDir, cleanup := createTestRepository(t, map[string]string{
		"README.md": "# Test Repository\n\nThis is a test.",
	})
	defer cleanup()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: tempDir,
	}

	content, err := client.FetchFileSparse(ctx, config, "README.md")
	if err != nil {
		t.Fatalf("FetchFileSparse() with branch failed: %v", err)
	}

	if len(content) == 0 {
		t.Error("Expected non-empty content for README.md")
	}

	if !strings.Contains(string(content), "Test Repository") {
		t.Errorf("Expected content to contain 'Test Repository', got: %s", string(content))
	}
}

func TestDefaultGitClient_FetchFileSparse_SparseCheckoutEfficiency(t *testing.T) {
	t.Parallel()

	// Create a test repository with multiple files in different directories
	sourceRepoDir, cleanup := createTestRepository(t, map[string]string{
		"pkg/registry/data/registry.json": `{"servers": []}`,
		"pkg/other/file1.txt":             "file1",
		"pkg/other/file2.txt":             "file2",
		"root.txt":                        "root",
		"another/deep/path/file.txt":      "deep",
	})
	defer cleanup()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: tempDir,
	}

	// Fetch a single file from a subdirectory
	_, err = client.FetchFileSparse(ctx, config, "pkg/registry/data/registry.json")
	if err != nil {
		t.Fatalf("FetchFileSparse() failed: %v", err)
	}

	// Count files in the working directory to verify sparse checkout worked
	fileCount := 0
	err = filepath.Walk(tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Base(path) != ".gitignore" {
			fileCount++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk directory: %v", err)
	}

	// Verify the target file exists
	targetFile := filepath.Join(tempDir, "pkg/registry/data/registry.json")
	if _, err := os.Stat(targetFile); err != nil {
		t.Error("Target file should exist in sparse checkout")
	}

	// Verify files from other directories don't exist (this is the key test for sparse checkout)
	otherFile := filepath.Join(tempDir, "pkg/other/file1.txt")
	if _, err := os.Stat(otherFile); !os.IsNotExist(err) {
		t.Error("Files from other directories should not exist in sparse checkout")
	}

	anotherFile := filepath.Join(tempDir, "another/deep/path/file.txt")
	if _, err := os.Stat(anotherFile); !os.IsNotExist(err) {
		t.Error("Files from deeply nested other directories should not exist in sparse checkout")
	}

	rootFile := filepath.Join(tempDir, "root.txt")
	if _, err := os.Stat(rootFile); !os.IsNotExist(err) {
		t.Error("Root level files should not exist when checking out subdirectory")
	}
}

func TestDefaultGitClient_FetchFileSparse_MultipleFiles(t *testing.T) {
	t.Parallel()

	// Create a test repository with multiple files
	sourceRepoDir, cleanup := createTestRepository(t, map[string]string{
		"pkg/registry/data/registry.json": `{"name": "registry"}`,
		"README.md":                       "# README",
	})
	defer cleanup()

	client := NewDefaultGitClient()
	ctx := context.Background()

	// Test fetching files from the same directory
	tempDir1, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir1)

	config1 := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: tempDir1,
	}

	content1, err := client.FetchFileSparse(ctx, config1, "pkg/registry/data/registry.json")
	if err != nil {
		t.Fatalf("First FetchFileSparse() failed: %v", err)
	}

	// Test fetching a different file from a different directory
	tempDir2, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir2)

	config2 := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: tempDir2,
	}

	content2, err := client.FetchFileSparse(ctx, config2, "README.md")
	if err != nil {
		t.Fatalf("Second FetchFileSparse() failed: %v", err)
	}

	if len(content1) == 0 || len(content2) == 0 {
		t.Error("Expected non-empty content for both files")
	}

	// Content should be different
	if string(content1) == string(content2) {
		t.Error("Expected different content for different files")
	}
}

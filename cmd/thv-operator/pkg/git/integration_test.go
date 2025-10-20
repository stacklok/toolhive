package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Integration tests use local test repositories created on-the-fly.
// These tests are fast (<1s each) and don't require network access.
// They test real Git operations with branches, commits, and tags.
//
// For E2E tests with remote repositories (slow), see e2e_test.go

// TestDefaultGitClient_FullWorkflow tests a complete Git workflow with a real repository
func TestDefaultGitClient_FullWorkflow(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for the source repository
	sourceRepoDir, err := os.MkdirTemp("", "git-source-*")
	if err != nil {
		t.Fatalf("Failed to create source temp dir: %v", err)
	}
	defer os.RemoveAll(sourceRepoDir)

	// Create a test repository
	sourceRepo, err := git.PlainInit(sourceRepoDir, false)
	if err != nil {
		t.Fatalf("Failed to init source repository: %v", err)
	}

	sourceWorkTree, err := sourceRepo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get source worktree: %v", err)
	}

	// Create a test file
	testFilePath := filepath.Join(sourceRepoDir, "registry.json")
	testContent := `{"name": "test-registry", "version": "1.0.0"}`
	err = os.WriteFile(testFilePath, []byte(testContent), 0600)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Add and commit the file
	_, err = sourceWorkTree.Add("registry.json")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	_, err = sourceWorkTree.Commit("Add registry file", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create a clone destination directory
	cloneDir, err := os.MkdirTemp("", "git-clone-*")
	if err != nil {
		t.Fatalf("Failed to create clone temp dir: %v", err)
	}
	defer os.RemoveAll(cloneDir)

	// Test the full workflow with new FetchFileSparse API
	client := NewDefaultGitClient()

	// Fetch the file using sparse checkout
	config := &CloneConfig{
		URL:       sourceRepoDir, // Use local path for testing
		Directory: cloneDir,
	}

	content, err := client.FetchFileSparse(context.Background(), config, "registry.json")
	if err != nil {
		t.Fatalf("Failed to fetch file: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("Expected content %q, got %q", testContent, string(content))
	}

	// Test FetchFileSparse with non-existent file
	_, err = client.FetchFileSparse(context.Background(), config, "nonexistent.json")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}

	// Cleanup is automatic - just remove the directory
	// (The new API doesn't require explicit cleanup)
}

// TestDefaultGitClient_CloneWithBranch tests cloning with a specific branch
func TestDefaultGitClient_CloneWithBranch(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for the source repository
	sourceRepoDir, err := os.MkdirTemp("", "git-source-branch-*")
	if err != nil {
		t.Fatalf("Failed to create source temp dir: %v", err)
	}
	defer os.RemoveAll(sourceRepoDir)

	// Create a test repository
	sourceRepo, err := git.PlainInit(sourceRepoDir, false)
	if err != nil {
		t.Fatalf("Failed to init source repository: %v", err)
	}

	sourceWorkTree, err := sourceRepo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get source worktree: %v", err)
	}

	// Create initial commit on main branch
	testFilePath := filepath.Join(sourceRepoDir, "main.txt")
	err = os.WriteFile(testFilePath, []byte("main branch content"), 0600)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err = sourceWorkTree.Add("main.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	_, err = sourceWorkTree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create and checkout feature branch
	featureBranchRef := plumbing.NewBranchReferenceName("feature")
	err = sourceWorkTree.Checkout(&git.CheckoutOptions{
		Branch: featureBranchRef,
		Create: true,
	})
	if err != nil {
		t.Fatalf("Failed to create and checkout feature branch: %v", err)
	}

	// Add content to feature branch
	featureFilePath := filepath.Join(sourceRepoDir, "feature.txt")
	err = os.WriteFile(featureFilePath, []byte("feature branch content"), 0600)
	if err != nil {
		t.Fatalf("Failed to write feature file: %v", err)
	}

	_, err = sourceWorkTree.Add("feature.txt")
	if err != nil {
		t.Fatalf("Failed to add feature file: %v", err)
	}

	_, err = sourceWorkTree.Commit("Add feature", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit feature: %v", err)
	}

	// Fetch from the feature branch using new API
	cloneDir, err := os.MkdirTemp("", "git-clone-branch-*")
	if err != nil {
		t.Fatalf("Failed to create clone temp dir: %v", err)
	}
	defer os.RemoveAll(cloneDir)

	client := NewDefaultGitClient()
	config := &CloneConfig{
		URL:       sourceRepoDir,
		Branch:    "feature",
		Directory: cloneDir,
	}

	// Fetch the feature-specific file
	content, err := client.FetchFileSparse(context.Background(), config, "feature.txt")
	if err != nil {
		t.Fatalf("Failed to fetch feature file: %v", err)
	}
	if string(content) != "feature branch content" {
		t.Errorf("Expected feature content, got %q", string(content))
	}

	// For the second fetch, use a new clone directory
	cloneDir2, err := os.MkdirTemp("", "git-clone-branch-2-*")
	if err != nil {
		t.Fatalf("Failed to create second clone temp dir: %v", err)
	}
	defer os.RemoveAll(cloneDir2)

	config2 := &CloneConfig{
		URL:       sourceRepoDir,
		Branch:    "feature",
		Directory: cloneDir2,
	}

	// Fetch main.txt from feature branch (should exist since feature was branched from main)
	mainContent, err := client.FetchFileSparse(context.Background(), config2, "main.txt")
	if err != nil {
		t.Fatalf("Failed to get main.txt content: %v", err)
	}
	if string(mainContent) != "main branch content" {
		t.Errorf("Expected main branch content, got %q", string(mainContent))
	}
}

// TestDefaultGitClient_CloneWithCommit tests cloning with a specific commit
func TestDefaultGitClient_CloneWithCommit(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for the source repository
	sourceRepoDir, err := os.MkdirTemp("", "git-source-commit-*")
	if err != nil {
		t.Fatalf("Failed to create source temp dir: %v", err)
	}
	defer os.RemoveAll(sourceRepoDir)

	// Create a test repository
	sourceRepo, err := git.PlainInit(sourceRepoDir, false)
	if err != nil {
		t.Fatalf("Failed to init source repository: %v", err)
	}

	sourceWorkTree, err := sourceRepo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get source worktree: %v", err)
	}

	// Create first commit
	file1Path := filepath.Join(sourceRepoDir, "file1.txt")
	err = os.WriteFile(file1Path, []byte("first commit"), 0600)
	if err != nil {
		t.Fatalf("Failed to write first file: %v", err)
	}

	_, err = sourceWorkTree.Add("file1.txt")
	if err != nil {
		t.Fatalf("Failed to add first file: %v", err)
	}

	firstCommit, err := sourceWorkTree.Commit("First commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to make first commit: %v", err)
	}

	// Create second commit
	file2Path := filepath.Join(sourceRepoDir, "file2.txt")
	err = os.WriteFile(file2Path, []byte("second commit"), 0600)
	if err != nil {
		t.Fatalf("Failed to write second file: %v", err)
	}

	_, err = sourceWorkTree.Add("file2.txt")
	if err != nil {
		t.Fatalf("Failed to add second file: %v", err)
	}

	_, err = sourceWorkTree.Commit("Second commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to make second commit: %v", err)
	}

	// Fetch file at the first commit using new API
	cloneDir, err := os.MkdirTemp("", "git-clone-commit-*")
	if err != nil {
		t.Fatalf("Failed to create clone temp dir: %v", err)
	}
	defer os.RemoveAll(cloneDir)

	client := NewDefaultGitClient()
	config := &CloneConfig{
		URL:       sourceRepoDir,
		Commit:    firstCommit.String(),
		Directory: cloneDir,
	}

	// Fetch the first file (should exist at first commit)
	content, err := client.FetchFileSparse(context.Background(), config, "file1.txt")
	if err != nil {
		t.Fatalf("Failed to fetch first file: %v", err)
	}
	if string(content) != "first commit" {
		t.Errorf("Expected first commit content, got %q", string(content))
	}

	// Try to fetch the second file (should not exist at first commit)
	_, err = client.FetchFileSparse(context.Background(), config, "file2.txt")
	if err == nil {
		t.Error("Expected error for file2.txt not present at first commit")
	}
}

// TestDefaultGitClient_WithSubdirectory tests fetching files from subdirectories
func TestDefaultGitClient_WithSubdirectory(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for the source repository
	sourceRepoDir, err := os.MkdirTemp("", "git-source-subdir-*")
	if err != nil {
		t.Fatalf("Failed to create source temp dir: %v", err)
	}
	defer os.RemoveAll(sourceRepoDir)

	// Create a test repository
	sourceRepo, err := git.PlainInit(sourceRepoDir, false)
	if err != nil {
		t.Fatalf("Failed to init source repository: %v", err)
	}

	sourceWorkTree, err := sourceRepo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get source worktree: %v", err)
	}

	// Create subdirectory and file
	subDir := filepath.Join(sourceRepoDir, "pkg", "registry")
	err = os.MkdirAll(subDir, 0700)
	if err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	testFilePath := filepath.Join(subDir, "data.json")
	testContent := `{"servers": ["server1", "server2"]}`
	err = os.WriteFile(testFilePath, []byte(testContent), 0600)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Add and commit the file
	_, err = sourceWorkTree.Add("pkg/registry/data.json")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	_, err = sourceWorkTree.Commit("Add data file", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Fetch file from subdirectory using sparse checkout
	cloneDir, err := os.MkdirTemp("", "git-clone-subdir-*")
	if err != nil {
		t.Fatalf("Failed to create clone temp dir: %v", err)
	}
	defer os.RemoveAll(cloneDir)

	client := NewDefaultGitClient()
	config := &CloneConfig{
		URL:       sourceRepoDir,
		Directory: cloneDir,
	}

	content, err := client.FetchFileSparse(context.Background(), config, "pkg/registry/data.json")
	if err != nil {
		t.Fatalf("Failed to fetch file from subdirectory: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("Expected content %q, got %q", testContent, string(content))
	}

	// Verify sparse checkout only checked out the necessary directory
	// (The working directory should not have all files, only the sparse checkout path)
	// This is verified by checking that only the pkg/registry directory exists
	pkgDir := filepath.Join(cloneDir, "pkg", "registry")
	if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
		t.Error("Expected pkg/registry directory to exist in sparse checkout")
	}
}

// TestDefaultGitClient_WithTag tests cloning with a specific tag
func TestDefaultGitClient_WithTag(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for the source repository
	sourceRepoDir, err := os.MkdirTemp("", "git-source-tag-*")
	if err != nil {
		t.Fatalf("Failed to create source temp dir: %v", err)
	}
	defer os.RemoveAll(sourceRepoDir)

	// Create a test repository
	sourceRepo, err := git.PlainInit(sourceRepoDir, false)
	if err != nil {
		t.Fatalf("Failed to init source repository: %v", err)
	}

	sourceWorkTree, err := sourceRepo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get source worktree: %v", err)
	}

	// Create first version
	file1Path := filepath.Join(sourceRepoDir, "version.txt")
	err = os.WriteFile(file1Path, []byte("v1.0.0"), 0600)
	if err != nil {
		t.Fatalf("Failed to write first file: %v", err)
	}

	_, err = sourceWorkTree.Add("version.txt")
	if err != nil {
		t.Fatalf("Failed to add first file: %v", err)
	}

	firstCommit, err := sourceWorkTree.Commit("First version", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to make first commit: %v", err)
	}

	// Create tag for v1.0.0
	tagRef := plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0.0"), firstCommit)
	err = sourceRepo.Storer.SetReference(tagRef)
	if err != nil {
		t.Fatalf("Failed to create tag: %v", err)
	}

	// Create second version (but don't tag it)
	err = os.WriteFile(file1Path, []byte("v2.0.0"), 0600)
	if err != nil {
		t.Fatalf("Failed to write second file: %v", err)
	}

	_, err = sourceWorkTree.Add("version.txt")
	if err != nil {
		t.Fatalf("Failed to add second file: %v", err)
	}

	_, err = sourceWorkTree.Commit("Second version", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to make second commit: %v", err)
	}

	// Fetch file at the v1.0.0 tag
	cloneDir, err := os.MkdirTemp("", "git-clone-tag-*")
	if err != nil {
		t.Fatalf("Failed to create clone temp dir: %v", err)
	}
	defer os.RemoveAll(cloneDir)

	client := NewDefaultGitClient()
	config := &CloneConfig{
		URL:       sourceRepoDir,
		Tag:       "v1.0.0",
		Directory: cloneDir,
	}

	content, err := client.FetchFileSparse(context.Background(), config, "version.txt")
	if err != nil {
		t.Fatalf("Failed to fetch file at tag: %v", err)
	}

	// Should get v1.0.0 content, not v2.0.0
	if string(content) != "v1.0.0" {
		t.Errorf("Expected v1.0.0 content, got %q", string(content))
	}
}

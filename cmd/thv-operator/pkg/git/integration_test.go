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

const (
	mainBranchName = "main"
)

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
	err = os.WriteFile(testFilePath, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Add and commit the file
	_, err = sourceWorkTree.Add("registry.json")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	commitHash, err := sourceWorkTree.Commit("Add registry file", &git.CommitOptions{
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

	// Test the full workflow
	client := NewDefaultGitClient()

	// Clone the repository
	config := &CloneConfig{
		URL:       sourceRepoDir, // Use local path for testing
		Directory: cloneDir,
	}

	repoInfo, err := client.Clone(context.Background(), config)
	if err != nil {
		t.Fatalf("Failed to clone repository: %v", err)
	}

	// Verify repository info was populated
	if repoInfo.Repository == nil {
		t.Error("Repository should not be nil")
	}
	if repoInfo.RemoteURL != sourceRepoDir {
		t.Errorf("Expected RemoteURL to be %s, got %s", sourceRepoDir, repoInfo.RemoteURL)
	}

	// Test GetCommitHash
	hash, err := client.GetCommitHash(repoInfo)
	if err != nil {
		t.Fatalf("Failed to get commit hash: %v", err)
	}
	if hash != commitHash.String() {
		t.Errorf("Expected commit hash %s, got %s", commitHash.String(), hash)
	}

	// Test GetFileContent
	content, err := client.GetFileContent(repoInfo, "registry.json")
	if err != nil {
		t.Fatalf("Failed to get file content: %v", err)
	}
	if string(content) != testContent {
		t.Errorf("Expected content %q, got %q", testContent, string(content))
	}

	// Test GetFileContent with non-existent file
	_, err = client.GetFileContent(repoInfo, "nonexistent.json")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}

	// Test Cleanup
	err = client.Cleanup(repoInfo)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}

	// Verify directory was removed
	_, err = os.Stat(cloneDir)
	if !os.IsNotExist(err) {
		t.Error("Expected clone directory to be removed")
	}
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

	// Create initial commit on mainBranchName branch
	testFilePath := filepath.Join(sourceRepoDir, "mainBranchName.txt")
	err = os.WriteFile(testFilePath, []byte("mainBranchName branch content"), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err = sourceWorkTree.Add("mainBranchName.txt")
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
	err = os.WriteFile(featureFilePath, []byte("feature branch content"), 0644)
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

	// Clone the feature branch
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

	repoInfo, err := client.Clone(context.Background(), config)
	if err != nil {
		t.Fatalf("Failed to clone feature branch: %v", err)
	}

	// Verify we're on the feature branch and have the feature file
	content, err := client.GetFileContent(repoInfo, "feature.txt")
	if err != nil {
		t.Fatalf("Failed to get feature file content: %v", err)
	}
	if string(content) != "feature branch content" {
		t.Errorf("Expected feature content, got %q", string(content))
	}

	// Verify we also have mainBranchName.txt (since feature branch was created from mainBranchName)
	mainBranchNameContent, err := client.GetFileContent(repoInfo, "mainBranchName.txt")
	if err != nil {
		t.Fatalf("Failed to get mainBranchName.txt content: %v", err)
	}
	if string(mainBranchNameContent) != "mainBranchName branch content" {
		t.Errorf("Expected mainBranchName branch content, got %q", string(mainBranchNameContent))
	}

	// Clean up
	err = client.Cleanup(repoInfo)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
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
	err = os.WriteFile(file1Path, []byte("first commit"), 0644)
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
	err = os.WriteFile(file2Path, []byte("second commit"), 0644)
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

	// Clone at the first commit
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

	repoInfo, err := client.Clone(context.Background(), config)
	if err != nil {
		t.Fatalf("Failed to clone at specific commit: %v", err)
	}

	// Verify we have the first file
	content, err := client.GetFileContent(repoInfo, "file1.txt")
	if err != nil {
		t.Fatalf("Failed to get first file content: %v", err)
	}
	if string(content) != "first commit" {
		t.Errorf("Expected first commit content, got %q", string(content))
	}

	// Verify we don't have the second file (since we're at first commit)
	_, err = client.GetFileContent(repoInfo, "file2.txt")
	if err == nil {
		t.Error("Expected error for file2.txt not present at first commit")
	}

	// Verify commit hash
	hash, err := client.GetCommitHash(repoInfo)
	if err != nil {
		t.Fatalf("Failed to get commit hash: %v", err)
	}
	if hash != firstCommit.String() {
		t.Errorf("Expected commit hash %s, got %s", firstCommit.String(), hash)
	}

	// Clean up
	err = client.Cleanup(repoInfo)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}
}

// TestDefaultGitClient_UpdateRepositoryInfo tests the updateRepositoryInfo method
func TestDefaultGitClient_UpdateRepositoryInfo(t *testing.T) {
	t.Parallel()
	// Create a temporary directory for the test repository
	tempRepoDir, err := os.MkdirTemp("", "git-repo-update-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempRepoDir)

	// Create a test repository
	repo, err := git.PlainInit(tempRepoDir, false)
	if err != nil {
		t.Fatalf("Failed to init repository: %v", err)
	}

	workTree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Create and commit a file to get a proper HEAD
	testFilePath := filepath.Join(tempRepoDir, "test.txt")
	err = os.WriteFile(testFilePath, []byte("test content"), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err = workTree.Add("test.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	commitHash, err := workTree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create a branch
	branchRef := plumbing.NewBranchReferenceName(mainBranchName)
	err = repo.Storer.SetReference(plumbing.NewHashReference(branchRef, commitHash))
	if err != nil {
		t.Fatalf("Failed to set branch reference: %v", err)
	}

	// Checkout the branch to set HEAD properly
	err = workTree.Checkout(&git.CheckoutOptions{
		Branch: branchRef,
	})
	if err != nil {
		t.Fatalf("Failed to checkout branch: %v", err)
	}

	client := NewDefaultGitClient()
	repoInfo := &RepositoryInfo{
		Repository: repo,
	}

	// Test updateRepositoryInfo
	err = client.updateRepositoryInfo(repoInfo)
	if err != nil {
		t.Fatalf("updateRepositoryInfo failed: %v", err)
	}

	// Verify the repository info was updated correctly
	if repoInfo.CurrentCommit != commitHash.String() {
		t.Errorf("Expected CurrentCommit to be %s, got %s", commitHash.String(), repoInfo.CurrentCommit)
	}
	if repoInfo.Branch != mainBranchName {
		t.Errorf("Expected Branch to be %q, got %s", mainBranchName, repoInfo.Branch)
	}
}

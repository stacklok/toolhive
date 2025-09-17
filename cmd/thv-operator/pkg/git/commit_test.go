package git

import (
	"context"
	"os"
	"testing"
)

// TestDefaultGitClient_CloneSpecificCommit_RealRepo tests cloning a specific commit from a real repository
func TestDefaultGitClient_CloneSpecificCommit_RealRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real repository test in short mode")
	}

	client := NewDefaultGitClient()
	ctx := context.Background()

	// Create temporary directory for cloning
	tempDir, err := os.MkdirTemp("", "git-commit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with a known commit from a public repository
	config := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive",
		Commit:    "395b6ba11bdd60b615a9630a66dcede2abcfbb48", // Known valid commit
		Directory: tempDir,
	}

	repoInfo, err := client.Clone(ctx, config)
	if err != nil {
		t.Fatalf("Failed to clone repository at specific commit: %v", err)
	}

	// Verify we're at the correct commit
	hash, err := client.GetCommitHash(repoInfo)
	if err != nil {
		t.Fatalf("Failed to get commit hash: %v", err)
	}

	if hash != config.Commit {
		t.Errorf("Expected commit hash %s, got %s", config.Commit, hash)
	}

	// Verify we can read the registry file at this commit
	content, err := client.GetFileContent(repoInfo, "pkg/registry/data/registry.json")
	if err != nil {
		t.Fatalf("Failed to get registry file content: %v", err)
	}

	if len(content) == 0 {
		t.Error("Expected registry file content, got empty content")
	}

	// Clean up
	err = client.Cleanup(repoInfo)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}

	// Verify directory was removed
	_, err = os.Stat(tempDir)
	if !os.IsNotExist(err) {
		t.Error("Expected directory to be removed after cleanup")
	}
}

// TestDefaultGitClient_CloneInvalidCommit tests error handling for invalid commit hash
func TestDefaultGitClient_CloneInvalidCommit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real repository test in short mode")
	}

	client := NewDefaultGitClient()
	ctx := context.Background()

	// Create temporary directory for cloning
	tempDir, err := os.MkdirTemp("", "git-invalid-commit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with an invalid commit hash
	config := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive",
		Commit:    "f4da6f2", // This is the original invalid commit that was causing the error
		Directory: tempDir,
	}

	repoInfo, err := client.Clone(ctx, config)
	if err == nil {
		t.Error("Expected error for invalid commit hash, got nil")
		if repoInfo != nil {
			client.Cleanup(repoInfo)
		}
	}

	if repoInfo != nil {
		t.Error("Expected nil repoInfo for invalid commit")
	}
}

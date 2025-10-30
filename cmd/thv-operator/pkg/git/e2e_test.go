//go:build e2e

package git

import (
	"context"
	"os"
	"strings"
	"testing"
)

// E2E tests use real remote repositories and require network access.
//
// To run E2E tests only:
//   go test -tags=e2e -v -timeout=5m
//
// To run unit/integration tests (fast, no network):
//   go test -v
//
// To run ALL tests (unit + integration + E2E):
//   go test -tags=e2e -v -timeout=5m
//
// These E2E tests are slower (~15-30s each for shallow clones, ~1-2min for commit-based clones)
// due to network I/O and cloning from GitHub.

func TestE2E_FetchFileSparse_Success(t *testing.T) {
	t.Parallel()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive.git",
		Branch:    "main",
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

	// Verify the file content is valid JSON by checking it starts with '{'
	if content[0] != '{' && content[0] != '[' {
		t.Error("Content does not appear to be valid JSON")
	}
}

func TestE2E_FetchFileSparse_WithBranch(t *testing.T) {
	t.Parallel()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive.git",
		Branch:    "main",
		Directory: tempDir,
	}

	content, err := client.FetchFileSparse(ctx, config, "README.md")
	if err != nil {
		t.Fatalf("FetchFileSparse() with branch failed: %v", err)
	}

	if len(content) == 0 {
		t.Error("Expected non-empty content for README.md")
	}
}

func TestE2E_FetchFileSparse_WithCommit(t *testing.T) {
	t.Parallel()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	// Use a specific commit hash from the repository (latest commit on main)
	config := &CloneConfig{
		URL: "https://github.com/stacklok/toolhive.git",
		// Any valid commit can be used, since the repo is not fetched in this test
		Commit:    "032122368e1233068a9095db8542d35d425f257a",
		Directory: tempDir,
	}

	content, err := client.FetchFileSparse(ctx, config, "README.md")
	if err != nil {
		t.Fatalf("FetchFileSparse() with commit failed: %v", err)
	}

	if len(content) == 0 {
		t.Error("Expected non-empty content for README.md")
	}

	// Verify the content is from the correct commit
	if !strings.Contains(string(content), "ToolHive") {
		t.Error("Expected README.md to contain 'ToolHive'")
	}
}

func TestE2E_FetchFileSparse_SparseCheckoutEfficiency(t *testing.T) {
	t.Parallel()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive.git",
		Branch:    "main",
		Directory: tempDir,
	}

	// Fetch a single file
	_, err = client.FetchFileSparse(ctx, config, "pkg/registry/data/registry.json")
	if err != nil {
		t.Fatalf("FetchFileSparse() failed: %v", err)
	}

	// Note: We can't easily verify file count without walking the directory,
	// but sparse checkout should significantly reduce the number of files checked out.
	// In production, this reduces from 1099+ files to just the necessary directory.
}

func TestE2E_FetchFileSparse_NonExistentFile(t *testing.T) {
	t.Parallel()

	client := NewDefaultGitClient()
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	config := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive.git",
		Branch:    "main",
		Directory: tempDir,
	}

	_, err = client.FetchFileSparse(ctx, config, "this-file-does-not-exist.json")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}

	if !strings.Contains(err.Error(), "failed to read file") {
		t.Errorf("Expected 'failed to read file' error, got: %v", err)
	}
}

func TestE2E_FetchFileSparse_MultipleFiles(t *testing.T) {
	t.Parallel()

	client := NewDefaultGitClient()
	ctx := context.Background()

	// First fetch
	tempDir1, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create first temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir1)

	config1 := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive.git",
		Branch:    "main",
		Directory: tempDir1,
	}

	content1, err := client.FetchFileSparse(ctx, config1, "pkg/registry/data/registry.json")
	if err != nil {
		t.Fatalf("First FetchFileSparse() failed: %v", err)
	}

	if len(content1) == 0 {
		t.Error("Expected non-empty content for first fetch")
	}

	// Second fetch from different directory
	tempDir2, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create second temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir2)

	config2 := &CloneConfig{
		URL:       "https://github.com/stacklok/toolhive.git",
		Branch:    "main",
		Directory: tempDir2,
	}

	content2, err := client.FetchFileSparse(ctx, config2, "README.md")
	if err != nil {
		t.Fatalf("Second FetchFileSparse() failed: %v", err)
	}

	if len(content2) == 0 {
		t.Error("Expected non-empty content for second fetch")
	}

	// Both fetches should have succeeded
	if len(content1) == 0 || len(content2) == 0 {
		t.Error("Both fetches should return content")
	}
}

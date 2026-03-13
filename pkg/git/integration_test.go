// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mainBranchName = "main"

// initTestRepo creates a local git repo with an initial commit containing the given files.
// Returns the repo directory path.
func initTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
		_, err = wt.Add(name)
		require.NoError(t, err)
	}

	_, err = wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	require.NoError(t, err)

	return dir
}

// TestDefaultGitClient_FullWorkflow exercises the complete clone → read → cleanup lifecycle
// against a local git repository to verify end-to-end correctness.
func TestDefaultGitClient_FullWorkflow(t *testing.T) {
	t.Parallel()

	testContent := `{"name": "test-registry", "version": "1.0.0"}`
	repoDir := initTestRepo(t, map[string]string{"registry.json": testContent})

	client := NewDefaultGitClient()
	ctx := t.Context()

	// Clone the local repository
	repoInfo, err := client.Clone(ctx, &CloneConfig{URL: repoDir})
	require.NoError(t, err)

	// Verify repository info was populated correctly
	require.NotNil(t, repoInfo.Repository)
	assert.Equal(t, repoDir, repoInfo.RemoteURL)

	// Retrieve and verify file content matches what was committed
	content, err := client.GetFileContent(repoInfo, "registry.json")
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))

	// Non-existent files should return an error
	_, err = client.GetFileContent(repoInfo, "nonexistent.json")
	require.Error(t, err)

	// Cleanup should release all in-memory resources
	require.NoError(t, client.Cleanup(ctx, repoInfo))
}

// TestDefaultGitClient_CloneWithBranch verifies that cloning with a specific branch
// checks out only that branch's content, including files inherited from its parent.
func TestDefaultGitClient_CloneWithBranch(t *testing.T) {
	t.Parallel()

	// Create repo with initial commit on main branch
	repoDir := initTestRepo(t, map[string]string{"main.txt": "main branch content"})

	// Create and checkout feature branch
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	}))

	// Add content to feature branch
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature branch content"), 0644))
	_, err = wt.Add("feature.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Add feature", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	require.NoError(t, err)

	// Clone the feature branch
	client := NewDefaultGitClient()

	repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: repoDir, Branch: "feature"})
	require.NoError(t, err)

	// Verify we're on the feature branch and have the feature file
	content, err := client.GetFileContent(repoInfo, "feature.txt")
	require.NoError(t, err)
	assert.Equal(t, "feature branch content", string(content))

	// Clean up
	require.NoError(t, client.Cleanup(t.Context(), repoInfo))
}

// TestDefaultGitClient_CloneWithTag verifies that cloning with a specific tag
// checks out the tree at the tagged commit.
func TestDefaultGitClient_CloneWithTag(t *testing.T) {
	t.Parallel()

	// Create repo with initial commit
	repoDir := initTestRepo(t, map[string]string{"v1.txt": "tagged content"})

	// Create a tag pointing to HEAD
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)

	_, err = repo.CreateTag("v1.0.0", head.Hash(), nil)
	require.NoError(t, err)

	// Add a second commit after the tag
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "v2.txt"), []byte("post-tag content"), 0644))
	_, err = wt.Add("v2.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Post-tag commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	require.NoError(t, err)

	// Clone at the tag — should have v1.txt but not v2.txt
	client := NewDefaultGitClient()

	repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: repoDir, Tag: "v1.0.0"})
	require.NoError(t, err)

	content, err := client.GetFileContent(repoInfo, "v1.txt")
	require.NoError(t, err)
	assert.Equal(t, "tagged content", string(content))

	// v2.txt was committed after the tag, so it should not be present
	_, err = client.GetFileContent(repoInfo, "v2.txt")
	require.Error(t, err)

	require.NoError(t, client.Cleanup(t.Context(), repoInfo))
}

// TestDefaultGitClient_CloneWithCommit verifies that cloning at a specific commit
// checks out exactly the tree at that point — later commits' files must not be visible.
func TestDefaultGitClient_CloneWithCommit(t *testing.T) {
	t.Parallel()

	// Create repo with first commit
	repoDir := initTestRepo(t, map[string]string{"file1.txt": "first commit"})

	// Create second commit
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Record first commit hash before adding second commit
	head, err := repo.Head()
	require.NoError(t, err)
	firstCommit := head.Hash()

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "file2.txt"), []byte("second commit"), 0644))
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Second commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	require.NoError(t, err)

	// Clone at the first commit
	client := NewDefaultGitClient()

	repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: repoDir, Commit: firstCommit.String()})
	require.NoError(t, err)

	// Verify we have the first file
	content, err := client.GetFileContent(repoInfo, "file1.txt")
	require.NoError(t, err)
	assert.Equal(t, "first commit", string(content))

	// Verify we don't have the second file (since we're at first commit)
	_, err = client.GetFileContent(repoInfo, "file2.txt")
	require.Error(t, err)

	// Clean up
	require.NoError(t, client.Cleanup(t.Context(), repoInfo))
}

// TestDefaultGitClient_UpdateRepositoryInfo verifies that updateRepositoryInfo
// correctly populates the Branch field from the repository's HEAD reference.
func TestDefaultGitClient_UpdateRepositoryInfo(t *testing.T) {
	t.Parallel()

	repoDir := initTestRepo(t, map[string]string{"test.txt": "test content"})

	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create and checkout a named branch
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewBranchReferenceName(mainBranchName)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(branchRef, head.Hash())))
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{Branch: branchRef}))

	client := NewDefaultGitClient()
	repoInfo := &RepositoryInfo{Repository: repo}

	// Test updateRepositoryInfo
	require.NoError(t, client.updateRepositoryInfo(repoInfo))

	// Verify the repository info was updated correctly
	assert.Equal(t, mainBranchName, repoInfo.Branch)
}

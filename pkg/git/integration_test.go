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

func TestDefaultGitClient_FullWorkflow(t *testing.T) {
	t.Parallel()

	testContent := `{"name": "test-registry", "version": "1.0.0"}`
	repoDir := initTestRepo(t, map[string]string{"registry.json": testContent})

	client := NewDefaultGitClient()
	ctx := t.Context()

	repoInfo, err := client.Clone(ctx, &CloneConfig{URL: repoDir})
	require.NoError(t, err)
	require.NotNil(t, repoInfo.Repository)
	assert.Equal(t, repoDir, repoInfo.RemoteURL)

	// Read existing file
	content, err := client.GetFileContent(repoInfo, "registry.json")
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))

	// Non-existent file
	_, err = client.GetFileContent(repoInfo, "nonexistent.json")
	require.Error(t, err)

	// HeadCommitHash
	hash, err := HeadCommitHash(repoInfo)
	require.NoError(t, err)
	assert.Len(t, hash, 40)

	// Cleanup
	require.NoError(t, client.Cleanup(ctx, repoInfo))
}

func TestDefaultGitClient_CloneWithBranch(t *testing.T) {
	t.Parallel()

	// Create repo with main branch
	repoDir := initTestRepo(t, map[string]string{"main.txt": "main branch content"})

	// Add a feature branch
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	}))

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

	content, err := client.GetFileContent(repoInfo, "feature.txt")
	require.NoError(t, err)
	assert.Equal(t, "feature branch content", string(content))

	require.NoError(t, client.Cleanup(t.Context(), repoInfo))
}

func TestDefaultGitClient_CloneWithCommit(t *testing.T) {
	t.Parallel()

	// Create repo with first commit
	repoDir := initTestRepo(t, map[string]string{"file1.txt": "first commit"})

	// Add second commit
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

	// Clone at first commit
	client := NewDefaultGitClient()

	repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: repoDir, Commit: firstCommit.String()})
	require.NoError(t, err)

	// file1.txt should exist at first commit
	content, err := client.GetFileContent(repoInfo, "file1.txt")
	require.NoError(t, err)
	assert.Equal(t, "first commit", string(content))

	// file2.txt should NOT exist at first commit
	_, err = client.GetFileContent(repoInfo, "file2.txt")
	require.Error(t, err)

	require.NoError(t, client.Cleanup(t.Context(), repoInfo))
}

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

	require.NoError(t, client.updateRepositoryInfo(repoInfo))
	assert.Equal(t, mainBranchName, repoInfo.Branch)
}

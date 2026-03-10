// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/git"
)

const validSkillMD = `---
name: my-skill
description: A test skill
version: "1.0.0"
---
# My Skill

This is a test skill.
`

// createTestRepo creates a local git repo with a skill at the given path.
// Returns the repo directory path.
func createTestRepo(t *testing.T, skillPath string, skillMD string) string {
	t.Helper()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create SKILL.md at the specified path
	fullDir := dir
	if skillPath != "" {
		fullDir = filepath.Join(dir, skillPath)
		require.NoError(t, os.MkdirAll(fullDir, 0755))
	}

	skillMDPath := filepath.Join(fullDir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillMDPath, []byte(skillMD), 0644))

	// Add a companion file
	readmePath := filepath.Join(fullDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test Skill"), 0644))

	// Stage and commit
	_, err = wt.Add(".")
	require.NoError(t, err)

	_, err = wt.Commit("Add test skill", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	return dir
}

// createTaggedTestRepo creates a local git repo with a tag, for testing tag-based clones.
func createTaggedTestRepo(t *testing.T, skillMD, tagName string) string {
	t.Helper()

	dir := createTestRepo(t, "", skillMD)
	repo, err := gogit.PlainOpen(dir)
	require.NoError(t, err)

	head, err := repo.Head()
	require.NoError(t, err)

	_, err = repo.CreateTag(tagName, head.Hash(), nil)
	require.NoError(t, err)

	return dir
}

func TestResolver_Resolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		skillPath   string
		skillMD     string
		ref         *GitReference
		expectError string
		expectName  string
		expectFiles int
	}{
		{
			name:        "skill at repo root",
			skillPath:   "",
			skillMD:     validSkillMD,
			ref:         &GitReference{Path: ""},
			expectName:  "my-skill",
			expectFiles: 2, // SKILL.md + README.md
		},
		{
			name:        "skill in subdirectory",
			skillPath:   "skills/my-skill",
			skillMD:     validSkillMD,
			ref:         &GitReference{Path: "skills/my-skill"},
			expectName:  "my-skill",
			expectFiles: 2,
		},
		{
			name:        "invalid SKILL.md",
			skillPath:   "",
			skillMD:     "not valid frontmatter",
			ref:         &GitReference{Path: ""},
			expectError: "parsing SKILL.md",
		},
		{
			name:      "invalid skill name",
			skillPath: "",
			skillMD: `---
name: INVALID
description: bad name
---
`,
			ref:         &GitReference{Path: ""},
			expectError: "invalid skill name",
		},
		{
			name:        "nonexistent path",
			skillPath:   "",
			skillMD:     validSkillMD,
			ref:         &GitReference{Path: "does/not/exist"},
			expectError: "reading SKILL.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoDir := createTestRepo(t, tt.skillPath, tt.skillMD)
			gitClient := git.NewDefaultGitClient()
			resolver := NewResolver(WithGitClient(gitClient))

			// Override the URL to point to the local repo
			ref := *tt.ref
			ref.URL = repoDir

			result, err := resolver.Resolve(t.Context(), &ref)

			if tt.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.expectName, result.SkillConfig.Name)
			assert.Len(t, result.Files, tt.expectFiles)
			assert.NotEmpty(t, result.CommitHash)
			assert.Len(t, result.CommitHash, 40)
		})
	}
}

func TestResolver_Resolve_TagRef(t *testing.T) {
	t.Parallel()

	repoDir := createTaggedTestRepo(t, validSkillMD, "v1.0.0")
	gitClient := git.NewDefaultGitClient()
	resolver := NewResolver(WithGitClient(gitClient))

	ref := &GitReference{URL: repoDir, Ref: "v1.0.0"}

	result, err := resolver.Resolve(t.Context(), ref)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "my-skill", result.SkillConfig.Name)
	assert.NotEmpty(t, result.CommitHash)
}

func TestResolver_Resolve_CommitRef(t *testing.T) {
	t.Parallel()

	repoDir := createTestRepo(t, "", validSkillMD)

	// Get the HEAD commit hash
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commitHash := head.Hash().String()

	gitClient := git.NewDefaultGitClient()
	resolver := NewResolver(WithGitClient(gitClient))

	ref := &GitReference{URL: repoDir, Ref: commitHash}

	result, err := resolver.Resolve(t.Context(), ref)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "my-skill", result.SkillConfig.Name)
	assert.Equal(t, commitHash, result.CommitHash)
}

func TestResolver_Resolve_MissingSkillMD(t *testing.T) {
	t.Parallel()

	// Create a repo without SKILL.md
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	readmePath := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# No skill here"), 0644))

	_, err = wt.Add(".")
	require.NoError(t, err)

	_, err = wt.Commit("No skill", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	resolver := NewResolver(WithGitClient(git.NewDefaultGitClient()))
	ref := &GitReference{URL: dir}

	result, err := resolver.Resolve(t.Context(), ref)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading SKILL.md")
	assert.Nil(t, result)
}

func TestResolver_Resolve_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Use a mock client that blocks on Clone until context is cancelled,
	// so we deterministically test context cancellation without network access.
	mockClient := &blockingCloneClient{}
	resolver := NewResolver(WithGitClient(mockClient))
	ref := &GitReference{URL: "https://example.com/org/repo"}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := resolver.Resolve(ctx, ref)
	require.Error(t, err)
	assert.Nil(t, result)
}

// blockingCloneClient is a mock git.Client that respects context cancellation.
type blockingCloneClient struct{}

func (*blockingCloneClient) Clone(ctx context.Context, _ *git.CloneConfig) (*git.RepositoryInfo, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("clone cancelled: %w", ctx.Err())
}

func (*blockingCloneClient) GetFileContent(_ *git.RepositoryInfo, _ string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (*blockingCloneClient) HeadCommitHash(_ *git.RepositoryInfo) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (*blockingCloneClient) Cleanup(_ context.Context, _ *git.RepositoryInfo) error {
	return nil
}

func TestResolver_SemverDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref      string
		wantTag  bool
		wantHex  bool
		wantBrnc bool
	}{
		{ref: "v1.0.0", wantTag: true},
		{ref: "v1.0", wantTag: true},
		{ref: "v1.2.3-rc1", wantTag: true},
		{ref: "v2", wantBrnc: true}, // no dot, treated as branch
		{ref: "main", wantBrnc: true},
		{ref: "feature/foo", wantBrnc: true},
		{ref: "abc123", wantBrnc: true},          // too short for commit hash
		{ref: "v1-beta-branch", wantBrnc: true},  // not semver, treated as branch
		{ref: "v2-experimental", wantBrnc: true}, // not semver, treated as branch
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			isCommit := len(tt.ref) == 40 && isHex(tt.ref)
			isSemver := semverLike.MatchString(tt.ref)

			assert.Equal(t, tt.wantHex, isCommit, "commit detection")
			assert.Equal(t, tt.wantTag, isSemver, "semver detection")
			if !isCommit && !isSemver {
				assert.True(t, tt.wantBrnc, "should be treated as branch")
			}
		})
	}
}

func TestIsHex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected bool
	}{
		{"", false},
		{"abc123", true},
		{"ABCDEF", true},
		{"0123456789abcdef", true},
		{"xyz", false},
		{"abc 123", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, isHex(tt.input))
		})
	}
}

func TestResolver_Resolve_BranchRef(t *testing.T) {
	t.Parallel()

	validSkillMD := `---
name: my-skill
description: A test skill
version: "1.0.0"
---
# My Skill
`
	// Create a repo with a feature branch
	repoDir := createTestRepo(t, "", validSkillMD)
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create a new branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature/test"),
		Create: true,
	})
	require.NoError(t, err)

	gitClient := git.NewDefaultGitClient()
	resolver := NewResolver(WithGitClient(gitClient))

	ref := &GitReference{URL: repoDir, Ref: "feature/test"}

	result, err := resolver.Resolve(t.Context(), ref)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "my-skill", result.SkillConfig.Name)
}

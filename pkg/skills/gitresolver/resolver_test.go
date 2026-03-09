// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/git"
)

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

func TestResolver_Resolve(t *testing.T) {
	t.Parallel()

	validSkillMD := `---
name: my-skill
description: A test skill
version: "1.0.0"
---
# My Skill

This is a test skill.
`

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

	resolver := NewResolver(WithGitClient(git.NewDefaultGitClient()))
	ref := &GitReference{URL: "https://github.com/nonexistent/nonexistent-repo-12345"}

	// Create a context derived from the test context and cancel it immediately.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Should fail from context cancellation (or network error).
	result, err := resolver.Resolve(ctx, ref)
	require.Error(t, err)
	assert.Nil(t, result)
}

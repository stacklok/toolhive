// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDefaultGitClient(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	require.NotNil(t, client)
	assert.IsType(t, &DefaultGitClient{}, client)
}

func TestDefaultGitClient_Clone_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config CloneConfig
	}{
		{name: "invalid URL", config: CloneConfig{URL: "invalid-url"}},
		{name: "conflicting branch and tag", config: CloneConfig{URL: "https://example.com/repo.git", Branch: "main", Tag: "v1.0"}},
		{name: "conflicting branch and commit", config: CloneConfig{URL: "https://example.com/repo.git", Branch: "main", Commit: "abc123"}},
		{name: "conflicting tag and commit", config: CloneConfig{URL: "https://example.com/repo.git", Tag: "v1.0", Commit: "abc123"}},
		{name: "all three refs set", config: CloneConfig{URL: "https://example.com/repo.git", Branch: "main", Tag: "v1.0", Commit: "abc123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewDefaultGitClient()

			repoInfo, err := client.Clone(t.Context(), &tt.config)
			require.Error(t, err)
			assert.Nil(t, repoInfo)
		})
	}
}

func TestDefaultGitClient_Cleanup_NilInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		repoInfo *RepositoryInfo
	}{
		{name: "nil repoInfo", repoInfo: nil},
		{name: "nil repository", repoInfo: &RepositoryInfo{Repository: nil}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewDefaultGitClient()

			err := client.Cleanup(t.Context(), tt.repoInfo)
			require.Error(t, err)
		})
	}
}

func TestDefaultGitClient_GetFileContent_NoRepo(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()

	content, err := client.GetFileContent(&RepositoryInfo{Repository: nil}, "test.txt")
	require.Error(t, err)
	assert.Nil(t, content)
}

func TestDefaultGitClient_GetFileContent_PathTraversal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "dot-dot traversal", path: "../etc/passwd"},
		{name: "absolute path", path: "/etc/passwd"},
		{name: "null byte", path: "file\x00.txt"},
		{name: "mid-path traversal", path: "foo/../../etc/passwd"},
	}

	// Use a non-nil repository stub to get past the nil check
	repoDir := initTestRepo(t, map[string]string{"dummy.txt": "x"})
	client := NewDefaultGitClient()
	repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: repoDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Cleanup(t.Context(), repoInfo) })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, err := client.GetFileContent(repoInfo, tt.path)
			require.ErrorIs(t, err, ErrInvalidFilePath)
			assert.Nil(t, content)
		})
	}
}

func TestDefaultGitClient_HeadCommitHash_NilInputs(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()

	tests := []struct {
		name     string
		repoInfo *RepositoryInfo
	}{
		{name: "nil RepositoryInfo", repoInfo: nil},
		{name: "nil Repository", repoInfo: &RepositoryInfo{Repository: nil}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hash, err := client.HeadCommitHash(tt.repoInfo)
			require.ErrorIs(t, err, ErrNilRepository)
			assert.Empty(t, hash)
		})
	}
}

func TestDefaultGitClient_HeadCommitHash_Valid(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()

	repoDir := initTestRepo(t, map[string]string{"test.txt": "content"})
	repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: repoDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Cleanup(t.Context(), repoInfo) })

	hash, err := client.HeadCommitHash(repoInfo)
	require.NoError(t, err)
	assert.Len(t, hash, 40, "commit hash should be 40 hex chars")
	assert.True(t, isAllHex(hash), "commit hash should be all hex")
}

// isAllHex checks if s is a non-empty lowercase hex string.
func isAllHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func TestCloneConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  CloneConfig
		wantErr bool
	}{
		{name: "URL only", config: CloneConfig{URL: "https://example.com/repo.git"}, wantErr: false},
		{name: "branch only", config: CloneConfig{URL: "u", Branch: "main"}, wantErr: false},
		{name: "tag only", config: CloneConfig{URL: "u", Tag: "v1"}, wantErr: false},
		{name: "commit only", config: CloneConfig{URL: "u", Commit: "abc"}, wantErr: false},
		{name: "branch+tag", config: CloneConfig{URL: "u", Branch: "main", Tag: "v1"}, wantErr: true},
		{name: "branch+commit", config: CloneConfig{URL: "u", Branch: "main", Commit: "abc"}, wantErr: true},
		{name: "tag+commit", config: CloneConfig{URL: "u", Tag: "v1", Commit: "abc"}, wantErr: true},
		{name: "all three", config: CloneConfig{URL: "u", Branch: "main", Tag: "v1", Commit: "abc"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.validate()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidCloneConfig)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

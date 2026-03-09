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
		name string
		url  string
	}{
		{name: "invalid URL", url: "invalid-url"},
		{name: "non-existent repo", url: "https://github.com/nonexistent/nonexistent.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewDefaultGitClient()

			repoInfo, err := client.Clone(t.Context(), &CloneConfig{URL: tt.url})
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

func TestHeadCommitHash_NilInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		repoInfo *RepositoryInfo
	}{
		{name: "nil repoInfo", repoInfo: nil},
		{name: "nil repository", repoInfo: &RepositoryInfo{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hash, err := HeadCommitHash(tt.repoInfo)
			require.Error(t, err)
			assert.Empty(t, hash)
		})
	}
}

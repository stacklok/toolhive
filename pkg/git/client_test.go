// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"testing"
)

func TestNewDefaultGitClient(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	if client == nil {
		t.Fatal("NewDefaultGitClient() returned nil")
	}

	// Verify it's the correct type
	if _, ok := any(client).(*DefaultGitClient); !ok {
		t.Fatal("NewDefaultGitClient() did not return *DefaultGitClient")
	}
}

func TestDefaultGitClient_Clone_InvalidURL(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()

	config := &CloneConfig{
		URL: "invalid-url",
	}

	repoInfo, err := client.Clone(t.Context(), config)
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
	if repoInfo != nil {
		t.Error("Expected nil repoInfo for invalid URL")
	}
}

func TestDefaultGitClient_Clone_NonExistentRepo(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()

	config := &CloneConfig{
		URL: "https://github.com/nonexistent/nonexistent.git",
	}

	repoInfo, err := client.Clone(t.Context(), config)
	if err == nil {
		t.Error("Expected error for non-existent repository, got nil")
	}
	if repoInfo != nil {
		t.Error("Expected nil repoInfo for non-existent repository")
	}
}

func TestDefaultGitClient_Cleanup_NilRepoInfo(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()

	err := client.Cleanup(t.Context(), nil)
	if err == nil {
		t.Errorf("Expected error for nil repoInfo, got nil")
	}
}

func TestDefaultGitClient_Cleanup_NilRepository(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	repoInfo := &RepositoryInfo{
		Repository: nil,
	}

	err := client.Cleanup(t.Context(), repoInfo)
	if err == nil {
		t.Errorf("Expected error for nil repository, got nil")
	}
}

func TestDefaultGitClient_GetFileContent_NoRepo(t *testing.T) {
	t.Parallel()
	client := NewDefaultGitClient()
	repoInfo := &RepositoryInfo{
		Repository: nil,
	}

	content, err := client.GetFileContent(repoInfo, "test.txt")
	if err == nil {
		t.Error("Expected error for nil repository, got nil")
	}
	if content != nil {
		t.Error("Expected nil content for nil repository")
	}
}

func TestHeadCommitHash_NilRepo(t *testing.T) {
	t.Parallel()

	hash, err := HeadCommitHash(nil)
	if err == nil {
		t.Error("Expected error for nil repoInfo")
	}
	if hash != "" {
		t.Error("Expected empty hash for nil repoInfo")
	}

	hash, err = HeadCommitHash(&RepositoryInfo{})
	if err == nil {
		t.Error("Expected error for nil repository")
	}
	if hash != "" {
		t.Error("Expected empty hash for nil repository")
	}
}

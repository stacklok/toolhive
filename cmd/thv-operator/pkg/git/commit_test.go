// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package git

import (
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// TestDefaultGitClient_CloneSpecificCommit_RealRepo tests cloning a specific commit from a real repository
func TestDefaultGitClient_CloneSpecificCommit_RealRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real repository test in short mode")
	}

	client := NewDefaultGitClient()
	ctx := log.IntoContext(t.Context(), logr.Discard())

	// Test with a known commit from a public repository
	config := &CloneConfig{
		URL:    "https://github.com/stacklok/toolhive",
		Commit: "395b6ba11bdd60b615a9630a66dcede2abcfbb48", // Known valid commit
	}

	repoInfo, err := client.Clone(ctx, config)
	if err != nil {
		t.Fatalf("Failed to clone repository at specific commit: %v", err)
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
	err = client.Cleanup(ctx, repoInfo)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}
}

// TestDefaultGitClient_CloneInvalidCommit tests error handling for invalid commit hash
func TestDefaultGitClient_CloneInvalidCommit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real repository test in short mode")
	}

	client := NewDefaultGitClient()
	ctx := log.IntoContext(t.Context(), logr.Discard())

	// Test with an invalid commit hash
	config := &CloneConfig{
		URL:    "https://github.com/stacklok/toolhive",
		Commit: "f4da6f2", // This is the original invalid commit that was causing the error
	}

	repoInfo, err := client.Clone(ctx, config)
	if err == nil {
		t.Error("Expected error for invalid commit hash, got nil")
		if repoInfo != nil {
			client.Cleanup(ctx, repoInfo)
		}
	}

	if repoInfo != nil {
		t.Error("Expected nil repoInfo for invalid commit")
	}
}

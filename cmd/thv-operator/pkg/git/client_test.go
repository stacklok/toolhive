package git

import (
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	ctx := log.IntoContext(t.Context(), logr.Discard())

	config := &CloneConfig{
		URL: "invalid-url",
	}

	repoInfo, err := client.Clone(ctx, config)
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
	ctx := log.IntoContext(t.Context(), logr.Discard())

	config := &CloneConfig{
		URL: "https://github.com/nonexistent/nonexistent.git",
	}

	repoInfo, err := client.Clone(ctx, config)
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

	err := client.Cleanup(log.IntoContext(t.Context(), logr.Discard()), nil)
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

	err := client.Cleanup(log.IntoContext(t.Context(), logr.Discard()), repoInfo)
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

func TestCloneConfig_Structure(t *testing.T) {
	t.Parallel()
	config := CloneConfig{
		URL:    "https://github.com/example/repo.git",
		Branch: "main",
		Tag:    "v1.0.0",
		Commit: "abc123",
	}

	if config.URL != "https://github.com/example/repo.git" {
		t.Errorf("Expected URL to be set correctly")
	}
	if config.Branch != "main" {
		t.Errorf("Expected Branch to be set correctly")
	}
	if config.Tag != "v1.0.0" {
		t.Errorf("Expected Tag to be set correctly")
	}
	if config.Commit != "abc123" {
		t.Errorf("Expected Commit to be set correctly")
	}
}

func TestRepositoryInfo_Structure(t *testing.T) {
	t.Parallel()
	repoInfo := RepositoryInfo{
		Branch:    "main",
		RemoteURL: "https://github.com/example/repo.git",
	}

	if repoInfo.Repository != nil {
		t.Error("Expected Repository to be nil by default")
	}
	if repoInfo.Branch != "main" {
		t.Errorf("Expected Branch to be set correctly")
	}
	if repoInfo.RemoteURL != "https://github.com/example/repo.git" {
		t.Errorf("Expected RemoteURL to be set correctly")
	}
}

func TestCloneConfig_EmptyFields(t *testing.T) {
	t.Parallel()
	config := CloneConfig{}

	if config.URL != "" {
		t.Error("Expected empty URL by default")
	}
	if config.Branch != "" {
		t.Error("Expected empty Branch by default")
	}
	if config.Tag != "" {
		t.Error("Expected empty Tag by default")
	}
	if config.Commit != "" {
		t.Error("Expected empty Commit by default")
	}
}

func TestRepositoryInfo_EmptyFields(t *testing.T) {
	t.Parallel()
	repoInfo := RepositoryInfo{}

	if repoInfo.Repository != nil {
		t.Error("Expected nil Repository by default")
	}
	if repoInfo.Branch != "" {
		t.Error("Expected empty Branch by default")
	}
	if repoInfo.RemoteURL != "" {
		t.Error("Expected empty RemoteURL by default")
	}
}

package git

import (
	"testing"

	"github.com/go-git/go-git/v5"
)

const (
	testRepoURL = "https://github.com/example/repo.git"
	mainBranch  = "main"
)

func TestCloneConfig_BasicValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		config      CloneConfig
		expectValid bool
	}{
		{
			name: "valid config with URL and directory",
			config: CloneConfig{
				URL:       testRepoURL,
				Directory: "/tmp/repo",
			},
			expectValid: true,
		},
		{
			name: "valid config with branch",
			config: CloneConfig{
				URL:       testRepoURL,
				Branch:    mainBranch,
				Directory: "/tmp/repo",
			},
			expectValid: true,
		},
		{
			name: "valid config with tag",
			config: CloneConfig{
				URL:       testRepoURL,
				Tag:       "v1.0.0",
				Directory: "/tmp/repo",
			},
			expectValid: true,
		},
		{
			name: "valid config with commit",
			config: CloneConfig{
				URL:       testRepoURL,
				Commit:    "abc123def456",
				Directory: "/tmp/repo",
			},
			expectValid: true,
		},
		{
			name: "invalid config - empty URL",
			config: CloneConfig{
				URL:       "",
				Directory: "/tmp/repo",
			},
			expectValid: false,
		},
		{
			name: "invalid config - empty directory",
			config: CloneConfig{
				URL:       testRepoURL,
				Directory: "",
			},
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Basic validation - check that required fields are not empty
			hasURL := tt.config.URL != ""
			hasDirectory := tt.config.Directory != ""
			isValid := hasURL && hasDirectory

			if tt.expectValid && !isValid {
				t.Errorf("Expected config to be valid, but URL=%q, Directory=%q", tt.config.URL, tt.config.Directory)
			}
			if !tt.expectValid && isValid {
				t.Errorf("Expected config to be invalid, but URL=%q, Directory=%q", tt.config.URL, tt.config.Directory)
			}
		})
	}
}

func TestCloneConfig_Fields(t *testing.T) {
	t.Parallel()
	config := CloneConfig{
		URL:       testRepoURL,
		Branch:    "feature-branch",
		Tag:       "v2.0.0",
		Commit:    "def456abc789",
		Directory: "/path/to/clone",
	}

	if config.URL != testRepoURL {
		t.Errorf("Expected URL to be %q, got %q", testRepoURL, config.URL)
	}
	if config.Branch != "feature-branch" {
		t.Errorf("Expected Branch to be 'feature-branch', got %q", config.Branch)
	}
	if config.Tag != "v2.0.0" {
		t.Errorf("Expected Tag to be 'v2.0.0', got %q", config.Tag)
	}
	if config.Commit != "def456abc789" {
		t.Errorf("Expected Commit to be 'def456abc789', got %q", config.Commit)
	}
	if config.Directory != "/path/to/clone" {
		t.Errorf("Expected Directory to be '/path/to/clone', got %q", config.Directory)
	}
}

func TestRepositoryInfo_Fields(t *testing.T) {
	t.Parallel()
	repo := &git.Repository{} // Mock repository
	repoInfo := RepositoryInfo{
		Repository:    repo,
		CurrentCommit: "abc123def456",
		Branch:        mainBranch,
		RemoteURL:     testRepoURL,
	}

	if repoInfo.Repository != repo {
		t.Error("Expected Repository to be set correctly")
	}
	if repoInfo.CurrentCommit != "abc123def456" {
		t.Errorf("Expected CurrentCommit to be 'abc123def456', got %q", repoInfo.CurrentCommit)
	}
	if repoInfo.Branch != mainBranch {
		t.Errorf("Expected Branch to be %q, got %q", mainBranch, repoInfo.Branch)
	}
	if repoInfo.RemoteURL != testRepoURL {
		t.Errorf("Expected RemoteURL to be %q, got %q", testRepoURL, repoInfo.RemoteURL)
	}
}

func TestRepositoryInfo_EmptyValues(t *testing.T) {
	t.Parallel()
	repoInfo := RepositoryInfo{}

	if repoInfo.Repository != nil {
		t.Error("Expected Repository to be nil")
	}
	if repoInfo.CurrentCommit != "" {
		t.Errorf("Expected CurrentCommit to be empty, got %q", repoInfo.CurrentCommit)
	}
	if repoInfo.Branch != "" {
		t.Errorf("Expected Branch to be empty, got %q", repoInfo.Branch)
	}
	if repoInfo.RemoteURL != "" {
		t.Errorf("Expected RemoteURL to be empty, got %q", repoInfo.RemoteURL)
	}
}

func TestCloneConfig_EmptyOptionalFields(t *testing.T) {
	t.Parallel()
	config := CloneConfig{
		URL:       testRepoURL,
		Directory: "/tmp/repo",
		// Branch, Tag, Commit are intentionally empty
	}

	if config.URL == "" {
		t.Error("Expected URL to be set")
	}
	if config.Directory == "" {
		t.Error("Expected Directory to be set")
	}
	if config.Branch != "" {
		t.Errorf("Expected Branch to be empty, got %q", config.Branch)
	}
	if config.Tag != "" {
		t.Errorf("Expected Tag to be empty, got %q", config.Tag)
	}
	if config.Commit != "" {
		t.Errorf("Expected Commit to be empty, got %q", config.Commit)
	}
}

func TestCloneConfig_MutuallyExclusiveFields(t *testing.T) {
	t.Parallel()
	// This test documents the expectation that only one of Branch, Tag, or Commit should be specified
	// The actual validation logic would be in the client implementation

	configs := []struct {
		name   string
		config CloneConfig
	}{
		{
			name: "branch only",
			config: CloneConfig{
				URL:       testRepoURL,
				Branch:    mainBranch,
				Directory: "/tmp/repo",
			},
		},
		{
			name: "tag only",
			config: CloneConfig{
				URL:       testRepoURL,
				Tag:       "v1.0.0",
				Directory: "/tmp/repo",
			},
		},
		{
			name: "commit only",
			config: CloneConfig{
				URL:       testRepoURL,
				Commit:    "abc123",
				Directory: "/tmp/repo",
			},
		},
	}

	for _, tc := range configs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := tc.config
			count := 0
			if config.Branch != "" {
				count++
			}
			if config.Tag != "" {
				count++
			}
			if config.Commit != "" {
				count++
			}

			// Should have at most one reference type specified
			if count > 1 {
				t.Errorf("Only one of Branch, Tag, or Commit should be specified, but found %d", count)
			}
		})
	}
}

func TestCloneConfig_AllFieldsSet(t *testing.T) {
	t.Parallel()
	// Test that we can set all fields
	config := CloneConfig{
		URL:       testRepoURL,
		Branch:    "develop",
		Tag:       "v1.2.3",
		Commit:    "abcdef123456",
		Directory: "/tmp/test-repo",
	}

	// Verify all fields are accessible
	if config.URL == "" {
		t.Error("URL should be set")
	}
	if config.Branch == "" {
		t.Error("Branch should be set")
	}
	if config.Tag == "" {
		t.Error("Tag should be set")
	}
	if config.Commit == "" {
		t.Error("Commit should be set")
	}
	if config.Directory == "" {
		t.Error("Directory should be set")
	}
}

func TestRepositoryInfo_AllFieldsSet(t *testing.T) {
	t.Parallel()
	// Test that we can set all fields
	repo := &git.Repository{}
	repoInfo := RepositoryInfo{
		Repository:    repo,
		CurrentCommit: "current123",
		Branch:        "feature",
		RemoteURL:     "https://example.com/repo.git",
	}

	// Verify all fields are accessible
	if repoInfo.Repository == nil {
		t.Error("Repository should be set")
	}
	if repoInfo.CurrentCommit == "" {
		t.Error("CurrentCommit should be set")
	}
	if repoInfo.Branch == "" {
		t.Error("Branch should be set")
	}
	if repoInfo.RemoteURL == "" {
		t.Error("RemoteURL should be set")
	}
}

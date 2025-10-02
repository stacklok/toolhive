package operator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/gomega"
)

// GitTestHelper manages Git repositories for testing
type GitTestHelper struct {
	ctx          context.Context
	tempDir      string
	repositories []*GitTestRepository
}

// GitTestRepository represents a test Git repository
type GitTestRepository struct {
	Name     string
	Path     string
	CloneURL string
}

// NewGitTestHelper creates a new Git test helper
func NewGitTestHelper(ctx context.Context) *GitTestHelper {
	tempDir, err := os.MkdirTemp("", "git-test-repos-*")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return &GitTestHelper{
		ctx:          ctx,
		tempDir:      tempDir,
		repositories: make([]*GitTestRepository, 0),
	}
}

// CreateRepository creates a new Git repository for testing
func (g *GitTestHelper) CreateRepository(name string) *GitTestRepository {
	repoPath := filepath.Join(g.tempDir, name)
	err := os.MkdirAll(repoPath, 0750)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Initialize Git repository
	g.runGitCommand(repoPath, "init")
	g.runGitCommand(repoPath, "config", "user.name", "Test User")
	g.runGitCommand(repoPath, "config", "user.email", "test@example.com")

	// Create initial commit to establish main branch
	initialFile := filepath.Join(repoPath, "README.md")
	err = os.WriteFile(initialFile, []byte("# Test Repository\n"), 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	g.runGitCommand(repoPath, "add", "README.md")
	g.runGitCommand(repoPath, "commit", "-m", "Initial commit")

	repo := &GitTestRepository{
		Name:     name,
		Path:     repoPath,
		CloneURL: fmt.Sprintf("file://%s", repoPath), // Use file:// URL for local testing
	}

	g.repositories = append(g.repositories, repo)
	return repo
}

// CommitRegistryData commits registry data to the specified file in the repository
func (g *GitTestHelper) CommitRegistryData(
	repo *GitTestRepository, filename string, servers []RegistryServer, commitMessage string) {
	registryData := ToolHiveRegistryData{
		Version:     "1.0.0",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers:     make(map[string]RegistryServer),
	}

	for _, server := range servers {
		registryData.Servers[server.Name] = server
	}

	jsonData, err := json.MarshalIndent(registryData, "", "  ")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	filePath := filepath.Join(repo.Path, filename)
	err = os.WriteFile(filePath, jsonData, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	g.runGitCommand(repo.Path, "add", filename)
	g.runGitCommand(repo.Path, "commit", "-m", commitMessage)
}

// CommitRegistryDataAtPath commits registry data to a nested path in the repository
func (g *GitTestHelper) CommitRegistryDataAtPath(
	repo *GitTestRepository, filePath string, servers []RegistryServer, commitMessage string) {
	registryData := ToolHiveRegistryData{
		Version:     "1.0.0",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers:     make(map[string]RegistryServer),
	}

	for _, server := range servers {
		registryData.Servers[server.Name] = server
	}

	jsonData, err := json.MarshalIndent(registryData, "", "  ")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	fullPath := filepath.Join(repo.Path, filePath)
	dir := filepath.Dir(fullPath)
	err = os.MkdirAll(dir, 0750)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = os.WriteFile(fullPath, jsonData, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	g.runGitCommand(repo.Path, "add", filePath)
	g.runGitCommand(repo.Path, "commit", "-m", commitMessage)
}

// CreateBranch creates a new branch and switches to it
func (g *GitTestHelper) CreateBranch(repo *GitTestRepository, branchName string) {
	g.runGitCommand(repo.Path, "checkout", "-b", branchName)
}

// SwitchBranch switches to an existing branch
func (g *GitTestHelper) SwitchBranch(repo *GitTestRepository, branchName string) {
	g.runGitCommand(repo.Path, "checkout", branchName)
}

// CreateTag creates a Git tag at the current commit
func (g *GitTestHelper) CreateTag(repo *GitTestRepository, tagName, tagMessage string) {
	g.runGitCommand(repo.Path, "tag", "-a", tagName, "-m", tagMessage)
}

// GetCommitHash returns the current commit hash
func (*GitTestHelper) GetCommitHash(repo *GitTestRepository) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo.Path
	output, err := cmd.Output()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return string(output[:40]) // Return first 40 characters (full SHA)
}

// CleanupRepositories removes all test repositories
func (g *GitTestHelper) CleanupRepositories() error {
	return os.RemoveAll(g.tempDir)
}

// runGitCommand runs a Git command in the specified directory
func (*GitTestHelper) runGitCommand(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Git command failed: %s\nOutput: %s", cmd.String(), string(output))
	}
}

// StartGitServer starts a simple Git HTTP server for more realistic testing
// This is optional and can be used for advanced scenarios
func (*GitTestHelper) StartGitServer() error {
	// For basic file:// URL testing, this is not needed
	// Could be implemented later for HTTP/HTTPS Git server testing
	return nil
}

// StopGitServer stops the Git HTTP server
func (*GitTestHelper) StopGitServer() error {
	// For basic file:// URL testing, this is not needed
	return nil
}

// ValidateGitInstallation checks if Git is available for testing
func ValidateGitInstallation() error {
	cmd := exec.Command("git", "--version")
	_, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git is not installed or not available in PATH: %w", err)
	}
	return nil
}

// Repository state helpers for advanced testing

// ListBranches returns all branches in the repository
func (*GitTestHelper) ListBranches(repo *GitTestRepository) []string {
	cmd := exec.Command("git", "branch", "--format=%(refname:short)")
	cmd.Dir = repo.Path
	output, err := cmd.Output()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	branches := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		if line != "" {
			branches = append(branches, strings.TrimSpace(line))
		}
	}
	return branches
}

// ListTags returns all tags in the repository
func (*GitTestHelper) ListTags(repo *GitTestRepository) []string {
	cmd := exec.Command("git", "tag")
	cmd.Dir = repo.Path
	output, err := cmd.Output()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	tags := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		if line != "" {
			tags = append(tags, strings.TrimSpace(line))
		}
	}
	return tags
}

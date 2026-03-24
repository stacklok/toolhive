// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// createBareGitRepoWithSkill creates a bare git repo containing a SKILL.md
// at the specified path within the repo. It returns the bare repo directory path.
// Uses go-git for repo creation, then runs "git update-server-info" so the repo
// can be served over dumb HTTP.
func createBareGitRepoWithSkill(skillName, description, skillPath string) string {
	// Create a non-bare repo first
	workDir := GinkgoT().TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	wt, err := repo.Worktree()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Determine the target directory for SKILL.md
	targetDir := workDir
	if skillPath != "" {
		targetDir = filepath.Join(workDir, skillPath)
		ExpectWithOffset(1, os.MkdirAll(targetDir, 0o755)).To(Succeed())
	}

	// Write SKILL.md
	skillMD := fmt.Sprintf(`---
name: %s
description: %s
version: "0.1.0"
---
# %s

%s
`, skillName, description, skillName, description)
	ExpectWithOffset(1, os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(skillMD), 0o644)).To(Succeed())

	// Write a companion README
	ExpectWithOffset(1, os.WriteFile(filepath.Join(targetDir, "README.md"), []byte("# "+skillName), 0o644)).To(Succeed())

	// Stage and commit
	_, err = wt.Add(".")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	_, err = wt.Commit("Add test skill", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "E2E Test",
			Email: "e2e@test.local",
		},
	})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Clone to bare repo
	bareDir := GinkgoT().TempDir()
	_, err = gogit.PlainClone(bareDir, true, &gogit.CloneOptions{
		URL: workDir,
	})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Run "git update-server-info" to enable dumb HTTP serving
	//nolint:gosec // test-only code, skillPath is controlled
	cmd := exec.Command("git", "update-server-info")
	cmd.Dir = bareDir
	ExpectWithOffset(1, cmd.Run()).To(Succeed())

	return bareDir
}

// createBareGitRepoWithTag creates a bare repo with a tagged commit.
func createBareGitRepoWithTag(skillName, description, tagName string) string {
	// Create a non-bare repo
	workDir := GinkgoT().TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	wt, err := repo.Worktree()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	skillMD := fmt.Sprintf(`---
name: %s
description: %s
version: "1.0.0"
---
# %s
`, skillName, description, skillName)
	ExpectWithOffset(1, os.WriteFile(filepath.Join(workDir, "SKILL.md"), []byte(skillMD), 0o644)).To(Succeed())

	_, err = wt.Add(".")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	hash, err := wt.Commit("Add skill", &gogit.CommitOptions{
		Author: &object.Signature{Name: "E2E Test", Email: "e2e@test.local"},
	})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Create lightweight tag
	_, err = repo.CreateTag(tagName, hash, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Clone to bare repo
	bareDir := GinkgoT().TempDir()
	_, err = gogit.PlainClone(bareDir, true, &gogit.CloneOptions{URL: workDir})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	// Ensure the tag ref is also in the bare repo
	bareRepo, err := gogit.PlainOpen(bareDir)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	err = bareRepo.Fetch(&gogit.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitconfig.RefSpec{"+refs/tags/*:refs/tags/*"},
	})
	// Ignore already-up-to-date errors
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
	}

	// Also manually create the tag ref if it doesn't exist
	_, err = bareRepo.Reference(plumbing.NewTagReferenceName(tagName), false)
	if err != nil {
		// Create the tag directly in the bare repo
		ref := plumbing.NewHashReference(plumbing.NewTagReferenceName(tagName), hash)
		ExpectWithOffset(1, bareRepo.Storer.SetReference(ref)).To(Succeed())
	}

	cmd := exec.Command("git", "update-server-info")
	cmd.Dir = bareDir
	ExpectWithOffset(1, cmd.Run()).To(Succeed())

	return bareDir
}

// startDumbGitHTTPServer starts an HTTP server that serves the bare git repo
// directory using dumb HTTP protocol (plain file serving).
func startDumbGitHTTPServer(bareRepoDir string) *httptest.Server {
	// Serve the bare repo under a path that looks like /test/skill-name
	// The git:// reference parser requires owner/repo format
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(bareRepoDir)))
	server := httptest.NewServer(mux)
	return server
}

// gitReference builds a git:// reference for a local test server.
// Format: git://host:port/owner/repo[@ref][#path]
//
// This relies on TOOLHIVE_DEV=true (set by the E2E test server) which causes
// ParseGitReference to emit http:// URLs and allows localhost in the SSRF check.
func gitReference(server *httptest.Server, ref, skillPath string) string {
	// Extract host:port from the server URL (http://127.0.0.1:PORT)
	addr := strings.TrimPrefix(server.URL, "http://")

	// owner/repo must have at least one slash — use "test/repo".
	result := fmt.Sprintf("git://%s/test/repo", addr)
	if ref != "" {
		result += "@" + ref
	}
	if skillPath != "" {
		result += "#" + skillPath
	}
	return result
}

var _ = Describe("Git-based skill installation", Label("api", "skills", "git", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("Direct git:// reference install", func() {
		It("should install a skill from a git:// reference", func() {
			skillName := "git-basic-skill"

			By("Creating a bare git repo with a test skill")
			bareRepo := createBareGitRepoWithSkill(skillName, "A basic git skill for E2E testing", "")

			By("Starting a local git HTTP server")
			gitServer := startDumbGitHTTPServer(bareRepo)
			DeferCleanup(gitServer.Close)

			By("Installing the skill via git:// reference")
			gitRef := gitReference(gitServer, "", "")
			installResp := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Verifying the install result")
			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Status).To(Equal("installed"))
			Expect(result.Skill.Metadata.Name).To(Equal(skillName))
			Expect(result.Skill.Digest).To(HaveLen(40)) // git commit hash
			Expect(result.Skill.Metadata.Version).To(Equal("0.1.0"))

			By("Verifying the skill appears in the list")
			listResp := listSkills(apiServer)
			defer listResp.Body.Close()
			var listResult skillListResponse
			Expect(json.NewDecoder(listResp.Body).Decode(&listResult)).To(Succeed())
			found := false
			for _, sk := range listResult.Skills {
				if sk.Metadata.Name == skillName {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "installed git skill should appear in list")

			By("Cleaning up")
			cleanupResp := uninstallSkill(apiServer, skillName)
			defer cleanupResp.Body.Close()
			Expect(cleanupResp.StatusCode).To(Equal(http.StatusNoContent))
		})

		It("should install a skill from a git:// reference with tag", func() {
			skillName := "git-tagged-skill"

			By("Creating a bare git repo with a tagged commit")
			bareRepo := createBareGitRepoWithTag(skillName, "A tagged git skill", "v1.0.0")

			By("Starting a local git HTTP server")
			gitServer := startDumbGitHTTPServer(bareRepo)
			DeferCleanup(gitServer.Close)

			By("Installing the skill via git:// reference with tag")
			gitRef := gitReference(gitServer, "v1.0.0", "")
			installResp := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Status).To(Equal("installed"))
			Expect(result.Skill.Metadata.Name).To(Equal(skillName))
			Expect(result.Skill.Metadata.Version).To(Equal("1.0.0"))

			By("Cleaning up")
			cleanupResp := uninstallSkill(apiServer, skillName)
			defer cleanupResp.Body.Close()
		})

		It("should install a skill from a git:// reference with subdirectory", func() {
			skillName := "git-subdir-skill"

			By("Creating a bare git repo with skill in a subdirectory")
			bareRepo := createBareGitRepoWithSkill(skillName, "A subdir git skill", "skills/my-skill")

			By("Starting a local git HTTP server")
			gitServer := startDumbGitHTTPServer(bareRepo)
			DeferCleanup(gitServer.Close)

			By("Installing via git:// reference with path fragment")
			gitRef := gitReference(gitServer, "", "skills/my-skill")
			installResp := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Metadata.Name).To(Equal(skillName))

			By("Cleaning up")
			cleanupResp := uninstallSkill(apiServer, skillName)
			defer cleanupResp.Body.Close()
		})
	})

	Describe("Git install lifecycle", func() {
		It("should support full lifecycle: install -> info -> uninstall -> verify gone", func() {
			skillName := "git-lifecycle-skill"

			bareRepo := createBareGitRepoWithSkill(skillName, "Lifecycle test skill", "")
			gitServer := startDumbGitHTTPServer(bareRepo)
			DeferCleanup(gitServer.Close)

			By("Installing")
			gitRef := gitReference(gitServer, "", "")
			installResp := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Getting skill info")
			infoResp := getSkillInfo(apiServer, skillName)
			defer infoResp.Body.Close()
			Expect(infoResp.StatusCode).To(Equal(http.StatusOK))

			By("Uninstalling")
			uninstallResp := uninstallSkill(apiServer, skillName)
			defer uninstallResp.Body.Close()
			Expect(uninstallResp.StatusCode).To(Equal(http.StatusNoContent))

			By("Verifying the skill is gone")
			infoResp2 := getSkillInfo(apiServer, skillName)
			defer infoResp2.Body.Close()
			Expect(infoResp2.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should be idempotent when reinstalling from same commit", func() {
			skillName := "git-idempotent-skill"

			bareRepo := createBareGitRepoWithSkill(skillName, "Idempotent test", "")
			gitServer := startDumbGitHTTPServer(bareRepo)
			DeferCleanup(gitServer.Close)

			gitRef := gitReference(gitServer, "", "")

			By("Installing the first time")
			resp1 := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer resp1.Body.Close()
			Expect(resp1.StatusCode).To(Equal(http.StatusCreated))

			var result1 installSkillResponse
			Expect(json.NewDecoder(resp1.Body).Decode(&result1)).To(Succeed())
			digest1 := result1.Skill.Digest

			By("Reinstalling (same commit)")
			resp2 := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer resp2.Body.Close()
			Expect(resp2.StatusCode).To(Equal(http.StatusCreated))

			var result2 installSkillResponse
			Expect(json.NewDecoder(resp2.Body).Decode(&result2)).To(Succeed())
			Expect(result2.Skill.Digest).To(Equal(digest1))

			By("Cleaning up")
			cleanupResp := uninstallSkill(apiServer, skillName)
			defer cleanupResp.Body.Close()
		})
	})

	Describe("Git reference validation errors", func() {
		It("should reject a malformed git:// reference", func() {
			resp := installSkill(apiServer, installSkillRequest{Name: "git://"})
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should reject git:// reference with path traversal", func() {
			resp := installSkill(apiServer, installSkillRequest{Name: "git://github.com/org/repo#../../../etc"})
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should return an error for a nonexistent git repo", func() {
			// Use a server that returns 404 for everything
			emptyServer := httptest.NewServer(http.NotFoundHandler())
			DeferCleanup(emptyServer.Close)

			gitRef := gitReference(emptyServer, "", "")
			resp := installSkill(apiServer, installSkillRequest{Name: gitRef})
			defer resp.Body.Close()
			// Should be 502 (bad gateway) since the upstream repo failed
			Expect(resp.StatusCode).To(Equal(http.StatusBadGateway))
		})
	})

	Describe("Registry fallback with git package type", func() {
		It("should resolve a plain name from registry with git package", func() {
			skillName := "git-registry-skill"

			By("Creating a bare git repo and local HTTP server")
			bareRepo := createBareGitRepoWithSkill(skillName, "Registry git fallback test", "")
			gitServer := startDumbGitHTTPServer(bareRepo)
			DeferCleanup(gitServer.Close)

			// Build the git:// URL for the registry entry
			gitAddr := strings.TrimPrefix(gitServer.URL, "http://")
			gitURL := fmt.Sprintf("https://%s/test/repo", gitAddr)

			By("Creating upstream-format registry JSON with git package type")
			registryFile := createUpstreamRegistryWithGitSkill(skillName, gitURL)

			By("Configuring the server to use the test registry")
			updateResp := updateRegistry(apiServer, "default", map[string]interface{}{
				"local_path": registryFile,
			})
			defer updateResp.Body.Close()
			Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

			DeferCleanup(func() {
				resetResp := updateRegistry(apiServer, "default", map[string]interface{}{})
				resetResp.Body.Close()
			})

			By("Installing by plain skill name — should resolve from registry via git")
			installResp := installSkill(apiServer, installSkillRequest{Name: skillName})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Status).To(Equal("installed"))
			Expect(result.Skill.Metadata.Name).To(Equal(skillName))
			Expect(result.Skill.Digest).To(HaveLen(40))

			By("Cleaning up")
			cleanupResp := uninstallSkill(apiServer, skillName)
			defer cleanupResp.Body.Close()
		})
	})
})

// createUpstreamRegistryWithGitSkill creates an upstream-format registry JSON file
// with a skill that has a git package type.
func createUpstreamRegistryWithGitSkill(skillName, gitURL string) string {
	registryData := map[string]interface{}{
		"$schema": "https://raw.githubusercontent.com/stacklok/toolhive-core/main/registry/types/data/upstream-registry.schema.json",
		"version": "1.0.0",
		"meta":    map[string]string{"last_updated": "2025-01-01T00:00:00Z"},
		"data": map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"name":        "dummy-server",
					"description": "Placeholder to satisfy registry validation",
					"repository": map[string]string{
						"url":  "https://github.com/example/dummy",
						"type": "git",
					},
					"version_detail": map[string]string{
						"version": "0.0.1",
					},
				},
			},
			"skills": []map[string]interface{}{
				{
					"namespace":   "e2e-test",
					"name":        skillName,
					"description": "E2E git-based test skill",
					"version":     "0.1.0",
					"packages": []map[string]interface{}{
						{
							"registryType": "git",
							"url":          gitURL,
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(registryData)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	tempDir := GinkgoT().TempDir()
	testFile := filepath.Join(tempDir, "test-git-skill-registry.json")
	ExpectWithOffset(1, os.WriteFile(testFile, data, 0o600)).To(Succeed())
	return testFile
}

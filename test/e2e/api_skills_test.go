// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// Response/request structs mirroring pkg/api/v1/skills_types.go and pkg/skills types.

type skillListResponse struct {
	Skills []installedSkillResponse `json:"skills"`
}

type installedSkillResponse struct {
	Metadata    skillMetadataResponse `json:"metadata"`
	Scope       string                `json:"scope"`
	ProjectRoot string                `json:"project_root,omitempty"`
	Reference   string                `json:"reference,omitempty"`
	Tag         string                `json:"tag,omitempty"`
	Digest      string                `json:"digest,omitempty"`
	Status      string                `json:"status"`
	InstalledAt time.Time             `json:"installed_at"`
	Clients     []string              `json:"clients,omitempty"`
}

type skillMetadataResponse struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags,omitempty"`
}

type installSkillRequest struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Client      string `json:"client,omitempty"`
	Force       bool   `json:"force,omitempty"`
	Group       string `json:"group,omitempty"`
}

type installSkillResponse struct {
	Skill installedSkillResponse `json:"skill"`
}

type validateSkillRequest struct {
	Path string `json:"path"`
}

type validationResultResponse struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type buildSkillRequest struct {
	Path string `json:"path"`
	Tag  string `json:"tag,omitempty"`
}

type buildResultResponse struct {
	Reference string `json:"reference"`
}

type skillInfoResponse struct {
	Metadata       skillMetadataResponse   `json:"metadata"`
	InstalledSkill *installedSkillResponse `json:"installed_skill,omitempty"`
}

// Helper functions

func listSkills(server *e2e.Server) *http.Response {
	resp, err := server.Get("/api/v1beta/skills")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func listSkillsInGroup(server *e2e.Server, group string) *http.Response {
	resp, err := server.Get("/api/v1beta/skills?group=" + group)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func installSkill(server *e2e.Server, req installSkillRequest) *http.Response {
	jsonData, err := json.Marshal(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/skills",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func uninstallSkill(server *e2e.Server, name string) *http.Response {
	client := &http.Client{}
	req, err := http.NewRequest(
		"DELETE",
		server.BaseURL()+"/api/v1beta/skills/"+name,
		nil,
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := client.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func getSkillInfo(server *e2e.Server, name string) *http.Response {
	resp, err := server.Get("/api/v1beta/skills/" + name)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func validateSkill(server *e2e.Server, path string) *http.Response {
	reqBody := validateSkillRequest{Path: path}
	jsonData, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/skills/validate",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func buildSkill(server *e2e.Server, path, tag string) *http.Response {
	reqBody := buildSkillRequest{Path: path, Tag: tag}
	jsonData, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/skills/build",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

// createTestSkillDir creates a temporary directory with a valid SKILL.md file.
// The directory name matches the skill name (validator requirement).
func createTestSkillDir(skillName, description string) string {
	parentDir := GinkgoT().TempDir()
	skillDir := filepath.Join(parentDir, skillName)
	ExpectWithOffset(1, os.MkdirAll(skillDir, 0o755)).To(Succeed())

	skillMD := fmt.Sprintf(`---
name: %s
description: %s
version: 0.1.0
---

# %s

This is a test skill.
`, skillName, description, skillName)

	ExpectWithOffset(1, os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte(skillMD),
		0o644,
	)).To(Succeed())

	return skillDir
}

// buildAndInstallSkill creates a skill directory, builds it, and installs by
// plain name via the build-then-install flow. Returns the skill name.
func buildAndInstallSkill(server *e2e.Server, skillName, description string) {
	skillDir := createTestSkillDir(skillName, description)

	buildResp := buildSkill(server, skillDir, "")
	defer buildResp.Body.Close()
	ExpectWithOffset(1, buildResp.StatusCode).To(Equal(http.StatusOK))

	installResp := installSkill(server, installSkillRequest{Name: skillName})
	defer installResp.Body.Close()
	ExpectWithOffset(1, installResp.StatusCode).To(Equal(http.StatusCreated))
}

func pushSkill(server *e2e.Server, reference string) *http.Response {
	reqBody := pushSkillRequest{Reference: reference}
	jsonData, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/skills/push",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

type pushSkillRequest struct {
	Reference string `json:"reference"`
}

// createUpstreamRegistryWithSkill creates a JSON file in the upstream registry
// format containing a single skill entry that points to the given OCI reference.
func createUpstreamRegistryWithSkill(skillName, ociReference string) string {
	registryData := map[string]interface{}{
		"$schema": "https://raw.githubusercontent.com/stacklok/toolhive-core/main/registry/types/data/upstream-registry.schema.json",
		"version": "1.0.0",
		"meta":    map[string]string{"last_updated": "2025-01-01T00:00:00Z"},
		"data": map[string]interface{}{
			"servers": []interface{}{},
			"skills": []map[string]interface{}{
				{
					"namespace":   "e2e-test",
					"name":        skillName,
					"description": "E2E test skill",
					"version":     "0.1.0",
					"packages": []map[string]interface{}{
						{
							"registryType": "oci",
							"identifier":   ociReference,
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(registryData)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	tempDir := GinkgoT().TempDir()
	testFile := filepath.Join(tempDir, "test-skill-registry.json")
	ExpectWithOffset(1, os.WriteFile(testFile, data, 0o600)).To(Succeed())
	return testFile
}

// Test suite

var _ = Describe("Skills API", Label("api", "api-clients", "skills", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("POST /api/v1beta/skills/validate - Validate a skill", func() {
		It("should validate a valid skill directory", func() {
			By("Creating a valid skill directory")
			skillDir := createTestSkillDir("my-test-skill", "A test skill for validation")

			By("Validating the skill")
			resp := validateSkill(apiServer, skillDir)
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying the skill is valid")
			var result validationResultResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Valid).To(BeTrue())
			Expect(result.Errors).To(BeEmpty())
		})

		It("should report invalid when SKILL.md is missing", func() {
			By("Creating an empty directory")
			emptyDir := GinkgoT().TempDir()

			By("Validating the empty directory")
			resp := validateSkill(apiServer, emptyDir)
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying the skill is invalid")
			var result validationResultResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Valid).To(BeFalse())
			Expect(result.Errors).ToNot(BeEmpty())
		})

		It("should report invalid when required fields are missing", func() {
			By("Creating a skill directory with empty frontmatter")
			parentDir := GinkgoT().TempDir()
			skillDir := filepath.Join(parentDir, "bad-skill")
			Expect(os.MkdirAll(skillDir, 0o755)).To(Succeed())

			skillMD := `---
---

# No metadata
`
			Expect(os.WriteFile(
				filepath.Join(skillDir, "SKILL.md"),
				[]byte(skillMD),
				0o644,
			)).To(Succeed())

			By("Validating the skill")
			resp := validateSkill(apiServer, skillDir)
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying the skill is invalid with field errors")
			var result validationResultResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Valid).To(BeFalse())
			Expect(result.Errors).ToNot(BeEmpty())
		})

		It("should reject empty path", func() {
			By("Sending validate request with empty path")
			resp := validateSkill(apiServer, "")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should reject relative path", func() {
			By("Sending validate request with relative path")
			resp := validateSkill(apiServer, "relative/path")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should report invalid for non-existent path", func() {
			By("Sending validate request with non-existent absolute path")
			resp := validateSkill(apiServer, "/nonexistent/path/to/skill")
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying the skill is invalid")
			var result validationResultResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Valid).To(BeFalse())
			Expect(result.Errors).ToNot(BeEmpty())
		})

		It("should reject path traversal", func() {
			By("Sending validate request with path traversal")
			resp := validateSkill(apiServer, "/tmp/../etc")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should reject malformed JSON", func() {
			By("Sending malformed JSON")
			resp, err := http.Post(
				apiServer.BaseURL()+"/api/v1beta/skills/validate",
				"application/json",
				bytes.NewBufferString(`{"invalid json`),
			)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("POST /api/v1beta/skills/build - Build a skill", func() {
		It("should build a valid skill with explicit tag", func() {
			By("Creating a valid skill directory")
			skillDir := createTestSkillDir("build-test-skill", "A skill for build testing")

			By("Building the skill with an explicit tag")
			resp := buildSkill(apiServer, skillDir, "v0.1.0")
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying build result has a reference")
			var result buildResultResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Reference).ToNot(BeEmpty())
		})

		It("should build a valid skill with default tag", func() {
			By("Creating a valid skill directory")
			skillDir := createTestSkillDir("default-tag-skill", "A skill with default tag")

			By("Building the skill without specifying a tag")
			resp := buildSkill(apiServer, skillDir, "")
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying build result has a reference")
			var result buildResultResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Reference).ToNot(BeEmpty())
		})

		It("should reject empty path", func() {
			By("Sending build request with empty path")
			resp := buildSkill(apiServer, "", "v1.0.0")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should reject malformed JSON", func() {
			By("Sending malformed JSON")
			resp, err := http.Post(
				apiServer.BaseURL()+"/api/v1beta/skills/build",
				"application/json",
				bytes.NewBufferString(`{"invalid json`),
			)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("Build then install from local store", func() {
		AfterEach(func() {
			// Clean up any skills installed by these tests so they don't
			// leak into other specs (e.g. "should return empty list initially").
			for _, name := range []string{"local-build-skill", "tagged-build-skill"} {
				resp := uninstallSkill(apiServer, name)
				resp.Body.Close()
				// Ignore 404 — the skill may not have been installed if the test failed early.
			}
		})

		It("should install a locally built skill with installed status", func() {
			By("Creating a valid skill directory")
			skillDir := createTestSkillDir("local-build-skill", "A skill for local build-then-install")

			By("Building the skill (tags with skill name by default)")
			buildResp := buildSkill(apiServer, skillDir, "")
			defer buildResp.Body.Close()
			Expect(buildResp.StatusCode).To(Equal(http.StatusOK))

			By("Installing by plain skill name")
			installResp := installSkill(apiServer, installSkillRequest{Name: "local-build-skill"})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Verifying the skill is installed (not pending)")
			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Status).To(Equal("installed"))
			Expect(result.Skill.Digest).ToNot(BeEmpty())
			Expect(result.Skill.Metadata.Version).To(Equal("0.1.0"))
		})

		It("should install with explicit build tag matching skill name", func() {
			By("Creating a valid skill directory")
			skillDir := createTestSkillDir("tagged-build-skill", "A skill with explicit tag")

			By("Building the skill with explicit tag matching skill name")
			buildResp := buildSkill(apiServer, skillDir, "tagged-build-skill")
			defer buildResp.Body.Close()
			Expect(buildResp.StatusCode).To(Equal(http.StatusOK))

			By("Installing by plain skill name")
			installResp := installSkill(apiServer, installSkillRequest{Name: "tagged-build-skill"})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Verifying the skill is installed (not pending)")
			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Status).To(Equal("installed"))
			Expect(result.Skill.Digest).ToNot(BeEmpty())
		})
	})

	Describe("GET /api/v1beta/skills - List skills", func() {
		AfterEach(func() {
			resp := uninstallSkill(apiServer, "list-test-skill")
			resp.Body.Close()
		})

		It("should return a valid list response", func() {
			By("Listing skills")
			resp := listSkills(apiServer)
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying the response decodes to a valid skills list")
			var result skillListResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			// We only check that the response is valid JSON with a skills array.
			// Other tests may run first and install skills, so the list is not
			// guaranteed to be empty.
			Expect(result.Skills).ToNot(BeNil())
		})

		It("should include installed skills", func() {
			By("Building and installing a skill")
			buildAndInstallSkill(apiServer, "list-test-skill", "A skill for list testing")

			By("Listing skills")
			resp := listSkills(apiServer)
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying the installed skill is in the list")
			var result skillListResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skills).ToNot(BeEmpty())

			found := false
			for _, s := range result.Skills {
				if s.Metadata.Name == "list-test-skill" {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "Expected 'list-test-skill' in the skills list")
		})
	})

	Describe("POST /api/v1beta/skills - Install a skill", func() {
		AfterEach(func() {
			for _, name := range []string{"install-test-skill", "dup-test-skill"} {
				resp := uninstallSkill(apiServer, name)
				resp.Body.Close()
			}
		})

		It("should return 404 for plain name not in local store or registry", func() {
			By("Attempting to install a skill by plain name without building first")
			resp := installSkill(apiServer, installSkillRequest{Name: "install-test-skill"})
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should reject empty name", func() {
			By("Attempting to install with empty name")
			resp := installSkill(apiServer, installSkillRequest{Name: ""})
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should reject invalid name", func() {
			By("Attempting to install with invalid name")
			resp := installSkill(apiServer, installSkillRequest{Name: "INVALID!"})
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should be idempotent for same digest", func() {
			By("Building and installing a skill")
			buildAndInstallSkill(apiServer, "dup-test-skill", "A skill for idempotent testing")

			By("Installing the same skill again (same digest)")
			resp2 := installSkill(apiServer, installSkillRequest{Name: "dup-test-skill"})
			defer resp2.Body.Close()

			By("Verifying response status is 201 Created (idempotent no-op)")
			Expect(resp2.StatusCode).To(Equal(http.StatusCreated))
		})

		It("should reject malformed JSON", func() {
			By("Sending malformed JSON")
			resp, err := http.Post(
				apiServer.BaseURL()+"/api/v1beta/skills",
				"application/json",
				bytes.NewBufferString(`{"invalid json`),
			)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("GET /api/v1beta/skills/{name} - Get skill info", func() {
		AfterEach(func() {
			resp := uninstallSkill(apiServer, "info-test-skill")
			resp.Body.Close()
		})

		It("should return info for an installed skill", func() {
			By("Building and installing a skill")
			buildAndInstallSkill(apiServer, "info-test-skill", "A skill for info testing")

			By("Getting skill info")
			resp := getSkillInfo(apiServer, "info-test-skill")
			defer resp.Body.Close()

			By("Verifying response status is 200 OK")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Verifying skill info")
			var result skillInfoResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result.Metadata.Name).To(Equal("info-test-skill"))
		})

		It("should return 404 for non-existent skill", func() {
			By("Getting info for a skill that doesn't exist")
			resp := getSkillInfo(apiServer, "no-such-skill")
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 400 for invalid name", func() {
			By("Getting info with invalid name")
			resp := getSkillInfo(apiServer, "INVALID!")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("DELETE /api/v1beta/skills/{name} - Uninstall a skill", func() {
		It("should uninstall an installed skill", func() {
			By("Building and installing a skill")
			buildAndInstallSkill(apiServer, "uninstall-test", "A skill for uninstall testing")

			By("Uninstalling the skill")
			resp := uninstallSkill(apiServer, "uninstall-test")
			defer resp.Body.Close()

			By("Verifying response status is 204 No Content")
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

			By("Verifying skill is no longer available")
			infoResp := getSkillInfo(apiServer, "uninstall-test")
			defer infoResp.Body.Close()
			Expect(infoResp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 404 for non-existent skill", func() {
			By("Attempting to uninstall a skill that doesn't exist")
			resp := uninstallSkill(apiServer, "no-such-skill")
			defer resp.Body.Close()

			By("Verifying response status is 404 Not Found")
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("should return 400 for invalid name", func() {
			By("Attempting to uninstall with invalid name")
			resp := uninstallSkill(apiServer, "INVALID!")
			defer resp.Body.Close()

			By("Verifying response status is 400 Bad Request")
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("Group integration", func() {
		var groupName string

		BeforeEach(func() {
			groupName = fmt.Sprintf("skill-group-%d", GinkgoRandomSeed())
			By("Creating a group for skill tests")
			resp := createGroup(apiServer, map[string]interface{}{"name": groupName})
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		})

		AfterEach(func() {
			for _, name := range []string{
				"group-install-skill", "group-filter-in", "group-filter-out",
				"group-uninstall-skill", "group-noexist-skill",
			} {
				resp := uninstallSkill(apiServer, name)
				resp.Body.Close()
			}
			deleteGroup(apiServer, groupName)
		})

		It("should register the skill in the group on install", func() {
			skillName := "group-install-skill"

			By("Creating and building the skill")
			skillDir := createTestSkillDir(skillName, "A skill for group install testing")
			buildResp := buildSkill(apiServer, skillDir, "")
			defer buildResp.Body.Close()
			Expect(buildResp.StatusCode).To(Equal(http.StatusOK))

			By("Installing the skill into the group")
			resp := installSkill(apiServer, installSkillRequest{Name: skillName, Group: groupName})
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			By("Verifying the group lists the skill")
			getResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/groups/%s", groupName))
			Expect(err).ToNot(HaveOccurred())
			defer getResp.Body.Close()
			Expect(getResp.StatusCode).To(Equal(http.StatusOK))

			var grp struct {
				Name   string   `json:"name"`
				Skills []string `json:"skills"`
			}
			Expect(json.NewDecoder(getResp.Body).Decode(&grp)).To(Succeed())
			Expect(grp.Skills).To(ContainElement(skillName))
		})

		It("should filter list by group", func() {
			skillInGroup := "group-filter-in"
			skillOutGroup := "group-filter-out"

			By("Creating and building the in-group skill")
			inDir := createTestSkillDir(skillInGroup, "A skill for group filter testing (in)")
			inBuild := buildSkill(apiServer, inDir, "")
			defer inBuild.Body.Close()
			Expect(inBuild.StatusCode).To(Equal(http.StatusOK))

			By("Installing the skill into the group")
			r1 := installSkill(apiServer, installSkillRequest{Name: skillInGroup, Group: groupName})
			defer r1.Body.Close()
			Expect(r1.StatusCode).To(Equal(http.StatusCreated))

			By("Creating and building the out-of-group skill")
			outDir := createTestSkillDir(skillOutGroup, "A skill for group filter testing (out)")
			outBuild := buildSkill(apiServer, outDir, "")
			defer outBuild.Body.Close()
			Expect(outBuild.StatusCode).To(Equal(http.StatusOK))

			By("Installing a skill without a group")
			r2 := installSkill(apiServer, installSkillRequest{Name: skillOutGroup})
			defer r2.Body.Close()
			Expect(r2.StatusCode).To(Equal(http.StatusCreated))

			By("Listing skills filtered by group")
			resp := listSkillsInGroup(apiServer, groupName)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var result skillListResponse
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())

			names := make([]string, 0, len(result.Skills))
			for _, s := range result.Skills {
				names = append(names, s.Metadata.Name)
			}
			Expect(names).To(ContainElement(skillInGroup))
			Expect(names).NotTo(ContainElement(skillOutGroup))
		})

		It("should remove the skill from the group on uninstall", func() {
			skillName := "group-uninstall-skill"

			By("Creating and building the skill")
			skillDir := createTestSkillDir(skillName, "A skill for group uninstall testing")
			buildResp := buildSkill(apiServer, skillDir, "")
			defer buildResp.Body.Close()
			Expect(buildResp.StatusCode).To(Equal(http.StatusOK))

			By("Installing the skill into the group")
			r1 := installSkill(apiServer, installSkillRequest{Name: skillName, Group: groupName})
			defer r1.Body.Close()
			Expect(r1.StatusCode).To(Equal(http.StatusCreated))

			By("Uninstalling the skill")
			r2 := uninstallSkill(apiServer, skillName)
			defer r2.Body.Close()
			Expect(r2.StatusCode).To(Equal(http.StatusNoContent))

			By("Verifying the group no longer lists the skill")
			getResp, err := apiServer.Get(fmt.Sprintf("/api/v1beta/groups/%s", groupName))
			Expect(err).ToNot(HaveOccurred())
			defer getResp.Body.Close()
			Expect(getResp.StatusCode).To(Equal(http.StatusOK))

			var grp struct {
				Name   string   `json:"name"`
				Skills []string `json:"skills"`
			}
			Expect(json.NewDecoder(getResp.Body).Decode(&grp)).To(Succeed())
			Expect(grp.Skills).NotTo(ContainElement(skillName))
		})

		It("should return error when installing into a non-existent group", func() {
			skillName := "group-noexist-skill"

			By("Creating and building the skill")
			skillDir := createTestSkillDir(skillName, "A skill for non-existent group testing")
			buildResp := buildSkill(apiServer, skillDir, "")
			defer buildResp.Body.Close()
			Expect(buildResp.StatusCode).To(Equal(http.StatusOK))

			By("Attempting to install the skill into a non-existent group")
			resp := installSkill(apiServer, installSkillRequest{
				Name:  skillName,
				Group: "no-such-group-xyz",
			})
			defer resp.Body.Close()

			By("Verifying the response indicates failure")
			Expect(resp.StatusCode).To(BeNumerically(">=", http.StatusBadRequest))
		})
	})

	Describe("Overwrite protection", func() {
		AfterEach(func() {
			for _, name := range []string{"overwrite-noflag", "overwrite-reinstall", "overwrite-force-dup"} {
				resp := uninstallSkill(apiServer, name)
				resp.Body.Close()
			}
		})

		It("should be idempotent when reinstalling same digest", func() {
			skillName := "overwrite-noflag"

			By("Building and installing the skill for the first time")
			buildAndInstallSkill(apiServer, skillName, "A skill for overwrite testing")

			By("Installing the same skill again (same local artifact)")
			resp2 := installSkill(apiServer, installSkillRequest{Name: skillName})
			defer resp2.Body.Close()

			By("Verifying response status is 201 Created (idempotent, same digest)")
			Expect(resp2.StatusCode).To(Equal(http.StatusCreated))
		})

		It("should allow reinstall after uninstall", func() {
			skillName := "overwrite-reinstall"

			By("Building and installing the skill")
			buildAndInstallSkill(apiServer, skillName, "A skill for reinstall testing")

			By("Uninstalling the skill")
			r2 := uninstallSkill(apiServer, skillName)
			defer r2.Body.Close()
			Expect(r2.StatusCode).To(Equal(http.StatusNoContent))

			By("Re-installing the skill (should succeed since DB record was removed)")
			r3 := installSkill(apiServer, installSkillRequest{Name: skillName})
			defer r3.Body.Close()
			Expect(r3.StatusCode).To(Equal(http.StatusCreated))
		})

		It("should be idempotent with force flag and same digest", func() {
			skillName := "overwrite-force-dup"

			By("Building and installing the skill for the first time")
			buildAndInstallSkill(apiServer, skillName, "A skill for force-dup testing")

			By("Force-installing the same skill again (same digest)")
			r2 := installSkill(apiServer, installSkillRequest{Name: skillName, Force: true})
			defer r2.Body.Close()

			By("Verifying response is 201 Created (idempotent, same digest)")
			Expect(r2.StatusCode).To(Equal(http.StatusCreated))
		})
	})

	Describe("Build and validate lifecycle", func() {
		It("should build, then validate, the same skill directory", func() {
			skillName := "build-validate-lifecycle"

			By("Creating a valid skill directory")
			skillDir := createTestSkillDir(skillName, "A skill for build-validate lifecycle")

			By("Validating the skill")
			vResp := validateSkill(apiServer, skillDir)
			defer vResp.Body.Close()
			Expect(vResp.StatusCode).To(Equal(http.StatusOK))
			var vResult validationResultResponse
			Expect(json.NewDecoder(vResp.Body).Decode(&vResult)).To(Succeed())
			Expect(vResult.Valid).To(BeTrue())

			By("Building the skill")
			bResp := buildSkill(apiServer, skillDir, "v0.1.0")
			defer bResp.Body.Close()
			Expect(bResp.StatusCode).To(Equal(http.StatusOK))
			var bResult buildResultResponse
			Expect(json.NewDecoder(bResp.Body).Decode(&bResult)).To(Succeed())
			Expect(bResult.Reference).ToNot(BeEmpty())
		})
	})

	Describe("Full lifecycle integration", func() {
		It("should support install → list → info → uninstall → list → info", func() {
			skillName := "lifecycle-test"

			By("Building and installing the skill")
			buildAndInstallSkill(apiServer, skillName, "A skill for lifecycle testing")

			By("Listing skills — should contain the skill")
			listResp := listSkills(apiServer)
			defer listResp.Body.Close()
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			var listResult skillListResponse
			Expect(json.NewDecoder(listResp.Body).Decode(&listResult)).To(Succeed())
			found := false
			for _, s := range listResult.Skills {
				if s.Metadata.Name == skillName {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "Expected skill in list after install")

			By("Getting skill info — should return 200")
			infoResp := getSkillInfo(apiServer, skillName)
			defer infoResp.Body.Close()
			Expect(infoResp.StatusCode).To(Equal(http.StatusOK))
			var infoResult skillInfoResponse
			Expect(json.NewDecoder(infoResp.Body).Decode(&infoResult)).To(Succeed())
			Expect(infoResult.Metadata.Name).To(Equal(skillName))

			By("Uninstalling the skill")
			deleteResp := uninstallSkill(apiServer, skillName)
			defer deleteResp.Body.Close()
			Expect(deleteResp.StatusCode).To(Equal(http.StatusNoContent))

			By("Listing skills — should not contain the uninstalled skill")
			listResp2 := listSkills(apiServer)
			defer listResp2.Body.Close()
			Expect(listResp2.StatusCode).To(Equal(http.StatusOK))
			var listResult2 skillListResponse
			Expect(json.NewDecoder(listResp2.Body).Decode(&listResult2)).To(Succeed())
			for _, s := range listResult2.Skills {
				Expect(s.Metadata.Name).ToNot(Equal(skillName), "Skill should not appear after uninstall")
			}

			By("Getting skill info — should return 404")
			infoResp2 := getSkillInfo(apiServer, skillName)
			defer infoResp2.Body.Close()
			Expect(infoResp2.StatusCode).To(Equal(http.StatusNotFound))
		})
	})

	Describe("Registry lookup install", func() {
		// TODO(#4200): The upstream registry JSON schema validation rejects
		// the test fixture. Debug the exact required fields and re-enable.
		PIt("should resolve a plain name from the registry and install from OCI", func() {
			skillName := "registry-lookup-skill"

			By("Starting an in-process OCI registry")
			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)

			// The OCI reference must use the skill name as the last path
			// component — the supply-chain check in installFromOCI validates
			// that the artifact's declared name matches the repository name.
			ociRef := fmt.Sprintf("%s/e2e-test/%s:v0.1.0",
				ociRegistry.Listener.Addr().String(), skillName)

			By("Creating and building the skill locally")
			skillDir := createTestSkillDir(skillName, "A skill for registry lookup E2E testing")
			buildResp := buildSkill(apiServer, skillDir, ociRef)
			defer buildResp.Body.Close()
			Expect(buildResp.StatusCode).To(Equal(http.StatusOK))

			By("Pushing the skill to the in-process OCI registry")
			pushResp := pushSkill(apiServer, ociRef)
			defer pushResp.Body.Close()
			Expect(pushResp.StatusCode).To(Equal(http.StatusNoContent))

			By("Creating an upstream-format registry JSON pointing to the OCI reference")
			registryFile := createUpstreamRegistryWithSkill(skillName, ociRef)

			By("Configuring the server to use the test registry")
			updateResp := updateRegistry(apiServer, "default", map[string]interface{}{
				"local_path": registryFile,
			})
			defer updateResp.Body.Close()
			Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

			By("Installing by plain skill name — should resolve from registry")
			installResp := installSkill(apiServer, installSkillRequest{Name: skillName})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Verifying the skill is fully installed (not pending)")
			var result installSkillResponse
			Expect(json.NewDecoder(installResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Skill.Status).To(Equal("installed"))
			Expect(result.Skill.Metadata.Name).To(Equal(skillName))
			Expect(result.Skill.Digest).ToNot(BeEmpty())
			Expect(result.Skill.Metadata.Version).To(Equal("0.1.0"))

			By("Cleaning up")
			cleanupResp := uninstallSkill(apiServer, skillName)
			defer cleanupResp.Body.Close()
		})
	})
})

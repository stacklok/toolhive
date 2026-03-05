// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Skills CLI", Label("api", "cli", "skills", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
		thvConfig *e2e.TestConfig
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
		thvConfig = e2e.NewTestConfig()
	})

	// thvSkillCmd creates a THVCommand for `thv skill <args>` with
	// TOOLHIVE_API_URL pointing to the test server.
	thvSkillCmd := func(args ...string) *e2e.THVCommand {
		fullArgs := append([]string{"skill"}, args...)
		return e2e.NewTHVCommand(thvConfig, fullArgs...).
			WithEnv("TOOLHIVE_API_URL=" + apiServer.BaseURL())
	}

	Describe("thv skill validate", func() {
		It("should succeed for a valid skill directory", func() {
			skillDir := createTestSkillDir("cli-valid-skill", "A valid skill for CLI testing")

			stdout, _ := thvSkillCmd("validate", skillDir).ExpectSuccess()
			// Text output should not contain "Error:" lines for a valid skill
			Expect(stdout).ToNot(ContainSubstring("Error:"))
		})

		It("should succeed with JSON output", func() {
			skillDir := createTestSkillDir("cli-valid-json", "A valid skill for JSON output")

			stdout, _ := thvSkillCmd("validate", "--format", "json", skillDir).ExpectSuccess()

			var result validationResultResponse
			Expect(json.Unmarshal([]byte(stdout), &result)).To(Succeed())
			Expect(result.Valid).To(BeTrue())
		})

		It("should fail for an invalid skill directory", func() {
			emptyDir := GinkgoT().TempDir()

			_, _, err := thvSkillCmd("validate", emptyDir).Run()
			Expect(err).To(HaveOccurred(), "validate should fail for directory without SKILL.md")
		})
	})

	Describe("thv skill build", func() {
		It("should build a valid skill and print the reference", func() {
			skillDir := createTestSkillDir("cli-build-skill", "A skill for CLI build testing")

			stdout, _ := thvSkillCmd("build", skillDir).ExpectSuccess()
			// The build command should output something (the reference)
			Expect(strings.TrimSpace(stdout)).ToNot(BeEmpty())
		})
	})

	Describe("thv skill install and list", func() {
		It("should install a skill and list it", func() {
			skillName := fmt.Sprintf("cli-install-%d", GinkgoRandomSeed())

			By("Installing the skill")
			thvSkillCmd("install", skillName).ExpectSuccess()

			By("Listing skills in text format — should show the installed skill")
			stdout, _ := thvSkillCmd("list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(skillName))

			By("Listing skills in JSON format")
			jsonOut, _ := thvSkillCmd("list", "--format", "json").ExpectSuccess()
			var skills []json.RawMessage
			Expect(json.Unmarshal([]byte(jsonOut), &skills)).To(Succeed())
			Expect(skills).ToNot(BeEmpty())
		})
	})

	Describe("thv skill info", func() {
		It("should show info for an installed skill", func() {
			skillName := fmt.Sprintf("cli-info-%d", GinkgoRandomSeed())

			By("Installing the skill")
			thvSkillCmd("install", skillName).ExpectSuccess()

			By("Getting info in text format")
			stdout, _ := thvSkillCmd("info", skillName).ExpectSuccess()
			Expect(stdout).To(ContainSubstring(skillName))

			By("Getting info in JSON format")
			jsonOut, _ := thvSkillCmd("info", "--format", "json", skillName).ExpectSuccess()
			Expect(jsonOut).To(ContainSubstring(skillName))
		})

		It("should fail for a non-existent skill", func() {
			_, _, err := thvSkillCmd("info", "no-such-skill-xyz").Run()
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("thv skill uninstall", func() {
		It("should uninstall an installed skill", func() {
			skillName := fmt.Sprintf("cli-uninstall-%d", GinkgoRandomSeed())

			By("Installing the skill")
			thvSkillCmd("install", skillName).ExpectSuccess()

			By("Uninstalling the skill")
			thvSkillCmd("uninstall", skillName).ExpectSuccess()

			By("Verifying the skill is no longer listed")
			stdout, _ := thvSkillCmd("list").ExpectSuccess()
			Expect(stdout).ToNot(ContainSubstring(skillName))
		})

		It("should fail for a non-existent skill", func() {
			_, _, err := thvSkillCmd("uninstall", "no-such-skill-xyz").Run()
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("CLI full lifecycle", func() {
		It("should support validate → build → install → list → info → uninstall → list", func() {
			skillName := fmt.Sprintf("cli-lifecycle-%d", GinkgoRandomSeed())

			By("Creating a valid skill directory")
			parentDir := GinkgoT().TempDir()
			skillDir := filepath.Join(parentDir, skillName)
			Expect(os.MkdirAll(skillDir, 0o755)).To(Succeed())

			skillMD := fmt.Sprintf(`---
name: %s
description: Full lifecycle CLI test
version: 1.0.0
---

# %s

A test skill for the full CLI lifecycle.
`, skillName, skillName)
			Expect(os.WriteFile(
				filepath.Join(skillDir, "SKILL.md"),
				[]byte(skillMD),
				0o644,
			)).To(Succeed())

			By("Validating the skill")
			thvSkillCmd("validate", skillDir).ExpectSuccess()

			By("Building the skill")
			thvSkillCmd("build", skillDir).ExpectSuccess()

			By("Installing the skill by name (pending)")
			thvSkillCmd("install", skillName).ExpectSuccess()

			By("Listing skills — should contain the skill")
			listOut, _ := thvSkillCmd("list").ExpectSuccess()
			Expect(listOut).To(ContainSubstring(skillName))

			By("Getting skill info")
			infoOut, _ := thvSkillCmd("info", skillName).ExpectSuccess()
			Expect(infoOut).To(ContainSubstring(skillName))

			By("Uninstalling the skill")
			thvSkillCmd("uninstall", skillName).ExpectSuccess()

			By("Listing skills — should no longer contain the skill")
			listOut2, _ := thvSkillCmd("list").ExpectSuccess()
			Expect(listOut2).ToNot(ContainSubstring(skillName))
		})
	})
})

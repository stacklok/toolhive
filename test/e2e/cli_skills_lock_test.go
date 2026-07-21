// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/test/e2e"
)

// This RFC THV-0080 feature is gated behind TOOLHIVE_SKILLS_LOCK_ENABLED
// while it lands across a stack of PRs (see skills.LockFileFeatureEnabled),
// so this Describe block runs its own server with the gate turned on rather
// than sharing the default-off server other CLI skills tests use.
var _ = Describe("Skills CLI lock file exit codes (RFC THV-0080)", Label("api", "cli", "skills", "skills-lock", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
		thvConfig *e2e.TestConfig
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		config.ExtraEnv = []string{"TOOLHIVE_SKILLS_LOCK_ENABLED=true"}
		apiServer = e2e.StartServer(config)
		thvConfig = e2e.NewTestConfig()
	})

	thvSkillCmd := func(args ...string) *e2e.THVCommand {
		fullArgs := append([]string{"skill"}, args...)
		return e2e.NewTHVCommand(thvConfig, fullArgs...).
			WithEnv("TOOLHIVE_API_URL=" + apiServer.BaseURL())
	}

	exitCodeOf := func(err error) int {
		var exitErr *exec.ExitError
		ExpectWithOffset(1, errors.As(err, &exitErr)).To(BeTrue(), "expected an *exec.ExitError, got %T: %v", err, err)
		return exitErr.ExitCode()
	}

	Describe("thv skill sync --check", func() {
		It("exits 0 when the project matches its lock file", func() {
			projectRoot := makeE2EProjectRoot()
			skillName := "cli-lock-clean-skill"

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushSkill(apiServer, ociRegistry, skillName, "A clean skill for CLI exit code testing")

			installResp := installSkill(apiServer, installSkillRequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(201))

			stdout, _ := thvSkillCmd("sync", "--check", "--project-root", projectRoot).ExpectSuccess()
			Expect(stdout).To(ContainSubstring("Up to date"))
			Expect(stdout).To(ContainSubstring(skillName))
		})

		It("exits 2 when the project has drifted from its lock file", func() {
			projectRoot := makeE2EProjectRoot()
			skillName := "cli-lock-drifted-skill"

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushSkill(apiServer, ociRegistry, skillName, "A drifted skill for CLI exit code testing")

			installResp := installSkill(apiServer, installSkillRequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(201))

			By("Deleting the installed files so the project drifts from the lock file")
			skillDir := filepath.Join(projectRoot, ".claude", "skills", skillName)
			Expect(os.RemoveAll(skillDir)).To(Succeed())

			_, _, err := thvSkillCmd("sync", "--check", "--project-root", projectRoot).Run()
			Expect(err).To(HaveOccurred())
			Expect(exitCodeOf(err)).To(Equal(2))
		})
	})

	Describe("thv skill sync without --yes", func() {
		It("exits 4 when running non-interactively without --yes", func() {
			projectRoot := makeE2EProjectRoot()

			_, _, err := thvSkillCmd("sync", "--project-root", projectRoot).Run()
			Expect(err).To(HaveOccurred(), "a non-interactive sync without --yes must refuse rather than proceed silently")
			Expect(exitCodeOf(err)).To(Equal(4))
		})
	})

	Describe("thv skill sync --yes", func() {
		It("exits 0 and proceeds without prompting", func() {
			projectRoot := makeE2EProjectRoot()

			stdout, _ := thvSkillCmd("sync", "--yes", "--project-root", projectRoot).ExpectSuccess()
			Expect(stdout).To(ContainSubstring("Nothing to sync"))
		})
	})

	Describe("thv skill upgrade --fail-on-changes", func() {
		It("exits 2 when a skill would change, without installing it", func() {
			projectRoot := makeE2EProjectRoot()
			skillName := "cli-lock-upgrade-fail-on-changes-skill"

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushSkill(apiServer, ociRegistry, skillName, "The original description")

			installResp := installSkill(apiServer, installSkillRequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(201))

			By("Republishing newer content at the same OCI reference")
			newSkillDir := createTestSkillDir(skillName, "The updated description")
			rebuildResp := buildSkill(apiServer, newSkillDir, ociRef)
			defer rebuildResp.Body.Close()
			Expect(rebuildResp.StatusCode).To(Equal(200))
			repushResp := pushSkill(apiServer, ociRef)
			defer repushResp.Body.Close()
			Expect(repushResp.StatusCode).To(Equal(204))

			root, err := lockfile.OpenRoot(projectRoot)
			Expect(err).ToNot(HaveOccurred())
			before, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			beforeEntry, ok := before.Get(skillName)
			Expect(ok).To(BeTrue())

			_, _, err = thvSkillCmd("upgrade", "--yes", "--fail-on-changes", "--project-root", projectRoot).Run()
			Expect(err).To(HaveOccurred())
			Expect(exitCodeOf(err)).To(Equal(2))

			By("Verifying nothing was actually installed before the conflict was reported")
			after, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			afterEntry, ok := after.Get(skillName)
			Expect(ok).To(BeTrue())
			Expect(afterEntry.Digest).To(Equal(beforeEntry.Digest),
				"--fail-on-changes must not install a changed skill before reporting the conflict")
		})
	})
})

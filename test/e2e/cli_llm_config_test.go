// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/llm"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("thv llm config", Label("cli", "llm", "e2e"), func() {
	var (
		thvConfig *e2e.TestConfig
		tempDir   string
		thvCmd    func(args ...string) *e2e.THVCommand
	)

	BeforeEach(func() {
		thvConfig = e2e.NewTestConfig()
		tempDir = GinkgoT().TempDir()

		// thvCmd creates a THVCommand with an isolated config and home directory
		// so these tests never touch the user's real config.yaml or secrets store.
		thvCmd = func(args ...string) *e2e.THVCommand {
			return e2e.NewTHVCommand(thvConfig, args...).
				WithEnv(
					"XDG_CONFIG_HOME="+tempDir,
					"HOME="+tempDir,
				)
		}

		// Configure the environment secrets provider so that commands like
		// "llm config reset" never touch the user's real keychain or 1Password.
		// The environment provider is non-interactive and read-only, making it
		// safe for E2E tests. DeleteCachedTokens is a no-op when the provider
		// cannot list or delete secrets.
		By("Configuring environment secrets provider")
		thvCmd("secret", "provider", "environment").ExpectSuccess()
	})

	Describe("thv llm config set", func() {
		It("persists gateway URL, issuer, and client-id; show --format json reflects them", func() {
			By("Setting all required fields")
			thvCmd(
				"llm", "config", "set",
				"--gateway-url", "https://llm.example.com",
				"--issuer", "https://auth.example.com",
				"--client-id", "test-client",
			).ExpectSuccess()

			By("Reading back the config via show --format json")
			stdout, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()

			var cfg llm.Config
			Expect(json.Unmarshal([]byte(stdout), &cfg)).To(Succeed())
			Expect(cfg.GatewayURL).To(Equal("https://llm.example.com"))
			Expect(cfg.OIDC.Issuer).To(Equal("https://auth.example.com"))
			Expect(cfg.OIDC.ClientID).To(Equal("test-client"))
		})

		It("rejects an HTTP gateway URL (HTTPS enforcement)", func() {
			By("Attempting to set an HTTP gateway URL")
			stdout, stderr, err := thvCmd(
				"llm", "config", "set",
				"--gateway-url", "http://llm.example.com",
			).Run()

			By("Verifying the command fails")
			Expect(err).To(HaveOccurred(),
				"HTTP gateway URL should be rejected; stdout=%q stderr=%q", stdout, stderr)

			By("Verifying the error mentions gateway_url")
			Expect(stderr).To(ContainSubstring("gateway_url"),
				"error message should reference the failing field")
		})

		It("allows incremental configuration without error", func() {
			By("Setting only the gateway URL (no issuer or client-id yet)")
			thvCmd(
				"llm", "config", "set",
				"--gateway-url", "https://llm.example.com",
			).ExpectSuccess()

			By("Reading back the partial config via show --format json")
			stdout, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()

			var cfg llm.Config
			Expect(json.Unmarshal([]byte(stdout), &cfg)).To(Succeed())
			Expect(cfg.GatewayURL).To(Equal("https://llm.example.com"))
		})
	})

	Describe("thv llm config show", func() {
		It("prints the 'not configured' message on a clean config", func() {
			By("Running show before any config has been set")
			stdout, _ := thvCmd("llm", "config", "show").ExpectSuccess()

			By("Verifying the not-configured message is present")
			Expect(stdout).To(ContainSubstring("not configured"),
				"show should explain that the LLM gateway is not configured")
		})

		It("prints human-readable text after configuration", func() {
			By("Setting all required fields")
			thvCmd(
				"llm", "config", "set",
				"--gateway-url", "https://llm.example.com",
				"--issuer", "https://auth.example.com",
				"--client-id", "test-client",
			).ExpectSuccess()

			By("Running show in text format")
			stdout, _ := thvCmd("llm", "config", "show").ExpectSuccess()

			Expect(stdout).To(ContainSubstring("https://llm.example.com"))
			Expect(stdout).To(ContainSubstring("https://auth.example.com"))
			Expect(stdout).To(ContainSubstring("test-client"))
		})
	})

	Describe("thv llm config reset", func() {
		It("clears the config so that show returns the not-configured message", func() {
			By("Setting all required fields first")
			thvCmd(
				"llm", "config", "set",
				"--gateway-url", "https://llm.example.com",
				"--issuer", "https://auth.example.com",
				"--client-id", "test-client",
			).ExpectSuccess()

			By("Resetting the config")
			thvCmd("llm", "config", "reset").ExpectSuccess()

			By("Verifying show returns the not-configured message")
			stdout, _ := thvCmd("llm", "config", "show").ExpectSuccess()
			Expect(stdout).To(ContainSubstring("not configured"))

			By("Verifying show --format json returns an empty config")
			stdout, _ = thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()

			var cfg llm.Config
			Expect(json.Unmarshal([]byte(stdout), &cfg)).To(Succeed())
			Expect(cfg.GatewayURL).To(BeEmpty())
			Expect(cfg.OIDC.Issuer).To(BeEmpty())
			Expect(cfg.OIDC.ClientID).To(BeEmpty())
		})
	})
})

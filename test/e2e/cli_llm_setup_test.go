// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// llmSetupEnv holds the isolated environment used by a single test.
type llmSetupEnv struct {
	cfg    *e2e.TestConfig
	tmpDir string
	thv    func(args ...string) *e2e.THVCommand
}

// newLLMSetupEnv builds an isolated HOME directory and a thv command factory
// that uses it. The gateway config is pre-seeded so that "thv llm setup" does
// not fail with "not configured".
func newLLMSetupEnv() *llmSetupEnv {
	cfg := e2e.NewTestConfig()
	tmp := GinkgoT().TempDir()

	thv := func(args ...string) *e2e.THVCommand {
		return e2e.NewTHVCommand(cfg, args...).
			WithEnv(
				"XDG_CONFIG_HOME="+tmp,
				"HOME="+tmp,
			)
	}

	// Use environment secrets provider so tests never touch the real keychain.
	thv("secret", "provider", "environment").ExpectSuccess()

	// Pre-seed a valid (but fake) LLM gateway config so setup can proceed.
	thv(
		"llm", "config", "set",
		"--gateway-url", "https://llm.example.com",
		"--issuer", "https://auth.example.com",
		"--client-id", "test-client",
	).ExpectSuccess()

	return &llmSetupEnv{cfg: cfg, tmpDir: tmp, thv: thv}
}

// simulateTool creates the settings directory that thv uses to detect whether
// a tool is installed, so setup will include it without needing the real binary.
func (e *llmSetupEnv) simulateTool(relDir ...string) {
	dir := filepath.Join(append([]string{e.tmpDir}, relDir...)...)
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
}

// readSettingsJSON reads and parses the JSON (or JSONC) file at path.
func readSettingsJSON(path string) map[string]any {
	data, err := os.ReadFile(path) //nolint:gosec // test reads a known temp-dir path
	Expect(err).ToNot(HaveOccurred(), "settings file should exist at %s", path)
	var out map[string]any
	Expect(json.Unmarshal(data, &out)).To(Succeed())
	return out
}

var _ = Describe("thv llm setup / teardown", Label("cli", "llm", "e2e"), func() {

	// ── Claude Code (direct mode) ──────────────────────────────────────────────

	Describe("claude-code (direct mode)", func() {
		var (
			env          *llmSetupEnv
			settingsPath string
		)

		BeforeEach(func() {
			env = newLLMSetupEnv()
			// Simulate Claude Code installation by creating its settings directory.
			env.simulateTool(".claude")
			settingsPath = filepath.Join(env.tmpDir, ".claude", "settings.json")
		})

		It("patches settings.json with apiKeyHelper and ANTHROPIC_BASE_URL on setup", func() {
			By("Running thv llm setup")
			stdout, _ := env.thv("llm", "setup").ExpectSuccess()
			Expect(stdout).To(ContainSubstring("claude-code"))

			By("Verifying the settings file was created")
			settings := readSettingsJSON(settingsPath)

			By("Verifying apiKeyHelper is set to the thv token-helper command")
			apiKeyHelper, ok := settings["apiKeyHelper"].(string)
			Expect(ok).To(BeTrue(), "apiKeyHelper should be a string")
			Expect(apiKeyHelper).To(ContainSubstring("llm token"),
				"apiKeyHelper should invoke 'thv llm token'")

			By("Verifying ANTHROPIC_BASE_URL is set to the gateway URL")
			envMap, ok := settings["env"].(map[string]any)
			Expect(ok).To(BeTrue(), "env should be an object")
			Expect(envMap["ANTHROPIC_BASE_URL"]).To(Equal("https://llm.example.com"))
		})

		It("removes all LLM gateway keys from settings.json on teardown", func() {
			By("Setting up first")
			env.thv("llm", "setup").ExpectSuccess()

			By("Running thv llm teardown")
			env.thv("llm", "teardown").ExpectSuccess()

			By("Verifying settings.json no longer contains LLM gateway keys")
			settings := readSettingsJSON(settingsPath)
			Expect(settings).ToNot(HaveKey("apiKeyHelper"),
				"apiKeyHelper should be removed after teardown")
			envMap, _ := settings["env"].(map[string]any)
			Expect(envMap).ToNot(HaveKey("ANTHROPIC_BASE_URL"),
				"ANTHROPIC_BASE_URL should be removed after teardown")
		})

		It("is idempotent: a second setup overwrites the first without duplicating keys", func() {
			By("Running setup twice")
			env.thv("llm", "setup").ExpectSuccess()
			env.thv("llm", "setup").ExpectSuccess()

			By("Verifying only one value exists for each key")
			settings := readSettingsJSON(settingsPath)
			Expect(settings["apiKeyHelper"]).To(BeAssignableToTypeOf(""),
				"apiKeyHelper should be a single string, not a list")
		})

		It("preserves existing non-LLM keys in settings.json during setup and teardown", func() {
			By("Pre-seeding settings.json with an unrelated key")
			existing := `{"someExistingKey": "existing-value"}`
			Expect(os.WriteFile(settingsPath, []byte(existing), 0o600)).To(Succeed())

			By("Running setup")
			env.thv("llm", "setup").ExpectSuccess()
			settings := readSettingsJSON(settingsPath)
			Expect(settings["someExistingKey"]).To(Equal("existing-value"),
				"setup should not remove pre-existing keys")

			By("Running teardown")
			env.thv("llm", "teardown").ExpectSuccess()
			settings = readSettingsJSON(settingsPath)
			Expect(settings["someExistingKey"]).To(Equal("existing-value"),
				"teardown should not remove pre-existing keys")
		})
	})

	// ── Gemini CLI (direct mode) ───────────────────────────────────────────────

	Describe("gemini-cli (direct mode)", func() {
		var (
			env          *llmSetupEnv
			settingsPath string
		)

		BeforeEach(func() {
			env = newLLMSetupEnv()
			// Simulate Gemini CLI installation by creating its settings directory.
			env.simulateTool(".gemini")
			settingsPath = filepath.Join(env.tmpDir, ".gemini", "settings.json")
		})

		It("patches settings.json with auth.tokenCommand and baseUrl on setup", func() {
			By("Running thv llm setup")
			stdout, _ := env.thv("llm", "setup").ExpectSuccess()
			Expect(stdout).To(ContainSubstring("gemini-cli"))

			By("Verifying the settings file was created")
			settings := readSettingsJSON(settingsPath)

			By("Verifying auth.tokenCommand is set to the thv token-helper command")
			authMap, ok := settings["auth"].(map[string]any)
			Expect(ok).To(BeTrue(), "auth should be an object")
			tokenCmd, ok := authMap["tokenCommand"].(string)
			Expect(ok).To(BeTrue(), "auth.tokenCommand should be a string")
			Expect(tokenCmd).To(ContainSubstring("llm token"),
				"auth.tokenCommand should invoke 'thv llm token'")

			By("Verifying baseUrl is set to the gateway URL")
			Expect(settings["baseUrl"]).To(Equal("https://llm.example.com"))
		})

		It("removes all LLM gateway keys from settings.json on teardown", func() {
			By("Setting up first")
			env.thv("llm", "setup").ExpectSuccess()

			By("Running thv llm teardown")
			env.thv("llm", "teardown").ExpectSuccess()

			By("Verifying settings.json no longer contains LLM gateway keys")
			settings := readSettingsJSON(settingsPath)
			authMap, _ := settings["auth"].(map[string]any)
			Expect(authMap).ToNot(HaveKey("tokenCommand"),
				"auth.tokenCommand should be removed after teardown")
			Expect(settings).ToNot(HaveKey("baseUrl"),
				"baseUrl should be removed after teardown")
		})

		It("preserves existing non-LLM keys in settings.json during setup and teardown", func() {
			By("Pre-seeding settings.json with an unrelated key")
			existing := `{"someExistingKey": "existing-value"}`
			Expect(os.WriteFile(settingsPath, []byte(existing), 0o600)).To(Succeed())

			By("Running setup")
			env.thv("llm", "setup").ExpectSuccess()
			settings := readSettingsJSON(settingsPath)
			Expect(settings["someExistingKey"]).To(Equal("existing-value"),
				"setup should not remove pre-existing keys")

			By("Running teardown")
			env.thv("llm", "teardown").ExpectSuccess()
			settings = readSettingsJSON(settingsPath)
			Expect(settings["someExistingKey"]).To(Equal("existing-value"),
				"teardown should not remove pre-existing keys")
		})
	})

	// ── Both tools simultaneously ──────────────────────────────────────────────

	Describe("multiple tools detected", func() {
		var env *llmSetupEnv

		BeforeEach(func() {
			env = newLLMSetupEnv()
			env.simulateTool(".claude")
			env.simulateTool(".gemini")
		})

		It("configures all detected tools in a single setup call", func() {
			By("Running thv llm setup")
			stdout, _ := env.thv("llm", "setup").ExpectSuccess()
			Expect(stdout).To(ContainSubstring("claude-code"))
			Expect(stdout).To(ContainSubstring("gemini-cli"))

			By("Verifying both settings files exist and contain LLM gateway keys")
			claudeSettings := readSettingsJSON(filepath.Join(env.tmpDir, ".claude", "settings.json"))
			Expect(claudeSettings).To(HaveKey("apiKeyHelper"))

			geminiSettings := readSettingsJSON(filepath.Join(env.tmpDir, ".gemini", "settings.json"))
			Expect(geminiSettings).To(HaveKey("baseUrl"))
		})

		It("reverts all tools in a single teardown call", func() {
			By("Setting up first")
			env.thv("llm", "setup").ExpectSuccess()

			By("Running thv llm teardown")
			env.thv("llm", "teardown").ExpectSuccess()

			By("Verifying both settings files have LLM keys removed")
			claudeSettings := readSettingsJSON(filepath.Join(env.tmpDir, ".claude", "settings.json"))
			Expect(claudeSettings).ToNot(HaveKey("apiKeyHelper"))

			geminiSettings := readSettingsJSON(filepath.Join(env.tmpDir, ".gemini", "settings.json"))
			Expect(geminiSettings).ToNot(HaveKey("baseUrl"))
		})
	})

	// ── Edge cases ─────────────────────────────────────────────────────────────

	Describe("edge cases", func() {
		It("setup fails with a clear error when LLM gateway is not configured", func() {
			cfg := e2e.NewTestConfig()
			tmp := GinkgoT().TempDir()
			thv := func(args ...string) *e2e.THVCommand {
				return e2e.NewTHVCommand(cfg, args...).
					WithEnv("XDG_CONFIG_HOME="+tmp, "HOME="+tmp)
			}
			thv("secret", "provider", "environment").ExpectSuccess()

			By("Simulating a tool installation without configuring the gateway")
			Expect(os.MkdirAll(filepath.Join(tmp, ".claude"), 0o700)).To(Succeed())

			By("Running setup without config set")
			_, stderr, err := thv("llm", "setup").Run()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("not configured"),
				"setup should explain that the gateway is not configured")
		})

		It("setup prints 'no supported tools detected' when no tool directories exist", func() {
			env := newLLMSetupEnv()

			By("Running setup without any tool directories present")
			stdout, _ := env.thv("llm", "setup").ExpectSuccess()
			Expect(stdout).To(ContainSubstring("No supported AI tools detected"))
		})

		It("teardown is a no-op when no tools were configured", func() {
			env := newLLMSetupEnv()
			// Do not run setup first.
			env.thv("llm", "teardown").ExpectSuccess()
		})
	})
})

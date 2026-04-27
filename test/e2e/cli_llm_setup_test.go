// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/llm"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/test/e2e"
)

// runSetupWithOIDCCompletion runs "thv llm setup" in a goroutine and
// concurrently satisfies the OIDC authorization request so the command
// completes without a real browser.
func runSetupWithOIDCCompletion(
	thvCmd func(args ...string) *e2e.THVCommand,
	oidcServer *e2e.OIDCMockServer,
	extraArgs ...string,
) (string, string, error) {
	type result struct {
		stdout, stderr string
		err            error
	}
	done := make(chan result, 1)

	args := append([]string{"llm", "setup"}, extraArgs...)
	go func() {
		stdout, stderr, err := thvCmd(args...).RunWithTimeout(60 * time.Second)
		done <- result{stdout, stderr, err}
	}()

	// Wait for the authorization request to hit the OIDC server and complete it.
	authReq, err := oidcServer.WaitForAuthRequest(30 * time.Second)
	if err != nil {
		// Drain the goroutine before returning.
		<-done
		return "", "", fmt.Errorf("waiting for OIDC auth request: %w", err)
	}
	if err := oidcServer.CompleteAuthRequest(authReq); err != nil {
		<-done
		return "", "", fmt.Errorf("completing OIDC auth request: %w", err)
	}

	r := <-done
	return r.stdout, r.stderr, r.err
}

var _ = Describe("thv llm setup / teardown", Label("cli", "llm", "setup", "e2e"), func() {
	var (
		thvConfig    *e2e.TestConfig
		tempDir      string
		oidcPort     int
		oidcServer   *e2e.OIDCMockServer
		thvCmd       func(args ...string) *e2e.THVCommand
		gatewayURL   = "https://llm.example.com"
		clientID     = "test-client"
		clientSecret = "test-secret"
	)

	BeforeEach(func() {
		thvConfig = e2e.NewTestConfig()
		tempDir = GinkgoT().TempDir()

		// Isolated environment: XDG_CONFIG_HOME and HOME point to tempDir so
		// these tests never touch the user's real config.yaml or secrets store.
		thvCmd = func(args ...string) *e2e.THVCommand {
			return e2e.NewTHVCommand(thvConfig, args...).
				WithEnv(
					"XDG_CONFIG_HOME="+tempDir,
					"HOME="+tempDir,
				)
		}

		// Use environment secrets provider to avoid touching the system keychain.
		By("Configuring environment secrets provider")
		thvCmd("secret", "provider", "environment").ExpectSuccess()

		// Allocate a free port for the OIDC mock server.
		var err error
		oidcPort, err = networking.FindOrUsePort(0)
		Expect(err).ToNot(HaveOccurred())

		// Create and start the mock OIDC server.
		By(fmt.Sprintf("Starting OIDC mock server on port %d", oidcPort))
		oidcServer, err = e2e.NewOIDCMockServer(oidcPort, clientID, clientSecret)
		Expect(err).ToNot(HaveOccurred())
		oidcServer.EnableAutoComplete()
		Expect(oidcServer.Start()).To(Succeed())

		// Wait for the OIDC discovery endpoint to be ready.
		Eventually(func() error {
			return checkServerHealth(fmt.Sprintf("http://localhost:%d/.well-known/openid-configuration", oidcPort))
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
	})

	AfterEach(func() {
		if oidcServer != nil {
			_ = oidcServer.Stop()
		}
	})

	// ── Test 1 ────────────────────────────────────────────────────────────────

	Describe("thv llm setup with inline flags", func() {
		It("patches detected tools and persists config", func() {
			// Create ~/.claude/ so the Claude Code adapter detects the tool.
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			By("Running thv llm setup with inline flags (OIDC auto-completes)")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd,
				oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("Verifying ~/.claude/settings.json was patched")
			settingsPath := filepath.Join(tempDir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			Expect(err).ToNot(HaveOccurred(), "settings.json should exist after setup")

			var settings map[string]any
			Expect(json.Unmarshal(data, &settings)).To(Succeed())
			Expect(settings).To(HaveKey("apiKeyHelper"),
				"apiKeyHelper should be set in settings.json")
			Expect(fmt.Sprintf("%v", settings["apiKeyHelper"])).To(ContainSubstring("llm token"),
				"apiKeyHelper should invoke thv llm token")

			By("Verifying config show --format json reflects ConfiguredTools")
			showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
			var cfg llm.Config
			Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
			Expect(cfg.ConfiguredTools).ToNot(BeEmpty(), "at least one tool should be configured")

			found := false
			for _, tc := range cfg.ConfiguredTools {
				if tc.Tool == "claude-code" {
					found = true
					Expect(tc.Mode).To(Equal("direct"))
					break
				}
			}
			Expect(found).To(BeTrue(), "claude-code should appear in ConfiguredTools")
		})
	})

	// ── Test 2 ────────────────────────────────────────────────────────────────

	Describe("thv llm teardown", func() {
		It("reverts all tool configs", func() {
			// Create ~/.claude/ to trigger Claude Code detection.
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			By("Running setup first")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd,
				oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("Verifying settings.json was patched")
			settingsPath := filepath.Join(tempDir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			Expect(err).ToNot(HaveOccurred())
			var before map[string]any
			Expect(json.Unmarshal(data, &before)).To(Succeed())
			Expect(before).To(HaveKey("apiKeyHelper"))

			By("Running thv llm teardown")
			thvCmd("llm", "teardown").ExpectSuccess()

			By("Verifying apiKeyHelper is no longer in settings.json")
			data, err = os.ReadFile(settingsPath)
			Expect(err).ToNot(HaveOccurred())
			var after map[string]any
			Expect(json.Unmarshal(data, &after)).To(Succeed())
			Expect(after).ToNot(HaveKey("apiKeyHelper"),
				"apiKeyHelper should be removed after teardown")

			By("Verifying ConfiguredTools is empty")
			showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
			var cfg llm.Config
			Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
			Expect(cfg.ConfiguredTools).To(BeEmpty())
		})
	})

	// ── Test 3 ────────────────────────────────────────────────────────────────

	Describe("thv llm teardown <tool-name>", func() {
		It("targets a single tool and leaves others configured", func() {
			// We only have one real detectable tool in the test environment (claude-code
			// via ~/.claude). This test verifies the targeted teardown path by tearing
			// down by the tool name that was configured in setup and confirming that
			// a subsequent teardown of an unknown tool returns an error.
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			By("Running setup")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd,
				oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("Tearing down only claude-code by name")
			thvCmd("llm", "teardown", "claude-code").ExpectSuccess()

			By("Verifying apiKeyHelper was removed")
			settingsPath := filepath.Join(tempDir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			Expect(err).ToNot(HaveOccurred())
			var settings map[string]any
			Expect(json.Unmarshal(data, &settings)).To(Succeed())
			Expect(settings).ToNot(HaveKey("apiKeyHelper"))

			By("Verifying teardown of unknown tool returns error")
			_, _, err = thvCmd("llm", "teardown", "nonexistent-tool").Run()
			Expect(err).To(HaveOccurred(), "teardown of unknown tool should fail")
		})
	})

	// ── Test 4 ────────────────────────────────────────────────────────────────

	Describe("thv llm setup without config and no flags", func() {
		It("returns an error about not configured", func() {
			By("Running setup with no prior config and no inline flags")
			stdout, stderr, err := thvCmd("llm", "setup").Run()

			By("Verifying the command fails")
			Expect(err).To(HaveOccurred(),
				"setup without config should fail; stdout=%q stderr=%q", stdout, stderr)

			By("Verifying the error message references configuration")
			Expect(stderr).To(ContainSubstring("not configured"),
				"error should mention the gateway is not configured")
		})
	})

	// ── Test 5 ────────────────────────────────────────────────────────────────

	Describe("thv llm teardown --purge-tokens", func() {
		It("clears cached tokens in addition to reverting tool configs", func() {
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			By("Running setup")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd,
				oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("Running teardown with --purge-tokens")
			thvCmd("llm", "teardown", "--purge-tokens").ExpectSuccess()

			By("Verifying config is cleared (ConfiguredTools empty)")
			showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
			var cfg llm.Config
			Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
			Expect(cfg.ConfiguredTools).To(BeEmpty(),
				"ConfiguredTools should be empty after teardown --purge-tokens")

			By("Verifying the CachedRefreshTokenRef is cleared (purge-tokens removes token ref)")
			// The environment provider cannot delete secrets, so the token ref is
			// removed only from config (not the env var). We verify at least that
			// ConfiguredTools is clean, which is the primary teardown contract.
			Expect(cfg.OIDC.CachedRefreshTokenRef).To(BeEmpty(),
				"cached token reference should be cleared after purge")
		})
	})
})

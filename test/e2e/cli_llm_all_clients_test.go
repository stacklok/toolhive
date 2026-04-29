// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/auth/tokensource"
	"github.com/stacklok/toolhive/pkg/llm"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/test/e2e"
)

const (
	osDarwin       = "darwin"
	clientThvProxy = "thv-proxy"
)

// llmClientTestCase defines everything needed to test a single LLM gateway
// client: directories to create for detection, an optional binary stub, the
// path to the settings file after setup, and the expected JSON keys.
type llmClientTestCase struct {
	// name is the thv client name (e.g. "claude-code")
	name string

	// detectionDir returns the directory path (relative to tempDir) that must
	// exist for thv to consider the client "installed".
	detectionDir func(tempDir string) string

	// binaryName is the stub executable to place on PATH (empty = no binary check).
	binaryName string

	// settingsPath returns the absolute path to the settings file that thv will
	// patch during setup.
	settingsPath func(tempDir string) string

	// mode is "direct" or "proxy".
	mode string

	// expectedKeys maps JSON pointer paths to a function that validates the value.
	// The function receives (gatewayURL, proxyBaseURL) for flexibility.
	expectedKeys map[string]func(gatewayURL, proxyURL string) string

	// skipOnOS is a GOOS value ("linux", "darwin") on which the test is skipped.
	// Empty means the test runs everywhere.
	skipOnOS string
}

// allClientTestCases returns the full test matrix for all supported LLM
// gateway clients. This mirrors the production detection logic in
// pkg/client/llm_gateway.go.
func allClientTestCases() []llmClientTestCase {
	return []llmClientTestCase{
		{
			name: "claude-code",
			detectionDir: func(tempDir string) string {
				return filepath.Join(tempDir, ".claude")
			},
			binaryName: "claude",
			settingsPath: func(tempDir string) string {
				return filepath.Join(tempDir, ".claude", "settings.json")
			},
			mode: "direct",
			expectedKeys: map[string]func(string, string) string{
				"/apiKeyHelper": func(_, _ string) string { return "llm token" },
				"/env/ANTHROPIC_BASE_URL": func(gatewayURL, _ string) string {
					return gatewayURL
				},
			},
		},
		{
			name: "gemini-cli",
			detectionDir: func(tempDir string) string {
				return filepath.Join(tempDir, ".gemini")
			},
			binaryName: "gemini",
			settingsPath: func(tempDir string) string {
				return filepath.Join(tempDir, ".gemini", "settings.json")
			},
			mode: "direct",
			expectedKeys: map[string]func(string, string) string{
				"/auth/tokenCommand": func(_, _ string) string { return "llm token" },
				"/baseUrl": func(gatewayURL, _ string) string {
					return gatewayURL
				},
			},
		},
		{
			name: "cursor",
			detectionDir: func(tempDir string) string {
				return llmSettingsDirFor("cursor", tempDir)
			},
			binaryName: "cursor",
			settingsPath: func(tempDir string) string {
				return filepath.Join(llmSettingsDirFor("cursor", tempDir), "settings.json")
			},
			mode: "proxy",
			expectedKeys: map[string]func(string, string) string{
				"/cursor.general.openAIBaseURL": func(_, proxyURL string) string {
					return proxyURL
				},
				"/cursor.general.openAIAPIKey": func(_, _ string) string {
					return clientThvProxy
				},
			},
		},
		{
			name: "vscode",
			detectionDir: func(tempDir string) string {
				return llmSettingsDirFor("vscode", tempDir)
			},
			binaryName: "code",
			settingsPath: func(tempDir string) string {
				return filepath.Join(llmSettingsDirFor("vscode", tempDir), "settings.json")
			},
			mode: "proxy",
			expectedKeys: map[string]func(string, string) string{
				"/github.copilot.advanced.serverUrl": func(_, proxyURL string) string {
					return proxyURL
				},
				"/github.copilot.advanced.apiKey": func(_, _ string) string {
					return clientThvProxy
				},
			},
		},
		{
			name: "vscode-insider",
			detectionDir: func(tempDir string) string {
				return llmSettingsDirFor("vscode-insider", tempDir)
			},
			binaryName: "code",
			settingsPath: func(tempDir string) string {
				return filepath.Join(llmSettingsDirFor("vscode-insider", tempDir), "settings.json")
			},
			mode: "proxy",
			expectedKeys: map[string]func(string, string) string{
				"/github.copilot.advanced.serverUrl": func(_, proxyURL string) string {
					return proxyURL
				},
				"/github.copilot.advanced.apiKey": func(_, _ string) string {
					return clientThvProxy
				},
			},
		},
		{
			name: "xcode",
			detectionDir: func(tempDir string) string {
				return llmSettingsDirFor("xcode", tempDir)
			},
			binaryName: "", // no binary check for xcode
			settingsPath: func(tempDir string) string {
				return filepath.Join(llmSettingsDirFor("xcode", tempDir), "editorSettings.json")
			},
			mode:     "proxy",
			skipOnOS: "linux", // xcode path is macOS-only
			expectedKeys: map[string]func(string, string) string{
				"/openAIBaseURL": func(_, proxyURL string) string {
					return proxyURL
				},
				"/apiKey": func(_, _ string) string {
					return clientThvProxy
				},
			},
		},
	}
}

// llmSettingsDirFor returns the directory (under tempDir) that thv uses for
// the LLM gateway settings file of the named client. The path mirrors the
// production buildLLMSettingsPath logic from pkg/client/config.go.
func llmSettingsDirFor(client, tempDir string) string {
	switch client {
	case "cursor":
		if runtime.GOOS == osDarwin {
			return filepath.Join(tempDir, "Library", "Application Support", "Cursor", "User")
		}
		return filepath.Join(tempDir, ".config", "Cursor", "User")
	case "vscode":
		if runtime.GOOS == osDarwin {
			return filepath.Join(tempDir, "Library", "Application Support", "Code", "User")
		}
		return filepath.Join(tempDir, ".config", "Code", "User")
	case "vscode-insider":
		if runtime.GOOS == osDarwin {
			return filepath.Join(tempDir, "Library", "Application Support", "Code - Insiders", "User")
		}
		return filepath.Join(tempDir, ".config", "Code - Insiders", "User")
	case "xcode":
		// macOS only
		return filepath.Join(tempDir, "Library", "Application Support", "GitHub Copilot for Xcode")
	default:
		panic(fmt.Sprintf("unknown client: %s", client))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Suite
// ─────────────────────────────────────────────────────────────────────────────

var _ = Describe("thv llm — all-client matrix", Label("cli", "llm", "clients", "e2e"), func() {
	if runtime.GOOS == "windows" {
		Skip("fake-browser stub is POSIX-only; skipping on Windows")
	}

	var (
		thvConfig    *e2e.TestConfig
		tempDir      string
		oidcPort     int
		oidcServer   *e2e.OIDCMockServer
		binDir       string
		thvCmd       func(args ...string) *e2e.THVCommand
		gatewayURL   = "https://llm.example.com"
		clientID     = "test-client"
		clientSecret = "test-secret"
	)

	BeforeEach(func() {
		thvConfig = e2e.NewTestConfig()
		tempDir = GinkgoT().TempDir()

		// Create a fake browser dir so OIDC can complete headlessly.
		var err error
		binDir, err = e2e.CreateFakeBrowserDir(tempDir)
		Expect(err).ToNot(HaveOccurred())

		// Allocate a free port for the OIDC mock server.
		oidcPort, err = networking.FindOrUsePort(0)
		Expect(err).ToNot(HaveOccurred())

		oidcServer, err = e2e.NewOIDCMockServer(oidcPort, clientID, clientSecret)
		Expect(err).ToNot(HaveOccurred())
		oidcServer.EnableAutoComplete()
		Expect(oidcServer.Start()).To(Succeed())

		Eventually(func() error {
			return checkServerHealth(fmt.Sprintf("http://localhost:%d/.well-known/openid-configuration", oidcPort))
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())

		thvCmd = func(args ...string) *e2e.THVCommand {
			return e2e.NewTHVCommand(thvConfig, args...).
				WithEnv(
					"XDG_CONFIG_HOME="+tempDir,
					"HOME="+tempDir,
					"PATH="+binDir+":"+os.Getenv("PATH"),
				)
		}

		By("Configuring environment secrets provider")
		thvCmd("secret", "provider", "environment").ExpectSuccess()
	})

	AfterEach(func() {
		if oidcServer != nil {
			_ = oidcServer.Stop()
		}
	})

	// ── Per-client setup + teardown ────────────────────────────────────────────

	Describe("per-client setup patches settings and teardown reverts them", func() {
		for _, clientTC := range allClientTestCases() {
			clientTC := clientTC // capture loop variable
			It(clientTC.name, func() {
				if clientTC.skipOnOS != "" && runtime.GOOS == clientTC.skipOnOS {
					Skip(fmt.Sprintf("client %q not supported on %s", clientTC.name, runtime.GOOS))
				}

				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

				// Create the detection directory so thv considers this client installed.
				By(fmt.Sprintf("[%s] creating detection directory", clientTC.name))
				detectionDir := clientTC.detectionDir(tempDir)
				Expect(os.MkdirAll(detectionDir, 0750)).To(Succeed())

				// Stub the required binary (if any) in binDir.
				if clientTC.binaryName != "" {
					By(fmt.Sprintf("[%s] stubbing binary %q", clientTC.name, clientTC.binaryName))
					Expect(createFakeBinary(binDir, clientTC.binaryName)).To(Succeed())
				}

				// ── setup ────────────────────────────────────────────────────────
				By(fmt.Sprintf("[%s] running thv llm setup", clientTC.name))
				stdout, stderr, err := runSetupWithOIDCCompletion(
					thvCmd,
					oidcServer,
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				)
				Expect(err).ToNot(HaveOccurred(),
					"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

				// Verify the settings file was created and patched.
				settingsFile := clientTC.settingsPath(tempDir)
				By(fmt.Sprintf("[%s] verifying settings file was patched: %s", clientTC.name, settingsFile))
				data, err := os.ReadFile(settingsFile)
				Expect(err).ToNot(HaveOccurred(), "settings file should exist after setup")

				var settings map[string]any
				Expect(json.Unmarshal(data, &settings)).To(Succeed())

				proxyBaseURL := fmt.Sprintf("http://localhost:%d/v1", llm.DefaultProxyListenPort)
				for pointer, valueFn := range clientTC.expectedKeys {
					expectedSubstr := valueFn(gatewayURL, proxyBaseURL)
					actualValue := jsonPointerGet(settings, pointer)
					Expect(actualValue).To(ContainSubstring(expectedSubstr),
						"JSON pointer %s should contain %q in %s", pointer, expectedSubstr, settingsFile)
				}

				// Verify config show reflects this client.
				By(fmt.Sprintf("[%s] verifying config show contains this client", clientTC.name))
				showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				var cfg llm.Config
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())

				found := false
				for _, toolCfg := range cfg.ConfiguredTools {
					if string(toolCfg.Tool) == clientTC.name {
						found = true
						Expect(toolCfg.Mode).To(Equal(clientTC.mode),
							"client %s should be in %s mode", clientTC.name, clientTC.mode)
						break
					}
				}
				Expect(found).To(BeTrue(), "client %q should appear in ConfiguredTools", clientTC.name)

				// ── teardown ─────────────────────────────────────────────────────
				By(fmt.Sprintf("[%s] running thv llm teardown %s", clientTC.name, clientTC.name))
				thvCmd("llm", "teardown", clientTC.name).ExpectSuccess()

				By(fmt.Sprintf("[%s] verifying settings file was reverted", clientTC.name))
				data, err = os.ReadFile(settingsFile)
				Expect(err).ToNot(HaveOccurred())
				var after map[string]any
				Expect(json.Unmarshal(data, &after)).To(Succeed())

				for pointer := range clientTC.expectedKeys {
					value := jsonPointerGet(after, pointer)
					Expect(value).To(BeEmpty(),
						"JSON pointer %s should be absent after teardown in %s", pointer, settingsFile)
				}

				showOut, _ = thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				cfg = llm.Config{}
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				Expect(cfg.ConfiguredTools).To(BeEmpty(),
					"ConfiguredTools should be empty after teardown of %s", clientTC.name)
			})
		}
	})

	// ── Multi-client setup ─────────────────────────────────────────────────────

	Describe("multi-client setup", func() {
		It("configures all detected clients in a single setup call", func() {
			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			// Install all clients (skip xcode on Linux).
			installedClients := installAllDetectedClients(tempDir, binDir)

			By(fmt.Sprintf("running setup with %d clients installed", len(installedClients)))
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd,
				oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("verifying all installed clients appear in ConfiguredTools")
			showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
			var cfg llm.Config
			Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())

			configuredNames := make(map[string]bool)
			for _, tc := range cfg.ConfiguredTools {
				configuredNames[string(tc.Tool)] = true
			}
			for _, clientName := range installedClients {
				Expect(configuredNames).To(HaveKey(clientName),
					"client %q should appear in ConfiguredTools after multi-client setup", clientName)
			}
			Expect(cfg.ConfiguredTools).To(HaveLen(len(installedClients)),
				"number of configured tools should match installed clients")
		})
	})

	// ── Targeted teardown ──────────────────────────────────────────────────────

	Describe("targeted teardown preserves other clients", func() {
		It("tears down only the named client while leaving others configured", func() {
			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			// Install claude-code and gemini-cli (both are cross-platform).
			claudeDir := filepath.Join(tempDir, ".claude")
			geminiDir := filepath.Join(tempDir, ".gemini")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
			Expect(os.MkdirAll(geminiDir, 0750)).To(Succeed())
			Expect(createFakeBinary(binDir, "claude")).To(Succeed())
			Expect(createFakeBinary(binDir, "gemini")).To(Succeed())

			By("running setup for both clients")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd, oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("verifying both clients are configured")
			showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
			var cfg llm.Config
			Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
			Expect(cfg.ConfiguredTools).To(HaveLen(2))

			By("tearing down only claude-code")
			thvCmd("llm", "teardown", "claude-code").ExpectSuccess()

			By("verifying claude-code settings are reverted")
			claudeSettings := filepath.Join(tempDir, ".claude", "settings.json")
			data, err := os.ReadFile(claudeSettings)
			Expect(err).ToNot(HaveOccurred())
			var claudeAfter map[string]any
			Expect(json.Unmarshal(data, &claudeAfter)).To(Succeed())
			Expect(claudeAfter).ToNot(HaveKey("apiKeyHelper"))

			By("verifying gemini-cli settings are still patched")
			geminiSettings := filepath.Join(tempDir, ".gemini", "settings.json")
			data, err = os.ReadFile(geminiSettings)
			Expect(err).ToNot(HaveOccurred())
			var geminiAfter map[string]any
			Expect(json.Unmarshal(data, &geminiAfter)).To(Succeed())
			Expect(jsonPointerGet(geminiAfter, "/auth/tokenCommand")).
				To(ContainSubstring("llm token"),
					"gemini-cli tokenCommand should still be set after claude-code teardown")

			By("verifying ConfiguredTools has only gemini-cli")
			showOut, _ = thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
			Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
			Expect(cfg.ConfiguredTools).To(HaveLen(1))
			Expect(string(cfg.ConfiguredTools[0].Tool)).To(Equal("gemini-cli"))
		})
	})

	// ── Proxy start ───────────────────────────────────────────────────────────

	Describe("thv llm proxy start", func() {
		It("starts and listens on the configured proxy port", func() {
			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			// Install claude-code so setup has at least one client to configure.
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
			Expect(createFakeBinary(binDir, "claude")).To(Succeed())

			By("running setup")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd, oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			// Allocate a free port for the proxy so we don't clash with port 14000
			// if another test or service already uses it.
			proxyPort, portErr := networking.FindOrUsePort(0)
			Expect(portErr).ToNot(HaveOccurred())

			By(fmt.Sprintf("setting proxy port to %d", proxyPort))
			thvCmd("llm", "config", "set", "--proxy-port", fmt.Sprintf("%d", proxyPort)).ExpectSuccess()

			By("starting the proxy in a goroutine")
			type proxyResult struct {
				stdout, stderr string
				err            error
			}
			done := make(chan proxyResult, 1)
			proxyCmd := thvCmd("llm", "proxy", "start")
			go func() {
				out, serr, rerr := proxyCmd.RunWithTimeout(15 * time.Second)
				done <- proxyResult{out, serr, rerr}
			}()

			By(fmt.Sprintf("waiting for proxy to listen on port %d", proxyPort))
			proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
			Eventually(func() error {
				conn, err := net.DialTimeout("tcp", proxyAddr, 200*time.Millisecond)
				if err != nil {
					return err
				}
				_ = conn.Close()
				return nil
			}, 10*time.Second, 300*time.Millisecond).Should(Succeed(),
				"proxy should be listening on %s", proxyAddr)

			By("interrupting the proxy")
			_ = proxyCmd.Interrupt()

			select {
			case r := <-done:
				// The proxy may exit with a non-zero code on SIGINT — that's OK.
				// We only care that it started cleanly (it was listening above).
				_ = r
			case <-time.After(5 * time.Second):
				Fail("proxy did not exit after interrupt within 5s")
			}
		})
	})

	// ── Token command ─────────────────────────────────────────────────────────

	Describe("thv llm token", func() {
		It("returns a token when a cached access token is present", func() {
			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

			// Install claude-code so setup configures at least one client.
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
			Expect(createFakeBinary(binDir, "claude")).To(Succeed())

			By("running setup to persist config")
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd, oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			// Inject a fake but structurally valid cached access token via the
			// environment secrets provider. The env provider reads from env vars
			// with prefix TOOLHIVE_SECRET_. The scoped key for LLM access-token
			// cache is: __thv_llm_<DeriveSecretKey(gateway, issuer)>_AT
			// Value format: <token>|<expiry_RFC3339>
			By("injecting cached access token into environment")
			secretKey := tokensource.DeriveSecretKey("LLM_OAUTH_", gatewayURL, issuerURL)
			scopedKey := "__thv_llm_" + secretKey + "_AT"
			envKey := secrets.EnvVarPrefix + scopedKey
			fakeToken := "test-access-token"
			tokenValue := fakeToken + "|" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

			// Re-build thvCmd with the extra env var for this step only.
			thvCmdWithToken := func(args ...string) *e2e.THVCommand {
				return e2e.NewTHVCommand(thvConfig, args...).
					WithEnv(
						"XDG_CONFIG_HOME="+tempDir,
						"HOME="+tempDir,
						"PATH="+binDir+":"+os.Getenv("PATH"),
						envKey+"="+tokenValue,
					)
			}

			By("running thv llm token")
			tokenOut, _, err := thvCmdWithToken("llm", "token").Run()
			Expect(err).ToNot(HaveOccurred(), "thv llm token should succeed with a cached token")
			Expect(strings.TrimSpace(tokenOut)).To(Equal(fakeToken),
				"thv llm token should print the cached access token")
		})
	})

	// ── Proxy: DNS rebinding protection ───────────────────────────────────────────

	Describe("thv llm proxy start — DNS rebinding protection", func() {
		It("returns 403 for a non-loopback Host header and allows loopback hosts", func() {
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
			Expect(createFakeBinary(binDir, "claude")).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd, oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			proxyPort, portErr := networking.FindOrUsePort(0)
			Expect(portErr).ToNot(HaveOccurred())

			By(fmt.Sprintf("setting proxy port to %d and starting proxy", proxyPort))
			thvCmd("llm", "config", "set", "--proxy-port", fmt.Sprintf("%d", proxyPort)).ExpectSuccess()

			done := make(chan struct{})
			proxyCmd := thvCmd("llm", "proxy", "start")
			go func() {
				defer close(done)
				_, _, _ = proxyCmd.RunWithTimeout(15 * time.Second)
			}()

			proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
			Eventually(func() error {
				conn, dialErr := net.DialTimeout("tcp", proxyAddr, 200*time.Millisecond)
				if dialErr != nil {
					return dialErr
				}
				_ = conn.Close()
				return nil
			}, 10*time.Second, 300*time.Millisecond).Should(Succeed(),
				"proxy should be listening on %s", proxyAddr)

			By("verifying a non-loopback Host header is rejected with 403")
			req, reqErr := http.NewRequest("GET", fmt.Sprintf("http://%s/v1/models", proxyAddr), nil)
			Expect(reqErr).ToNot(HaveOccurred())
			req.Host = "attacker.example.com"

			resp, doErr := http.DefaultClient.Do(req) //nolint:noctx
			Expect(doErr).ToNot(HaveOccurred())
			_ = resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
				"non-loopback Host should be rejected with 403")

			By("verifying a loopback Host header is not rejected with 403")
			req2, reqErr2 := http.NewRequest("GET", fmt.Sprintf("http://%s/v1/models", proxyAddr), nil)
			Expect(reqErr2).ToNot(HaveOccurred())
			// Use the default Host (127.0.0.1:port) — no override needed.

			resp2, doErr2 := http.DefaultClient.Do(req2) //nolint:noctx
			Expect(doErr2).ToNot(HaveOccurred())
			_ = resp2.Body.Close()
			Expect(resp2.StatusCode).ToNot(Equal(http.StatusForbidden),
				"loopback Host should pass the DNS-rebinding guard (got %d instead)", resp2.StatusCode)

			By("stopping the proxy")
			_ = proxyCmd.Interrupt()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				Fail("proxy did not exit after interrupt within 5s")
			}
		})
	})

	// ── Proxy: port conflict ───────────────────────────────────────────────────────

	Describe("thv llm proxy start — port conflict", func() {
		It("exits with an error when the configured port is already in use", func() {
			claudeDir := filepath.Join(tempDir, ".claude")
			Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
			Expect(createFakeBinary(binDir, "claude")).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd, oidcServer,
				"--gateway-url", gatewayURL,
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			By("pre-binding a port to simulate a conflict")
			listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
			Expect(listenErr).ToNot(HaveOccurred())
			defer listener.Close() //nolint:errcheck
			occupiedPort := listener.Addr().(*net.TCPAddr).Port

			thvCmd("llm", "config", "set", "--proxy-port", fmt.Sprintf("%d", occupiedPort)).ExpectSuccess()

			By(fmt.Sprintf("starting proxy on occupied port %d — expecting failure", occupiedPort))
			_, _, err = thvCmd("llm", "proxy", "start").RunWithTimeout(10 * time.Second)
			Expect(err).To(HaveOccurred(),
				"proxy start should fail when the configured port is already in use")
		})
	})

	// ── Proxy end-to-end token forwarding ────────────────────────────────────────
	//
	// These tests start a real mock LLM gateway and verify that the proxy
	// correctly forwards the Bearer token to the upstream on every request.
	// We iterate over all clients so that each client's setup + proxy combo is
	// exercised against a real HTTP server rather than a fake URL.

	Describe("thv llm proxy — end-to-end token forwarding", func() {
		for _, clientTC := range allClientTestCases() {
			clientTC := clientTC
			It(fmt.Sprintf("forwards Bearer token to gateway for %s", clientTC.name), func() {
				if clientTC.skipOnOS != "" && runtime.GOOS == clientTC.skipOnOS {
					Skip(fmt.Sprintf("client %q not supported on %s", clientTC.name, runtime.GOOS))
				}

				// Allocate ports for the mock gateway and the proxy.
				gatewayPort, portErr := networking.FindOrUsePort(0)
				Expect(portErr).ToNot(HaveOccurred())
				proxyPort, portErr := networking.FindOrUsePort(0)
				Expect(portErr).ToNot(HaveOccurred())

				// Start the mock LLM gateway (plain HTTP — no TLS cert trust needed
				// in the subprocess).
				By(fmt.Sprintf("[%s] starting mock LLM gateway on port %d", clientTC.name, gatewayPort))
				gateway := e2e.NewLLMGatewayMockHTTP(gatewayPort)
				Expect(gateway.Start()).To(Succeed())
				defer func() { _ = gateway.Stop() }()

				mockGatewayURL := gateway.URL()
				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

				// Create the detection directory and binary stub for this client.
				By(fmt.Sprintf("[%s] installing client", clientTC.name))
				Expect(os.MkdirAll(clientTC.detectionDir(tempDir), 0750)).To(Succeed())
				if clientTC.binaryName != "" {
					Expect(createFakeBinary(binDir, clientTC.binaryName)).To(Succeed())
				}

				// Run setup pointing at the mock gateway so the proxy knows where to
				// forward requests.
				By(fmt.Sprintf("[%s] running thv llm setup against mock gateway", clientTC.name))
				stdout, stderr, err := runSetupWithOIDCCompletion(
					thvCmd, oidcServer,
					"--gateway-url", mockGatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				)
				Expect(err).ToNot(HaveOccurred(),
					"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

				// Inject a valid cached access token so the proxy can present it to
				// the gateway without triggering a real OIDC browser flow.
				// The environment secrets provider reads TOOLHIVE_SECRET_<scopedKey>.
				// Value format: <token>|<expiry_RFC3339>
				By(fmt.Sprintf("[%s] injecting cached access token", clientTC.name))
				secretKey := tokensource.DeriveSecretKey("LLM_OAUTH_", mockGatewayURL, issuerURL)
				scopedKey := "__thv_llm_" + secretKey + "_AT"
				envKey := secrets.EnvVarPrefix + scopedKey
				fakeToken := "e2e-bearer-token-" + clientTC.name
				tokenValue := fakeToken + "|" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

				thvCmdWithToken := func(args ...string) *e2e.THVCommand {
					return e2e.NewTHVCommand(thvConfig, args...).
						WithEnv(
							"XDG_CONFIG_HOME="+tempDir,
							"HOME="+tempDir,
							"PATH="+binDir+":"+os.Getenv("PATH"),
							envKey+"="+tokenValue,
						)
				}

				// Configure the proxy port and start it.
				By(fmt.Sprintf("[%s] setting proxy port to %d", clientTC.name, proxyPort))
				thvCmd("llm", "config", "set", "--proxy-port", fmt.Sprintf("%d", proxyPort)).ExpectSuccess()

				By(fmt.Sprintf("[%s] starting the proxy", clientTC.name))
				done := make(chan struct{})
				proxyCmd := thvCmdWithToken("llm", "proxy", "start")
				go func() {
					defer close(done)
					_, _, _ = proxyCmd.RunWithTimeout(20 * time.Second)
				}()

				proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
				Eventually(func() error {
					conn, dialErr := net.DialTimeout("tcp", proxyAddr, 200*time.Millisecond)
					if dialErr != nil {
						return dialErr
					}
					_ = conn.Close()
					return nil
				}, 10*time.Second, 300*time.Millisecond).Should(Succeed(),
					"proxy should be listening on %s", proxyAddr)

				// Send a request through the proxy and verify the gateway received it
				// with the correct Bearer token.
				By(fmt.Sprintf("[%s] sending GET /v1/models through the proxy", clientTC.name))
				resp, doErr := http.Get(fmt.Sprintf("http://%s/v1/models", proxyAddr)) //nolint:noctx
				Expect(doErr).ToNot(HaveOccurred(), "GET /v1/models through proxy should not error")
				_ = resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"proxy should forward the request and return 200 OK")

				By(fmt.Sprintf("[%s] verifying Bearer token was forwarded to gateway", clientTC.name))
				Expect(gateway.LastBearerToken()).To(Equal(fakeToken),
					"proxy should forward the cached access token as the Bearer token")

				// Also verify the response payload is the expected mock JSON.
				By(fmt.Sprintf("[%s] sending POST /v1/chat/completions through the proxy", clientTC.name))
				chatResp, chatErr := http.Post( //nolint:noctx
					fmt.Sprintf("http://%s/v1/chat/completions", proxyAddr),
					"application/json",
					strings.NewReader(`{"model":"mock-gpt-4","messages":[{"role":"user","content":"hi"}]}`),
				)
				Expect(chatErr).ToNot(HaveOccurred())
				Expect(chatResp.StatusCode).To(Equal(http.StatusOK))
				var chatBody map[string]any
				Expect(json.NewDecoder(chatResp.Body).Decode(&chatBody)).To(Succeed())
				_ = chatResp.Body.Close()
				Expect(chatBody).To(HaveKey("choices"),
					"chat completions response should contain 'choices'")

				By(fmt.Sprintf("[%s] stopping the proxy", clientTC.name))
				_ = proxyCmd.Interrupt()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					Fail("proxy did not exit after interrupt within 5s")
				}
			})
		}
	})

	// ── Edge cases ─────────────────────────────────────────────────────────────────

	Describe("edge cases", func() {
		Describe("setup with no clients detected", func() {
			It("exits cleanly and prints an informative message", func() {
				// No detection dirs or binary stubs → no clients found.
				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)

				By("running setup with no installed clients")
				stdout, _ := thvCmd(
					"llm", "setup",
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				).ExpectSuccess()

				Expect(stdout).To(ContainSubstring("No supported AI tools detected"),
					"setup should explain that no tools were found")
			})
		})

		Describe("setup with a corrupted settings file", func() {
			It("fails gracefully without modifying the corrupted file", func() {
				claudeDir := filepath.Join(tempDir, ".claude")
				Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
				Expect(createFakeBinary(binDir, "claude")).To(Succeed())

				By("writing invalid JSON to the settings file")
				settingsPath := filepath.Join(claudeDir, "settings.json")
				corruptContent := []byte(`{not valid json!!!`)
				Expect(os.WriteFile(settingsPath, corruptContent, 0600)).To(Succeed())

				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
				_, stderr, err := runSetupWithOIDCCompletion(
					thvCmd, oidcServer,
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				)
				Expect(err).To(HaveOccurred(),
					"setup should fail when the settings file is corrupted; stderr=%q", stderr)

				By("verifying the corrupted file was not modified")
				data, readErr := os.ReadFile(settingsPath)
				Expect(readErr).ToNot(HaveOccurred())
				Expect(data).To(Equal(corruptContent),
					"corrupted settings file should be left untouched on parse failure")
			})
		})

		Describe("setup with an unreachable OIDC issuer", func() {
			It("fails with an OIDC error and leaves settings files unmodified", func() {
				claudeDir := filepath.Join(tempDir, ".claude")
				Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
				Expect(createFakeBinary(binDir, "claude")).To(Succeed())

				By("running setup with an unreachable issuer (port 1)")
				_, stderr, err := thvCmd(
					"llm", "setup",
					"--gateway-url", gatewayURL,
					"--issuer", "http://localhost:1",
					"--client-id", "bad-client",
				).RunWithTimeout(15 * time.Second)

				Expect(err).To(HaveOccurred(),
					"setup with unreachable issuer should fail")
				Expect(stderr).To(ContainSubstring("OIDC"),
					"error should mention OIDC; got stderr=%q", stderr)

				By("verifying settings.json was not created")
				settingsPath := filepath.Join(claudeDir, "settings.json")
				_, statErr := os.Stat(settingsPath)
				Expect(os.IsNotExist(statErr)).To(BeTrue(),
					"settings.json should not be created when OIDC login fails")
			})
		})

		Describe("thv llm token with an expired cached access token", func() {
			It("does not return the expired token", func() {
				claudeDir := filepath.Join(tempDir, ".claude")
				Expect(os.MkdirAll(claudeDir, 0750)).To(Succeed())
				Expect(createFakeBinary(binDir, "claude")).To(Succeed())

				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
				By("running setup to persist OIDC config")
				stdout, stderr, err := runSetupWithOIDCCompletion(
					thvCmd, oidcServer,
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				)
				Expect(err).ToNot(HaveOccurred(),
					"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

				By("injecting an expired cached access token via the environment provider")
				secretKey := tokensource.DeriveSecretKey("LLM_OAUTH_", gatewayURL, issuerURL)
				scopedKey := "__thv_llm_" + secretKey + "_AT"
				envKey := secrets.EnvVarPrefix + scopedKey
				expiredToken := "expired-test-token"
				expiredAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
				tokenValue := expiredToken + "|" + expiredAt

				thvCmdWithExpired := func(args ...string) *e2e.THVCommand {
					return e2e.NewTHVCommand(thvConfig, args...).
						WithEnv(
							"XDG_CONFIG_HOME="+tempDir,
							"HOME="+tempDir,
							"PATH="+binDir+":"+os.Getenv("PATH"),
							envKey+"="+tokenValue,
						)
				}

				By("running thv llm token with the expired token in the environment")
				tokenOut, _, tokenErr := thvCmdWithExpired("llm", "token").Run()

				// The expired token must never be printed, regardless of whether
				// re-auth succeeded or failed (no refresh token is available with
				// the read-only environment secrets provider).
				Expect(strings.TrimSpace(tokenOut)).ToNot(Equal(expiredToken),
					"expired token must not be returned directly")

				if tokenErr == nil {
					// Re-auth via OIDC browser flow succeeded (mock OIDC auto-completes).
					Expect(strings.TrimSpace(tokenOut)).ToNot(BeEmpty(),
						"a fresh token should have been obtained after expiry")
				}
				// If tokenErr != nil the command failed (expected when no refresh
				// token is cached), which is also correct behaviour.
			})
		})
	})
})

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// createFakeBinary writes a minimal no-op shell script named `name` in dir.
// This satisfies the LLMBinaryName check in DetectedLLMGatewayClients.
func createFakeBinary(dir, name string) error {
	script := []byte("#!/bin/sh\nexit 0\n")
	return os.WriteFile(filepath.Join(dir, name), script, 0750)
}

// installAllDetectedClients creates the detection directories (and binary
// stubs) for every client in the test matrix that should be detected on the
// current OS. It returns the list of client names that were installed.
func installAllDetectedClients(tempDir, binDir string) []string {
	var installed []string
	for _, tc := range allClientTestCases() {
		if tc.skipOnOS != "" && runtime.GOOS == tc.skipOnOS {
			continue
		}
		dir := tc.detectionDir(tempDir)
		Expect(os.MkdirAll(dir, 0750)).To(Succeed())
		if tc.binaryName != "" {
			Expect(createFakeBinary(binDir, tc.binaryName)).To(Succeed())
		}
		installed = append(installed, tc.name)
	}
	return installed
}

// jsonPointerGet resolves a simplified JSON pointer against a map[string]any,
// returning the string value or empty string if not found. Supports two-level
// nesting (e.g. "/auth/tokenCommand") but not arrays.
func jsonPointerGet(obj map[string]any, pointer string) string {
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var cur any = obj
	for _, seg := range segments {
		// Unescape RFC 6901 tokens: ~1 → /, ~0 → ~
		seg = strings.ReplaceAll(seg, "~1", "/")
		seg = strings.ReplaceAll(seg, "~0", "~")

		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[seg]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprintf("%v", cur)
}

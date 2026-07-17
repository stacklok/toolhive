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
	"github.com/pelletier/go-toml/v2"

	"github.com/stacklok/toolhive/pkg/auth/tokensource"
	"github.com/stacklok/toolhive/pkg/llm"
	"github.com/stacklok/toolhive/pkg/llmgateway"
	"github.com/stacklok/toolhive/pkg/networking"
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

	// envFilePath returns the absolute path to the .env file that thv will write,
	// or nil when the client has no .env file to manage.
	envFilePath func(tempDir string) string

	// expectedEnvKeys maps env var names to resolver functions, checked in the
	// .env file written by setup. Only used when envFilePath is non-nil.
	expectedEnvKeys map[string]func(gatewayURL, proxyURL string) string

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
			mode: "proxy",
			expectedKeys: map[string]func(string, string) string{
				// Force API-key auth so GOOGLE_GEMINI_BASE_URL is respected.
				"/security/auth/selectedType": func(_, _ string) string { return "gemini-api-key" },
			},
			// Gemini CLI reads these from process.env, so thv writes them to .env.
			envFilePath: func(tempDir string) string {
				return filepath.Join(tempDir, ".gemini", ".env")
			},
			expectedEnvKeys: map[string]func(string, string) string{
				"GEMINI_API_KEY": func(_, _ string) string { return clientThvProxy },
				// ProxyOrigin strips the path so Gemini CLI can append /v1beta/...
				"GOOGLE_GEMINI_BASE_URL": func(_, proxyURL string) string {
					return llmgateway.ProxyOriginOf(proxyURL)
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
			binaryName: "code-insiders",
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
					actualValue, found := jsonPointerGet(settings, pointer)
					Expect(found).To(BeTrue(),
						"JSON pointer %s should be present in %s", pointer, settingsFile)
					Expect(actualValue).To(ContainSubstring(expectedSubstr),
						"JSON pointer %s should contain %q in %s", pointer, expectedSubstr, settingsFile)
				}

				// Verify the .env file was written (if applicable).
				if clientTC.envFilePath != nil {
					envFile := clientTC.envFilePath(tempDir)
					By(fmt.Sprintf("[%s] verifying .env file was written: %s", clientTC.name, envFile))
					envData, err := os.ReadFile(envFile)
					Expect(err).ToNot(HaveOccurred(), ".env file should exist after setup")
					envContent := string(envData)
					for key, valueFn := range clientTC.expectedEnvKeys {
						expectedVal := valueFn(gatewayURL, proxyBaseURL)
						Expect(envContent).To(ContainSubstring(key+"="+expectedVal),
							".env file should contain %s=%s", key, expectedVal)
					}
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
					_, found := jsonPointerGet(after, pointer)
					Expect(found).To(BeFalse(),
						"JSON pointer %s should be absent after teardown in %s", pointer, settingsFile)
				}

				// Verify the .env file had thv-managed entries removed (if applicable).
				if clientTC.envFilePath != nil {
					envFile := clientTC.envFilePath(tempDir)
					By(fmt.Sprintf("[%s] verifying .env file entries were removed: %s", clientTC.name, envFile))
					envData, err := os.ReadFile(envFile)
					Expect(err).ToNot(HaveOccurred(), ".env file should still exist after teardown")
					envContent := string(envData)
					for key := range clientTC.expectedEnvKeys {
						Expect(envContent).ToNot(ContainSubstring(key+"="),
							".env file should not contain %s after teardown", key)
					}
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

			By("verifying gemini-cli settings.json still has auth type patched")
			geminiSettings := filepath.Join(tempDir, ".gemini", "settings.json")
			data, err = os.ReadFile(geminiSettings)
			Expect(err).ToNot(HaveOccurred())
			var geminiAfter map[string]any
			Expect(json.Unmarshal(data, &geminiAfter)).To(Succeed())
			authType, found := jsonPointerGet(geminiAfter, "/security/auth/selectedType")
			Expect(found).To(BeTrue(),
				"gemini-cli /security/auth/selectedType should still be present after claude-code teardown")
			Expect(authType).To(Equal("gemini-api-key"),
				"gemini-cli auth type should still be gemini-api-key after claude-code teardown")

			By("verifying gemini-cli .env file still has proxy env vars")
			geminiEnv := filepath.Join(tempDir, ".gemini", ".env")
			envData, envErr := os.ReadFile(geminiEnv)
			Expect(envErr).ToNot(HaveOccurred(), ".env file should still exist after claude-code teardown")
			Expect(string(envData)).To(ContainSubstring("GOOGLE_GEMINI_BASE_URL="),
				"gemini-cli GOOGLE_GEMINI_BASE_URL should still be in .env after claude-code teardown")

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
			DeferCleanup(func() {
				_ = proxyCmd.Interrupt()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
			})

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
			envKey := tokensource.LLMAccessTokenEnvVar(gatewayURL, issuerURL)
			fakeToken := "test-access-token"
			tokenValue := fakeToken + "|" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

			thvCmdWithToken := func(args ...string) *e2e.THVCommand {
				return thvCmd(args...).WithEnv(envKey + "=" + tokenValue)
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

			// Start a local HTTPS mock gateway so the proxy can forward requests
			// quickly rather than timing out on DNS resolution for a fake domain.
			gwPort, portErr := networking.FindOrUsePort(0)
			Expect(portErr).ToNot(HaveOccurred())
			gw, gwErr := e2e.NewLLMGatewayMock(gwPort)
			Expect(gwErr).ToNot(HaveOccurred())
			Expect(gw.Start()).To(Succeed())
			defer func() { _ = gw.Stop() }()

			gwCertFile := filepath.Join(tempDir, "rebind-gw-cert.pem")
			Expect(os.WriteFile(gwCertFile, gw.CertPEM(), 0600)).To(Succeed())

			issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
			stdout, stderr, err := runSetupWithOIDCCompletion(
				thvCmd, oidcServer,
				"--gateway-url", gw.URL(),
				"--issuer", issuerURL,
				"--client-id", clientID,
			)
			Expect(err).ToNot(HaveOccurred(),
				"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

			// Inject a cached access token so the proxy returns a response
			// immediately rather than hanging on the 10-second token-fetch timeout.
			// The loopback-Host check fires before token fetch, but a valid-looking
			// token lets the proxy attempt forwarding and return 502 (not 403) fast.
			rebindEnvKey := tokensource.LLMAccessTokenEnvVar(gw.URL(), issuerURL)
			rebindToken := "rebind-test-token|" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

			proxyPort, portErr2 := networking.FindOrUsePort(0)
			Expect(portErr2).ToNot(HaveOccurred())

			By(fmt.Sprintf("setting proxy port to %d and starting proxy", proxyPort))
			thvCmd("llm", "config", "set", "--proxy-port", fmt.Sprintf("%d", proxyPort)).ExpectSuccess()

			done := make(chan struct{})
			proxyCmd := thvCmd("llm", "proxy", "start").WithEnv(
				"SSL_CERT_FILE="+gwCertFile,
				rebindEnvKey+"="+rebindToken,
			)
			go func() {
				defer close(done)
				_, _, _ = proxyCmd.RunWithTimeout(15 * time.Second)
			}()
			DeferCleanup(func() {
				_ = proxyCmd.Interrupt()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
			})

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

			rebindClient := &http.Client{Timeout: 10 * time.Second}

			By("verifying a non-loopback Host header is rejected with 403")
			req, reqErr := http.NewRequest("GET", fmt.Sprintf("http://%s/v1/models", proxyAddr), nil)
			Expect(reqErr).ToNot(HaveOccurred())
			req.Host = "attacker.example.com"

			resp, doErr := rebindClient.Do(req) //nolint:noctx
			Expect(doErr).ToNot(HaveOccurred())
			_ = resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
				"non-loopback Host should be rejected with 403")

			By("verifying a loopback Host header is not rejected with 403")
			req2, reqErr2 := http.NewRequest("GET", fmt.Sprintf("http://%s/v1/models", proxyAddr), nil)
			Expect(reqErr2).ToNot(HaveOccurred())
			// Use the default Host (127.0.0.1:port) — no override needed.

			resp2, doErr2 := rebindClient.Do(req2) //nolint:noctx
			Expect(doErr2).ToNot(HaveOccurred())
			_ = resp2.Body.Close()
			Expect(resp2.StatusCode).ToNot(Equal(http.StatusForbidden),
				"loopback Host should pass the DNS-rebinding guard (got %d instead)", resp2.StatusCode)
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

				// Start the mock LLM gateway (HTTPS with self-signed cert).
				By(fmt.Sprintf("[%s] starting mock LLM gateway on port %d", clientTC.name, gatewayPort))
				gateway, gwErr := e2e.NewLLMGatewayMock(gatewayPort)
				Expect(gwErr).ToNot(HaveOccurred())
				Expect(gateway.Start()).To(Succeed())
				defer func() { _ = gateway.Stop() }()

				// Write the self-signed cert to a temp file so the thv subprocess
				// can trust it via SSL_CERT_FILE (respected by Go on Linux).
				certFile := filepath.Join(tempDir, fmt.Sprintf("gw-cert-%d.pem", gatewayPort))
				Expect(os.WriteFile(certFile, gateway.CertPEM(), 0600)).To(Succeed())

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
				envKey := tokensource.LLMAccessTokenEnvVar(mockGatewayURL, issuerURL)
				fakeToken := "e2e-bearer-token-" + clientTC.name
				tokenValue := fakeToken + "|" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

				thvCmdWithToken := func(args ...string) *e2e.THVCommand {
					return thvCmd(args...).WithEnv(
						envKey+"="+tokenValue,
						"SSL_CERT_FILE="+certFile,
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
				DeferCleanup(func() {
					_ = proxyCmd.Interrupt()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
					}
				})

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

				// Send requests through the proxy and verify the gateway received them
				// with the correct Bearer token. Use a client with an explicit timeout
				// so a stalled proxy fails fast rather than hanging the suite.
				proxyClient := &http.Client{Timeout: 10 * time.Second}

				By(fmt.Sprintf("[%s] sending GET /v1/models through the proxy", clientTC.name))
				resp, doErr := proxyClient.Get(fmt.Sprintf("http://%s/v1/models", proxyAddr)) //nolint:noctx
				Expect(doErr).ToNot(HaveOccurred(), "GET /v1/models through proxy should not error")
				_ = resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"proxy should forward the request and return 200 OK")

				By(fmt.Sprintf("[%s] verifying Bearer token was forwarded to gateway", clientTC.name))
				Expect(gateway.LastBearerToken()).To(Equal(fakeToken),
					"proxy should forward the cached access token as the Bearer token")

				// Also verify the response payload is the expected mock JSON.
				By(fmt.Sprintf("[%s] sending POST /v1/chat/completions through the proxy", clientTC.name))
				chatResp, chatErr := proxyClient.Post( //nolint:noctx
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

		Describe("claude-desktop (credential-helper) setup and teardown", func() {
			// Claude Desktop uses a configLibrary document + _meta.json selector and
			// a credential-helper shim, not single-file JSON-pointer patching — so it
			// gets its own assertions rather than the allClientTestCases matrix.
			claudeDesktopPaths := func(root string) (appDir, configLib, shim string) {
				if runtime.GOOS == osDarwin {
					base := filepath.Join(root, "Library", "Application Support")
					return filepath.Join(base, "Claude"),
						filepath.Join(base, "Claude-3p", "configLibrary"),
						filepath.Join(root, ".toolhive", "llm", "claude-desktop-helper.sh")
				}
				return filepath.Join(root, "Claude"),
					filepath.Join(root, "Claude-3p", "configLibrary"),
					filepath.Join(root, ".toolhive", "llm", "claude-desktop-helper.sh")
			}

			// readAppliedConfigDoc resolves _meta.json's appliedId to its config doc.
			readAppliedConfigDoc := func(configLib string) (map[string]any, map[string]any) {
				metaData, err := os.ReadFile(filepath.Join(configLib, "_meta.json"))
				Expect(err).ToNot(HaveOccurred(), "_meta.json should exist after setup")
				var meta map[string]any
				Expect(json.Unmarshal(metaData, &meta)).To(Succeed())

				appliedID, _ := meta["appliedId"].(string)
				Expect(appliedID).ToNot(BeEmpty(), "appliedId should be set after setup")
				docData, err := os.ReadFile(filepath.Join(configLib, appliedID+".json"))
				Expect(err).ToNot(HaveOccurred(), "applied config document should exist")
				var doc map[string]any
				Expect(json.Unmarshal(docData, &doc)).To(Succeed())
				return meta, doc
			}

			It("writes the config document, selector, and shim, then reverts them", func() {
				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
				appDir, configLib, shim := claudeDesktopPaths(tempDir)

				By("creating the Claude Desktop app directory so detection succeeds")
				Expect(os.MkdirAll(appDir, 0750)).To(Succeed())

				By("running thv llm setup --client claude-desktop")
				// Pin the Anthropic prefix so setup skips the network probe (fast,
				// deterministic base URL).
				stdout, stderr, err := runSetupWithOIDCCompletion(
					thvCmd, oidcServer,
					"--client", "claude-desktop",
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
					"--anthropic-path-prefix", "/anthropic",
					"--models", "claude-opus-4-8,claude-sonnet-4-6",
				)
				Expect(err).ToNot(HaveOccurred(),
					"setup should succeed; stdout=%q stderr=%q", stdout, stderr)
				Expect(stdout).To(ContainSubstring("quit"),
					"setup should tell the user to relaunch Claude Desktop")

				By("verifying the config document contents")
				_, doc := readAppliedConfigDoc(configLib)
				Expect(doc["inferenceProvider"]).To(Equal("gateway"))
				Expect(doc["inferenceCredentialKind"]).To(Equal("helper-script"))
				Expect(doc["inferenceGatewayAuthScheme"]).To(Equal("bearer"))
				Expect(doc["inferenceGatewayBaseUrl"]).To(Equal(gatewayURL + "/anthropic"))
				Expect(doc["inferenceCredentialHelper"]).To(Equal(shim))
				Expect(doc["inferenceModels"]).To(ConsistOf("claude-opus-4-8", "claude-sonnet-4-6"))

				By("verifying the credential-helper shim is executable and calls thv llm token")
				info, statErr := os.Stat(shim)
				Expect(statErr).ToNot(HaveOccurred(), "shim should exist after setup")
				Expect(info.Mode().Perm()&0100).ToNot(BeZero(), "shim should be executable")
				shimData, readErr := os.ReadFile(shim)
				Expect(readErr).ToNot(HaveOccurred())
				Expect(string(shimData)).To(ContainSubstring("llm token"))

				By("verifying config show lists claude-desktop in credential-helper mode")
				showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				var cfg llm.Config
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				var found bool
				for _, toolCfg := range cfg.ConfiguredTools {
					if string(toolCfg.Tool) == "claude-desktop" {
						found = true
						Expect(toolCfg.Mode).To(Equal("credential-helper"))
					}
				}
				Expect(found).To(BeTrue(), "claude-desktop should appear in ConfiguredTools")

				By("running thv llm teardown claude-desktop")
				thvCmd("llm", "teardown", "claude-desktop").ExpectSuccess()

				By("verifying the config document and shim are removed and the selector cleared")
				metaData, err := os.ReadFile(filepath.Join(configLib, "_meta.json"))
				Expect(err).ToNot(HaveOccurred(), "_meta.json should still exist after teardown")
				var meta map[string]any
				Expect(json.Unmarshal(metaData, &meta)).To(Succeed())
				Expect(meta["appliedId"]).To(Equal(""), "appliedId should be cleared after teardown")
				Expect(meta["entries"]).To(BeEmpty(), "ToolHive entry should be removed after teardown")
				_, shimStatErr := os.Stat(shim)
				Expect(os.IsNotExist(shimStatErr)).To(BeTrue(), "shim should be deleted after teardown")

				showOut, _ = thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				cfg = llm.Config{}
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				Expect(cfg.ConfiguredTools).To(BeEmpty(),
					"ConfiguredTools should be empty after teardown")
			})
		})

		Describe("codex (codex-auth) setup and teardown", func() {
			// Codex's LLM gateway config lives in ~/.codex/config.toml, patched via a
			// dedicated TOML writer (auth is a command/args table, not a JSON pointer
			// key), so it gets its own assertions rather than the allClientTestCases
			// matrix, which only understands JSON.
			readCodexConfig := func(configPath string) map[string]any {
				data, err := os.ReadFile(configPath)
				Expect(err).ToNot(HaveOccurred(), "config.toml should exist")
				var config map[string]any
				Expect(toml.Unmarshal(data, &config)).To(Succeed())
				return config
			}

			It("detects the macOS desktop app without CLI evidence", func() {
				if runtime.GOOS != osDarwin {
					Skip("Codex desktop detection is macOS-only")
				}

				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
				configPath := filepath.Join(tempDir, ".codex", "config.toml")
				plistPath := filepath.Join(tempDir, "Applications", "ChatGPT.app", "Contents", "Info.plist")
				plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.openai.codex</string></dict></plist>`

				By("creating only the ChatGPT desktop app bundle evidence")
				Expect(os.MkdirAll(filepath.Dir(plistPath), 0750)).To(Succeed())
				Expect(os.WriteFile(plistPath, []byte(plist), 0600)).To(Succeed())
				_, err := os.Stat(filepath.Join(tempDir, ".codex"))
				Expect(os.IsNotExist(err)).To(BeTrue(), ".codex must not exist before setup")
				_, err = os.Stat(filepath.Join(binDir, "codex"))
				Expect(os.IsNotExist(err)).To(BeTrue(), "no fake codex binary should exist")

				// plutil is invoked by absolute path (/usr/bin/plutil) so it does
				// not need to be on $PATH. The PATH is still restricted to exclude
				// user-level package-manager paths that may contain a real Codex CLI.
				appOnlyTHVCmd := func(args ...string) *e2e.THVCommand {
					return thvCmd(args...).WithEnv("PATH=" + binDir + ":/usr/bin:/bin")
				}
				stdout, stderr, err := runSetupWithOIDCCompletion(
					appOnlyTHVCmd, oidcServer,
					"--client", "codex",
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				)
				Expect(err).ToNot(HaveOccurred(),
					"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

				By("verifying app-only detection wrote the canonical Codex configuration")
				config := readCodexConfig(configPath)
				Expect(config["model_provider"]).To(Equal("toolhive-gateway"))
				providers, ok := config["model_providers"].(map[string]any)
				Expect(ok).To(BeTrue())
				provider, ok := providers["toolhive-gateway"].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(provider["base_url"]).To(Equal(gatewayURL + "/v1"))
				auth, ok := provider["auth"].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(auth["command"]).ToNot(BeEmpty())
				Expect(auth["args"]).To(Equal([]any{"llm", "token", "--skip-browser"}))

				showOut, _ := appOnlyTHVCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				var cfg llm.Config
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				Expect(cfg.ConfiguredTools).To(HaveLen(1))
				Expect(string(cfg.ConfiguredTools[0].Tool)).To(Equal("codex"))
				Expect(cfg.ConfiguredTools[0].Mode).To(Equal("codex-auth"))

				By("tearing down and verifying the provider is removed")
				appOnlyTHVCmd("llm", "teardown", "codex").ExpectSuccess()
				config = readCodexConfig(configPath)
				_, hasModelProvider := config["model_provider"]
				Expect(hasModelProvider).To(BeFalse())
				if providers, ok := config["model_providers"].(map[string]any); ok {
					_, stillPresent := providers["toolhive-gateway"]
					Expect(stillPresent).To(BeFalse())
				}
				showOut, _ = appOnlyTHVCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				cfg = llm.Config{}
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				Expect(cfg.ConfiguredTools).To(BeEmpty())
			})

			It("writes the model_provider and auth command, then reverts them", func() {
				issuerURL := fmt.Sprintf("http://localhost:%d", oidcPort)
				codexDir := filepath.Join(tempDir, ".codex")
				configPath := filepath.Join(codexDir, "config.toml")

				By("creating the .codex directory and codex binary so detection succeeds")
				Expect(os.MkdirAll(codexDir, 0750)).To(Succeed())
				Expect(createFakeBinary(binDir, "codex")).To(Succeed())

				By("running thv llm setup --client codex")
				stdout, stderr, err := runSetupWithOIDCCompletion(
					thvCmd, oidcServer,
					"--client", "codex",
					"--gateway-url", gatewayURL,
					"--issuer", issuerURL,
					"--client-id", clientID,
				)
				Expect(err).ToNot(HaveOccurred(),
					"setup should succeed; stdout=%q stderr=%q", stdout, stderr)

				By("verifying config.toml contains the toolhive-gateway provider")
				config := readCodexConfig(configPath)
				Expect(config["model_provider"]).To(Equal("toolhive-gateway"))
				providers, ok := config["model_providers"].(map[string]any)
				Expect(ok).To(BeTrue(), "model_providers table should exist")
				provider, ok := providers["toolhive-gateway"].(map[string]any)
				Expect(ok).To(BeTrue(), "model_providers.toolhive-gateway table should exist")
				Expect(provider["name"]).To(Equal("ToolHive Gateway"))
				Expect(provider["base_url"]).To(Equal(gatewayURL + "/v1"))
				Expect(provider["wire_api"]).To(Equal("responses"))

				auth, ok := provider["auth"].(map[string]any)
				Expect(ok).To(BeTrue(), "model_providers.toolhive-gateway.auth table should exist")
				Expect(auth["command"]).ToNot(BeEmpty(), "auth.command should point at the thv executable")
				args, ok := auth["args"].([]any)
				Expect(ok).To(BeTrue(), "auth.args should be an array")
				Expect(args).To(Equal([]any{"llm", "token", "--skip-browser"}))

				By("verifying config show lists codex in codex-auth mode")
				showOut, _ := thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				var cfg llm.Config
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				var found bool
				for _, toolCfg := range cfg.ConfiguredTools {
					if string(toolCfg.Tool) == "codex" {
						found = true
						Expect(toolCfg.Mode).To(Equal("codex-auth"))
					}
				}
				Expect(found).To(BeTrue(), "codex should appear in ConfiguredTools")

				By("running thv llm teardown codex")
				thvCmd("llm", "teardown", "codex").ExpectSuccess()

				By("verifying model_provider and the toolhive-gateway table were removed")
				config = readCodexConfig(configPath)
				_, hasModelProvider := config["model_provider"]
				Expect(hasModelProvider).To(BeFalse(), "model_provider should be cleared after teardown")
				if providers, ok := config["model_providers"].(map[string]any); ok {
					_, stillPresent := providers["toolhive-gateway"]
					Expect(stillPresent).To(BeFalse(), "toolhive-gateway provider should be removed after teardown")
				}

				showOut, _ = thvCmd("llm", "config", "show", "--format", "json").ExpectSuccess()
				cfg = llm.Config{}
				Expect(json.Unmarshal([]byte(showOut), &cfg)).To(Succeed())
				Expect(cfg.ConfiguredTools).To(BeEmpty(),
					"ConfiguredTools should be empty after teardown")
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
				envKey := tokensource.LLMAccessTokenEnvVar(gatewayURL, issuerURL)
				expiredToken := "expired-test-token"
				expiredAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
				tokenValue := expiredToken + "|" + expiredAt

				thvCmdWithExpired := func(args ...string) *e2e.THVCommand {
					return thvCmd(args...).WithEnv(envKey + "=" + tokenValue)
				}

				By("running thv llm token with the expired token in the environment")
				// The cached access token is expired and the read-only environment
				// provider holds no refresh token, so "thv llm token" falls through to
				// the interactive OIDC browser flow. runWithOIDCCompletion satisfies the
				// callback so the command obtains a fresh token instead of blocking.
				tokenOut, stderrOut, tokenErr := runWithOIDCCompletion(thvCmdWithExpired, oidcServer, "llm", "token")
				Expect(tokenErr).ToNot(HaveOccurred(),
					"deferred re-auth should succeed; stdout=%q stderr=%q", tokenOut, stderrOut)

				// The expired token must never be printed; a fresh one must be returned.
				Expect(strings.TrimSpace(tokenOut)).ToNot(Equal(expiredToken),
					"expired token must not be returned directly")
				Expect(strings.TrimSpace(tokenOut)).ToNot(BeEmpty(),
					"a fresh token should have been obtained after expiry")
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

// jsonPointerGet resolves a simplified JSON pointer (RFC 6901) against a
// map[string]any. Returns the string value and true if the key exists, or
// ("", false) if any segment is missing or a non-map node is encountered.
// Supports arbitrary nesting depth but not array indexing (e.g. "/a/b/c" works,
// "/items/0/name" does not).
func jsonPointerGet(obj map[string]any, pointer string) (string, bool) {
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var cur any = obj
	for _, seg := range segments {
		// Unescape RFC 6901 tokens: ~1 → /, ~0 → ~
		seg = strings.ReplaceAll(seg, "~1", "/")
		seg = strings.ReplaceAll(seg, "~0", "~")

		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[seg]
		if !ok {
			return "", false
		}
	}
	if cur == nil {
		return "", false
	}
	return fmt.Sprintf("%v", cur), true
}

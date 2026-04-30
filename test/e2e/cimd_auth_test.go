// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/e2e"
)

// startCIMDRunCommand starts `thv run <mcpURL> --name <serverName> --remote-auth …`
// and returns the exec.Cmd together with a buffer that captures combined stdout
// and stderr. The buffer is safe to read concurrently from the test goroutine.
func startCIMDRunCommand(
	config *e2e.TestConfig,
	serverName string,
	mcpURL string,
	asIssuerURL string,
) (*exec.Cmd, *bytes.Buffer) {
	args := []string{
		"run",
		mcpURL,
		"--name", serverName,
		"--remote-auth",
		"--remote-auth-skip-browser",
		"--remote-auth-issuer", asIssuerURL,
		"--remote-auth-timeout", "30s",
	}

	GinkgoWriter.Printf("Starting thv run with args: %v\n", args)

	cmd := exec.Command(config.THVBinary, args...) //nolint:gosec // Intentional for e2e testing
	cmd.Env = os.Environ()

	var outputBuffer bytes.Buffer
	multiWriter := io.MultiWriter(&outputBuffer, GinkgoWriter)
	cmd.Stdout = multiWriter
	cmd.Stderr = multiWriter

	err := cmd.Start()
	Expect(err).ToNot(HaveOccurred(), "thv run should start without error")

	return cmd, &outputBuffer
}

// extractAuthURL scans the captured output buffer for the OAuth browser URL
// that ToolHive prints when --remote-auth-skip-browser is set.
func extractAuthURL(output string) string {
	urlPattern := regexp.MustCompile(`Please open this URL in your browser: (https?://[^\s"]+)`)
	matches := urlPattern.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// appendAutoComplete appends or sets auto_complete=true on an authorize URL so
// that the cimdMockAuthServer will immediately redirect to the callback.
func appendAutoComplete(authURL string) string {
	if authURL == "" {
		return authURL
	}
	separator := "&"
	if !strings.Contains(authURL, "?") {
		separator = "?"
	}
	return authURL + separator + "auto_complete=true"
}

var _ = Describe("CIMD Authentication", Label("remote", "auth", "cimd"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available for testing")
	})

	Context("when the authorization server advertises CIMD support", func() {
		It("uses the CIMD client_id and skips DCR", func() {
			By("Starting mock authorization server with CIMD support enabled")
			mockAS := newCIMDMockAuthServer(GinkgoT(), true)

			By("Starting mock MCP server that requires authentication")
			mockMCP := newCIMDMockMCPServer(GinkgoT(), mockAS.URL())

			serverName := e2e.GenerateUniqueServerName("cimd-cimd-supported")

			By("Starting thv run pointing at the mock MCP server")
			cmd, outputBuffer := startCIMDRunCommand(config, serverName, mockMCP.URL, mockAS.IssuerURL())

			defer func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				if config.CleanupAfter {
					_ = e2e.StopAndRemoveMCPServer(config, serverName)
				}
			}()

			By("Waiting for the OAuth URL to appear in the output")
			var authURL string
			Eventually(func() string {
				authURL = extractAuthURL(outputBuffer.String())
				return authURL
			}, 30*time.Second, 500*time.Millisecond).ShouldNot(BeEmpty(),
				"thv run should print 'Please open this URL in your browser'")

			By("Completing the OAuth flow via auto_complete")
			autoURL := appendAutoComplete(authURL)
			client := &http.Client{
				Timeout: 10 * time.Second,
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return nil // follow redirects
				},
			}
			resp, err := client.Get(autoURL) //nolint:gosec // URL is test-controlled
			Expect(err).ToNot(HaveOccurred(), "GET to auto-complete URL should succeed")
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			Expect(resp.StatusCode).To(BeNumerically("<", 400),
				"auto-complete redirect chain should succeed")

			By("Waiting for the authorization request to be captured by the mock AS")
			authReq, err := mockAS.WaitForAuthRequest(15 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "mock AS should receive an authorization request")

			By("Asserting client_id equals the CIMD metadata URL")
			Expect(authReq.ClientID).To(Equal(oauthproto.ToolHiveClientMetadataDocumentURL),
				"thv run should use the CIMD metadata URL as client_id when AS advertises support")

			By("Asserting PKCE code_challenge was included")
			Expect(authReq.CodeChallenge).ToNot(BeEmpty(),
				"PKCE code_challenge must be present in the authorization request")

			By("Asserting DCR was NOT called")
			Expect(mockAS.DcrWasCalled()).To(BeFalse(),
				"DCR registration endpoint must not be called when CIMD is used")

			By("Waiting for thv to report the server as running")
			err = e2e.WaitForMCPServer(config, serverName, 30*time.Second)
			Expect(err).ToNot(HaveOccurred(), "server should appear as running in thv list")
		})
	})

	Context("when the authorization server does NOT advertise CIMD support", func() {
		It("falls back to DCR and does not use the CIMD client_id", func() {
			By("Starting mock authorization server with CIMD support disabled")
			mockAS := newCIMDMockAuthServer(GinkgoT(), false)

			By("Starting mock MCP server that requires authentication")
			mockMCP := newCIMDMockMCPServer(GinkgoT(), mockAS.URL())

			serverName := e2e.GenerateUniqueServerName("cimd-dcr-fallback")

			By("Starting thv run pointing at the mock MCP server")
			cmd, outputBuffer := startCIMDRunCommand(config, serverName, mockMCP.URL, mockAS.IssuerURL())

			defer func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				if config.CleanupAfter {
					_ = e2e.StopAndRemoveMCPServer(config, serverName)
				}
			}()

			By("Waiting for the OAuth URL to appear in the output")
			var authURL string
			Eventually(func() string {
				authURL = extractAuthURL(outputBuffer.String())
				return authURL
			}, 30*time.Second, 500*time.Millisecond).ShouldNot(BeEmpty(),
				"thv run should print 'Please open this URL in your browser'")

			By("Completing the OAuth flow via auto_complete")
			autoURL := appendAutoComplete(authURL)
			client := &http.Client{
				Timeout: 10 * time.Second,
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return nil
				},
			}
			resp, err := client.Get(autoURL) //nolint:gosec // URL is test-controlled
			Expect(err).ToNot(HaveOccurred(), "GET to auto-complete URL should succeed")
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			Expect(resp.StatusCode).To(BeNumerically("<", 400))

			By("Waiting for the authorization request to be captured by the mock AS")
			authReq, err := mockAS.WaitForAuthRequest(15 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "mock AS should receive an authorization request")

			By("Asserting client_id is NOT the CIMD metadata URL")
			Expect(authReq.ClientID).ToNot(Equal(oauthproto.ToolHiveClientMetadataDocumentURL),
				"thv run must not use the CIMD metadata URL when the AS does not advertise support")

			By("Asserting DCR WAS called")
			// Give thv a moment to hit the DCR endpoint before asserting.
			Eventually(mockAS.DcrWasCalled, 10*time.Second, 500*time.Millisecond).Should(BeTrue(),
				"DCR registration endpoint must be called when CIMD is not advertised")
		})
	})
})

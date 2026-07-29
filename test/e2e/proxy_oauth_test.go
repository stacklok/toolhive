// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// generateUniqueOIDCServerName creates a unique server name for OIDC mock tests
func generateUniqueOIDCServerName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), GinkgoRandomSeed())
}

var _ = Describe("Proxy OAuth Authentication E2E", Label("proxy", "oauth", "e2e"), Serial, func() {
	var (
		config          *e2e.TestConfig
		mockOIDCPort    int
		proxyPort       int
		mockOIDCServer  *e2e.OIDCMockServer
		proxyCmd        *exec.Cmd
		osvServerName   string
		proxyServerName string
		clientID        = "test-client"
		clientSecret    = "test-secret"
		mockOIDCBaseURL string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available for testing")

		// Generate unique names for this test run
		osvServerName = generateUniqueOIDCServerName("osv-oauth-target")
		proxyServerName = generateUniqueOIDCServerName("proxy-oauth-test")

		// proxyPort is discovered per-It from the `thv proxy` subprocess's own
		// stdout once it binds -- see discoverProxyPort. It's started with
		// port 0 rather than a pre-selected port to close the find-then-bind
		// TOCTOU window.

		// Start mock OIDC server using Ory Fosite
		By("Starting mock OIDC server")
		specReport := CurrentSpecReport()
		if strings.Contains(specReport.FullText(), "Proxy OAuth Authentication E2E") {
			mockOIDCServer, err = e2e.NewOIDCMockServer(
				0, clientID, clientSecret,
				e2e.WithAccessTokenLifespan(2*time.Second),
			)
		} else {
			mockOIDCServer, err = e2e.NewOIDCMockServer(0, clientID, clientSecret)
		}
		Expect(err).ToNot(HaveOccurred())
		mockOIDCPort = mockOIDCServer.Port()
		mockOIDCBaseURL = fmt.Sprintf("http://localhost:%d", mockOIDCPort)

		// Enable auto-complete for MCP tests
		mockOIDCServer.EnableAutoComplete()

		err = mockOIDCServer.Start()
		Expect(err).ToNot(HaveOccurred())

		// Wait for OIDC server to be ready
		Eventually(func() error {
			return checkServerHealth(fmt.Sprintf("%s/.well-known/openid-configuration", mockOIDCBaseURL))
		}, 5*time.Minute, 1*time.Second).Should(Succeed())

		// Start OSV MCP server that will be our target
		By("Starting OSV MCP server as target")
		e2e.NewTHVCommand(config, "run",
			"--name", osvServerName,
			"--transport", "streamable-http",
			"osv").ExpectSuccess()

		// Wait for OSV server to be ready
		err = e2e.WaitForMCPServer(config, osvServerName, 5*time.Minute)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		By("Cleaning up test resources")

		// Stop proxy if running
		if proxyCmd != nil && proxyCmd.Process != nil {
			proxyCmd.Process.Kill()
			proxyCmd.Wait()
		}

		// Stop and remove OSV server
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, osvServerName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}

		// Stop mock OIDC server
		if mockOIDCServer != nil {
			err := mockOIDCServer.Stop()
			if err != nil {
				GinkgoWriter.Printf("Warning: Failed to stop OIDC mock server: %v\n", err)
			}
		}
	})

	Context("when OAuth authentication is enabled", func() {
		It("should successfully start proxy with OAuth configuration", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			// remove path from server url
			parsedURL, err := url.Parse(osvServerURL)
			if err != nil {
				GinkgoWriter.Printf("Failed to parse OSV server URL: %v\n", err)
			}
			base := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

			By("Starting the proxy with OAuth configuration")
			var out *syncBuffer
			proxyCmd, out = startProxyWithOAuth(config, proxyServerName, base, 0, mockOIDCBaseURL, clientID, clientSecret)
			// This test never drives the OAuth flow to completion (no
			// WaitForAuthRequest/CompleteAuthRequest call), so the proxy may
			// exit on --remote-auth-timeout before it ever binds a listener
			// and prints the port line -- pre-existing behavior, unrelated to
			// port selection. Discovery failure here is expected and
			// harmless: the assertions below already tolerate the port being
			// unset (proxyPort stays 0) or the proxy having exited.
			if p, portErr := discoverProxyPort(out, 10*time.Second); portErr != nil {
				GinkgoWriter.Printf("proxy port not discovered (OAuth flow may not have completed): %v\n", portErr)
			} else {
				proxyPort = p
			}

			// Give the proxy some time to start and potentially complete OAuth flow
			time.Sleep(10 * time.Second)

			By("Verifying proxy process is still running")
			// If OAuth flow failed, the process would have exited
			Expect(proxyCmd.ProcessState).To(BeNil(), "Proxy process should still be running")

			By("Testing proxy endpoint accessibility")
			// Try to access the proxy endpoint
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(fmt.Sprintf("http://localhost:%d/mcp", proxyPort))
			if err == nil {
				defer resp.Body.Close()
				// We expect some response, even if it's not a successful MCP connection
				// The important thing is that the proxy is running and accessible
				Expect(resp.StatusCode).To(BeNumerically(">=", 200))
				Expect(resp.StatusCode).To(BeNumerically("<", 500))
			}
		})

		It("should handle OAuth auto-detection when target requires authentication", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			// remove path from server url
			parsedURL, err := url.Parse(osvServerURL)
			if err != nil {
				GinkgoWriter.Printf("Failed to parse OSV server URL: %v\n", err)
			}
			base := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

			By("Starting the proxy with OAuth auto-detection")
			var out *syncBuffer
			proxyCmd, out, proxyPort, err = startProxyAndDiscoverPort(func() (*exec.Cmd, *syncBuffer) {
				return startProxyWithOAuthDetection(config, proxyServerName, base, 0, clientID, clientSecret)
			}, 5*time.Second)
			Expect(err).ToNot(HaveOccurred(), "proxy output: %s", out.String())

			// Give the proxy time to start
			time.Sleep(5 * time.Second)

			By("Verifying proxy starts successfully")
			// The proxy should start even if OAuth detection doesn't find requirements
			Expect(proxyCmd.ProcessState).To(BeNil(), "Proxy process should be running")
		})
	})

	Context("when OAuth authentication fails", func() {
		It("should handle invalid OAuth credentials gracefully", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			// remove path from server url
			parsedURL, err := url.Parse(osvServerURL)
			if err != nil {
				GinkgoWriter.Printf("Failed to parse OSV server URL: %v\n", err)
			}
			base := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

			By("Starting the proxy with invalid OAuth credentials")
			var out *syncBuffer
			proxyCmd, out = startProxyWithOAuth(config, proxyServerName, base, 0, mockOIDCBaseURL, "invalid-client", "invalid-secret")
			// The proxy is expected to exit on the OAuth failure below before it
			// ever binds a listener, so it never prints the port line -- this
			// test doesn't use proxyPort, so a discovery failure is expected
			// and harmless.
			if _, portErr := discoverProxyPort(out, 2*time.Second); portErr != nil {
				GinkgoWriter.Printf("proxy port not discovered (expected, proxy should exit on OAuth failure): %v\n", portErr)
			}

			By("Verifying the proxy process exits due to OAuth failure")
			// The proxy should exit when OAuth fails due to invalid client credentials
			// Use a goroutine to wait for the process with a timeout
			done := make(chan error, 1)
			go func() {
				done <- proxyCmd.Wait()
			}()

			select {
			case err := <-done:
				// Process exited as expected
				Expect(err).To(HaveOccurred(), "Process should exit with error due to invalid OAuth credentials")
				Expect(proxyCmd.ProcessState).ToNot(BeNil(), "Process should have exited")
				Expect(proxyCmd.ProcessState.Exited()).To(BeTrue(), "Process should have exited")
				Expect(proxyCmd.ProcessState.Success()).To(BeFalse(), "Process should exit with error")
			case <-time.After(10 * time.Second):
				Fail("Process should have exited within 10 seconds due to invalid OAuth credentials")
			}
		})

		It("should handle missing OAuth issuer gracefully when remote-auth is explicitly enabled", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			// remove path from server url
			parsedURL, err := url.Parse(osvServerURL)
			if err != nil {
				GinkgoWriter.Printf("Failed to parse OSV server URL: %v\n", err)
			}
			base := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

			By("Starting the proxy with missing OAuth issuer but remote-auth enabled")
			const emptyIssuer = ""
			var out *syncBuffer
			proxyCmd, out = startProxyWithOAuth(config, proxyServerName, base, 0, emptyIssuer, clientID, clientSecret)
			// The proxy is expected to exit immediately below due to the
			// missing issuer, before it ever binds a listener, so it never
			// prints the port line -- this test doesn't use proxyPort, so a
			// discovery failure is expected and harmless.
			if _, portErr := discoverProxyPort(out, 2*time.Second); portErr != nil {
				GinkgoWriter.Printf("proxy port not discovered (expected, proxy should exit due to missing issuer): %v\n", portErr)
			}

			By("Verifying the proxy process exits due to missing issuer")
			// The proxy should exit immediately when --remote-auth is enabled but issuer is missing
			// Use a goroutine to wait for the process with a timeout
			done := make(chan error, 1)
			go func() {
				done <- proxyCmd.Wait()
			}()

			select {
			case err := <-done:
				// Process exited as expected
				Expect(err).To(HaveOccurred(), "Process should exit with error due to missing issuer")
				Expect(proxyCmd.ProcessState).ToNot(BeNil(), "Process should have exited")
				Expect(proxyCmd.ProcessState.Exited()).To(BeTrue(), "Process should have exited")
				Expect(proxyCmd.ProcessState.Success()).To(BeFalse(), "Process should exit with error")
			case <-time.After(5 * time.Second):
				Fail("Process should have exited within 5 seconds due to missing issuer")
			}
		})

		It("should handle auto-detection when target server returns WWW-Authenticate header", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			// remove path from server url
			parsedURL, err := url.Parse(osvServerURL)
			if err != nil {
				GinkgoWriter.Printf("Failed to parse OSV server URL: %v\n", err)
			}
			base := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

			By("Starting the proxy with auto-detection (no --remote-auth flag)")
			var out *syncBuffer
			proxyCmd, out, proxyPort, err = startProxyAndDiscoverPort(func() (*exec.Cmd, *syncBuffer) {
				return startProxyWithAutoDetection(config, proxyServerName, base, 0, clientID, clientSecret)
			}, 5*time.Second)
			Expect(err).ToNot(HaveOccurred(), "proxy output: %s", out.String())

			// Give the proxy time to try auto-detection
			time.Sleep(5 * time.Second)

			By("Verifying proxy starts successfully even when no auth is detected")
			// The proxy should start successfully since OSV server doesn't require auth
			Expect(proxyCmd.ProcessState).To(BeNil(), "Proxy process should be running")
		})
	})

	Context("when testing proxy functionality with MCP protocol", func() {
		It("should proxy MCP requests successfully after OAuth", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting base URL for transparent proxy")
			// With streamable-http: http://127.0.0.1:21929/mcp (no fragment)
			// But the transparent proxy needs the base URL: http://127.0.0.1:21929
			baseURL := strings.TrimSuffix(osvServerURL, "/mcp")
			GinkgoWriter.Printf("Original server URL: %s\n", osvServerURL)
			GinkgoWriter.Printf("Base URL for proxy: %s\n", baseURL)

			By("Starting the proxy with OAuth configuration and longer timeout")
			var outputBuffer *syncBuffer
			// startProxyAndDiscoverPort retries the whole closure -- fresh subprocess,
			// fresh port, fresh OAuth exchange -- if the previous attempt lost the
			// real port-bind race (see the doc comment on discoverProxyPort).
			proxyCmd, outputBuffer, proxyPort, err = startProxyAndDiscoverPort(func() (*exec.Cmd, *syncBuffer) {
				// baseURL, not the full server URL -- the transparent proxy needs the
				// base URL (see the comment above where it's derived).
				cmd, out := startProxyWithOAuthForMCP(config, proxyServerName, baseURL, 0, mockOIDCBaseURL, clientID, clientSecret)

				By("Extracting OAuth URL from proxy output and completing the flow")
				// Give the proxy a moment to start and display the OAuth URL
				time.Sleep(5 * time.Second)

				// Extract OAuth URL from captured output
				output := out.String()
				GinkgoWriter.Printf("Captured proxy output: %s\n", output)

				// Use regex to extract the OAuth URL
				// Pattern: "Please open this URL in your browser: <URL>"
				urlPattern := regexp.MustCompile(`Please open this URL in your browser: (https?://[^\s"]+)`)
				matches := urlPattern.FindStringSubmatch(output)

				var authURL string
				if len(matches) >= 2 {
					authURL = matches[1]
					GinkgoWriter.Printf("Extracted OAuth URL from buffer: %s\n", authURL)
				} else {
					// Fallback: construct the URL from what we know
					// We can see the URL in the logs, so let's construct it
					authURL = fmt.Sprintf("%s/auth?client_id=%s&response_type=code&scope=openid+profile+email", mockOIDCBaseURL, clientID)
					GinkgoWriter.Printf("Using constructed OAuth URL: %s\n", authURL)
				}

				// Complete the OAuth flow by visiting the URL with auto_complete parameter
				if flowErr := completeOAuthFlow(authURL); flowErr != nil {
					GinkgoWriter.Printf("Failed to complete OAuth flow: %v\n", flowErr)
					Skip("Skipping MCP test due to OAuth flow completion failure")
				}

				return cmd, out
			}, 15*time.Second)
			By("Waiting for the proxy to complete OAuth and bind its listener")
			Expect(err).ToNot(HaveOccurred(), "proxy output: %s", outputBuffer.String())

			By("Testing MCP connection through proxy")
			proxyURL := fmt.Sprintf("http://localhost:%d/mcp", proxyPort)

			// Wait for proxy to be ready for MCP connections
			err = e2e.WaitForMCPServerReady(config, proxyURL, "streamable-http", 5*time.Minute)
			if err != nil {
				GinkgoWriter.Printf("MCP connection through proxy failed: %v\n", err)
				Skip("Skipping MCP test due to proxy not being ready")
			}

			By("Creating MCP client through proxy")
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, proxyURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err = mcpClient.Initialize(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Testing basic MCP operations through proxy")
			err = mcpClient.Ping(ctx)
			Expect(err).ToNot(HaveOccurred())

			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "Should have OSV tools available through proxy")
		})
	})

	Context("when testing proxy functionality with MCP protocol and token refresh", func() {
		It("should refresh token after expiry and continue MCP operations", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting base URL for transparent proxy")
			baseURL := strings.TrimSuffix(osvServerURL, "/mcp")
			GinkgoWriter.Printf("Base URL for proxy: %s\n", baseURL)

			By("Starting the proxy with OAuth-enabled MCP support")
			var outputBuffer *syncBuffer
			// startProxyAndDiscoverPort retries the whole closure -- fresh subprocess,
			// fresh port, fresh OAuth exchange -- if the previous attempt lost the
			// real port-bind race (see the doc comment on discoverProxyPort).
			proxyCmd, outputBuffer, proxyPort, err = startProxyAndDiscoverPort(func() (*exec.Cmd, *syncBuffer) {
				cmd, out := startProxyWithOAuthForMCP(config, proxyServerName, baseURL, 0, mockOIDCBaseURL, clientID, clientSecret)

				By("Completing the initial OAuth flow")
				Eventually(out.String, 5*time.Second, 500*time.Millisecond).
					Should(ContainSubstring("Please open this URL"))

				matches := regexp.MustCompile(`Please open this URL in your browser: (https?://[^\s"]+)`).
					FindStringSubmatch(out.String())
				Expect(matches).To(HaveLen(2))
				authURL := matches[1]
				Expect(completeOAuthFlow(authURL)).To(Succeed())

				return cmd, out
			}, 10*time.Second)
			By("Waiting for the proxy to finish the OAuth exchange and bind its listener")
			Expect(err).ToNot(HaveOccurred(), "proxy output: %s", outputBuffer.String())

			By("Waiting for access token to expire")
			time.Sleep(3 * time.Second) // longer than the 2s lifespan

			By("Reconnecting via MCP to trigger token refresh")
			proxyURL := fmt.Sprintf("http://localhost:%d/mcp", proxyPort)
			err = e2e.WaitForMCPServerReady(config, proxyURL, "streamable-http", 5*time.Minute)
			Expect(err).ToNot(HaveOccurred(), "MCP server not ready after token expiry")

			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, proxyURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			Expect(mcpClient.Initialize(ctx)).To(Succeed())
			Expect(mcpClient.Ping(ctx)).To(Succeed())

			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "Should list tools after refresh")
		})
	})

})

// Helper functions

func checkServerHealth(healthUrl string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(healthUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("server not healthy, status: %d", resp.StatusCode)
}

// syncBuffer is a bytes.Buffer guarded by a mutex, safe for concurrent
// writes (exec.Cmd's internal stdout/stderr copier goroutines) and reads
// (a polling goroutine like discoverProxyPort, or Eventually matchers)
// while the subprocess is still running.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// startLongRunningWithCapture is like e2e.StartLongRunningTHVCommand, but
// also captures stdout/stderr into a buffer. Callers inspect the buffer
// (e.g. via discoverProxyPort) rather than reaping the process via cmd.Wait
// -- cmd.Wait must never be called concurrently, and each test in this file
// that cares about the process's exit decides independently whether and
// when to call it.
func startLongRunningWithCapture(config *e2e.TestConfig, args ...string) (*exec.Cmd, *syncBuffer) {
	cmd := exec.Command(config.THVBinary, args...) //nolint:gosec // Intentional for e2e testing
	cmd.Env = os.Environ()

	out := &syncBuffer{}
	multiWriter := io.MultiWriter(out, GinkgoWriter)
	cmd.Stdout = multiWriter
	cmd.Stderr = multiWriter

	Expect(cmd.Start()).To(Succeed())
	return cmd, out
}

// proxyBoundPortPattern matches the port number `thv proxy` prints once its
// listener is actually bound, e.g. "... on port 54321 -> ..." (see
// cmd/thv/app/proxy.go's fmt.Printf after proxy.Start()).
var proxyBoundPortPattern = regexp.MustCompile(`on port (\d+)`)

// discoverProxyPort polls out (the captured stdout/stderr of a `thv proxy`
// subprocess started with --port 0) for the line the subprocess prints once
// it has actually bound its listener, and returns the port it chose.
//
// Passing port 0 and reading the real port back afterwards -- rather than
// pre-selecting a port and handing it to the subprocess -- narrows the
// find-then-bind TOCTOU window but does not close it: FindOrUsePort(0)
// (cmd/thv/app/proxy.go) still does the classic probe-then-release, and the
// real bind happens later, inside transparent.NewTransparentProxy(...).Start(),
// after the OAuth exchange (handleOutgoingAuthentication) completes. Another
// process can still steal the port during that window; the window is just
// usually much shorter than the original pre-selected-port bug. See
// startProxyAndDiscoverPort for the retry that closes it at call sites that
// use it.
//
// It's expected (and safe to ignore) for this to time out on tests where the
// proxy process exits before ever binding, e.g. on an OAuth failure.
func discoverProxyPort(out *syncBuffer, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		if m := proxyBoundPortPattern.FindStringSubmatch(out.String()); m != nil {
			return strconv.Atoi(m[1])
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("proxy port not found in output within %s", timeout)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// startProxyAndDiscoverPort starts a `thv proxy` subprocess via start, waits
// for it to report its real bound port via discoverProxyPort, and retries
// once (a fresh subprocess, drawing a fresh random port) if the failure was
// a lost port-bind race -- detected by the captured output containing
// "address already in use". A discovery failure for any OTHER reason (e.g.
// this call site's proxy is expected to exit before ever binding, such as
// on an OAuth-config failure) is returned as-is, uncorrected: the caller
// decides whether that's fatal or tolerable.
func startProxyAndDiscoverPort(
	start func() (*exec.Cmd, *syncBuffer),
	timeout time.Duration,
) (*exec.Cmd, *syncBuffer, int, error) {
	cmd, out := start()
	port, err := discoverProxyPort(out, timeout)
	if err == nil {
		return cmd, out, port, nil
	}
	if !strings.Contains(out.String(), "address already in use") {
		return cmd, out, 0, err
	}
	GinkgoWriter.Printf("proxy lost a port-bind race, retrying once: %v\n", err)
	cmd, out = start()
	port, err = discoverProxyPort(out, timeout)
	return cmd, out, port, err
}

func startProxyWithOAuth(config *e2e.TestConfig, serverName, targetURL string, port int, issuer, clientID, clientSecret string) (*exec.Cmd, *syncBuffer) {
	args := []string{
		"proxy",
		"--host", "localhost",
		"--port", strconv.Itoa(port),
		"--target-uri", targetURL,
		"--remote-auth-skip-browser",  // Important for headless testing
		"--remote-auth-timeout", "5s", // Short timeout for testing
	}

	// Only add OAuth flags if issuer is provided
	if issuer != "" {
		args = append(args,
			"--remote-auth",
			"--remote-auth-issuer", issuer,
			"--remote-auth-client-id", clientID,
			"--remote-auth-client-secret", clientSecret)
	} else {
		// For missing issuer test, we still need to enable remote auth
		args = append(args,
			"--remote-auth",
			"--remote-auth-client-id", clientID,
			"--remote-auth-client-secret", clientSecret)
	}

	args = append(args, serverName)

	// Log the command for debugging
	GinkgoWriter.Printf("Starting proxy with args: %v\n", args)

	return startLongRunningWithCapture(config, args...)
}

func startProxyWithOAuthDetection(config *e2e.TestConfig, serverName, targetURL string, port int, clientID, clientSecret string) (*exec.Cmd, *syncBuffer) {
	args := []string{
		"proxy",
		"--host", "localhost",
		"--port", strconv.Itoa(port),
		"--target-uri", targetURL,
		"--remote-auth-client-id", clientID,
		"--remote-auth-client-secret", clientSecret,
		"--remote-auth-skip-browser",
		serverName,
	}

	return startLongRunningWithCapture(config, args...)
}

func startProxyWithAutoDetection(config *e2e.TestConfig, serverName, targetURL string, port int, clientID, clientSecret string) (*exec.Cmd, *syncBuffer) {
	args := []string{
		"proxy",
		"--host", "localhost",
		"--port", strconv.Itoa(port),
		"--target-uri", targetURL,
		"--remote-auth-client-id", clientID,
		"--remote-auth-client-secret", clientSecret,
		"--remote-auth-skip-browser",
		serverName,
	}

	// Log the command for debugging
	GinkgoWriter.Printf("Starting proxy with auto-detection args: %v\n", args)

	return startLongRunningWithCapture(config, args...)
}

func startProxyWithOAuthForMCP(config *e2e.TestConfig, serverName, targetURL string, port int, issuer, clientID, clientSecret string) (*exec.Cmd, *syncBuffer) {
	args := []string{
		"proxy",
		"--host", "localhost",
		"--port", strconv.Itoa(port),
		"--target-uri", targetURL,
		"--remote-auth-skip-browser",   // Important for headless testing
		"--remote-auth-timeout", "30s", // Longer timeout for MCP testing
		"--remote-auth",
		"--remote-auth-issuer", issuer,
		"--remote-auth-client-id", clientID,
		"--remote-auth-client-secret", clientSecret,
		serverName,
	}

	// Log the command for debugging
	GinkgoWriter.Printf("Starting proxy with OAuth for MCP args: %v\n", args)

	return startLongRunningWithCapture(config, args...)
}

// completeOAuthFlow programmatically completes the OAuth flow by visiting the authorization URL
func completeOAuthFlow(authURL string) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Follow redirects automatically
			return nil
		},
	}

	// Add auto_complete parameter to trigger automatic OAuth completion
	if authURL != "" {
		separator := "&"
		if !strings.Contains(authURL, "?") {
			separator = "?"
		}
		authURL = authURL + separator + "auto_complete=true"
	}

	// Make a request to the authorization URL
	// This will trigger the OAuth flow and redirect to the callback
	resp, err := client.Get(authURL)
	if err != nil {
		return fmt.Errorf("failed to complete OAuth flow: %w", err)
	}
	defer resp.Body.Close()

	// The response should be a redirect to the callback URL
	// or a success page if the flow completed
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return nil
	}

	return fmt.Errorf("OAuth flow failed with status: %d", resp.StatusCode)
}

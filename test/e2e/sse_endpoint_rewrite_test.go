// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

const (
	// sseEndpoint is the SSE endpoint path
	sseEndpoint = "/sse"
)

var _ = Describe("SSE Endpoint URL Rewriting", Label("sse", "endpoint-rewrite", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("SSE endpoint URL rewriting with explicit prefix", func() {
		Context("when using --endpoint-prefix flag", func() {
			var serverName string
			var mockSSEServer *httptest.Server
			var sseEndpointHit bool

			BeforeEach(func() {
				serverName = e2e.GenerateUniqueServerName("sse-rewrite-explicit")
				sseEndpointHit = false

				// Create a mock SSE server that mimics MCP SSE behavior
				mockSSEServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == sseEndpoint {
						sseEndpointHit = true
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Cache-Control", "no-cache")
						w.Header().Set("Connection", "keep-alive")
						w.WriteHeader(http.StatusOK)

						// Send an endpoint event (this is what MCP servers send during initialization)
						// The transparent proxy should rewrite this URL with the configured prefix
						flusher, ok := w.(http.Flusher)
						Expect(ok).To(BeTrue(), "ResponseWriter should support flushing")

						fmt.Fprintf(w, "event: endpoint\n")
						fmt.Fprintf(w, "data: /sse?sessionId=test-session-123\n")
						fmt.Fprintf(w, "\n")
						flusher.Flush()

						// Also send a message event to ensure it's NOT rewritten
						fmt.Fprintf(w, "event: message\n")
						fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"tools/list\",\"id\":1}\n")
						fmt.Fprintf(w, "\n")
						flusher.Flush()

						// Keep connection open briefly
						time.Sleep(100 * time.Millisecond)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			})

			AfterEach(func() {
				if mockSSEServer != nil {
					mockSSEServer.Close()
				}
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should rewrite SSE endpoint URLs with configured prefix [Serial]", func() {
				By("Starting a proxied remote server with explicit endpoint prefix")
				endpointPrefix := "/my-mcp-prefix"
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--endpoint-prefix", endpointPrefix,
					"--remote-url", mockSSEServer.URL,
				).ExpectSuccess()

				Expect(stdout+stderr).To(ContainSubstring(serverName), "Output should mention the server name")

				By("Waiting for the proxy to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Proxy should be running within 60 seconds")

				By("Getting the proxy URL")
				proxyURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get proxy URL")

				By("Connecting to the SSE endpoint through the proxy")
				// Parse the proxy URL and construct SSE endpoint
				parsedURL, err := url.Parse(proxyURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to parse proxy URL")

				// Construct SSE endpoint URL
				sseURL := fmt.Sprintf("http://%s/sse", parsedURL.Host)

				client := &http.Client{Timeout: 10 * time.Second}
				req, err := http.NewRequest("GET", sseURL, nil)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create request")
				req.Header.Set("Accept", "text/event-stream")

				resp, err := client.Do(req)
				Expect(err).ToNot(HaveOccurred(), "Should be able to connect to SSE endpoint")
				Expect(resp).ToNot(BeNil(), "Response should not be nil")
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK), "Should get 200 OK")
				Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("text/event-stream"),
					"Should return SSE content type")

				By("Reading the SSE stream and verifying URL rewriting")
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(make([]byte, 0, 1024), 1024*1024) // 1MB buffer for large responses

				var sseLines []string
				done := make(chan struct{})
				go func() {
					defer GinkgoRecover()
					defer close(done)
					for scanner.Scan() {
						line := scanner.Text()
						sseLines = append(sseLines, line)
						// Stop after reading a few events
						if len(sseLines) > 10 {
							break
						}
					}
				}()

				// Wait for SSE data with timeout
				select {
				case <-done:
					// Successfully read SSE stream
				case <-time.After(5 * time.Second):
					// Timeout - that's okay, we may have read enough
				}

				By("Verifying the SSE stream content")
				sseContent := strings.Join(sseLines, "\n")
				GinkgoWriter.Printf("Received SSE stream:\n%s\n", sseContent)

				// Verify endpoint event URL was rewritten with prefix
				Expect(sseContent).To(ContainSubstring("event: endpoint"),
					"Should contain endpoint event")
				Expect(sseContent).To(ContainSubstring(fmt.Sprintf("data: %s/sse?sessionId=test-session-123", endpointPrefix)),
					"Endpoint URL should be rewritten with configured prefix")

				// Verify message event data was NOT rewritten
				Expect(sseContent).To(ContainSubstring("event: message"),
					"Should contain message event")
				Expect(sseContent).To(ContainSubstring(`data: {"jsonrpc":"2.0","method":"tools/list","id":1}`),
					"Message event data should NOT be rewritten")

				By("Verifying the backend SSE server was actually hit")
				Expect(sseEndpointHit).To(BeTrue(), "Backend SSE server should have been called")
			})
		})

		Context("when using trust-proxy-headers with X-Forwarded-Prefix", func() {
			var serverName string
			var mockSSEServer *httptest.Server
			var forwardedPrefix string

			BeforeEach(func() {
				serverName = e2e.GenerateUniqueServerName("sse-rewrite-header")
				forwardedPrefix = "/ingress-path"

				// Create a mock SSE server
				mockSSEServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == sseEndpoint {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)

						flusher, ok := w.(http.Flusher)
						Expect(ok).To(BeTrue())

						fmt.Fprintf(w, "event: endpoint\n")
						fmt.Fprintf(w, "data: /sse?sessionId=header-test-456\n")
						fmt.Fprintf(w, "\n")
						flusher.Flush()

						time.Sleep(100 * time.Millisecond)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			})

			AfterEach(func() {
				if mockSSEServer != nil {
					mockSSEServer.Close()
				}
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred())
				}
			})

			It("should rewrite URLs using X-Forwarded-Prefix when trust-proxy-headers is enabled [Serial]", func() {
				By("Starting a proxied server with trust-proxy-headers enabled")
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--trust-proxy-headers",
					"--remote-url", mockSSEServer.URL,
				).ExpectSuccess()

				Expect(stdout + stderr).To(ContainSubstring(serverName))

				By("Waiting for the proxy to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the proxy URL")
				proxyURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				By("Connecting with X-Forwarded-Prefix header")
				parsedURL, err := url.Parse(proxyURL)
				Expect(err).ToNot(HaveOccurred())

				sseURL := fmt.Sprintf("http://%s/sse", parsedURL.Host)

				client := &http.Client{Timeout: 10 * time.Second}
				req, err := http.NewRequest("GET", sseURL, nil)
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Accept", "text/event-stream")
				req.Header.Set("X-Forwarded-Prefix", forwardedPrefix)

				resp, err := client.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Reading SSE stream and verifying URL rewriting with header-based prefix")
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

				var sseLines []string
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				done := make(chan struct{})
				go func() {
					defer close(done)
					for scanner.Scan() && ctx.Err() == nil {
						sseLines = append(sseLines, scanner.Text())
						if len(sseLines) > 10 {
							break
						}
					}
				}()

				select {
				case <-done:
				case <-ctx.Done():
				}

				sseContent := strings.Join(sseLines, "\n")
				GinkgoWriter.Printf("SSE stream with X-Forwarded-Prefix:\n%s\n", sseContent)

				// Verify URL was rewritten with the header-based prefix
				Expect(sseContent).To(ContainSubstring("event: endpoint"))
				Expect(sseContent).To(ContainSubstring(fmt.Sprintf("data: %s/sse?sessionId=header-test-456", forwardedPrefix)),
					"Endpoint URL should be rewritten with X-Forwarded-Prefix value")
			})
		})

		Context("when testing prefix priority", func() {
			var serverName string
			var mockSSEServer *httptest.Server

			BeforeEach(func() {
				serverName = e2e.GenerateUniqueServerName("sse-rewrite-priority")

				mockSSEServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == sseEndpoint {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)

						flusher, ok := w.(http.Flusher)
						Expect(ok).To(BeTrue())

						fmt.Fprintf(w, "event: endpoint\n")
						fmt.Fprintf(w, "data: /sse?sessionId=priority-test-789\n")
						fmt.Fprintf(w, "\n")
						flusher.Flush()

						time.Sleep(100 * time.Millisecond)
					}
				}))
			})

			AfterEach(func() {
				if mockSSEServer != nil {
					mockSSEServer.Close()
				}
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred())
				}
			})

			It("should prioritize explicit --endpoint-prefix over X-Forwarded-Prefix [Serial]", func() {
				By("Starting proxy with both explicit prefix and trust-proxy-headers")
				explicitPrefix := "/explicit-config"
				headerPrefix := "/from-header"

				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--endpoint-prefix", explicitPrefix,
					"--trust-proxy-headers",
					"--remote-url", mockSSEServer.URL,
				).ExpectSuccess()

				Expect(stdout + stderr).To(ContainSubstring(serverName))

				By("Waiting for the proxy to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the proxy URL")
				proxyURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				By("Connecting with X-Forwarded-Prefix (which should be ignored)")
				parsedURL, err := url.Parse(proxyURL)
				Expect(err).ToNot(HaveOccurred())

				sseURL := fmt.Sprintf("http://%s/sse", parsedURL.Host)

				client := &http.Client{Timeout: 10 * time.Second}
				req, err := http.NewRequest("GET", sseURL, nil)
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Accept", "text/event-stream")
				req.Header.Set("X-Forwarded-Prefix", headerPrefix) // This should be ignored

				resp, err := client.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Verifying explicit prefix takes priority")
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

				var sseLines []string
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				done := make(chan struct{})
				go func() {
					defer close(done)
					for scanner.Scan() && ctx.Err() == nil {
						sseLines = append(sseLines, scanner.Text())
						if len(sseLines) > 10 {
							break
						}
					}
				}()

				select {
				case <-done:
				case <-ctx.Done():
				}

				sseContent := strings.Join(sseLines, "\n")
				GinkgoWriter.Printf("SSE stream (priority test):\n%s\n", sseContent)

				// Verify explicit prefix was used, not the header value
				Expect(sseContent).To(ContainSubstring(fmt.Sprintf("data: %s/sse?sessionId=priority-test-789", explicitPrefix)),
					"Should use explicit --endpoint-prefix, not X-Forwarded-Prefix header")
				Expect(sseContent).ToNot(ContainSubstring(headerPrefix),
					"Should NOT use X-Forwarded-Prefix when explicit prefix is configured")
			})
		})

		Context("when testing with real MCP server from registry", func() {
			var serverName string

			BeforeEach(func() {
				serverName = e2e.GenerateUniqueServerName("sse-rewrite-real")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred())
				}
			})

			It("should work with a real SSE MCP server from registry [Serial]", func() {
				By("Starting an OSV server with SSE transport and endpoint prefix")
				endpointPrefix := "/api/mcp"

				// Check if osv server is available in registry
				stdout, _ := e2e.NewTHVCommand(config, "list", "--registry").ExpectSuccess()
				if !strings.Contains(stdout, "osv") {
					Skip("OSV server not available in registry")
				}

				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--transport", "sse",
					"--endpoint-prefix", endpointPrefix,
					"osv",
				).ExpectSuccess()

				Expect(stdout + stderr).To(ContainSubstring(serverName))

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 5*time.Minute)
				Expect(err).ToNot(HaveOccurred())

				By("Getting the server URL")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred())

				By("Waiting for MCP server to be ready")
				err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 2*time.Minute)
				Expect(err).ToNot(HaveOccurred())

				By("Connecting to SSE endpoint and checking for endpoint event")
				parsedURL, err := url.Parse(serverURL)
				Expect(err).ToNot(HaveOccurred())

				// For SSE transport, we need to connect to /sse endpoint
				sseURL := fmt.Sprintf("http://%s/sse", parsedURL.Host)

				client := &http.Client{Timeout: 30 * time.Second}
				req, err := http.NewRequest("GET", sseURL, nil)
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Accept", "text/event-stream")

				resp, err := client.Do(req)
				if err != nil {
					GinkgoWriter.Printf("Failed to connect to SSE endpoint: %v\n", err)
					// Get server logs for debugging
					logs, _, _ := e2e.NewTHVCommand(config, "logs", serverName).Run()
					GinkgoWriter.Printf("Server logs:\n%s\n", logs)
				}
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				By("Reading SSE stream and looking for endpoint event")
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

				var foundEndpointEvent bool
				var foundRewrittenURL bool
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				done := make(chan struct{})
				go func() {
					defer close(done)
					currentEvent := ""
					for scanner.Scan() && ctx.Err() == nil {
						line := scanner.Text()
						GinkgoWriter.Printf("SSE line: %s\n", line)

						if strings.HasPrefix(line, "event:") {
							currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
						} else if strings.HasPrefix(line, "data:") && currentEvent == "endpoint" {
							foundEndpointEvent = true
							// Check if the URL contains the configured prefix
							if strings.Contains(line, endpointPrefix) {
								foundRewrittenURL = true
								GinkgoWriter.Printf("Found rewritten endpoint URL: %s\n", line)
							}
						}

						if foundRewrittenURL {
							break
						}
					}
				}()

				select {
				case <-done:
				case <-ctx.Done():
				}

				By("Verifying endpoint event was found and URL was rewritten")
				Expect(foundEndpointEvent).To(BeTrue(), "Should find endpoint event in SSE stream")
				Expect(foundRewrittenURL).To(BeTrue(), "Endpoint URL should contain the configured prefix")
			})
		})
	})
})

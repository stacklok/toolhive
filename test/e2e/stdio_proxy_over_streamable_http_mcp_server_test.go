// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("TimeStreamableHttpMcpServer", Label("proxy", "streamable-http", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Context("when starting the time server with streamable-http proxy", func() {
		var serverName string

		BeforeEach(func() {
			serverName = e2e.GenerateUniqueServerName("time-streamable-test")
		})

		AfterEach(func() {
			if config.CleanupAfter {
				err := e2e.StopAndRemoveMCPServer(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
			}
		})

		It("should respond to a single get_current_time request and reject a batch request", func() {
			By("Starting the time MCP server with streamable-http proxy")
			e2e.NewTHVCommand(config, "run",
				"--name", serverName,
				"--proxy-mode", "streamable-http",
				"time").ExpectSuccess()

			By("Waiting for the server to be running")
			err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Getting the server URL")
			serverURL, err := e2e.GetMCPServerURL(config, serverName)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for MCP server to be ready")
			err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("Creating MCP client and initializing connection")
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = mcpClient.Initialize(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Calling get_current_time tool")
			mcpClient.ExpectToolExists(ctx, "get_current_time")
			arguments := map[string]interface{}{
				"timezone": "Europe/London",
			}
			result := mcpClient.ExpectToolCall(ctx, "get_current_time", arguments)
			Expect(result.Content).ToNot(BeEmpty(), "Should return the current time")

			By("Sending a batch JSON-RPC request, which must be rejected")
			// JSON-RPC batching was removed in MCP revision 2025-06-18. ToolHive
			// serves only 2025-11-25 and 2026-07-28, so the proxy rejects any
			// batch (a top-level JSON array) with an HTTP 400 / JSON-RPC -32600
			// "Invalid Request" and a null id, rather than forwarding its nested
			// calls past authz/audit/tool-filtering (see #5745, #5931).
			batch := []map[string]interface{}{
				{
					"method": "tools/call",
					"params": map[string]interface{}{
						"name": "get_current_time",
						"arguments": map[string]interface{}{
							"timezone": "Asia/Karachi",
						},
					},
					"jsonrpc": "2.0",
					"id":      4,
				},
				{
					"method": "tools/call",
					"params": map[string]interface{}{
						"name": "convert_time",
						"arguments": map[string]interface{}{
							"source_timezone": "Asia/Karachi",
							"time":            "16:50",
							"target_timezone": "Europe/London",
						},
					},
					"jsonrpc": "2.0",
					"id":      5,
				},
			}

			batchBytes, err := json.Marshal(batch)
			Expect(err).ToNot(HaveOccurred())

			client := &http.Client{Timeout: 10 * time.Second}
			req, err := http.NewRequestWithContext(ctx, "POST", serverURL, bytes.NewReader(batchBytes))
			Expect(err).ToNot(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "Batch requests must be rejected")

			body, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`"code":-32600`), "Should carry a JSON-RPC Invalid Request error")
			Expect(string(body)).To(ContainSubstring(`"id":null`), "Batch rejection carries a null JSON-RPC id")
		})
	})
})

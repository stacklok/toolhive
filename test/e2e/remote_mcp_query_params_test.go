// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Remote MCP server with URL query parameters",
	Label("remote", "mcp", "e2e", "proxy"), Serial, func() {
		var config *e2e.TestConfig

		BeforeEach(func() {
			config = e2e.NewTestConfig()

			// Check if thv binary is available
			err := e2e.CheckTHVBinaryAvailable(config)
			Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
		})

		Context("when registering a remote server URL with query parameters", func() {
			var serverName string

			BeforeEach(func() {
				serverName = e2e.GenerateUniqueServerName("remote-query-params-test")
			})

			AfterEach(func() {
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
				}
			})

			It("should not include URL query parameters in the generated proxy URL [Serial]", func() {
				By("Starting a remote MCP server with query parameters in the URL")
				// Use the standard remote test server with a query parameter appended.
				// The server ignores unknown params; we verify ToolHive strips them
				// from the client-facing proxy URL (the proxy forwards them transparently).
				registrationURL := remoteServerURL + "?toolsets=query-test"
				e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					registrationURL).ExpectSuccess()

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 30*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 30 seconds")

				By("Verifying the proxy URL does not contain query parameters from the registration URL")
				stdout, _ := e2e.NewTHVCommand(config, "list", "--format", "json").ExpectSuccess()

				var workloads []WorkloadInfo
				err = json.Unmarshal([]byte(stdout), &workloads)
				Expect(err).ToNot(HaveOccurred(), "Should be able to parse JSON output")

				var serverInfo *WorkloadInfo
				for i := range workloads {
					if workloads[i].Name == serverName {
						serverInfo = &workloads[i]
						break
					}
				}

				Expect(serverInfo).ToNot(BeNil(), "Server should appear in the list")
				// The proxy URL must not include query params — the transparent proxy
				// forwards them to the upstream on every request via WithRemoteRawQuery.
				// Including them in the client URL would cause duplication at the upstream.
				Expect(serverInfo.URL).NotTo(ContainSubstring("toolsets=query-test"),
					"Proxy URL should not include query parameters — the proxy forwards them transparently")
			})
		})
	})

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// This suite checks tools/list visibility end to end. By default the PDP is asked only
// mcp:tool:call. The PDP allows that for query_vulnerability and denies every other tool, so
// only query_vulnerability must be listed, and the PDP must never see mcp:tool:read.
var _ = Describe("Tool visibility with forward_tool_read", Label("middleware", "authz", "http-pdp", "e2e"), Serial, Ordered, func() {
	var (
		config         *e2e.TestConfig
		serverName     string
		mcpClient      *e2e.MCPClientHelper
		serverURL      string
		pdpServer      *mockPDPServer
		authzConfigDir string
		cancel         context.CancelFunc
	)

	BeforeAll(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("tool-read-fallback-test")

		pdpServer = newMockPDPServer()
		// Allow only tool:call on query_vulnerability. tool:read is not granted
		// anywhere, so every visibility decision must come from the fallback.
		pdpServer.addRule(true, func(porc map[string]interface{}) bool {
			operation, _ := porc["operation"].(string)
			resource, _ := porc["resource"].(string)
			return operation == "mcp:tool:read" && strings.HasSuffix(resource, ":tool:query_vulnerability")
		})
		pdpServer.addRule(false, func(_ map[string]interface{}) bool {
			return true
		})

		authzConfig := fmt.Sprintf(`{
  "version": "1.0",
  "type": "httpv1",
  "pdp": {
    "http": {
      "url": "%s",
      "timeout": 10,
      "insecure_skip_verify": true
    },
    "claim_mapping": "standard",
    "forward_tool_read": true
  }
}`, pdpServer.url())

		var err error
		authzConfigDir, err = os.MkdirTemp("", "tool-read-fallback-test")
		Expect(err).ToNot(HaveOccurred())
		authzConfigPath := filepath.Join(authzConfigDir, "authz-config.json")
		Expect(os.WriteFile(authzConfigPath, []byte(authzConfig), 0644)).To(Succeed())

		e2e.NewTHVCommand(config, "run",
			"--name", serverName,
			"--transport", "streamable-http",
			"--authz-config", authzConfigPath,
			"osv").ExpectSuccess()

		Expect(e2e.WaitForMCPServer(config, serverName, 60*time.Second)).To(Succeed())
		serverURL, err = e2e.GetMCPServerURL(config, serverName)
		Expect(err).ToNot(HaveOccurred())
		Expect(e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)).To(Succeed())
	})

	BeforeEach(func() {
		var err error
		mcpClient, err = e2e.NewMCPClientForStreamableHTTP(config, serverURL)
		Expect(err).ToNot(HaveOccurred())

		var ctx context.Context
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		Expect(mcpClient.Initialize(ctx)).To(Succeed())
	})

	AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		if mcpClient != nil {
			mcpClient.Close()
		}
	})

	AfterAll(func() {
		if config.CleanupAfter {
			Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())
			if pdpServer != nil {
				pdpServer.close()
			}
			if authzConfigDir != "" {
				os.RemoveAll(authzConfigDir)
			}
		}
	})

	It("lists only the tool the PDP allows, asking the PDP only for tool:read with forward_tool_read [Serial]", func() {
		ctx, cancelList := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelList()

		result, err := mcpClient.ListTools(ctx)
		Expect(err).ToNot(HaveOccurred())

		var names []string
		for _, tool := range result.Tools {
			names = append(names, tool.Name)
		}
		Expect(names).To(ConsistOf("query_vulnerability"),
			"the PDP allows tool:call only for query_vulnerability, so it must be the only tool listed")

		pdpServer.mu.RLock()
		captured := append([]map[string]interface{}{}, pdpServer.requests...)
		pdpServer.mu.RUnlock()

		// With forward_tool_read the PDP answers tool:read itself, once, and is never asked
		// tool:call for the listed tool, because there is no call fallback.
		readsForListed := 0
		for _, porc := range captured {
			op, _ := porc["operation"].(string)
			resource, _ := porc["resource"].(string)
			if !strings.HasSuffix(resource, ":tool:query_vulnerability") {
				continue
			}
			Expect(op).ToNot(Equal("mcp:tool:call"), "forward_tool_read must not fall back to mcp:tool:call")
			if op == "mcp:tool:read" {
				readsForListed++
			}
		}
		Expect(readsForListed).To(Equal(1), "the PDP must be asked mcp:tool:read exactly once for the listed tool")
	})
})

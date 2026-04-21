// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package e2e_test provides end-to-end tests for the vMCP optimizer tiers.
//
// vmcp_cli_features_test.go covers basic Tier-1 (FTS5 keyword optimizer,
// --optimizer flag) surface: tool exposure and find_tool query results.
// This file adds deeper coverage for RFC THV-0059 Phase 4:
//
//   - Tier-1 find→call round-trip: verifies that find_tool locates the yardstick
//     echo tool by description and call_tool invokes it end-to-end.
//   - Tier-1 two-backend with conflict resolution: verifies that optimizer
//     discovers tools from both backends when prefix conflict resolution is active.
//   - Tier-1 composite + optimizer: verifies that composite tools are indexed by
//     the optimizer and callable through call_tool.
//
// Tier-2 (TEI semantic optimizer) behaviour is covered by the unit tests in
// pkg/vmcp/cli/embedding_manager_test.go, which exercise container lifecycle,
// health polling, reuse, and error paths via mocks without requiring a running
// Docker daemon or a large model image.
package e2e_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcp "github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e"
)

// vmcpEndpointURL returns the MCP endpoint URL for a vMCP serve process
// listening on the given port.
func vmcpEndpointURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
}

// toolNames returns the Name field of each tool in order.
func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// findToolNames parses the StructuredContent of a find_tool result and returns
// the names of all returned tools. Returns nil when the content is absent or
// has an unexpected shape.
func findToolNames(result *mcp.CallToolResult) []string {
	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return nil
	}
	tools, ok := content["tools"].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if tool, ok := t.(map[string]any); ok {
			if name, ok := tool["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// firstToolNameContaining returns the first tool name from a find_tool result
// that contains the given substring, or "" if none is found.
func firstToolNameContaining(result *mcp.CallToolResult, substring string) string {
	for _, name := range findToolNames(result) {
		if strings.Contains(name, substring) {
			return name
		}
	}
	return ""
}

var _ = Describe("vMCP optimizer", Label("vmcp", "e2e", "optimizer"), func() {

	// -------------------------------------------------------------------------
	// Tier-1 find→call round-trip
	// Verifies that find_tool locates the yardstick echo tool by description
	// and that call_tool successfully invokes it, returning the echoed input.
	// -------------------------------------------------------------------------
	Context("Tier-1 optimizer find→call round-trip (single backend, quick mode)", func() {
		var fx singleBackendFixture

		BeforeEach(func() { fx.setup("vmcp-opt-roundtrip", "yardstick", "") })
		AfterEach(func() { fx.teardown() })

		It("find_tool locates the echo tool and call_tool invokes it end-to-end", func() {
			By("starting thv vmcp serve with --optimizer")
			fx.vMCPCmd = e2e.StartLongRunningTHVCommand(fx.cfg,
				"vmcp", "serve",
				"--group", fx.groupName,
				"--optimizer",
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			vMCPURL := vmcpEndpointURL(fx.vMCPPort)
			Expect(e2e.WaitForMCPServerReady(fx.cfg, vMCPURL, "streamable-http", 60*time.Second)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(fx.cfg, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			By("calling find_tool to locate the echo tool by description")
			findResult, err := mcpClient.CallTool(ctx, "find_tool", map[string]any{
				"tool_description": "echo a message back",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(findResult.IsError).To(BeFalse(), "find_tool must not return an error")

			echoToolName := firstToolNameContaining(findResult, "echo")
			Expect(echoToolName).ToNot(BeEmpty(),
				"find_tool must return a tool matching 'echo'; structured content: %v",
				findResult.StructuredContent)

			By(fmt.Sprintf("invoking %s via call_tool with a test message", echoToolName))
			callResult, err := mcpClient.CallTool(ctx, "call_tool", map[string]any{
				"tool_name":  echoToolName,
				"parameters": map[string]any{"input": "hellooptimizer"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callResult.IsError).To(BeFalse(), "call_tool must not return an error")
			Expect(callResult.Content).ToNot(BeEmpty(), "call_tool must return content")
			Expect(mcp.GetTextFromContent(callResult.Content[0])).To(ContainSubstring("hellooptimizer"),
				"echo tool must return the input message")
		})
	})

	// -------------------------------------------------------------------------
	// Tier-1 two-backend with prefix conflict resolution + optimizer
	// Two yardstick backends both expose "echo". With prefix conflict
	// resolution both tools are indexed; find_tool must discover at least one.
	// call_tool must invoke the discovered tool successfully.
	// -------------------------------------------------------------------------
	Context("Tier-1 optimizer two-backend with prefix conflict resolution", Ordered, func() {
		var fx twoBackendFixture

		BeforeAll(func() { fx.setupBackends("vmcp-opt-multi") })
		AfterAll(func() { fx.teardownBackends() })
		BeforeEach(func() { fx.setupPerTest("vmcp-opt-multi-*") })
		AfterEach(func() { fx.teardownPerTest() })

		It("find_tool discovers tools from both backends and call_tool invokes one", func() {
			configPath := filepath.Join(fx.tmpDir, "vmcp.yaml")
			initVMCPConfig(fx.cfg, fx.groupName, configPath)

			Expect(modifyVMCPConfig(configPath, func(c *vmcpconfig.Config) {
				if c.Aggregation == nil {
					c.Aggregation = &vmcpconfig.AggregationConfig{}
				}
				c.Aggregation.ConflictResolution = vmcp.ConflictStrategyPrefix
				c.Aggregation.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{
					PrefixFormat: "{workload}_",
				}
				c.Optimizer = &vmcpconfig.OptimizerConfig{}
			})).To(Succeed())

			By("starting vMCP serve with prefix conflict resolution and optimizer")
			fx.vMCPCmd = e2e.StartLongRunningTHVCommand(fx.cfg,
				"vmcp", "serve",
				"--config", configPath,
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			vMCPURL := vmcpEndpointURL(fx.vMCPPort)
			Expect(e2e.WaitForMCPServerReady(fx.cfg, vMCPURL, "streamable-http", 60*time.Second)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(fx.cfg, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			By("verifying only find_tool and call_tool are exposed")
			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(toolNames(tools.Tools)).To(ConsistOf("find_tool", "call_tool"))

			// With prefix resolution, each backend's echo tool is named
			// "<backendName>_echo". Query each backend's prefixed name directly
			// to confirm both are indexed independently.
			By("verifying backend A's prefixed echo tool is discoverable via find_tool")
			findA, err := mcpClient.CallTool(ctx, "find_tool", map[string]any{
				"tool_description": fx.backendAName + " echo",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(findA.IsError).To(BeFalse())
			Expect(findToolNames(findA)).To(ContainElement(ContainSubstring(fx.backendAName)),
				"find_tool must return backend A's prefixed echo tool; got: %v", findToolNames(findA))

			By("verifying backend B's prefixed echo tool is discoverable via find_tool")
			findB, err := mcpClient.CallTool(ctx, "find_tool", map[string]any{
				"tool_description": fx.backendBName + " echo",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(findB.IsError).To(BeFalse())
			Expect(findToolNames(findB)).To(ContainElement(ContainSubstring(fx.backendBName)),
				"find_tool must return backend B's prefixed echo tool; got: %v", findToolNames(findB))

			By("invoking a discovered echo tool via call_tool")
			echoToolName := firstToolNameContaining(findA, "echo")
			Expect(echoToolName).ToNot(BeEmpty())

			callResult, err := mcpClient.CallTool(ctx, "call_tool", map[string]any{
				"tool_name":  echoToolName,
				"parameters": map[string]any{"input": "multibackend"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callResult.IsError).To(BeFalse(), "call_tool must not return an error")
			Expect(callResult.Content).ToNot(BeEmpty(), "call_tool must return content")
			Expect(mcp.GetTextFromContent(callResult.Content[0])).To(ContainSubstring("multibackend"))
		})
	})

	// -------------------------------------------------------------------------
	// Tier-1 composite tool + optimizer (config-file mode)
	// Registers an echo_twice composite tool alongside optimizer. Verifies that
	// find_tool indexes it and call_tool executes the workflow end-to-end.
	// -------------------------------------------------------------------------
	Context("Tier-1 optimizer with composite tool (config-file mode)", func() {
		var fx singleBackendFixture

		BeforeEach(func() { fx.setup("vmcp-opt-composite", "yardstick", "vmcp-opt-composite-*") })
		AfterEach(func() { fx.teardown() })

		It("find_tool discovers the composite tool and call_tool executes it", func() {
			configPath := filepath.Join(fx.tmpDir, "vmcp.yaml")
			initVMCPConfig(fx.cfg, fx.groupName, configPath)

			Expect(modifyVMCPConfig(configPath, func(c *vmcpconfig.Config) {
				if c.Aggregation == nil {
					c.Aggregation = &vmcpconfig.AggregationConfig{}
				}
				c.Aggregation.ConflictResolution = vmcp.ConflictStrategyPrefix
				c.Optimizer = &vmcpconfig.OptimizerConfig{}
				c.CompositeTools = []vmcpconfig.CompositeToolConfig{
					{
						Name:        "echo_twice",
						Description: "Echoes the input message twice in sequence",
						Parameters: thvjson.NewMap(map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{
									"type":        "string",
									"description": "The message to echo twice",
								},
							},
							"required": []any{"message"},
						}),
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "first_echo",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", fx.backendName),
								Arguments: thvjson.NewMap(map[string]any{
									"input": "{{ .params.message }}",
								}),
							},
							{
								ID:        "second_echo",
								Type:      "tool",
								Tool:      fmt.Sprintf("%s.echo", fx.backendName),
								DependsOn: []string{"first_echo"},
								Arguments: thvjson.NewMap(map[string]any{
									"input": "{{ .params.message }}",
								}),
							},
						},
					},
				}
			})).To(Succeed())

			By("starting vMCP serve with composite tool and optimizer")
			fx.vMCPCmd = e2e.StartLongRunningTHVCommand(fx.cfg,
				"vmcp", "serve",
				"--config", configPath,
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			vMCPURL := vmcpEndpointURL(fx.vMCPPort)
			Expect(e2e.WaitForMCPServerReady(fx.cfg, vMCPURL, "streamable-http", 60*time.Second)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(fx.cfg, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			By("verifying only find_tool and call_tool are exposed")
			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(toolNames(tools.Tools)).To(ConsistOf("find_tool", "call_tool"))

			By("discovering the composite tool via find_tool")
			findResult, err := mcpClient.CallTool(ctx, "find_tool", map[string]any{
				"tool_description": "echo a message twice in sequence",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(findResult.IsError).To(BeFalse())
			Expect(findToolNames(findResult)).To(ContainElement(ContainSubstring("echo_twice")),
				"find_tool must discover the composite tool; got: %v", findResult.StructuredContent)

			By("invoking echo_twice via call_tool and verifying the result")
			callResult, err := mcpClient.CallTool(ctx, "call_tool", map[string]any{
				"tool_name":  "echo_twice",
				"parameters": map[string]any{"message": "hellocomposite"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callResult.IsError).To(BeFalse(), "call_tool must not return an error for composite tool")
			Expect(callResult.Content).ToNot(BeEmpty(), "call_tool must return content from composite tool")
		})
	})

}) // end Describe("vMCP optimizer")

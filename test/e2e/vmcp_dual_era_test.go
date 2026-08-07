// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/test/e2e"
)

// This suite is the live e2e tier proving vMCP bridges all four client x
// backend MCP-revision combinations (issue #5912). vMCP is an aggregating
// gateway: it terminates the client connection and re-originates calls to
// each backend, classifying the two edges independently -- the client edge
// per request (Legacy 2025-11-25 vs. Modern 2026-07-28, cached per
// server/discover probe on the backend edge). Existing coverage
// (pkg/vmcp/server/bridge_regression_test.go) pins this at the unit/
// integration level with mocked backends; this file proves it with two real
// yardstick-server backends and a real thv vmcp serve process.
//
// De-risked live before writing this: a Modern server/discover probe sent
// BY vMCP through the thv proxy fronting a stateless yardstick backend is
// correctly classified Modern (its tool appears in vMCP's aggregated
// tools/list under the "{backend}_" prefix, and a tools/call routed to it
// succeeds) -- confirming the one assumption this whole suite rests on.
//
// Also confirmed live: an ordinary (non-initialize) tools/call response --
// through vMCP, on either client edge -- never carries Mcp-Session-Id; only
// the Legacy initialize response does. So "session id present for Legacy"
// is asserted on the initialize handshake, not on every subsequent call
// (mirrors the same finding already documented in dual_era_mixing_test.go
// for the single-server transparent proxy).
//
// Every request in this file sends the streamable-HTTP Accept header both MCP
// revisions require: "application/json, text/event-stream". Legacy responses
// may therefore be SSE-framed by the go-sdk transport, while Modern responses
// remain plain JSON; e2e.RawMCPClient parses both shapes before assertions.
var _ = Describe("vMCP Dual-Era Bridge", Label("vmcp", "dual-era", "e2e"), Serial, func() {
	Context("one Legacy and one Modern backend in the same group", func() {
		var (
			config            *e2e.TestConfig
			groupName         string
			legacyBackendName string
			modernBackendName string
			legacyToolName    string
			modernToolName    string
			vMCPCmd           *exec.Cmd
			vMCPPort          int
			vMCPURL           string
			rawClient         *e2e.RawMCPClient
		)

		BeforeEach(func() {
			config = e2e.NewTestConfig()
			groupName = e2e.GenerateUniqueServerName("vmcp-dual-era-group")
			legacyBackendName = e2e.GenerateUniqueServerName("vmcp-dual-era-legacy")
			modernBackendName = e2e.GenerateUniqueServerName("vmcp-dual-era-modern")
			legacyToolName = legacyBackendName + "_echo"
			modernToolName = modernBackendName + "_echo"
			vMCPCmd = nil
			vMCPPort = allocateVMCPPort()
			vMCPURL = vmcpEndpointURL(vMCPPort)

			err := e2e.CheckTHVBinaryAvailable(config)
			Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

			rawClient, err = e2e.NewRawMCPClient(20 * time.Second)
			Expect(err).ToNot(HaveOccurred())

			By("creating a group with one Legacy and one Modern-capable backend")
			e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
			// allocateVMCPPort just allocates any free 127.0.0.1 port; reused here
			// for the backends too.
			startYardstickLegacyOnPort(config, groupName, legacyBackendName, allocateVMCPPort())
			startYardstickModernOnPort(config, groupName, modernBackendName, allocateVMCPPort())

			By("generating a vMCP config with health monitoring enabled")
			// Config-file mode (not quick mode) solely so health monitoring can be
			// turned on -- see appendHealthCheckConfig's doc comment -- which is what
			// makes /status's mcp_revision observable below.
			tmpDir, err := os.MkdirTemp("", "vmcp-dual-era-config-*")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
			configFilePath := filepath.Join(tmpDir, "vmcp.yaml")
			initVMCPConfig(config, groupName, configFilePath)
			appendHealthCheckConfig(configFilePath)

			By("starting thv vmcp serve")
			vMCPCmd = startDualEraVMCP(config, groupName, configFilePath, vMCPPort)
			err = e2e.WaitForMCPServerReady(config, vMCPURL, "streamable-http", 60*time.Second)
			Expect(err).ToNot(HaveOccurred(), "vMCP server should become ready")

			By("confirming vMCP classified each backend's MCP revision correctly")
			// The positive, direct proof that this suite's 2x2 matrix has not
			// silently collapsed to 1x2 -- see waitForBackendRevision's doc comment.
			statusURL := vmcpStatusURL(vMCPPort)
			waitForBackendRevision(statusURL, legacyBackendName, mcpparser.MCPVersionLegacy)
			waitForBackendRevision(statusURL, modernBackendName, mcpparser.MCPVersionModern)
		})

		AfterEach(func() {
			stopVMCPProcess(vMCPCmd)
			vMCPCmd = nil

			if config.CleanupAfter {
				_ = e2e.StopAndRemoveMCPServer(config, legacyBackendName)
				_ = e2e.StopAndRemoveMCPServer(config, modernBackendName)
				_ = e2e.RemoveGroup(config, groupName)
			}
		})

		It("bridges Legacy client to Legacy backend (baseline)", func() {
			ctx := context.Background()
			sessionID := legacyInitialize(ctx, rawClient, vMCPURL)

			resp := legacyToolCall(ctx, rawClient, vMCPURL, sessionID, legacyToolName, "legacytolegacy")
			assertBridgedCall(resp, "legacytolegacy", "")
		})

		It("bridges Modern client to Modern backend (both edges stateless)", func() {
			ctx := context.Background()
			resp := modernToolCall(ctx, rawClient, vMCPURL, modernToolName, "moderntomodern")
			assertBridgedCall(resp, "moderntomodern", "complete")
			Expect(resp.Headers.Get(e2e.HeaderMCPSessionID)).To(BeEmpty(), "Modern responses must never carry Mcp-Session-Id")
		})

		It("bridges Modern client to Legacy backend (vMCP synthesizes a per-request backend session)", func() {
			ctx := context.Background()
			resp := modernToolCall(ctx, rawClient, vMCPURL, legacyToolName, "moderntolegacy")
			assertBridgedCall(resp, "moderntolegacy", "complete")
			Expect(resp.Headers.Get(e2e.HeaderMCPSessionID)).To(BeEmpty(), "Modern responses must never carry Mcp-Session-Id")
		})

		It("bridges Legacy client to Modern backend (vMCP terminates the client session, re-originates stateless)", func() {
			ctx := context.Background()
			sessionID := legacyInitialize(ctx, rawClient, vMCPURL)

			resp := legacyToolCall(ctx, rawClient, vMCPURL, sessionID, modernToolName, "legacytomodern")
			assertBridgedCall(resp, "legacytomodern", "")
		})

		It("preserves response content under plain JSON and SSE-capable Accept", func() {
			ctx := context.Background()
			sessionID := legacyInitialize(ctx, rawClient, vMCPURL)

			legacyPlain := legacyToolCallWithAccept(ctx, rawClient, vMCPURL, sessionID, legacyToolName, "legacyaccept", false)
			legacyConformant := legacyToolCallWithAccept(ctx, rawClient, vMCPURL, sessionID, legacyToolName, "legacyaccept", true)
			assertBridgedCall(legacyPlain, "legacyaccept", "")
			assertBridgedCall(legacyConformant, "legacyaccept", "")
			Expect(legacyConformant.Result).To(MatchJSON(legacyPlain.Result),
				"Legacy content must be identical whether the transport frames the response as plain JSON or SSE")

			modernPlain := modernToolCallWithAccept(ctx, rawClient, vMCPURL, modernToolName, "modernaccept", false)
			modernConformant := modernToolCallWithAccept(ctx, rawClient, vMCPURL, modernToolName, "modernaccept", true)
			assertBridgedCall(modernPlain, "modernaccept", "complete")
			assertBridgedCall(modernConformant, "modernaccept", "complete")
			Expect(modernConformant.Result).To(MatchJSON(modernPlain.Result),
				"Modern content must be identical under both Accept values")
		})

		It("never cross-delivers between two concurrent principals across mismatched backends", func() {
			// Principal A: a Legacy session calling the Legacy backend, perEra
			// times per round. Principal B: a stateless Modern client calling the
			// Modern backend, also perEra times per round. All 2*perEra requests
			// fire concurrently, each carrying a unique nonce -- a wrong echoed
			// nonce means vMCP delivered one request's response to another. Firing
			// several requests per era (not just one) also covers same-era
			// same-backend interleaving, not only a cross-era swap.
			const rounds = 5
			const perEra = 4
			sessionID := legacyInitialize(context.Background(), rawClient, vMCPURL)

			for round := 0; round < rounds; round++ {
				label := fmt.Sprintf("round-%d", round)
				results := fireConcurrentBridgedBatch(rawClient, vMCPURL, sessionID, legacyToolName, modernToolName, perEra, round)
				assertCorrelated(results, 2*perEra, label)
			}
		})

		It("never cross-delivers when both BRIDGE cells are in flight together", func() {
			// The spec above races the two SAME-era cells (Legacy client→Legacy
			// backend, Modern client→Modern backend). That leaves the cells this
			// suite exists for untested under concurrency: here the tool names are
			// crossed, so the Legacy session calls the MODERN backend and the
			// stateless Modern client calls the LEGACY backend -- both bridge cells,
			// concurrently, with distinct principals.
			//
			// This is the configuration where a cross-delivery would matter most.
			// Each bridge cell re-originates through a different egress (the Legacy
			// session's calls reach a Modern backend via the stateless modernCall
			// path; the Modern client's calls reach a Legacy backend via a
			// per-request initialize+call+close), and both are in flight at once. A
			// wrong echoed nonce means vMCP crossed a response between two
			// principals whose requests took *different* egress paths -- the failure
			// the per-request client construction is what prevents, and which
			// RFC-0083 D6's shared connection pool is the change most likely to
			// break. The pkg/vmcp/server bridge pins cover that at the unit level
			// for the Modern→Legacy direction; this is the live counterpart for
			// both directions at once.
			const rounds = 5
			const perEra = 4
			sessionID := legacyInitialize(context.Background(), rawClient, vMCPURL)

			for round := 0; round < rounds; round++ {
				label := fmt.Sprintf("bridge-round-%d", round)
				// Crossed on purpose: legacy CLIENT calls the modern tool, modern
				// CLIENT calls the legacy tool.
				results := fireConcurrentBridgedBatch(rawClient, vMCPURL, sessionID, modernToolName, legacyToolName, perEra, round)
				assertCorrelated(results, 2*perEra, label)
			}
		})
	})
})

// legacyInitialize sends a Legacy initialize request, followed by the
// notifications/initialized notification the 2025-11-25 spec requires a
// client to send immediately after (before any other request on the
// session) -- a real client always sends it, so this suite should too rather
// than only ever exercising the (also-valid, but unrepresentative) omitted
// case. Returns the assigned Mcp-Session-Id, failing the spec if the
// handshake does not succeed, omits the session id, or the notification is
// not accepted.
func legacyInitialize(ctx context.Context, client *e2e.RawMCPClient, url string) string {
	GinkgoHelper()
	req := e2e.NewLegacyInitializeRequest("dual-era-legacy-client", "1.0").WithStreamableAccept()
	resp, err := client.Send(ctx, url, req)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(200), "body: %s", resp.Body)
	sessionID := resp.Headers.Get(e2e.HeaderMCPSessionID)
	Expect(sessionID).ToNot(BeEmpty(), "Legacy initialize must assign a session id")

	notifyReq, err := e2e.NewLegacyRequest("notifications/initialized", nil)
	Expect(err).ToNot(HaveOccurred())
	// WithID(nil) omits "id" entirely, making this a true JSON-RPC notification.
	notifyReq.WithID(nil).WithSessionID(sessionID).SetHeader(e2e.HeaderMCPProtocolVersion, mcpparser.MCPVersionLegacy).WithStreamableAccept()
	notifyResp, err := client.Send(ctx, url, notifyReq)
	Expect(err).ToNot(HaveOccurred())
	Expect(notifyResp.StatusCode).To(Equal(202), "body: %s", notifyResp.Body)

	return sessionID
}

// legacyToolCall sends a Legacy tools/call for toolName with sessionID,
// carrying the MCP-Protocol-Version header the 2025-11-25 spec requires on
// every post-initialize request.
func legacyToolCall(
	ctx context.Context, client *e2e.RawMCPClient, url, sessionID, toolName, input string,
) *e2e.RawResponse {
	GinkgoHelper()
	return legacyToolCallWithAccept(ctx, client, url, sessionID, toolName, input, true)
}

func legacyToolCallWithAccept(
	ctx context.Context, client *e2e.RawMCPClient, url, sessionID, toolName, input string, streamableAccept bool,
) *e2e.RawResponse {
	GinkgoHelper()
	req, err := e2e.NewLegacyRequest("tools/call", map[string]any{
		"name":      toolName,
		"arguments": map[string]any{"input": input},
	})
	Expect(err).ToNot(HaveOccurred())
	req.WithSessionID(sessionID).SetHeader(e2e.HeaderMCPProtocolVersion, mcpparser.MCPVersionLegacy)
	if streamableAccept {
		req.WithStreamableAccept()
	}
	resp, err := client.Send(ctx, url, req)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

// modernToolCall sends a stateless Modern tools/call for toolName.
func modernToolCall(ctx context.Context, client *e2e.RawMCPClient, url, toolName, input string) *e2e.RawResponse {
	GinkgoHelper()
	return modernToolCallWithAccept(ctx, client, url, toolName, input, true)
}

func modernToolCallWithAccept(
	ctx context.Context, client *e2e.RawMCPClient, url, toolName, input string, streamableAccept bool,
) *e2e.RawResponse {
	GinkgoHelper()
	req, err := e2e.NewModernRequest("tools/call", map[string]any{
		"name":      toolName,
		"arguments": map[string]any{"input": input},
	})
	Expect(err).ToNot(HaveOccurred())
	if streamableAccept {
		req.WithStreamableAccept()
	}
	resp, err := client.Send(ctx, url, req)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

// fireConcurrentBridgedBatch fires perEra concurrent Legacy tools/call
// requests (principal A, sessionID, legacyClientTool) and perEra concurrent
// Modern tools/call requests (principal B, stateless, modernClientTool) --
// 2*perEra requests total, all released together via a start channel so they
// are genuinely in flight at once (not sequential per era) -- each carrying a
// round-unique nonce in _meta. Reuses crossDeliveryResult/extractNonce from
// dual_era_proxy_test.go (same package); the caller asserts on the returned
// results with assertCorrelated.
//
// The two tool names decide which CELLS run concurrently, and the caller picks:
// passing (legacyTool, modernTool) races the two SAME-era cells, while passing
// them crossed (modernTool, legacyTool) races the two BRIDGE cells. Both matter
// -- see the callers.
func fireConcurrentBridgedBatch(
	client *e2e.RawMCPClient, url, sessionID, legacyClientTool, modernClientTool string, perEra, round int,
) []crossDeliveryResult {
	const joinTimeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()

	total := 2 * perEra
	results := make([]crossDeliveryResult, total)
	var wg sync.WaitGroup
	start := make(chan struct{})

	fire := func(i int, build func() (*e2e.RawRequest, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nonce := fmt.Sprintf("round-%d-req-%d", round, i)
			<-start

			req, err := build()
			if err != nil {
				results[i] = crossDeliveryResult{nonce: nonce, err: err}
				return
			}
			req.SetMeta("nonce", nonce)

			reqStart := time.Now()
			resp, sendErr := client.Send(ctx, url, req)
			elapsed := time.Since(reqStart)
			if sendErr != nil {
				results[i] = crossDeliveryResult{nonce: nonce, err: sendErr, elapsed: elapsed}
				return
			}
			results[i] = crossDeliveryResult{
				nonce:      nonce,
				statusCode: resp.StatusCode,
				elapsed:    elapsed,
				rpcErr:     resp.Error,
				gotNonce:   extractNonce(resp),
			}
		}()
	}

	for i := 0; i < perEra; i++ {
		fire(i, func() (*e2e.RawRequest, error) {
			req, err := e2e.NewLegacyRequest("tools/call", map[string]any{
				"name":      legacyClientTool,
				"arguments": map[string]any{"input": "concurrencycheck"},
			})
			if err != nil {
				return nil, err
			}
			return req.WithSessionID(sessionID).SetHeader(e2e.HeaderMCPProtocolVersion, mcpparser.MCPVersionLegacy).WithStreamableAccept(), nil
		})
	}
	for i := 0; i < perEra; i++ {
		fire(perEra+i, func() (*e2e.RawRequest, error) {
			req, err := e2e.NewModernRequest("tools/call", map[string]any{
				"name":      modernClientTool,
				"arguments": map[string]any{"input": "concurrencycheck"},
			})
			if err != nil {
				return nil, err
			}
			return req.WithStreamableAccept(), nil
		})
	}

	close(start) // release every goroutine together

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(joinTimeout):
		Fail(fmt.Sprintf("round %d: timed out waiting for %d concurrent requests", round, total))
	}

	return results
}

// echoResult is the decoded shape of yardstick's echo tool result.
// ResultType is only present in a Modern client's response envelope
// (empty for Legacy) -- see this file's doc comment.
type echoResult struct {
	ResultType        string `json:"resultType"`
	StructuredContent struct {
		Output string `json:"output"`
	} `json:"structuredContent"`
}

// assertBridgedCall asserts a tools/call response succeeded, round-tripped
// wantOutput, and carries the era-correct resultType (only a Modern client's
// own response is wrapped with resultType, regardless of which era backend
// served it). This proves a round-trip to *a* backend with the era-correct
// envelope; it does NOT by itself prove WHICH backend answered -- both
// backends run the same yardstick echo tool, so a misrouted call would echo
// the same value back. Backend attribution is proven separately, by the
// waitForBackendRevision /status check in this context's BeforeEach.
func assertBridgedCall(resp *e2e.RawResponse, wantOutput, wantResultType string) {
	GinkgoHelper()
	Expect(resp.Error).To(BeNil(), "unexpected JSON-RPC error: %+v", resp.Error)
	Expect(resp.StatusCode).To(Equal(200), "body: %s", resp.Body)

	var result echoResult
	Expect(json.Unmarshal(resp.Result, &result)).To(Succeed(), "result: %s", resp.Result)
	Expect(result.StructuredContent.Output).To(Equal(wantOutput),
		"tools/call round-trip did not echo the expected output")
	Expect(result.ResultType).To(Equal(wantResultType))
}

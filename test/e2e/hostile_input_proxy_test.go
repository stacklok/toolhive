// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/test/e2e"
)

// This spec is the hostile-input tier of the dual-era proxy e2e suite (issue
// #5837, tier 2): a hand-rolled raw client drives malformed/adversarial
// Modern (2026-07-28) traffic at the transparent proxy (remote MCP
// passthrough, session-aware -- no --stateless) fronting the in-process
// statelessMockMCPServer (see stateless_proxy_test.go), and asserts each
// rejection fires at the edge -- exact error.code, error.data, and echoed
// id -- without the backend ever being contacted.
//
// Known gaps, deliberately not asserted here (see the design plan and
// pkg/mcp/revision.go's ClassifyRevision doc comment): Mcp-Method/Mcp-Name
// header validation (unimplemented), batch/client-response smuggling
// (handled as Legacy today, deferred), and header-driven GET/DELETE 405
// (that gate is stateless-mode-driven, not classifier-driven). Also a known
// spec divergence: ClassifyRevision is transport-agnostic and cannot tell
// "stdio, no header concept" (-32602 is correct) apart from "HTTP, header
// omitted" (where the draft's Server Validation rules make a missing
// MCP-Protocol-Version header itself a -32020 condition) -- see the TODO in
// revision.go. This suite exercises the HTTP transport but the classifier
// currently returns -32602 either way.
var _ = Describe("Hostile Input Proxy", Label("proxy", "stateless", "hostile-input", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		mockServer *statelessMockMCPServer
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("hostile-input")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		mockServer, err = newStatelessMockMCPServer()
		Expect(err).ToNot(HaveOccurred(), "should be able to start mock server")

		client, err = e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		By("Starting thv against the in-process mock (transparent proxy, session-aware)")
		e2e.NewTHVCommand(config, "run", "--name", serverName, mockServer.URL()+"/mcp").ExpectSuccess()

		err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred(), "server should be running within 60 seconds")

		proxyURL, err = e2e.GetMCPServerURL(config, serverName)
		Expect(err).ToNot(HaveOccurred(), "should be able to get proxy URL")
		if !strings.HasSuffix(proxyURL, "/mcp") {
			proxyURL += "/mcp"
		}
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Stop()
			mockServer = nil
		}
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "should be able to stop and remove server")
		}
	})

	It("rejects hostile Modern inputs at the edge without contacting the backend", func() {
		// A manual loop over cases, not DescribeTable: BeforeEach's thv run
		// setup (~seconds) is shared once across all cases here instead of
		// paying it 5x, one per DescribeTable entry.
		for _, tc := range hostileRejectionCases() {
			By(tc.name)
			countBefore := mockServer.GetCount()

			req, err := e2e.NewModernRequest("tools/list", nil)
			Expect(err).ToNot(HaveOccurred(), tc.name)
			req.WithID(tc.id)
			tc.mutate(req)

			resp, err := client.Send(context.Background(), proxyURL, req)
			Expect(err).ToNot(HaveOccurred(), tc.name)
			Expect(resp.StatusCode).To(Equal(tc.wantStatus), tc.name)
			if tc.idNotEchoed {
				Expect(resp.ID).To(BeNil(), "%s: echoed id", tc.name)
			} else {
				Expect(resp.ID).To(Equal(json.Number(fmt.Sprintf("%d", tc.id))), "%s: echoed id", tc.name)
			}
			Expect(resp.Error).ToNot(BeNil(), tc.name)
			Expect(resp.Error.Code).To(Equal(tc.wantCode), tc.name)
			if tc.wantData != nil {
				var gotData map[string]any
				Expect(json.Unmarshal(resp.Error.Data, &gotData)).To(Succeed(), "%s: error.data must decode", tc.name)
				Expect(gotData).To(Equal(tc.wantData), "%s: error.data", tc.name)
			} else {
				Expect(resp.Error.Data).To(BeEmpty(), "%s: error.data should be absent", tc.name)
			}
			Expect(mockServer.GetCount()).To(Equal(countBefore),
				"%s: backend must not be contacted for a rejected request", tc.name)
		}
	})

	It("forwards a well-formed Modern request with no session id (200)", func() {
		countBefore := mockServer.GetCount()

		req, err := e2e.NewModernRequest("tools/list", nil)
		Expect(err).ToNot(HaveOccurred())
		req.WithID(int64(2001))

		resp, err := client.Send(context.Background(), proxyURL, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Error).To(BeNil())
		Expect(resp.ID).To(Equal(json.Number("2001")))
		// "exactly once", not just ">= 1": proves the proxy forwards a
		// well-formed sessionless Modern request to the backend a single
		// time -- no retry, no double-send.
		Expect(mockServer.GetCount()).To(Equal(countBefore+1), "backend must be contacted exactly once")
	})

	It("handles oversized and truncated bodies cleanly, without a crash or hang", func() {
		By("an oversized body is rejected with 413, not forwarded")
		countBefore := mockServer.GetCount()
		oversized := []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":3001,"method":"tools/list","params":{"padding":"%s"}}`,
			strings.Repeat("a", 9<<20))) // 9 MiB, above bodylimit.DefaultMaxRequestBodySize (8 MiB)
		resp, err := client.SendRaw(context.Background(), proxyURL,
			map[string]string{"Content-Type": "application/json"}, oversized)
		Expect(err).ToNot(HaveOccurred(), "must not hang or crash the proxy")
		Expect(resp.StatusCode).To(Equal(http.StatusRequestEntityTooLarge))
		Expect(mockServer.GetCount()).To(Equal(countBefore),
			"oversized body must be rejected by bodylimit before the backend is contacted")

		// Unlike the classification cases above, an unparseable body is NOT
		// rejected at the edge: parseRPCRequest fails to decode it as either a
		// single request or a batch, so RoundTrip never calls ClassifyRevision
		// and forwards the body unclassified (Legacy passthrough) -- the proxy
		// does not validate JSON syntax before forwarding. The 400 below comes
		// from the *backend's* own json.Unmarshal failure, after it has
		// already been contacted.
		By("a truncated body is forwarded as Legacy passthrough; the backend rejects it")
		countBefore = mockServer.GetCount()
		truncated := []byte(`{"jsonrpc":"2.0","id":3002,"method":"tools/list","para`) // deliberately cut mid-object
		resp, err = client.SendRaw(context.Background(), proxyURL,
			map[string]string{"Content-Type": "application/json"}, truncated)
		Expect(err).ToNot(HaveOccurred(), "must not hang or crash the proxy")
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(mockServer.GetCount()).To(Equal(countBefore+1),
			"an unparseable body skips classification and IS forwarded to the backend")

		By("the proxy still serves a well-formed request after the hostile inputs above")
		req, err := e2e.NewModernRequest("tools/list", nil)
		Expect(err).ToNot(HaveOccurred())
		req.WithID(int64(3003))
		resp, err = client.Send(context.Background(), proxyURL, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "proxy must survive the oversized/truncated requests above")
	})
})

// hostileCase is one adversarial Modern request: start from a well-formed
// NewModernRequest, apply mutate to break exactly one classification/session
// rule, and expect the proxy to reject it before the backend is contacted.
type hostileCase struct {
	name       string
	id         int64
	mutate     func(req *e2e.RawRequest)
	wantStatus int
	wantCode   int64
	wantData   map[string]any // nil means the error must carry no "data" field at all
	// idNotEchoed marks a rejection path that -- unlike ClassificationErrorResponse
	// -- doesn't echo the request id at all: session.NotFoundResponse hardcodes a
	// nil id (see jsonrpc_errors.go's NotFoundBody(nil) call). A real minor
	// asymmetry in the current code, left flagged rather than fixed (out of scope
	// for a test-only step).
	idNotEchoed bool
}

// hostileRejectionCases enumerates the classification-error and session-guard
// rejections the transparent proxy must produce for adversarial Modern
// traffic. Each starts from NewModernRequest's well-formed default (header
// MCP-Protocol-Version + _meta.protocolVersion both "2026-07-28",
// _meta.clientCapabilities {}) and mutates exactly the one thing that breaks it.
func hostileRejectionCases() []hostileCase {
	return []hostileCase{
		{
			name: "header/body protocolVersion mismatch -> -32020",
			id:   1001,
			mutate: func(req *e2e.RawRequest) {
				// Body still claims Modern (NewModernRequest's default _meta);
				// the header disagrees. Neither value wins -- hard rejection.
				req.SetHeader(e2e.HeaderMCPProtocolVersion, "2025-11-25")
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   mcp.CodeHeaderMismatch,
			wantData:   map[string]any{"header": "2025-11-25", "body": e2e.MCPVersionModern},
		},
		{
			name: "unsupported version in _meta.protocolVersion -> -32022",
			id:   1002,
			mutate: func(req *e2e.RawRequest) {
				req.SetMeta(e2e.MetaKeyProtocolVersion, "2099-01-01")
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   mcp.CodeUnsupportedProtocolVersion,
			wantData:   map[string]any{"supported": []any{e2e.MCPVersionModern}, "requested": "2099-01-01"},
		},
		{
			name: "missing clientCapabilities -> -32021",
			id:   1003,
			mutate: func(req *e2e.RawRequest) {
				req.DeleteMeta(e2e.MetaKeyClientCapabilities)
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   mcp.CodeMissingClientCapability,
			wantData:   map[string]any{"requiredCapabilities": map[string]any{}},
		},
		{
			name: "reserved _meta key, no header, no valid version -> -32602",
			id:   1004,
			mutate: func(req *e2e.RawRequest) {
				// clientCapabilities alone remains as the reserved-key Modern
				// signal; with no header and no body protocolVersion, the draft
				// spec defines no dedicated code, so this falls back to the
				// standard JSON-RPC Invalid Params code.
				req.DeleteHeader(e2e.HeaderMCPProtocolVersion)
				req.DeleteMeta(e2e.MetaKeyProtocolVersion)
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   mcp.CodeInvalidParams,
			wantData:   nil,
		},
		{
			name: "foreign Mcp-Session-Id -> 404 session-not-found",
			id:   1005,
			mutate: func(req *e2e.RawRequest) {
				// Otherwise well-formed Modern request, but with a session id
				// never registered with this proxy. The 2026-07-28 draft spec
				// says Modern SHOULD ignore session id -- and the *streamable*
				// proxy does (resolveSessionForRequest mints a fresh routing
				// token unconditionally, see the crown jewel). The transparent
				// proxy deliberately deviates: here Mcp-Session-Id has
				// downstream authority -- it's looked up in the shared session
				// store, rewritten to the backend's assigned SID, and forwarded
				// to a stateful backend (see RoundTrip's guard + rewrite in
				// transparent_proxy.go). Branching that guard on the
				// client-forgeable classified revision instead of header
				// presence would be fail-open: a forged Modern _meta could
				// carry a downstream-authoritative SID past session validation.
				// Keying on presence alone is the fail-safe choice, and it is
				// the security invariant this suite exists to pin down --
				// see TestGuardUnknownSessionFiresDespiteForgedModernRevision
				// in revision_guard_regression_test.go.
				req.WithSessionID("foreign-" + uuid.NewString())
			},
			wantStatus:  http.StatusNotFound,
			wantCode:    session.CodeSessionNotFound,
			wantData:    nil,
			idNotEchoed: true,
		},
	}
}

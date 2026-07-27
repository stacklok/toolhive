// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This spec is the dual-era mixing tier of the e2e suite (issue #5837, tier
// 2): Legacy (2025-11-25, session-based) and Modern (2026-07-28, stateless)
// raw clients hit the same streamable proxy instance concurrently. This
// tier exercises concurrent mixed-era *coexistence* -- it does NOT force a
// simultaneity collision like the crown jewel (dual_era_proxy_test.go) does
// with a barrier backend. What it proves is response-body delivery
// correctness (no client ever receives another client's response body),
// bounded by the echo backend having no observable session-scoped state --
// it does not prove session-metadata isolation inside the proxy itself.
//
// Cross-delivery correlation reuses the crown jewel's mechanism
// (dual_era_proxy_test.go's extractNonce): every request, Legacy or Modern,
// carries a unique nonce in a custom _meta key (not one of the reserved
// io.modelcontextprotocol/* keys, so it never flips a Legacy request's
// classification -- see pkg/mcp/revision.go's hasModernSignal), and
// yardstick's echo tool reflects whatever _meta it received regardless of
// era. A response with the wrong nonce means this request's response body
// was delivered to (or received from) a different client.
//
// Confirmed live (thv run + curl) before writing this: Legacy initialize
// returns Mcp-Session-Id; a Legacy tools/call reusing that session id and a
// Modern tools/call both echo a custom _meta.nonce; neither ordinary
// tools/call response (Legacy or Modern) carries Mcp-Session-Id -- only
// initialize does; and a Legacy tools/call with a bogus/unregistered
// Mcp-Session-Id gets HTTP 404 with JSON-RPC -32001 "Session not found",
// proving the proxy actually enforces the session rather than merely
// minting one (see the negative probe below). So era-distinctness isn't a
// per-response header check; it's that Legacy requires and enforces a
// session id issued by initialize while Modern never sends one at all (see
// runMixedRound's assertions).

// Deliberately NOT setting "Accept: application/json, text/event-stream"
// (mcp_raw_client.go's Send doc comment floats it for a live streamable-HTTP
// endpoint): verified empirically that doing so makes THIS proxy switch to
// its SSE response path (handleSingleRequestSSE in streamable_proxy.go,
// triggered by any Accept header containing "text/event-stream"), and the
// raw client's populateEnvelope only parses a plain JSON object body, not an
// SSE-framed one -- so nonce extraction silently returns "". Without the
// header the proxy returns a plain JSON response, which is what every other
// spec in this suite (and this one) relies on.
var _ = Describe("Dual-Era Concurrent Mixing", Label("proxy", "stateless", "dual-era", "e2e"), Serial, func() {
	const (
		legacySessions = 3
		modernPerRound = 3
		rounds         = 3
		clientTimeout  = 15 * time.Second
		// joinTimeout bounds one round: comfortably above clientTimeout (the
		// longest a single request should take) so a stuck proxy fails fast
		// instead of hanging until the suite's global timeout.
		joinTimeout = clientTimeout + 10*time.Second
	)

	var (
		config     *e2e.TestConfig
		serverName string
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("dual-era-mix")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		client, err = e2e.NewRawMCPClient(clientTimeout)
		Expect(err).ToNot(HaveOccurred())

		By("Starting the yardstick backend under --transport stdio with BACKEND_MODE=echo")
		e2e.NewTHVCommand(config, "run",
			"--name", serverName,
			"--transport", "stdio",
			"-e", "BACKEND_MODE=echo",
			images.YardstickServerImage,
		).ExpectSuccess()

		err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred(), "server should be running within 60 seconds")

		proxyURL, err = e2e.GetMCPServerURL(config, serverName)
		Expect(err).ToNot(HaveOccurred(), "should be able to get proxy URL")
		if !strings.HasSuffix(proxyURL, "/mcp") {
			proxyURL += "/mcp"
		}
	})

	AfterEach(func() {
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "should be able to stop and remove server")
		}
	})

	It("mixes concurrent Legacy and Modern traffic without cross-era delivery", func() {
		// Standing gate: Legacy initialize assigns a session id (existing
		// Legacy behavioral assertion, not a byte-for-byte golden -- no
		// committed Legacy golden fixture exists to diff against; see
		// mcp_raw_client_golden_test.go, which only covers Modern).
		sessionIDs := make([]string, legacySessions)
		for i := range sessionIDs {
			initReq := e2e.NewLegacyInitializeRequest(fmt.Sprintf("legacy-client-%d", i), "1.0")
			resp, err := client.Send(context.Background(), proxyURL, initReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200))
			sessionIDs[i] = resp.Headers.Get(e2e.HeaderMCPSessionID)
			Expect(sessionIDs[i]).ToNot(BeEmpty(), "initialize must assign a session id")
		}

		// Negative probe: Legacy must actually ENFORCE the session, not just
		// mint one. The echo tool is session-agnostic, so a Legacy path that
		// silently degraded to stateless would still echo correctly and this
		// suite's conservation checks alone wouldn't catch it. Confirmed live
		// (thv run + curl) before writing this: a bogus/unregistered session
		// id gets HTTP 404, JSON-RPC -32001 "Session not found".
		bogusReq, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "shouldneverrun"},
		})
		Expect(err).ToNot(HaveOccurred())
		bogusReq.WithSessionID("bogus-" + uuid.NewString())
		resp, err := client.Send(context.Background(), proxyURL, bogusReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).ToNot(Equal(200), "a bogus/unregistered Mcp-Session-Id must be rejected")
		Expect(resp.Error).ToNot(BeNil(), "rejection must be a JSON-RPC error")

		for round := 0; round < rounds; round++ {
			runMixedRound(client, proxyURL, round, sessionIDs, modernPerRound, joinTimeout)
		}
	})
})

// mixResult is one goroutine's outcome within a single mixed round.
type mixResult struct {
	era        string // "legacy" or "modern", for failure messages
	nonce      string
	gotNonce   string
	err        error
	statusCode int
	rpcErr     *e2e.RawRPCError
	sessionHdr string // response's Mcp-Session-Id header, if any
	start, end time.Time
}

// runMixedRound fires len(sessionIDs) Legacy requests (each reusing its own
// already-initialized session) and modernPerRound stateless Modern requests
// concurrently -- a start channel releases every goroutine together, so both
// eras are genuinely in flight at once, not sequential per era (the crown
// jewel's anti-theater fire pattern). Every request carries a unique nonce
// in _meta; a wrong echoed nonce is a cross-era or cross-client leak.
func runMixedRound(
	client *e2e.RawMCPClient,
	proxyURL string,
	round int,
	sessionIDs []string,
	modernPerRound int,
	joinTimeout time.Duration,
) {
	total := len(sessionIDs) + modernPerRound
	results := make([]mixResult, total)
	var wg sync.WaitGroup
	start := make(chan struct{})

	// Derived from joinTimeout (not context.Background()) so a stuck round
	// tears down its in-flight requests promptly, mirroring the crown jewel.
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()

	fire := func(i int, era string, build func() (*e2e.RawRequest, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nonce := fmt.Sprintf("round-%d-%s-%d", round, era, i)
			<-start

			req, err := build()
			if err != nil {
				results[i] = mixResult{era: era, nonce: nonce, err: err}
				return
			}
			req.SetMeta("nonce", nonce)

			reqStart := time.Now()
			resp, sendErr := client.Send(ctx, proxyURL, req)
			reqEnd := time.Now()
			if sendErr != nil {
				results[i] = mixResult{era: era, nonce: nonce, err: sendErr, start: reqStart, end: reqEnd}
				return
			}
			results[i] = mixResult{
				era:        era,
				nonce:      nonce,
				statusCode: resp.StatusCode,
				rpcErr:     resp.Error,
				gotNonce:   extractNonce(resp),
				sessionHdr: resp.Headers.Get(e2e.HeaderMCPSessionID),
				start:      reqStart,
				end:        reqEnd,
			}
		}()
	}

	for i, sid := range sessionIDs {
		sid := sid
		fire(i, "legacy", func() (*e2e.RawRequest, error) {
			req, err := e2e.NewLegacyRequest("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"input": "mixcheck"},
			})
			if err != nil {
				return nil, err
			}
			return req.WithSessionID(sid), nil
		})
	}
	for i := 0; i < modernPerRound; i++ {
		fire(len(sessionIDs)+i, "modern", func() (*e2e.RawRequest, error) {
			return e2e.NewModernRequest("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"input": "mixcheck"},
			})
		})
	}

	close(start) // release every goroutine (both eras) together

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(joinTimeout):
		Fail(fmt.Sprintf("round %d: timed out waiting for %d mixed requests to finish", round, total))
	}

	distinctNonces := map[string]bool{}
	var legacyOverlapsModern bool
	for i, r := range results {
		ExpectWithOffset(1, r.err).ToNot(HaveOccurred(), "round %d %s request %d", round, r.era, i)
		ExpectWithOffset(1, r.rpcErr).To(BeNil(), "round %d %s request %d: %+v", round, r.era, i, r.rpcErr)
		ExpectWithOffset(1, r.statusCode).To(Equal(200), "round %d %s request %d", round, r.era, i)
		ExpectWithOffset(1, r.gotNonce).To(Equal(r.nonce),
			"round %d %s request %d: echoed nonce must match -- a mismatch is a cross-era/cross-client leak",
			round, r.era, i)
		if r.era == "modern" {
			ExpectWithOffset(1, r.sessionHdr).To(BeEmpty(),
				"round %d modern request %d: stateless Modern must never carry Mcp-Session-Id", round, i)
		}
		distinctNonces[r.gotNonce] = true

		// Anti-theater: both eras must have genuinely overlapped in flight at
		// least once this round, not fired sequentially era-by-era.
		for _, other := range results {
			if r.era != other.era && r.start.Before(other.end) && other.start.Before(r.end) {
				legacyOverlapsModern = true
			}
		}
	}
	ExpectWithOffset(1, distinctNonces).To(HaveLen(total), "round %d: must see %d distinct correlated nonces", round, total)
	ExpectWithOffset(1, legacyOverlapsModern).To(BeTrue(),
		"round %d: no Legacy request overlapped in time with a Modern request -- "+
			"this round fired the eras sequentially instead of concurrently", round)
}

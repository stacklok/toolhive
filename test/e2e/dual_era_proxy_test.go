// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This spec is the crown-jewel regression test for the streamable HTTP
// proxy's per-request routing (pkg/transport/proxy/streamable): a Modern
// (2026-07-28) request always gets a fresh UUID routing token and never
// falls through to the client-supplied Mcp-Session-Id, even when many
// concurrent Modern requests share the same (session, id) pair. If that
// guarantee regresses, one client's response can be delivered to another
// client waiting on the identical composite key -- a confidentiality bug,
// see resolveSessionForRequest's doc comment in streamable_proxy.go.
//
// yardstick-server's barrier mode (BACKEND_MODE=barrier) buffers BARRIER_N
// non-lifecycle requests and releases all their responses at once, forcing
// BARRIER_N requests to be simultaneously in-flight at the proxy -- the
// precondition the leak needs.
//
// This spec loops internally (rounds), and CI additionally runs it repeated
// (ginkgo --repeat / go test -count) plus nightly, since a race window this
// narrow does not reliably reproduce on a single run.
var _ = Describe("Dual-Era Cross-Delivery Proxy", Label("proxy", "stateless", "dual-era", "e2e"), Serial, func() {
	const (
		barrierN       = 8
		rounds         = 20
		barrierTimeout = 3 * time.Second
		// clientTimeout bounds a single HTTP round trip: comfortably above
		// barrierTimeout so a legitimate barrier release is never mistaken
		// for a stuck proxy, but still fails the test (instead of the
		// suite's global timeout) if the proxy hangs.
		clientTimeout = barrierTimeout + 10*time.Second
		// wellBeforeBarrierTimeout bounds how long any single response may
		// take. Barrier mode's safety valve flushes a *partial* buffer on
		// starvation, so "N responses came back" alone doesn't prove the
		// barrier filled to N -- a response arriving near/after
		// barrierTimeout means the round never actually forced all N
		// requests to collide, and must fail rather than pass.
		wellBeforeBarrierTimeout = barrierTimeout / 2

		// All requests in a round deliberately share this session and id --
		// the exact leak precondition -- and differ only by nonce.
		crossDeliverySessionID = "cross-delivery-probe"
		crossDeliveryRequestID = int64(1)
	)

	var (
		config     *e2e.TestConfig
		serverName string
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("dual-era")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		client, err = e2e.NewRawMCPClient(clientTimeout)
		Expect(err).ToNot(HaveOccurred())

		By("Starting the yardstick backend under --transport stdio with BACKEND_MODE=barrier")
		e2e.NewTHVCommand(config, "run",
			"--name", serverName,
			"--transport", "stdio",
			"-e", "BACKEND_MODE=barrier",
			"-e", fmt.Sprintf("BARRIER_N=%d", barrierN),
			"-e", fmt.Sprintf("BARRIER_TIMEOUT_SECONDS=%d", int(barrierTimeout.Seconds())),
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

	It("never delivers one client's response to another under forced request collisions", func() {
		// Positive control, run once: barrierN-1 requests can never fill the
		// barrier, so they must be held until BARRIER_TIMEOUT_SECONDS's safety
		// flush (~barrierTimeout), not answered immediately. Without this, a
		// barrier that silently degraded into a no-op (answering every request
		// right away) would be indistinguishable from a real barrier: both
		// produce all-N-fast in the rounds below, so the crown jewel would stay
		// green while forcing zero collisions.
		By("Verifying the barrier actually withholds a short-of-N batch until its safety timeout")
		results := fireCrossDeliveryBatch(client, proxyURL, "control", barrierN-1, barrierTimeout,
			crossDeliverySessionID, crossDeliveryRequestID)
		assertCorrelated(results, barrierN-1, "control")
		for i, r := range results {
			ExpectWithOffset(1, r.elapsed).To(BeNumerically(">=", wellBeforeBarrierTimeout),
				"control request %d: returned too fast (%v) for a %d-of-%d batch -- the barrier is not "+
					"withholding responses below N, so it cannot be trusted to force the collision the rounds below rely on",
				i, r.elapsed, barrierN-1, barrierN)
		}

		for round := 0; round < rounds; round++ {
			label := fmt.Sprintf("round-%d", round)
			results := fireCrossDeliveryBatch(client, proxyURL, label, barrierN, barrierTimeout,
				crossDeliverySessionID, crossDeliveryRequestID)
			assertCorrelated(results, barrierN, label)
			for i, r := range results {
				ExpectWithOffset(1, r.elapsed).To(BeNumerically("<", wellBeforeBarrierTimeout),
					"%s request %d: response arrived too close to BARRIER_TIMEOUT_SECONDS (%v) -- the barrier "+
						"likely flushed a partial buffer via its safety timeout instead of filling to N, meaning this "+
						"round never forced the collision", label, i, barrierTimeout)
			}
		}
	})
})

// crossDeliveryResult is one goroutine's outcome within a single barrier batch.
type crossDeliveryResult struct {
	nonce      string // the nonce this goroutine sent
	err        error
	statusCode int
	elapsed    time.Duration
	rpcErr     *e2e.RawRPCError
	gotNonce   string // the nonce reflected back in the response, if any
}

// fireCrossDeliveryBatch fires count concurrent Modern tools/call requests --
// all sharing the same foreign Mcp-Session-Id and JSON-RPC id, differing only
// by a per-request nonce carried in _meta -- and returns each goroutine's
// outcome, indexed by request number. label distinguishes nonces/failure
// messages across batches (a round number, or "control").
func fireCrossDeliveryBatch(
	client *e2e.RawMCPClient,
	proxyURL, label string,
	count int,
	barrierTimeout time.Duration,
	sessionID string,
	requestID int64,
) []crossDeliveryResult {
	// joinTimeout bounds the whole batch: comfortably above barrierTimeout (the
	// longest a request should ever be held) so a stuck proxy fails fast
	// instead of hanging until the suite's global timeout.
	joinTimeout := barrierTimeout + 15*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()

	results := make([]crossDeliveryResult, count)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nonce := fmt.Sprintf("%s-req-%d", label, i)
			<-start // released together -- sequential firing deadlocks the barrier

			req, err := e2e.NewModernRequest("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"input": "crossdelivery"},
			})
			if err != nil {
				results[i] = crossDeliveryResult{nonce: nonce, err: err}
				return
			}
			req.WithID(requestID).WithSessionID(sessionID).SetMeta("nonce", nonce)

			reqStart := time.Now()
			resp, sendErr := client.Send(ctx, proxyURL, req)
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
		}(i)
	}

	close(start) // fire all requests concurrently

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(joinTimeout):
		Fail(fmt.Sprintf("%s: timed out waiting for %d goroutines to finish", label, count))
	}

	return results
}

// assertCorrelated checks the universal cross-delivery invariants for a batch:
// every request succeeded with no error/JSON-RPC error/non-200, and every
// response's nonce matches the request that goroutine sent (a mismatch is a
// cross-delivery leak), with exactly wantCount distinct nonces observed.
func assertCorrelated(results []crossDeliveryResult, wantCount int, label string) {
	distinctNonces := map[string]bool{}
	for i, r := range results {
		ExpectWithOffset(2, r.err).ToNot(HaveOccurred(),
			"%s request %d: must not error or time out", label, i)
		ExpectWithOffset(2, r.rpcErr).To(BeNil(),
			"%s request %d: must not get a JSON-RPC error: %+v", label, i, r.rpcErr)
		ExpectWithOffset(2, r.statusCode).To(Equal(http.StatusOK),
			"%s request %d: must return HTTP 200", label, i)
		ExpectWithOffset(2, r.gotNonce).To(Equal(r.nonce),
			"%s request %d: response nonce must match the request this goroutine sent -- "+
				"a mismatch is a cross-delivery leak", label, i)
		distinctNonces[r.gotNonce] = true
	}
	ExpectWithOffset(2, distinctNonces).To(HaveLen(wantCount),
		"%s: must see exactly %d distinct correlated nonces", label, wantCount)
}

// extractNonce pulls the echoed "nonce" key out of a tools/call response's
// result._meta, per yardstick-server's echo tool: "Also echoes back any
// _meta field from the request for testing metadata propagation." Returns
// "" if absent or unparseable, which the caller's nonce-equality assertion
// then reports as a mismatch.
func extractNonce(resp *e2e.RawResponse) string {
	if resp.Result == nil {
		return ""
	}
	var result struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return ""
	}
	nonce, _ := result.Meta["nonce"].(string)
	return nonce
}

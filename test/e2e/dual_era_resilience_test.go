// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
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

// This spec is the fault-injection tier of the e2e suite (issue #5837,
// Commit 3, Step 3.1): the streamable proxy must degrade gracefully when its
// backend hangs or crashes mid-request -- a clean bounded error to the
// caller, no proxy-wide hang, and other traffic unaffected.
//
// Confirmed live before writing this (yardstick-server 1.2.0's
// BACKEND_MODE=hang/crash, via a real `thv run` instance):
//
//   - Hang: HANG_AFTER_N counts requests; the Nth one never gets a backend
//     response. The proxy's own TOOLHIVE_PROXY_REQUEST_TIMEOUT bounds it (a
//     plain-text 504 "Timeout waiting for response from container" via
//     writeHTTPError -- not a JSON-RPC body, so RawResponse.Error stays nil;
//     assert on StatusCode instead). Concurrent unrelated requests are NOT
//     blocked while the hung one waits (verified: a concurrent request
//     completed in 14ms while the hung one was still waiting on its own
//     goroutine, and the hung one's later 504 arrived on schedule). The
//     backend process itself survives a hang -- no restart, a request sent
//     afterward succeeds immediately.
//   - Crash: the in-flight request at CRASH_AFTER_N gets the same clean
//     writeHTTPError response (a 5xx -- observed 504, "server error" is the
//     load-bearing property, not the exact code) well within a few seconds -- the
//     stdio transport detects the closed pipe faster than any configured
//     timeout, it doesn't wait for TOOLHIVE_PROXY_REQUEST_TIMEOUT to elapse.
//     Recovery is real (`pkg/workloads/manager.go`'s RunWorkload retries
//     with backoff) but SLOW and layered in this environment: Docker's own
//     restart policy fires first (~13s), races a doomed thv re-attach
//     (409 conflict, observed), then thv's own workload-level restart
//     kicks in only after re-attach gives up (~48s total in one measured
//     run) -- and the proxy port is unreachable for the whole window. That
//     is far outside what a per-spec e2e timeout should eat, and getting it
//     reliably non-flaky would mean re-resolving the proxy URL and polling
//     through repeated container recreation, which is disproportionate to
//     this step. Per the plan's explicit either/or, this spec asserts only
//     the clean-error window for crash, not full recovery -- documented
//     here as a deliberate scope decision, not an oversight.
//
// Redis-unavailable -> 503 (case 3) is OUT OF SCOPE for this file: `thv run`
// has NO CLI flag exposing Redis-backed session storage at all --
// RunConfig.ScalingConfig.SessionRedis (pkg/runner/config.go) is set only via
// a hand-built RunConfig (e.g. `--from-config`) or the Kubernetes operator;
// there is no `thv run --session-storage redis` equivalent to stand up in a
// local e2e test. Standing this up would mean hand-authoring a full
// RunConfig JSON with no existing local-e2e precedent (the one Redis
// fixture in this suite, vmcp_infra_features_test.go, wires it into `thv
// vmcp serve`, a different proxy entirely). Flagging per the plan's
// "STOP and report" clause rather than building something disproportionate.
var _ = Describe("Dual-Era Resilience — Fault Injection", Label("proxy", "stateless", "dual-era", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	AfterEach(func() {
		if config == nil || !config.CleanupAfter {
			return
		}
		// The crash spec's backend may still be mid Docker-restart / thv
		// re-attach (see the file doc comment -- ~48s, one measured run) when
		// this fires; retry rather than hard-failing the spec on a transient
		// "container is restarting" error masking the real test result.
		Eventually(func() error {
			return e2e.StopAndRemoveMCPServer(config, serverName)
		}, 60*time.Second, 2*time.Second).Should(Succeed(), "should be able to stop and remove server")
	})

	startFaultBackend := func(mode string, extraEnv ...string) {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("resilience-" + mode)

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		client, err = e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		args := []string{
			"run",
			"--name", serverName,
			"--transport", "stdio",
			"-e", "BACKEND_MODE=" + mode,
		}
		for _, e := range extraEnv {
			args = append(args, "-e", e)
		}
		args = append(args, images.YardstickServerImage)

		// Bound the request timeout so a hang fails fast in the test, not
		// after the streamable proxy's 60s default.
		e2e.NewTHVCommand(config, args...).WithEnv("TOOLHIVE_PROXY_REQUEST_TIMEOUT=5s").ExpectSuccess()

		err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred(), "server should be running within 60 seconds")

		proxyURL, err = e2e.GetMCPServerURL(config, serverName)
		Expect(err).ToNot(HaveOccurred(), "should be able to get proxy URL")
		if !strings.HasSuffix(proxyURL, "/mcp") {
			proxyURL += "/mcp"
		}
	}

	echoRequest := func(input string) *e2e.RawRequest {
		req, err := e2e.NewModernRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": input},
		})
		Expect(err).ToNot(HaveOccurred())
		return req
	}

	Context("backend hang", func() {
		BeforeEach(func() {
			// The 2nd request hangs; the 1st is a healthy baseline.
			startFaultBackend("hang", "HANG_AFTER_N=2")
		})

		It("isolates a hung request from concurrent traffic, and the backend stays usable", func() {
			By("a baseline request succeeds")
			resp, err := client.Send(context.Background(), proxyURL, echoRequest("baseline"))
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("firing the hang-triggering request concurrently with an unrelated one")
			var wg sync.WaitGroup
			var hungErr, normalErr error
			var hungStatus, normalStatus int
			var hungElapsed, normalElapsed time.Duration

			// Deterministic ordering: launch the hang-trigger first and give
			// it a head start to actually reach the backend as request #2,
			// THEN launch "normal" -- racing both from a shared start gate
			// left which request HANG_AFTER_N=2 counted as a coin flip (MoE
			// flake finding). "normal" still overlaps the hang in time (fired
			// while the hang-trigger is still in flight), which is exactly
			// the isolation property under test.
			wg.Add(1)
			go func() {
				defer wg.Done()
				t0 := time.Now()
				r, sendErr := client.Send(context.Background(), proxyURL, echoRequest("hangtrigger"))
				hungElapsed = time.Since(t0)
				hungErr = sendErr
				if r != nil {
					hungStatus = r.StatusCode
				}
			}()
			time.Sleep(300 * time.Millisecond)

			wg.Add(1)
			go func() {
				defer wg.Done()
				t0 := time.Now()
				r, sendErr := client.Send(context.Background(), proxyURL, echoRequest("normal"))
				normalElapsed = time.Since(t0)
				normalErr = sendErr
				if r != nil {
					normalStatus = r.StatusCode
				}
			}()

			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				Fail("timed out waiting for concurrent hang/normal requests to finish")
			}

			Expect(hungErr).ToNot(HaveOccurred())
			Expect(normalErr).ToNot(HaveOccurred())
			Expect(normalStatus).To(Equal(http.StatusOK), "the unrelated concurrent request must succeed")
			Expect(normalElapsed).To(BeNumerically("<", 5*time.Second),
				"the unrelated request must not be blocked behind the hung one")
			Expect(hungStatus).To(Equal(http.StatusGatewayTimeout), "the hung request must get a clean bounded error")
			Expect(hungElapsed).To(BeNumerically(">=", 4*time.Second),
				"sanity: the hang must actually have been waited out by the proxy timeout, not failed for some other reason")

			By("Legacy recovers: a fresh initialize + call after the hang still works")
			initReq := e2e.NewLegacyInitializeRequest("resilience-client", "1.0")
			initResp, err := client.Send(context.Background(), proxyURL, initReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(initResp.StatusCode).To(Equal(http.StatusOK))
			sessionID := initResp.Headers.Get(e2e.HeaderMCPSessionID)
			Expect(sessionID).ToNot(BeEmpty())

			legacyReq, err := e2e.NewLegacyRequest("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"input": "postH"},
			})
			Expect(err).ToNot(HaveOccurred())
			legacyReq.WithSessionID(sessionID)
			legacyResp, err := client.Send(context.Background(), proxyURL, legacyReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(legacyResp.StatusCode).To(Equal(http.StatusOK))
			Expect(legacyResp.Error).To(BeNil())
		})
	})

	Context("backend crash", func() {
		BeforeEach(func() {
			// The 2nd request crashes the backend; the 1st is a healthy baseline.
			startFaultBackend("crash", "CRASH_AFTER_N=2")
		})

		It("returns a clean error for the crashing request instead of hanging or panicking the proxy", func() {
			By("a baseline request succeeds")
			resp, err := client.Send(context.Background(), proxyURL, echoRequest("baseline"))
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("the crash-triggering request gets a clean, bounded error")
			t0 := time.Now()
			crashResp, err := client.Send(context.Background(), proxyURL, echoRequest("crashtrigger"))
			elapsed := time.Since(t0)
			Expect(err).ToNot(HaveOccurred(), "the client must get an HTTP response, not a connection error/hang")
			Expect(crashResp.StatusCode).To(BeNumerically(">=", 500), "a crash must surface as a server error, not a silent 200")
			// 10s: comfortably above the observed ~3-5s crash-detection, far below
			// the ~48s full-recovery latency -- tight enough to actually fail if
			// crash-handling regressed to riding out recovery instead of detecting
			// the closed pipe. (The 15s client.Send timeout would fire first and
			// fail the Expect above, so bounding by that alone is a tautology.)
			Expect(elapsed).To(BeNumerically("<", 10*time.Second),
				fmt.Sprintf("must fail fast (took %s) -- a crash must not ride out recovery", elapsed))
		})
	})
})

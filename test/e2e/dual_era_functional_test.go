// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This spec is the Tier-1 functional round-trip tier of the e2e suite (issue
// #5837, Commit 2). Modern (2026-07-28) fidelity is driven by the real,
// independent SDK client (yardstick-client) against the streamable proxy --
// call-tool succeeds and -action info confirms it actually negotiated
// 2026-07-28 with an empty session id, not a silent downgrade -- plus one
// raw-client wire check against the transparent proxy for the
// no-Mcp-Session-Id property (that proxy's own, distinct Modern code path).
// Legacy (2025-11-25) is yardstick-client negotiating down: per Step 2.1's
// discovery, yardstick-client emits genuine Modern traffic on its own
// (server/discover, no initialize) whenever the backend's own discover
// response advertises 2026-07-28 support, so the Legacy leg deliberately
// uses a NON-stateless yardstick-server (its own StreamableHTTPOptions.Stateless
// is false), whose discover response omits 2026-07-28, forcing the SDK
// client to fall back to a real initialize handshake -- confirmed live
// before writing this (see the two legs below).

// execTimeout bounds go install and every yardstick-client invocation below,
// so a wedged module proxy or stuck client fails fast instead of riding out
// the suite's global timeout.
const execTimeout = 90 * time.Second

// yardstickClientVersion is derived from images.YardstickServerImage's tag
// (the part after ":") so a future image bump can't silently pair a
// mismatched go-sdk version between yardstick-server and yardstick-client.
var yardstickClientVersion = "v" + strings.SplitN(images.YardstickServerImage, ":", 2)[1]

var (
	yardstickClientOnce sync.Once
	yardstickClientPath string
	yardstickClientErr  error
)

// ensureYardstickClientBinary go-installs the real yardstick-client module
// (github.com/stackloklabs/yardstick, a separate Go module -- this does NOT
// add go-sdk v1.7 to this repo's go.mod) into a scratch GOBIN, once per test
// run, and returns its path.
//
// ponytail: the scratch dir is not removed -- it's shared (via sync.Once)
// across every spec in this file, so a DeferCleanup tied to whichever spec
// happens to trigger the install first would delete it out from under later
// specs. One leaked temp dir per suite run is a cheap tradeoff; the OS temp
// dir is reaped independently. Upgrade to a suite-level AfterSuite removal
// if that ever stops being true (e.g. a long-lived CI temp volume).
func ensureYardstickClientBinary() (string, error) {
	yardstickClientOnce.Do(func() {
		dir, err := os.MkdirTemp("", "yardstick-client-")
		if err != nil {
			yardstickClientErr = fmt.Errorf("create scratch GOBIN: %w", err)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "install",
			"github.com/stackloklabs/yardstick/cmd/yardstick-client@"+yardstickClientVersion)
		cmd.Env = append(os.Environ(), "GOBIN="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			yardstickClientErr = fmt.Errorf("go install yardstick-client@%s: %w: %s", yardstickClientVersion, err, out)
			return
		}
		yardstickClientPath = filepath.Join(dir, "yardstick-client")
	})
	return yardstickClientPath, yardstickClientErr
}

// runYardstickClient runs yardstick-client with a bounded timeout and returns
// its combined output.
func runYardstickClient(clientPath string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	//nolint:gosec // fixed action/flags, test-controlled address/port
	out, err := exec.CommandContext(ctx, clientPath, args...).CombinedOutput()
	return string(out), err
}

// proxyPort extracts the port from a proxy URL like "http://127.0.0.1:PORT/mcp".
func proxyPort(proxyURL string) string {
	rest := strings.TrimPrefix(proxyURL, "http://")
	hostPort := strings.SplitN(rest, "/", 2)[0]
	return strings.SplitN(hostPort, ":", 2)[1]
}

// sessionIDFromInfo parses yardstick-client's `-action info` output and
// returns the value of its "Session ID: " line (empty for Modern, a real id
// for Legacy). Parsing the value, rather than substring-matching the whole
// line, survives a trailing space or relabel and can't pass vacuously.
func sessionIDFromInfo(infoOutput string) string {
	for _, line := range strings.Split(infoOutput, "\n") {
		if rest, ok := strings.CutPrefix(line, "  Session ID:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

var _ = Describe("Modern Functional Round-Trip — Streamable Proxy", Label("proxy", "stateless", "dual-era", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("func-streamable")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		client, err = e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

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

	It("round-trips through a real independent SDK client (yardstick-client) as Modern", func() {
		// Wire-level "no Mcp-Session-Id for Modern" is already covered by the
		// crown jewel + mixing specs; this proves functional interop with a
		// real go-sdk v1.7 client instead of re-asserting that.
		clientPath, err := ensureYardstickClientBinary()
		Expect(err).ToNot(HaveOccurred(), "yardstick-client must be installable")
		port := proxyPort(proxyURL)

		out, err := runYardstickClient(clientPath,
			"-transport", "streamable-http", "-address", "127.0.0.1", "-port", port,
			"-action", "call-tool", "-tool", "echo", "-args", `{"input":"modernfunctional"}`,
		)
		Expect(err).ToNot(HaveOccurred(), "yardstick-client call-tool should succeed: %s", out)
		Expect(out).To(ContainSubstring("modernfunctional"))

		// This backend's own transport is stdio (unaffected by any Stateless
		// gating -- see the file doc comment), so yardstick-client's discover
		// negotiates Modern here. info confirms it, not a silent downgrade.
		infoOut, err := runYardstickClient(clientPath,
			"-transport", "streamable-http", "-address", "127.0.0.1", "-port", port, "-action", "info",
		)
		Expect(err).ToNot(HaveOccurred(), "yardstick-client info should succeed: %s", infoOut)
		Expect(infoOut).To(ContainSubstring("Protocol Version: 2026-07-28"))
		Expect(sessionIDFromInfo(infoOut)).To(BeEmpty(), "Modern must have no session id (inverse of the Legacy leg)")
	})

	It("reaches server/discover (no authz configured, so it is NOT 403-denied)", func() {
		// The design plan's ground truth said server/discover "currently
		// default-denies (403) through authz" -- verified at Step 2.1 that
		// this only holds when authz middleware is actually wired, which
		// only happens when RunConfig.AuthzConfig != nil
		// (pkg/runner/middleware.go:210). A bare `thv run` like this
		// BeforeEach never enables it, so server/discover reaches the
		// backend and succeeds. See pkg/authz/middleware.go's comment on
		// why server/discover isn't allow-listed for when authz IS on.
		req, err := e2e.NewModernRequest("server/discover", nil)
		Expect(err).ToNot(HaveOccurred())

		resp, err := client.Send(context.Background(), proxyURL, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.Error).To(BeNil())
	})
})

// No second yardstick-client leg here: SDK negotiation (discover/initialize)
// is backend-side logic, identical regardless of which ToolHive proxy fronts
// it -- the streamable-proxy leg above already proves it end-to-end. This
// spec keeps the raw-client no-session-id check, which does exercise the
// transparent proxy's own (distinct) Modern code path.
var _ = Describe("Modern Functional Round-Trip — Transparent Proxy", Label("proxy", "stateless", "dual-era", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		mockServer *statelessMockMCPServer
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("func-transparent")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		mockServer, err = newStatelessMockMCPServer()
		Expect(err).ToNot(HaveOccurred())

		client, err = e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		e2e.NewTHVCommand(config, "run", "--name", serverName, "--stateless", mockServer.URL()+"/mcp").ExpectSuccess()

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

	It("round-trips a Modern request with no Mcp-Session-Id", func() {
		req, err := e2e.NewModernRequest("tools/list", nil)
		Expect(err).ToNot(HaveOccurred())

		resp, err := client.Send(context.Background(), proxyURL, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.Error).To(BeNil())
		Expect(resp.Headers.Get(e2e.HeaderMCPSessionID)).To(BeEmpty(), "Modern must never carry Mcp-Session-Id")
	})
})

var _ = Describe("Legacy Functional Round-Trip — real SDK client (yardstick-client)", Label("proxy", "stateless", "dual-era", "e2e"), Serial, func() {
	var (
		config      *e2e.TestConfig
		backendName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		backendName = e2e.GenerateUniqueServerName("func-legacy-backend")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Deliberately NOT stateless: yardstick-server's own discover response
		// then omits 2026-07-28 (StreamableHTTPOptions.Stateless gates it --
		// confirmed live), so yardstick-client's own SDK negotiation falls
		// back to a real Legacy initialize handshake. --group "default" is
		// the always-present built-in group; no group create/cleanup needed.
		startYardstickOnPort(config, "default", backendName, allocateVMCPPort())
	})

	AfterEach(func() {
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, backendName)
			Expect(err).ToNot(HaveOccurred(), "should be able to stop and remove server")
		}
	})

	It("completes a Legacy tool call end-to-end through the transparent proxy", func() {
		clientPath, err := ensureYardstickClientBinary()
		Expect(err).ToNot(HaveOccurred(), "yardstick-client must be installable")

		proxyURL, err := e2e.GetMCPServerURL(config, backendName)
		Expect(err).ToNot(HaveOccurred())
		port := proxyPort(proxyURL)

		out, err := runYardstickClient(clientPath,
			"-transport", "streamable-http",
			"-address", "127.0.0.1",
			"-port", port,
			"-action", "call-tool",
			"-tool", "echo",
			"-args", `{"input":"legacyfunctional"}`,
		)
		Expect(err).ToNot(HaveOccurred(), "yardstick-client call-tool should succeed: %s", out)
		Expect(out).To(ContainSubstring("legacyfunctional"))

		// info action reports the negotiated protocol version and session id;
		// confirms this really was the Legacy leg, not a silent Modern upgrade.
		infoOut, err := runYardstickClient(clientPath,
			"-transport", "streamable-http", "-address", "127.0.0.1", "-port", port, "-action", "info")
		Expect(err).ToNot(HaveOccurred(), "yardstick-client info should succeed: %s", infoOut)
		Expect(infoOut).To(ContainSubstring("Protocol Version: 2025-11-25"))
		Expect(sessionIDFromInfo(infoOut)).ToNot(BeEmpty(), "Legacy must have a real session id")
	})
})

// server/discover is deliberately excluded from pkg/authz/middleware.go's
// MCPMethodToFeatureOperation map (see its comment at middleware.go:57-61):
// discover's response enumerates every tool/resource descriptor and would
// bypass ResponseFilteringWriter (which only filters tools/list, prompts/list,
// resources/list, and find_tool) -- a fail-safe deny, not an oversight.
// Consequence (known "for now" gap, #5830): a real Modern client (like
// yardstick-client) that hits this 403 has no JSON-RPC "unsupported version"
// data to parse, so its own fallback-on-non-modern-error path kicks in and it
// silently downgrades to a Legacy 2025-11-25 initialize handshake instead of
// surfacing the denial -- which is why this guard uses the raw client, not
// yardstick-client, to actually observe the 403.
var _ = Describe("server/discover Authorization Guard", Label("proxy", "stateless", "dual-era", "e2e", "authz"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		client     *e2e.RawMCPClient
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("func-discover-authz")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		client, err = e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		// Minimal Cedar policy allowing tools/call+tools/list (so the backend
		// is otherwise usable) -- mirrors osv_authz_test.go's pattern.
		authzConfig := `{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": [
      "permit(principal, action == Action::\"call_tool\", resource == Tool::\"echo\");",
      "permit(principal, action == Action::\"list_tools\", resource);"
    ],
    "entities_json": "[]"
  }
}`
		tempDir, err := os.MkdirTemp("", "func-discover-authz")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(tempDir) })
		authzConfigPath := filepath.Join(tempDir, "authz-config.json")
		Expect(os.WriteFile(authzConfigPath, []byte(authzConfig), 0600)).To(Succeed())

		e2e.NewTHVCommand(config, "run",
			"--name", serverName,
			"--transport", "stdio",
			"-e", "BACKEND_MODE=echo",
			"--authz-config", authzConfigPath,
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

	It("denies server/discover with 403 when authz is enabled", func() {
		req, err := e2e.NewModernRequest("server/discover", nil)
		Expect(err).ToNot(HaveOccurred())

		resp, err := client.Send(context.Background(), proxyURL, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(403))
		Expect(resp.Error).ToNot(BeNil())
		// The denial envelope is currently non-conformant JSON-RPC (capitalized
		// Result/Error/ID, no "jsonrpc":"2.0" -- tracked in #5950); this parses
		// only because encoding/json's unmarshal is case-insensitive. That's the
		// envelope's job to fix, not this guard's -- here we're asserting authz
		// *behavior* (denied, code 403), not wire conformance.
		Expect(resp.Error.Code).To(Equal(int64(403)))

		By("confirming authz still allows the policy-permitted echo tool call")
		callReq, err := e2e.NewModernRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "stillworks"},
		})
		Expect(err).ToNot(HaveOccurred())
		callResp, err := client.Send(context.Background(), proxyURL, callReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(callResp.StatusCode).To(Equal(200))
		Expect(callResp.Error).To(BeNil())
	})
})

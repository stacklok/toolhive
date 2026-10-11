// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// These tests verify that the HTTP proxy rejects a request carrying a body on any
// method other than POST. MCP transports send JSON-RPC messages only in POST
// bodies, so such a request is never produced by a conforming client; the proxy
// must refuse it rather than forward it unparsed.
var _ = Describe("Non-POST method handling", Label("middleware", "sse", "e2e"), Serial, func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Running an MCP server behind the proxy", Ordered, func() {
		var serverName string
		var mcpEndpoint string

		BeforeAll(func() {
			serverName = e2e.GenerateUniqueServerName("method-gate-test")

			e2e.NewTHVCommand(config, "run",
				"--name", serverName,
				"--transport", "sse",
				"osv").ExpectSuccess()

			err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			serverURL, err := e2e.GetMCPServerURL(config, serverName)
			Expect(err).ToNot(HaveOccurred())

			err = e2e.WaitForMCPServerReady(config, serverURL, "sse", 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			// The parser middleware wraps every path the proxy serves, so any path on
			// the proxy origin exercises it. Derive the origin from the advertised URL.
			parsed, perr := url.Parse(strings.SplitN(serverURL, "#", 2)[0])
			Expect(perr).ToNot(HaveOccurred())
			mcpEndpoint = parsed.Scheme + "://" + parsed.Host + "/messages"
		})

		AfterAll(func() {
			if config.CleanupAfter {
				err := e2e.StopAndRemoveMCPServer(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
			}
		})

		DescribeTable("rejects a JSON body on a non-POST method",
			func(method string) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()

				body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_vulnerability","arguments":{}}}`
				req, err := http.NewRequestWithContext(ctx, method, mcpEndpoint, strings.NewReader(body))
				Expect(err).ToNot(HaveOccurred())
				req.Header.Set("Content-Type", "application/json")

				resp, err := http.DefaultClient.Do(req)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()
				_, _ = io.Copy(io.Discard, resp.Body)

				Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed),
					"method %q carrying a body must be rejected", method)
				Expect(resp.Header.Get("Allow")).To(Equal("GET, POST, DELETE"))
			},
			Entry("lower-case post", "post"),
			Entry("mixed-case Post", "Post"),
			Entry("PUT", http.MethodPut),
			Entry("PATCH", http.MethodPatch),
		)
	})
})

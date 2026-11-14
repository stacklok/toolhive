// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

const (
	cliProxyReadTimeout   = time.Second
	slowUploadChunkDelay  = 250 * time.Millisecond
	slowUploadBodyBytes   = 20
	slowUploadMaxDuration = 4 * time.Second
)

// cliSlowUploadBody takes five seconds to produce its complete body. A proxy
// configured with cliProxyReadTimeout must terminate the request well before
// that, while an unwired flag leaves the default 30-second timeout in effect.
type cliSlowUploadBody struct {
	emitted int
}

func (b *cliSlowUploadBody) Read(p []byte) (int, error) {
	if b.emitted >= slowUploadBodyBytes {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}

	time.Sleep(slowUploadChunkDelay)
	b.emitted++
	p[0] = ' '
	return 1, nil
}

var _ = Describe("Proxy read timeout CLI wiring", Label("proxy", "read-timeout", "e2e"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		mockServer *statelessMockMCPServer
		proxyURL   string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("proxy-read-timeout")

		Expect(e2e.CheckTHVBinaryAvailable(config)).To(Succeed(), "thv binary should be available")

		var err error
		mockServer, err = newStatelessMockMCPServer()
		Expect(err).ToNot(HaveOccurred(), "should start the mock MCP server")

		By("starting a workload through thv run with --proxy-read-timeout")
		e2e.NewTHVCommand(config,
			"run",
			"--name", serverName,
			"--stateless",
			"--proxy-read-timeout", cliProxyReadTimeout.String(),
			mockServer.URL()+"/mcp",
		).ExpectSuccess()

		Expect(e2e.WaitForMCPServer(config, serverName, e2e.ServerReadyTimeout())).To(Succeed())

		proxyURL, err = e2e.GetMCPServerURL(config, serverName)
		Expect(err).ToNot(HaveOccurred(), "should discover the workload proxy URL")
		if !strings.HasSuffix(proxyURL, "/mcp") {
			proxyURL += "/mcp"
		}
	})

	AfterEach(func() {
		if config != nil && config.CleanupAfter && serverName != "" {
			Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())
		}
		if mockServer != nil {
			mockServer.Stop()
			mockServer = nil
		}
	})

	It("applies --proxy-read-timeout to a live proxy", func() {
		body := &cliSlowUploadBody{}

		requestCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, proxyURL, body)
		Expect(err).ToNot(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = slowUploadBodyBytes

		client := &http.Client{Timeout: 8 * time.Second}
		started := time.Now()
		resp, requestErr := client.Do(req)
		elapsed := time.Since(started)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
		}

		if requestErr == nil {
			Expect(resp).ToNot(BeNil())
			Expect(resp.StatusCode).ToNot(Equal(http.StatusOK),
				"a timed-out upload must not produce a successful MCP response")
		}
		Expect(elapsed).To(BeNumerically("<", slowUploadMaxDuration),
			"the configured one-second timeout should terminate the five-second upload")
	})
})

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This spec is the observability tier of the e2e suite (issue #5837, Commit
// 2, Step 2.3): a real OTLP/HTTP collector, not a log-grep, receives actual
// spans from a live `thv run` process and asserts real attribute values --
// telemetry_middleware_e2e_test.go only greps proxy log lines and never
// inspects a span. mcp.protocol.version and mcp.client.name are set in
// pkg/telemetry/middleware.go from the incoming MCP-Protocol-Version header
// and _meta.clientInfo respectively (Modern's only source of client
// attribution, since Modern has no initialize request -- see
// addMethodSpecificAttributes's doc comment).

// otlpSpan is a minimal, test-only projection of a received OTLP span.
type otlpSpan struct {
	Name       string
	Attributes map[string]string
}

// otlpTraceCollector is an in-process OTLP/HTTP trace receiver: it decodes
// real ExportTraceServiceRequest protobuf (the wire format
// otlptracehttp actually sends -- no compression by default, see
// pkg/telemetry/providers/otlp/tracing.go) and records every span's name and
// string attributes for assertions.
type otlpTraceCollector struct {
	server *httptest.Server
	mu     sync.Mutex
	spans  []otlpSpan
}

func newOTLPTraceCollector() *otlpTraceCollector {
	c := &otlpTraceCollector{}
	c.server = httptest.NewServer(http.HandlerFunc(c.handle))
	return c
}

func (c *otlpTraceCollector) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, gzErr := gzip.NewReader(bytes.NewReader(body))
		if gzErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer gz.Close()
		if body, err = io.ReadAll(gz); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	var req coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				attrs := make(map[string]string, len(s.GetAttributes()))
				for _, kv := range s.GetAttributes() {
					attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
				}
				c.spans = append(c.spans, otlpSpan{Name: s.GetName(), Attributes: attrs})
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// Spans returns a snapshot of every span received so far.
func (c *otlpTraceCollector) Spans() []otlpSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]otlpSpan(nil), c.spans...)
}

func (c *otlpTraceCollector) URL() string { return c.server.URL }
func (c *otlpTraceCollector) Stop()       { c.server.Close() }

var _ = Describe("Dual-Era Observability — OTLP Spans", Label("proxy", "stateless", "dual-era", "e2e", "telemetry"), Serial, func() {
	var (
		config     *e2e.TestConfig
		serverName string
		client     *e2e.RawMCPClient
		proxyURL   string
		collector  *otlpTraceCollector
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("func-otel")

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		collector = newOTLPTraceCollector()

		client, err = e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		e2e.NewTHVCommand(config, "run",
			"--name", serverName,
			"--transport", "stdio",
			"-e", "BACKEND_MODE=echo",
			"--otel-endpoint", collector.URL(),
			"--otel-insecure",
			"--otel-sampling-rate", "1.0", // always-sample: assertions must not race the default 10% ratio
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
		if collector != nil {
			collector.Stop()
		}
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "should be able to stop and remove server")
		}
	})

	It("emits mcp.protocol.version and mcp.client.name on a Modern span", func() {
		req, err := e2e.NewModernRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "otelcheck"},
		})
		Expect(err).ToNot(HaveOccurred())
		req.WithClientInfo("otel-e2e-client", "1.0")

		resp, err := client.Send(context.Background(), proxyURL, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.Error).To(BeNil())

		// The SDK's BatchSpanProcessor flushes on its own timer (default 5s),
		// not on every span -- poll rather than assert immediately.
		Eventually(func() []otlpSpan {
			return collector.Spans()
		}, 20*time.Second, 500*time.Millisecond).Should(ContainElement(SatisfyAll(
			HaveField("Attributes", HaveKeyWithValue("mcp.protocol.version", e2e.MCPVersionModern)),
			HaveField("Attributes", HaveKeyWithValue("mcp.client.name", "otel-e2e-client")),
		)), "expected a span carrying both mcp.protocol.version and mcp.client.name")
	})
})

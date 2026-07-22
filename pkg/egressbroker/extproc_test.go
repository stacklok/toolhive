// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	envoycore "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/egressbroker"
)

// --- shared test plumbing -------------------------------------------------

func newTestMeterProvider(t *testing.T) (metric.MeterProvider, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// metricPoints flattens a counter's datapoints into attr-set → value.
func metricPoints(t *testing.T, m metricdata.Metrics) []map[string]string {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected a Sum counter, got %T", m.Data)
	var out []map[string]string
	for _, dp := range sum.DataPoints {
		attrs := map[string]string{}
		for _, kv := range dp.Attributes.ToSlice() {
			attrs[string(kv.Key)] = kv.Value.AsString()
		}
		for i := int64(0); i < dp.Value; i++ {
			out = append(out, attrs)
		}
	}
	return out
}

// captureHandler records slog records for audit assertions.
type captureHandler struct {
	mu      sync.Mutex
	records []slogRecord
}

type slogRecord struct {
	msg    string
	attrs  map[string]string
	rawLog string // every attribute rendered (for token-substring guards)
}

func (*captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := slogRecord{msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		v := a.Value.String()
		rec.attrs[a.Key] = v
		rec.rawLog += a.Key + "=" + v + " "
		return true
	})
	h.records = append(h.records, rec)
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *captureHandler) byMsg(msg string) []slogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slogRecord
	for _, r := range h.records {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func newCaptureAudit() (*captureHandler, egressbroker.AuditLogger) {
	h := &captureHandler{}
	return h, egressbroker.NewAuditLoggerWith(h)
}

const (
	testHost      = "api.github.com"
	testRequestID = "req-scan-1"
	testToken     = "gho_s3cr3t-t0ken"
	testHeaderVal = "Bearer " + testToken
)

// scanFixture wires map + scanner + metrics + audit with one recorded
// injection for testRequestID (mirrors what the injector does on allow).
type scanFixture struct {
	tokens  *egressbroker.TokenMap
	scanner *egressbroker.ExternalProcessorServer
	metrics *egressbroker.BrokerMetrics
	reader  *sdkmetric.ManualReader
	audit   *captureHandler
	state   *egressbroker.StreamState
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()
	return newScanFixtureWithFailOpen(t, true)
}

func newScanFixtureWithFailOpen(t *testing.T, failOpen bool) *scanFixture {
	t.Helper()
	tokens, err := egressbroker.NewTokenMap(egressbroker.ScanCorrelationTTL, egressbroker.ScanCorrelationMaxEntries)
	require.NoError(t, err)
	mp, reader := newTestMeterProvider(t)
	metrics, err := egressbroker.NewBrokerMetrics(mp)
	require.NoError(t, err)
	auditHandler, auditLog := newCaptureAudit()
	scanner, err := egressbroker.NewExternalProcessorServer(
		tokens,
		egressbroker.ScannerBounds{MaxBodyBytes: egressbroker.DefaultScanMaxBodyBytes},
		failOpen,
		testIdentity,
		metrics,
		auditLog,
	)
	require.NoError(t, err)
	return &scanFixture{tokens: tokens, scanner: scanner, metrics: metrics, reader: reader, audit: auditHandler}
}

// record mirrors the injector's injection-time hook (same map, same API).
func (f *scanFixture) record() {
	f.tokens.Record(testRequestID, testHeaderVal, "github")
}

// handleHeaders / handleBody drive one response exchange with shared stream
// state (as the gRPC Process loop would).
func (f *scanFixture) handleHeaders(req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	f.state = &egressbroker.StreamState{}
	return f.scanner.Handle(context.Background(), f.state, req)
}

func (f *scanFixture) handleBody(req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	return f.scanner.Handle(context.Background(), f.state, req)
}

// respHeaders builds the response-headers processing request with the D6c
// correlation metadata the rendered route would set.
func respHeaders(requestID string, headers ...*envoycore.HeaderValue) *extprocv3.ProcessingRequest {
	ns, err := structpb.NewStruct(map[string]any{"request_id": requestID, "host": testHost})
	requireNotErr(err)
	return &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_ResponseHeaders{
			ResponseHeaders: &extprocv3.HttpHeaders{
				Headers: &envoycore.HeaderMap{Headers: headers},
			},
		},
		MetadataContext: &envoycore.Metadata{
			FilterMetadata: map[string]*structpb.Struct{"io.toolhive.egress": ns},
		},
	}
}

func respBody(body []byte, requestID string) *extprocv3.ProcessingRequest {
	ns, err := structpb.NewStruct(map[string]any{"request_id": requestID, "host": testHost})
	requireNotErr(err)
	return &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_ResponseBody{
			ResponseBody: &extprocv3.HttpBody{Body: body, EndOfStream: true},
		},
		MetadataContext: &envoycore.Metadata{
			FilterMetadata: map[string]*structpb.Struct{"io.toolhive.egress": ns},
		},
	}
}

func requireNotErr(err error) {
	if err != nil {
		panic(err)
	}
}

func isImmediate(resp *extprocv3.ProcessingResponse) *extprocv3.ImmediateResponse {
	imm, ok := resp.GetResponse().(*extprocv3.ProcessingResponse_ImmediateResponse)
	if !ok {
		return nil
	}
	return imm.ImmediateResponse
}

// --- §1.3 test matrix ------------------------------------------------------

// §1.3 #1: response body contains the injected token → 502 + generic body;
// the token appears nowhere; metric + audit emitted.
func TestExtProc_LeakInBodyBlocked(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	resp := f.handleHeaders(respHeaders(testRequestID))
	assert.Nil(t, isImmediate(resp), "clean headers must pass")

	body := []byte(`{"debug":"` + testHeaderVal + `"}`)
	resp = f.handleBody(respBody(body, testRequestID))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "echoed credential in body must suppress the response")
	assert.Equal(t, int32(502), int32(imm.GetStatus().GetCode()))
	assert.Equal(t, "response suppressed by egress policy", string(imm.GetBody()))
	assert.NotContains(t, string(imm.GetBody()), testToken, "generic body must never carry the matched value")
	assert.NotContains(t, imm.GetDetails(), testToken)

	// Metric: one leak scan with mcpserver+provider+where labels.
	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	require.Len(t, scans, 1)
	assert.Equal(t, map[string]string{
		"mcpserver": "github-mcp", "provider": "github", "result": "leak", "where": "body",
	}, scans[0])

	// Audit: exactly one Leak line, no token substring anywhere.
	leaks := f.audit.byMsg("egressbroker response suppressed: credential echo detected")
	require.Len(t, leaks, 1)
	assert.Equal(t, "github-mcp", leaks[0].attrs["mcpserver"])
	assert.Equal(t, "github", leaks[0].attrs["provider"])
	assert.Equal(t, "api.github.com", leaks[0].attrs["host"])
	assert.Equal(t, testRequestID, leaks[0].attrs["request_id"])
	assert.Equal(t, "body", leaks[0].attrs["where"])
	assert.NotContains(t, leaks[0].rawLog, testToken)
	assert.NotContains(t, leaks[0].rawLog, testHeaderVal)
}

// §1.3 #2: response headers contain the token → same suppression.
func TestExtProc_LeakInHeadersBlocked(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	echoed := &envoycore.HeaderValue{Key: "x-echo-authorization", Value: testHeaderVal}
	resp := f.handleHeaders(respHeaders(testRequestID, echoed))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "echoed credential in a response header must suppress the response")
	assert.Equal(t, int32(502), int32(imm.GetStatus().GetCode()))
	assert.NotContains(t, string(imm.GetBody()), testToken)

	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	require.Len(t, scans, 1)
	assert.Equal(t, "header", scans[0]["where"])
	leaks := f.audit.byMsg("egressbroker response suppressed: credential echo detected")
	require.Len(t, leaks, 1)
	assert.Equal(t, "header", leaks[0].attrs["where"])
	assert.NotContains(t, leaks[0].rawLog, testToken)
}

// §1.3 #3: clean response passes untouched.
func TestExtProc_CleanResponsePasses(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	resp := f.handleHeaders(respHeaders(testRequestID,
		&envoycore.HeaderValue{Key: "content-type", Value: "application/json"}))
	assert.Nil(t, isImmediate(resp))
	assert.IsType(t, &extprocv3.ProcessingResponse_ResponseHeaders{}, resp.GetResponse(),
		"clean headers must get a pass-through response")

	resp = f.handleBody(respBody([]byte(`{"ok":true}`), testRequestID))
	assert.Nil(t, isImmediate(resp))
	assert.IsType(t, &extprocv3.ProcessingResponse_ResponseBody{}, resp.GetResponse(),
		"clean body must get a pass-through response")

	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	require.Len(t, scans, 1)
	assert.Equal(t, "ok", scans[0]["result"])
	assert.Empty(t, f.audit.byMsg("egressbroker response suppressed: credential echo detected"))
}

// §1.3 #4: token present base64-encoded → blocked.
func TestExtProc_Base64TokenBlocked(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	resp := f.handleHeaders(respHeaders(testRequestID))
	assert.Nil(t, isImmediate(resp))
	encoded := base64.StdEncoding.EncodeToString([]byte(testHeaderVal))
	resp = f.handleBody(respBody([]byte("data: "+encoded), testRequestID))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "base64-encoded echo must suppress the response")
	assert.NotContains(t, string(imm.GetBody()), testToken)
}

// §1.3 #5: body over cap → body not scanned (header scan still applies) +
// scan_skipped_total metric. Q1 metric semantics: the skip must NOT also
// record a result=ok datapoint for the unscanned body phase.
func TestExtProc_OversizeBodySkipped(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	// Exactly at the cap is scanned: token within the cap must block...
	bodyCap := egressbroker.DefaultScanMaxBodyBytes
	atCap := append(bytes.Repeat([]byte("a"), bodyCap-len(testHeaderVal)), testHeaderVal...)
	require.Len(t, atCap, bodyCap)
	_ = f.handleHeaders(respHeaders(testRequestID))
	resp := f.handleBody(respBody(atCap, testRequestID))
	assert.NotNil(t, isImmediate(resp), "token at [cap-len, cap) is scanned and blocks")

	// ...and over the cap is NOT: the same body one byte longer passes with a
	// skip (never scan past the cap).
	over := append(atCap, 'b')
	f.record() // the previous lookup consumed the entry
	resp = f.handleHeaders(respHeaders(testRequestID))
	assert.Nil(t, isImmediate(resp))
	resp = f.handleBody(respBody(over, testRequestID))
	assert.Nil(t, isImmediate(resp), "over-cap body passes (fail-open default for the body)")

	got := collectMetrics(t, f.reader)
	skips := metricPoints(t, got["egress_broker_scan_skipped_total"])
	require.Len(t, skips, 1)
	assert.Equal(t, map[string]string{"mcpserver": "github-mcp", "provider": "github"}, skips[0])

	// Q1: the skipped body phase must not double-count as result=ok — the
	// only scan datapoint is the earlier leak.
	scans := metricPoints(t, got["egress_broker_response_scan_total"])
	require.Len(t, scans, 1, "skip-then-ok double counting: an unscanned body must not record result=ok")
	assert.Equal(t, "leak", scans[0]["result"])
}

// §1.3 #5b: fail-closed + over-cap body → 502 (the configured posture
// governs in-band decisions, not just Envoy-side unavailability).
func TestExtProc_FailClosedOversizeBodyBlocked(t *testing.T) {
	t.Parallel()
	f := newScanFixtureWithFailOpen(t, false)
	f.record()

	over := bytes.Repeat([]byte("a"), egressbroker.DefaultScanMaxBodyBytes+1)
	resp := f.handleHeaders(respHeaders(testRequestID))
	assert.Nil(t, isImmediate(resp), "clean headers pass even fail-closed")
	resp = f.handleBody(respBody(over, testRequestID))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "fail-closed: an unscannable (over-cap) body must be suppressed")
	assert.Equal(t, int32(502), int32(imm.GetStatus().GetCode()))

	got := collectMetrics(t, f.reader)
	skips := metricPoints(t, got["egress_broker_scan_skipped_total"])
	require.Len(t, skips, 1, "the skip is recorded before the fail-closed suppression")
	// An unscanned body records no scan-result datapoint (no skip-then-ok
	// double count): the only body outcome was the skip above.
	if m, ok := got["egress_broker_response_scan_total"]; ok {
		assert.Empty(t, metricPoints(t, m))
	}
}

// §1.3 #6b: fail-closed + unknown request-id → 502 (nothing proves the
// response does not echo a credential).
func TestExtProc_FailClosedUnknownRequestIDBlocked(t *testing.T) {
	t.Parallel()
	f := newScanFixtureWithFailOpen(t, false)
	// Nothing recorded.

	resp := f.handleHeaders(respHeaders("never-injected"))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "fail-closed: unknown request-id must be suppressed")
	assert.Equal(t, int32(502), int32(imm.GetStatus().GetCode()))
	assert.NotContains(t, string(imm.GetBody()), testToken)

	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	require.Len(t, scans, 1, "unknown is still counted exactly once")
	assert.Equal(t, "unknown_request", scans[0]["result"])
}

// §1.3 #6: unknown request-id → pass + unknown metric.
func TestExtProc_UnknownRequestIDPasses(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	// Nothing recorded.

	resp := f.handleHeaders(respHeaders("never-injected"))
	assert.Nil(t, isImmediate(resp))
	resp = f.handleBody(respBody([]byte("anything"), "never-injected"))
	assert.Nil(t, isImmediate(resp))

	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	require.Len(t, scans, 1, "unknown counted once (header phase)")
	assert.Equal(t, "unknown_request", scans[0]["result"])
}

// Expired entries read as unknown (scanner restart / TTL eviction equivalent).
// Uses the injected fake clock — no time.Sleep against the TTL.
func TestTokenMap_ExpiredEntryReadsAsUnknown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := &fakeClock{now: now}
	short, err := egressbroker.NewTokenMapWithClock(time.Minute, 10, clock.Now)
	require.NoError(t, err)
	short.Record("req-expired", testHeaderVal, "github")

	clock.Advance(2 * time.Minute)

	_, ok := short.Lookup("req-expired")
	assert.False(t, ok, "expired entry must read as unknown")
}

// §1.3 #8: fail-closed config renders failure_mode_allow: false (the 502 on
// scanner error is then Envoy's). Asserted end-to-end in envoyconfig_test;
// here the constructor rejects a nonsensical scan cap (fail loudly).
func TestExtProc_ConstructorValidation(t *testing.T) {
	t.Parallel()
	tokens, err := egressbroker.NewTokenMap(time.Minute, 10)
	require.NoError(t, err)
	_, auditLog := newCaptureAudit()

	_, err = egressbroker.NewExternalProcessorServer(nil,
		egressbroker.ScannerBounds{MaxBodyBytes: 1}, true, testIdentity, nil, auditLog)
	assert.Error(t, err, "nil token map")
	_, err = egressbroker.NewExternalProcessorServer(tokens,
		egressbroker.ScannerBounds{MaxBodyBytes: 0}, true, testIdentity, nil, auditLog)
	assert.Error(t, err, "zero body cap must fail loudly")
	_, err = egressbroker.NewExternalProcessorServer(tokens,
		egressbroker.ScannerBounds{MaxBodyBytes: 1}, true, testIdentity, nil, nil)
	assert.Error(t, err, "nil audit logger")
	_, err = egressbroker.NewExternalProcessorServer(tokens,
		egressbroker.ScannerBounds{MaxBodyBytes: 1}, true, egressbroker.PodIdentity{}, nil, auditLog)
	assert.Error(t, err, "incomplete identity")

	// nil metrics is allowed (uninstrumented path), noop provider too.
	m, err := egressbroker.NewBrokerMetrics(noop.NewMeterProvider())
	require.NoError(t, err)
	_, err = egressbroker.NewExternalProcessorServer(tokens,
		egressbroker.ScannerBounds{MaxBodyBytes: 1}, false, testIdentity, m, auditLog)
	assert.NoError(t, err)
}

// §1.3 #9: TTL-map eviction under load → no panic, oldest evicted, scan miss
// counted as unknown.
func TestTokenMap_EvictionUnderLoad(t *testing.T) {
	t.Parallel()
	m, err := egressbroker.NewTokenMap(time.Minute, 100)
	require.NoError(t, err)

	// Oldest ("req-0") must be evicted when entry 101 lands.
	for i := 0; i < 99; i++ {
		m.Record(fmt.Sprintf("req-%d", i), testHeaderVal, "github")
	}
	m.Record("req-99", testHeaderVal, "github")
	m.Record("req-100", testHeaderVal, "github")
	assert.Equal(t, 100, m.Len())
	_, ok := m.Lookup("req-0")
	assert.False(t, ok, "oldest entry must be evicted (scan miss)")
	_, ok = m.Lookup("req-100")
	assert.True(t, ok, "newest entry survives")

	// Lookup consumes (one injection ↔ one scanned response).
	_, ok = m.Lookup("req-100")
	assert.False(t, ok, "consumed entry reports unknown")

	// Re-recording the same request-id refreshes in place.
	m.Record("req-1", testHeaderVal, "github")
	m.Record("req-1", testHeaderVal, "github")
	assert.Equal(t, 99, m.Len(), "two consumed (one evicted, one looked up) + one refreshed")
}

// §1.3 #10: deny path (no injection) → no map entry; response passes.
// Covered at the scanner boundary by TestExtProc_UnknownRequestIDPasses;
// here the injector proves the deny path never records.
func TestInjector_DenyPathRecordsNothing(t *testing.T) {
	t.Parallel()
	tokens, err := egressbroker.NewTokenMap(time.Minute, 10)
	require.NoError(t, err)
	inj := mustInjector(t, newDenyAllReader(t))
	inj.WithScanCorrelation(tokens)

	res := inj.Evaluate(context.Background(),
		egressbroker.Destination{Host: "evil.example.com", Method: "GET", Path: "/"}, "req-denied")
	assert.False(t, res.Allow)
	assert.Equal(t, 0, tokens.Len(), "a denied request must leave no scan-correlation entry")
	_, ok := tokens.Lookup("req-denied")
	assert.False(t, ok)
}

// --- token map bounds ------------------------------------------------------

func TestTokenMap_ConstructorValidation(t *testing.T) {
	t.Parallel()
	_, err := egressbroker.NewTokenMap(0, 10)
	assert.Error(t, err, "zero TTL must fail loudly")
	_, err = egressbroker.NewTokenMap(time.Minute, 0)
	assert.Error(t, err, "zero bound must fail loudly")
}

// TestScanCorrelationRecordedOnInject proves the injector records
// (request-id → token) exactly when it allows, keyed by the x-request-id.
func TestScanCorrelationRecordedOnInject(t *testing.T) {
	t.Parallel()
	tokens, err := egressbroker.NewTokenMap(time.Minute, 10)
	require.NoError(t, err)
	inj := mustInjector(t, newGithubTokenReader(t, testToken))
	inj.WithScanCorrelation(tokens)

	res := inj.Evaluate(context.Background(),
		egressbroker.Destination{Host: "api.github.com", Method: "GET", Path: "/repos/foo"}, testRequestID)
	require.True(t, res.Allow)
	rec, ok := tokens.Lookup(testRequestID)
	require.True(t, ok, "allow must record the scan correlation")
	assert.Equal(t, "github", rec.Provider)
	// The hash is of the exact injected header value.
	assert.Equal(t, sha256Of(testHeaderVal), rec.TokenHash)
}

// --- low-cardinality guard (ADR D11) ---------------------------------------

// TestMetricLabelsAreLowCardinality asserts no datapoint on any broker
// instrument carries a user/pod/request-id label.
func TestMetricLabelsAreLowCardinality(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()
	f.handleHeaders(respHeaders(testRequestID,
		&envoycore.HeaderValue{Key: "x-echo", Value: testHeaderVal}))
	f.handleHeaders(respHeaders("unknown-req"))

	allowed := map[string]map[string]bool{
		"egress_broker_response_scan_total": {"mcpserver": true, "provider": true, "result": true, "where": true},
		"egress_broker_scan_skipped_total":  {"mcpserver": true, "provider": true},
		"egress_broker_injections_total":    {"mcpserver": true, "provider": true},
		"egress_broker_denials_total":       {"mcpserver": true, "provider": true, "result": true},
	}
	for name, m := range collectMetrics(t, f.reader) {
		keys, ok := allowed[name]
		require.True(t, ok, "unexpected instrument %q", name)
		sum, isSum := m.Data.(metricdata.Sum[int64])
		require.True(t, isSum, "%s must be a counter", name)
		for _, dp := range sum.DataPoints {
			for _, attr := range dp.Attributes.ToSlice() {
				assert.True(t, keys[string(attr.Key)],
					"%s carries forbidden label %q (D11: no user/pod/request-id labels)", name, attr.Key)
				assert.NotContains(t, attr.Value.AsString(), "user-123")
				assert.NotContains(t, attr.Value.AsString(), testRequestID)
			}
		}
	}
}

// newDenyAllReader returns a TokenReader mock with no expectations: any
// credential load fails the test (deny paths must never load).
func newDenyAllReader(t *testing.T) *mocks.MockTokenReader {
	t.Helper()
	return mocks.NewMockTokenReader(gomock.NewController(t))
}

// newGithubTokenReader returns a TokenReader mock serving one github
// credential for the test session.
func newGithubTokenReader(t *testing.T, token string) *mocks.MockTokenReader {
	t.Helper()
	reader := mocks.NewMockTokenReader(gomock.NewController(t))
	reader.EXPECT().
		GetAllUpstreamCredentials(gomock.Any(), testIdentity.SessionID, gomock.Any()).
		Return(map[string]upstreamtoken.UpstreamCredential{
			"github": {AccessToken: token},
		}, nil, nil)
	return reader
}

func sha256Of(v string) [32]byte { return sha256.Sum256([]byte(v)) }

var _ slog.Handler = (*captureHandler)(nil)

// --- TokenMap concurrency + eviction ---------------------------------------

// fakeClock is the injected now-seam for TokenMap tests (no time.Sleep).
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Concurrency flood (run with -race): concurrent Record/Lookup against
// distinct ids ≫ maxEntries must keep Len ≤ maxEntries, never panic, and
// evict oldest-first.
func TestTokenMap_ConcurrencyFlood(t *testing.T) {
	t.Parallel()
	const maxEntries = 64
	m, err := egressbroker.NewTokenMap(time.Minute, maxEntries)
	require.NoError(t, err)

	var wg sync.WaitGroup
	ids := make([]string, 0, 8*maxEntries)
	for i := 0; i < 8*maxEntries; i++ {
		ids = append(ids, fmt.Sprintf("flood-%d", i))
	}
	for _, id := range ids {
		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			m.Record(id, testHeaderVal, "github")
		}(id)
		go func(id string) {
			defer wg.Done()
			_, _ = m.Lookup(id) // racing a record of the same id is fine
		}(id)
	}
	waitWithTimeout(t, &wg, "record/lookup flood")

	assert.LessOrEqual(t, m.Len(), maxEntries, "the map must stay bounded under flood")
	// The oldest ids must have been evicted; the newest survive.
	for _, id := range ids[:maxEntries] {
		_, ok := m.Lookup(id)
		assert.False(t, ok, "oldest entry %s must be evicted", id)
	}
	survivors := 0
	for _, id := range ids[len(ids)-maxEntries:] {
		if _, ok := m.Lookup(id); ok {
			survivors++
		}
	}
	assert.Positive(t, survivors, "recently recorded entries must survive")
}

// Two streams racing one request-id: exactly one gets the scan record; the
// other reads unknown (one injection ↔ one scanned response) — asserted at
// the scanner level so the metric semantics are pinned too.
func TestExtProc_ConcurrentSameRequestIDExactlyOneScan(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	respHeadersForRace := func() *extprocv3.ProcessingRequest {
		return respHeaders(testRequestID)
	}
	const winner = 0
	results := make([]*extprocv3.ProcessingResponse, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = f.scanner.Handle(context.Background(), &egressbroker.StreamState{}, respHeadersForRace())
		}(i)
	}
	waitWithTimeout(t, &wg, "two streams racing one request-id")

	// Both pass (fail-open), but exactly one was the known scan.
	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	unknowns := 0
	for _, s := range scans {
		if s["result"] == "unknown_request" {
			unknowns++
		}
	}
	assert.Equal(t, 1, unknowns, "exactly one racer reads unknown_request")
	assert.Nil(t, isImmediate(results[winner]))
}

// TokenMap-level race guard: the consuming lookup gives exactly one winner.
func TestTokenMap_ConcurrentLookupSameRequestID(t *testing.T) {
	t.Parallel()
	m, err := egressbroker.NewTokenMap(time.Minute, 10)
	require.NoError(t, err)
	m.Record(testRequestID, testHeaderVal, "github")

	const racers = 16
	found := make(chan bool, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := m.Lookup(testRequestID)
			found <- ok
		}()
	}
	waitWithTimeout(t, &wg, "concurrent same-id lookup")
	close(found)
	wins := 0
	for ok := range found {
		if ok {
			wins++
		}
	}
	assert.Equal(t, 1, wins, "exactly one stream gets the scan record; the rest read unknown_request")
}

// RunEvictionLoop exits on ctx cancel (no goroutine leak) and sweeps expired
// entries on tick.
func TestTokenMap_RunEvictionLoop(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Now()}
	m, err := egressbroker.NewTokenMapWithClock(time.Minute, 100, clock.Now)
	require.NoError(t, err)
	m.Record("stale", testHeaderVal, "github")
	m.Record("fresh", testHeaderVal, "github")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.RunEvictionLoop(ctx, time.Millisecond)
		close(done)
	}()

	// Only "stale" is past TTL: the sweep drops it and keeps "fresh".
	clock.Advance(2 * time.Minute)
	m.Record("fresh", testHeaderVal, "github") // re-record with the advanced clock so only "stale" is expired
	require.Eventually(t, func() bool { return m.Len() == 1 },
		5*time.Second, time.Millisecond, "the sweep must evict exactly the expired entry")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunEvictionLoop did not exit on ctx cancel (goroutine leak)")
	}
}

// waitWithTimeout fails fast instead of hanging the suite on a deadlock.
func waitWithTimeout(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
}

// hungStreamServer wedges every ext_proc stream until told otherwise: this
// is the pathological stream that would block grpc.GracefulStop forever.
// entered is closed when the first stream handler starts, so the test wedges
// shutdown only after a handler is genuinely mid-stream.
type hungStreamServer struct {
	extprocv3.UnimplementedExternalProcessorServer
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (h *hungStreamServer) Process(extprocv3.ExternalProcessor_ProcessServer) error {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return nil
}

// GracefulStop wedge (R4): with a stream that never returns, canceling Run's
// ctx must still return — GracefulStop times out and Stop() breaks the wedge.
// This test takes gracefulStopTimeout to run (the wedge IS the budget).
func TestServerRunHungStreamBoundedShutdown(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	hung := &hungStreamServer{release: make(chan struct{}), entered: make(chan struct{})}
	grpcServer := grpc.NewServer()
	extprocv3.RegisterExternalProcessorServer(grpcServer, hung)
	server := egressbroker.NewTestServer(grpcServer, listener)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(ctx) }()

	// Open one stream and leave it hanging (no messages, never closed). The
	// client stream uses its own ctx so it outlives Run's cancellation.
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	client := extprocv3.NewExternalProcessorClient(conn)
	stream, err := client.Process(context.Background())
	require.NoError(t, err)

	// Wait until the handler is genuinely mid-stream, then cancel: Run must
	// return within gracefulStopTimeout + slack even though GracefulStop
	// blocks on the hung stream.
	select {
	case <-hung.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("hung stream handler never started")
	}
	cancel()
	select {
	case err := <-runDone:
		assert.NoError(t, err)
	case <-time.After(egressbroker.GracefulStopTimeoutForTest() + 10*time.Second):
		t.Fatal("Run wedged on a hung stream: GracefulStop timeout path broken")
	}
	close(hung.release)
	_ = stream.CloseSend()
	_ = conn.Close()
}

// --- documented scanner gaps (ADR-0001 §5) ---------------------------------

// ACCEPTED GAP (ADR-0001 §5 "does NOT defeat" #3): a server that transforms
// the echo evades the byte-exact scan. A gzipped body containing the token
// passes unscanned. The allowlist + destination binding are the real
// boundary; the scan is a tripwire. This test documents the gap so it can
// never be mistaken for a regression.
func TestExtProc_GzipEvasionIsAnAcceptedGap(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write([]byte(`{"debug":"` + testHeaderVal + `"}`))
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	resp := f.handleHeaders(respHeaders(testRequestID,
		&envoycore.HeaderValue{Key: "content-encoding", Value: "gzip"}))
	assert.Nil(t, isImmediate(resp))
	resp = f.handleBody(respBody(buf.Bytes(), testRequestID))
	assert.Nil(t, isImmediate(resp),
		"ACCEPTED GAP (ADR §5): gzip-transformed echoes evade the byte-exact scan; "+
			"the allowlist + destination binding are the boundary")
}

// ACCEPTED GAP (ADR-0001 §5): response trailers are never delivered to the
// scanner (the rendered config sets response_trailer_mode: SKIP). If one
// ever arrives, the broker answers with the matching pass-through variant so
// the filter never stalls — and asserts nothing about its content.
func TestExtProc_ResponseTrailersAreUnscanned(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)

	// A trailer carrying the credential would pass: the phase is not scanned
	// (SKIP mode asserted in the envoyconfig tests).
	req := &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_ResponseTrailers{
			ResponseTrailers: &extprocv3.HttpTrailers{
				Trailers: &envoycore.HeaderMap{Headers: []*envoycore.HeaderValue{
					{Key: "x-echo", Value: testHeaderVal},
				}},
			},
		},
	}
	resp := f.scanner.Handle(context.Background(), &egressbroker.StreamState{}, req)
	require.NotNil(t, resp, "trailer messages must be answered so the filter never stalls")
	assert.IsType(t, &extprocv3.ProcessingResponse_ResponseTrailers{}, resp.GetResponse())
	assert.Nil(t, isImmediate(resp),
		"ACCEPTED GAP (ADR §5): response trailers are unscanned (SKIP mode)")
}

// base64 of the injected header value leaked into a response header must
// block: the needle set is {exact header value, base64(header value)}, and
// the header scan matches both (§1.3 #4 covers the body path).
func TestExtProc_Base64HeaderLeak(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	// base64 of the full injected header value IS a needle and must block.
	encoded := base64.StdEncoding.EncodeToString([]byte(testHeaderVal))
	resp := f.handleHeaders(respHeaders(testRequestID,
		&envoycore.HeaderValue{Key: "x-data", Value: "payload=" + encoded}))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "base64 of the injected header value in a response header must block")
	assert.Equal(t, int32(502), int32(imm.GetStatus().GetCode()))

	scans := metricPoints(t, collectMetrics(t, f.reader)["egress_broker_response_scan_total"])
	require.Len(t, scans, 1)
	assert.Equal(t, "header", scans[0]["where"])
}

// containsRawValue: a non-UTF8 header value (delivered via raw_value, the
// value field empty) carrying the credential must block.
func TestExtProc_NonUTF8RawValueHeaderBlocked(t *testing.T) {
	t.Parallel()
	f := newScanFixture(t)
	f.record()

	// 0xff makes the value invalid UTF-8 → Envoy would deliver it as
	// raw_value with an empty value string.
	raw := append([]byte{0xff}, []byte(testHeaderVal)...)
	resp := f.handleHeaders(respHeaders(testRequestID,
		&envoycore.HeaderValue{Key: "x-bin", RawValue: raw}))
	imm := isImmediate(resp)
	require.NotNil(t, imm, "a non-UTF8 raw_value echo must be scanned and blocked")
	assert.Equal(t, int32(502), int32(imm.GetStatus().GetCode()))
}

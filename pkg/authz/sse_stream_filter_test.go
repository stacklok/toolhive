// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// runSSEStream serves a GET event stream through the authz middleware. The
// handler writes each chunk in order, so a test can split an event across writes.
func runSSEStream(t *testing.T, authorizer *mockAuthorizer, contentType string, chunks ...string) *httptest.ResponseRecorder {
	t.Helper()
	return runSSEStreamWithStatus(t, authorizer, http.StatusOK, contentType, chunks...)
}

func runSSEStreamWithStatus(
	t *testing.T, authorizer *mockAuthorizer, status int, contentType string, chunks ...string,
) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
		}
	})
	handler := Middleware(authorizer, next, nil)
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func sseStreamEvent(data string) string {
	return "event: message\ndata: " + data + "\n\n"
}

func TestSSEStreamFiltersEachListShape(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		result string
	}{
		{name: "tools", result: `{"tools":[{"name":"weather","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}`},
		{name: "prompts", result: `{"prompts":[{"name":"weather"},{"name":"secret"}]}`},
		{name: "resources", result: `{"resources":[{"uri":"weather","name":"weather"},{"uri":"secret","name":"secret"}]}`},
		{name: "resourceTemplates", result: `{"resourceTemplates":[{"uriTemplate":"weather","name":"weather"},{"uriTemplate":"secret","name":"secret"}]}`},
		{name: "skills", result: `{"skills":[{"uri":"weather","name":"weather"},{"uri":"secret","name":"secret"}]}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			authorizer := &mockAuthorizer{results: map[string]mockResult{
				"weather": {authorized: true},
				"secret":  {authorized: false},
			}}
			body := sseStreamEvent(`{"jsonrpc":"2.0","id":1,"result":` + tc.result + `}`)
			rr := runSSEStream(t, authorizer, "text/event-stream", body)

			assert.NotContains(t, rr.Body.String(), "secret", "an unauthorized entry must be removed from the stream")
			assert.Contains(t, rr.Body.String(), "weather")
		})
	}
}

func TestSSEStreamPassesNonListResultsUnchanged(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`) +
		sseStreamEvent(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.Equal(t, body, rr.Body.String())
}

func TestSSEStreamHoldsPartialEventUntilComplete(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	event := sseStreamEvent(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}}`)
	half := len(event) / 2
	rr := runSSEStream(t, authorizer, "text/event-stream", event[:half], event[half:])

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), "weather")
}

func TestSSEStreamFailsClosedOnSmuggledResult(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// A request frame (has a method) that also carries a tools result.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":3,"method":"tools/list","result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a smuggled list must not reach the client")
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamFailsClosedOnAmbiguousResult(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// Two list members in one result: filtering one would leave the other visible.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":4,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}}],"prompts":[{"name":"secret"}]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamNonEventStreamBodies(t *testing.T) {
	t.Parallel()

	const toolsList = `{"jsonrpc":"2.0","id":5,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}}`
	const plainResult = `{"jsonrpc":"2.0","id":6,"result":{"content":[{"type":"text","text":"ok"}]}}`
	testCases := []struct {
		name         string
		status       int
		body         string
		wantContains string
		wantAbsent   string
		wantExact    string
	}{
		// A 2xx JSON-RPC list answer is sniffed and filtered, never sent raw.
		{name: "2xx sniffed list is filtered", status: http.StatusOK, body: toolsList, wantContains: "weather", wantAbsent: "secret"},
		// A 2xx JSON-RPC answer with no list member is sent unchanged.
		{name: "2xx non-list result passes through", status: http.StatusOK, body: plainResult, wantExact: plainResult},
		// A 2xx body with no JSON-RPC result passes through, as the buffered path does.
		{name: "2xx body with no result passes through", status: http.StatusOK, body: "tools: secret", wantExact: "tools: secret"},
		// An error body is passed through unchanged.
		{name: "non-2xx other media type passes through", status: http.StatusNotFound, body: toolsList, wantExact: toolsList},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authorizer := &mockAuthorizer{results: map[string]mockResult{
				"secret":  {authorized: false},
				"weather": {authorized: true},
			}}
			rr := runSSEStreamWithStatus(t, authorizer, tc.status, "application/json", tc.body)

			require.Equal(t, tc.status, rr.Code)
			if tc.wantExact != "" || tc.body == "tools: secret" {
				assert.Equal(t, tc.wantExact, rr.Body.String())
			}
			if tc.wantContains != "" {
				assert.Contains(t, rr.Body.String(), tc.wantContains)
			}
			if tc.wantAbsent != "" {
				assert.NotContains(t, rr.Body.String(), tc.wantAbsent)
			}
		})
	}
}

func TestSSEStreamFailsClosedOnCaseAliasMember(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// "Tools" is not the canonical "tools", so the filter cannot tell it apart from a
	// list a case-insensitive client would read.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":11,"result":{"Tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a case alias of a list member must not reach the client")
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamErrorCarriesRequestID(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// Two list members: the envelope must echo the request id so the client's
	// pending call is answered instead of hanging until its timeout.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":9,"result":{"tools":[],"prompts":[]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.Contains(t, rr.Body.String(), `"id":9`, "the fail-closed error must carry the request id")
}

func TestSSEStreamStripsLeadingBOM(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// A byte order mark in front of the first event must not hide its data field
	// from the filter.
	body := "\uFEFF" + sseStreamEvent(`{"jsonrpc":"2.0","id":12,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a list behind a leading BOM must still be filtered")
}

func TestSSEStreamFailsClosedOnOversizedEvent(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// An event that never ends is buffered until the size limit, then the stream
	// closes. Nothing past the limit may be written.
	huge := "data: " + strings.Repeat("a", maxSSEEventBytes+1024)
	rr := runSSEStream(t, authorizer, "text/event-stream", huge)

	assert.Zero(t, rr.Body.Len(), "an oversized event must not be written through")
}
func TestSSEStreamFlushBeforeWriteSetsNoStore(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// The handler flushes before writing anything. The flush commits the headers,
	// so the uncacheable marking must already be in place.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
	})
	handler := Middleware(authorizer, next, nil)
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "private, no-store", rr.Header().Get("Cache-Control"))
}

func TestSSEStreamFailsClosedOnMalformedThenResult(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The first data line is undecodable, so only a per-value probe finds the result on the second.
	body := "data: {bad\ndata: " +
		`{"jsonrpc":"2.0","id":8,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}` +
		"\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a result split across data lines must not reach the client")
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamFiltersCRLFFraming(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	body := "event: message\r\ndata: " +
		`{"jsonrpc":"2.0","id":9,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}}` +
		"\r\n\r\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "CRLF framing must not hide a denied tool")
	assert.Contains(t, rr.Body.String(), "weather")
}

func TestSSEStreamPassesKeepAliveAndEndpointEvents(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	body := ": keep-alive\n\nevent: endpoint\ndata: /messages?session=1\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.Equal(t, body, rr.Body.String(), "comments and endpoint events are not list answers")
}

func TestSSEStreamFlushBetweenPartialChunks(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// The handler flushes while an event is still incomplete. The flush must not
	// write the partial event, and the completed event must still be filtered.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: "+`{"jsonrpc":"2.0","id":10,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}},`)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, `{"name":"secret","inputSchema":{"type":"object"}}]}}`+"\n\n")
		w.(http.Flusher).Flush()
	})
	handler := Middleware(authorizer, next, nil)
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.NotContains(t, rr.Body.String(), "secret", "a list split across a flush must still be filtered")
	assert.Contains(t, rr.Body.String(), "weather")
}

func TestSSEStreamFlushesCompleteEventWhileHandlerRuns(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// ServeHTTP runs the handler on this goroutine, so the outer recorder can be
	// read inside the handler. A complete event must already be on the wire after a flush.
	rr := httptest.NewRecorder()
	var midStream string
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseStreamEvent(`{"jsonrpc":"2.0","id":11,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}}`))
		w.(http.Flusher).Flush()
		midStream = rr.Body.String()
	})
	Middleware(authorizer, next, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	require.NotEmpty(t, midStream, "a complete event must be written before the handler returns")
	assert.Contains(t, midStream, "weather")
	assert.NotContains(t, midStream, "secret", "the event flushed mid-stream must already be filtered")
}

func TestSSEStreamStripsBOMSplitAcrossWrites(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The BOM arrives one byte at a time with flushes between, so the first write
	// alone does not hold the whole BOM.
	// No event: line, so the BOM sits directly in front of the data field.
	event := "data: " + `{"jsonrpc":"2.0","id":13,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}` + "\n\n"
	bom := string(mcpparser.UTF8BOM)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := range len(bom) {
			_, _ = io.WriteString(w, bom[i:i+1])
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, event)
	})
	handler := Middleware(authorizer, next, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	assert.NotContains(t, rr.Body.String(), "secret", "a BOM split across writes must not hide the list")
}

func TestSSEStreamSniffsSSEFramedBodyWithWrongContentType(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// SSE framing under a non-event-stream type: the body is read as SSE, so the
	// denied tool is removed rather than dropping the whole body.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":14,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}}`)
	rr := runSSEStream(t, authorizer, "application/octet-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), "weather")
}

func TestSSEStreamInformationalStatusDoesNotDecideMode(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// A 103 before the final 200 must not latch the mode, or the list passes unfiltered.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseStreamEvent(`{"jsonrpc":"2.0","id":15,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`))
	})
	rr := httptest.NewRecorder()
	Middleware(authorizer, next, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	assert.NotContains(t, rr.Body.String(), "secret", "a 1xx response must not stop the list from being filtered")
}

func TestSSEStreamFailsClosedOnOversizedCompleteEvent(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// The whole oversized event arrives in one write with its blank line, so it is
	// complete when it arrives. It must still fail closed before anything is written.
	huge := "data: " + strings.Repeat("a", maxSSEEventBytes+1024) + "\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", huge)

	assert.Zero(t, rr.Body.Len(), "a complete event over the cap must not be written through")
}

func TestSSEStreamAllowsManySmallEventsInOneWrite(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// Many small complete events in one write total more than the cap, but each is under
	// it. The stream must pass through, since the cap is per event.
	event := ": " + strings.Repeat("x", 10*1024) + "\n\n"
	body := strings.Repeat(event, 200)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.Equal(t, body, rr.Body.String())
}

func TestSSEStreamStripsContentLengthOnImplicitOK(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// The handler sets Content-Length and writes without calling WriteHeader, so the
	// 200 is implicit. Filtering changes the size, so the header must still go.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "9999")
		_, _ = io.WriteString(w, ": keep-alive\n\n")
	})
	rr := httptest.NewRecorder()
	Middleware(authorizer, next, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	assert.Empty(t, rr.Header().Get("Content-Length"), "a filtered stream must not keep Content-Length")
}

func TestSSEStreamFailsClosedOnAmbiguousResultKey(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// Go reads "Result" last and sees no list, while a client reads the exact "result"
	// key and sees the denied tool. The event must fail closed.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":17,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]},"Result":{"ok":true}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a case-variant result key must not leak the list")
	assert.Contains(t, rr.Body.String(), `"id":17`, "the fail-closed error must answer the request")
}

func TestSSEStreamFailsClosedOnDuplicateResultKey(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":18,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]},"result":{"ok":true}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a duplicated result key must not leak the list")
	assert.Contains(t, rr.Body.String(), `"id":18`, "the fail-closed error must answer the request")
}

func TestSSEStreamRefusesContentEncodedBody(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseStreamEvent(`{"jsonrpc":"2.0","id":19,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`))
	})
	rr := httptest.NewRecorder()
	Middleware(authorizer, next, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	assert.Equal(t, http.StatusBadGateway, rr.Code, "a content-coded list stream cannot be filtered and must be refused")
	assert.NotContains(t, rr.Body.String(), "secret")
}

func TestSSEStreamSniffedAmbiguousListAnswersWithError(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// Two list members in one result are ambiguous. The client must get an error that
	// carries the request id, not an empty 200 it waits on.
	body := `{"jsonrpc":"2.0","id":16,"result":{"tools":[],"prompts":[]}}`
	rr := runSSEStream(t, authorizer, "text/plain", body)

	assert.Contains(t, rr.Body.String(), `"id":16`, "the error must answer the request")
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamFailsClosedOnLoneCRLineBreak(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// A spec client splits on the lone CR and reads the data line. The LF-only split
	// sees one comment line, so the event must fail closed.
	body := ": x\rdata: " +
		`{"jsonrpc":"2.0","id":20,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}` +
		"\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a lone CR must not hide a list from the filter")
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamPassesResultFreeBodyThrough(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A non-MCP 2xx body with no result is not a list answer, so it passes through.
	body := `{"status":"ok"}`
	rr := runSSEStream(t, authorizer, "application/json", body)

	assert.Equal(t, body, rr.Body.String())
}

func TestSSEStreamDropsResultSplitAcrossDataLines(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// Each data line alone is not a result, but a client joins them into one. The
	// denied tool must not reach the client.
	body := "data: {\"jsonrpc\":\"2.0\",\"id\":21,\ndata: \"result\":{\"tools\":[{\"name\":\"secret\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n"
	rr := runSSEStream(t, authorizer, "application/octet-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret")
}

// hijackRecorder is a recorder whose underlying writer does support Hijack. If the
// filter let http.ResponseController unwrap to it, the hijack would succeed.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

func TestSSEStreamRefusesHijack(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A handler that asks for the connection must be refused before the underlying
	// writer is reached, so no backend bytes can skip the filter.
	var hijackErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _, hijackErr = http.NewResponseController(w).Hijack()
	})
	hr := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	Middleware(authorizer, next, nil).ServeHTTP(hr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	require.ErrorIs(t, hijackErr, http.ErrNotSupported)
	assert.False(t, hr.hijacked, "the underlying writer must not be hijacked")
}

func TestSSEStreamFailsClosedOnLoneCRSplitAcrossDataLines(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// Read with CR line breaks, the two data lines join into one result. The LF-only
	// split sees a comment and no data, so the event must fail closed.
	body := ": c\rdata: {\"jsonrpc\":\"2.0\",\"id\":30,\rdata: \"result\":{\"tools\":[{\"name\":\"secret\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a result split across CR-separated data lines must not leak")
}

func TestSSEStreamSniffedResultAfterUndecodableEvent(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The first event is not JSON. The split result in the second event must still be filtered.
	body := "data: x\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":31,\n" +
		"data: \"result\":{\"tools\":[{\"name\":\"secret\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n"
	rr := runSSEStream(t, authorizer, "application/octet-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a result after an undecodable event must still be filtered")
}

func TestSSEStreamRefusesRepeatedContentEncodingHeader(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Add("Content-Encoding", "identity")
		w.Header().Add("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseStreamEvent(`{"jsonrpc":"2.0","id":32,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]}}`))
	})
	rr := httptest.NewRecorder()
	Middleware(authorizer, next, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sse", nil))

	assert.Equal(t, http.StatusBadGateway, rr.Code, "a gzip coding in a second header line must be refused")
	assert.NotContains(t, rr.Body.String(), "secret")
}

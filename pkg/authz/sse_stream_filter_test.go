// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// sseRequest returns a GET that asks for an event stream, as the MCP stream GET does.
func sseRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/sse", nil)
	r.Header.Set("Accept", "text/event-stream")
	return r
}

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
	req := sseRequest()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// TestSSEStreamWriterUnwrapsToUnderlyingWriter pins Unwrap, which lets
// http.ResponseController reach the connection's deadlines through this writer.
func TestSSEStreamWriterUnwrapsToUnderlyingWriter(t *testing.T) {
	t.Parallel()

	underlying := httptest.NewRecorder()
	w := newSSEStreamFilterWriter(underlying, nil, sseRequest(), nil, nil)
	assert.Same(t, underlying, w.Unwrap())
}

// TestSSEStreamFilterRejectionIDMatchesClientDecode pins the id on a rejected 2xx body
// with a duplicate "id" key. encoding/json keeps the last duplicate, so the client
// reads id 2 and the rejection must answer id 2, not the first id.
func TestSSEStreamFilterRejectionIDMatchesClientDecode(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	body := `{"jsonrpc":"2.0","id":1,"ID":2,"result":{"tools":[{"name":"secret"}]},"Result":{"tools":[{"name":"secret"}]}}`
	rr := runSSEStream(t, authorizer, "application/json", body)

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), `"id":2`)
	assert.NotContains(t, rr.Body.String(), `"id":1`)
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
	assert.Contains(t, rr.Body.String(), `"id":3`, "the fail-closed error must answer the frame's own id")
}

func TestSSEStreamFailsClosedOnResultNestedPastDecoderDepth(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// Nested past encoding/json's depth cap, so the frame does not decode and carriesResult
	// cannot see the result. It must still fail closed, not pass through.
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":42,"result":{"tools":[{"name":"secret","inputSchema":` + deep + `}]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a result nested past the depth cap must not leak")
	assert.Contains(t, rr.Body.String(), `"error"`)
	assert.Contains(t, rr.Body.String(), `"id":42`, "the id is read from the frame even though it cannot be decoded")
}

func TestSSEStreamFailsClosedOnResultBehindCRBoundary(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The first event is junk. A CR-honouring client ends it at the CR CR, then reads the
	// result in the second event, which the LF-only split still holds in one event.
	body := "data: garbage\r\rdata: {\"jsonrpc\":\"2.0\",\"id\":43,\rdata: \"result\":{\"tools\":[{\"name\":\"secret\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a result behind a CR boundary must not leak")
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
			if tc.wantExact != "" {
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
	assert.Contains(t, rr.Body.String(), `"id":12`, "the filtered list must still be sent")
}

func TestSSEStreamFailsClosedOnUnterminatedOversizedEvent(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// An event that never ends is buffered until the size limit. Its held bytes are not
	// written. This event has no readable id, so no error is written either: an
	// id-less error would end the client's session.
	huge := "data: " + strings.Repeat("a", maxSSEEventBytes+1024)
	rr := runSSEStream(t, authorizer, "text/event-stream", huge)

	assert.NotContains(t, rr.Body.String(), "aaaa", "held bytes must not be written through")
	assert.NotContains(t, rr.Body.String(), `"error"`, "an error with no id must not be written")
	assert.Less(t, rr.Body.Len(), 1024)
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
	req := sseRequest()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "private, no-store", rr.Header().Get("Cache-Control"))
}

func TestSSEStreamDropsMalformedThenResult(t *testing.T) {
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
	assert.NotContains(t, rr.Body.String(), `"error"`, "an event with no readable id is dropped, not answered with an id-less error")
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
	req := sseRequest()
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
	Middleware(authorizer, next, nil).ServeHTTP(rr, sseRequest())

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
	handler.ServeHTTP(rr, sseRequest())

	assert.NotContains(t, rr.Body.String(), "secret", "a BOM split across writes must not hide the list")
	assert.Contains(t, rr.Body.String(), `"id":13`, "the filtered list must still be sent")
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
	Middleware(authorizer, next, nil).ServeHTTP(rr, sseRequest())

	assert.NotContains(t, rr.Body.String(), "secret", "a 1xx response must not stop the list from being filtered")
}

func TestSSEStreamFiltersCompleteEventOverTheCap(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// A complete list event larger than the cap is filtered like any other, and the
	// stream carries on past it. Only an unterminated event is cut off at the cap.
	pad := strings.Repeat("a", maxSSEEventBytes+1024)
	list := sseStreamEvent(`{"jsonrpc":"2.0","id":40,"result":{"tools":[{"name":"secret","description":"` + pad +
		`","inputSchema":{"type":"object"}}]}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", list, ": after\n\n")

	assert.NotContains(t, rr.Body.String(), "secret", "the oversized list must be filtered")
	assert.Contains(t, rr.Body.String(), ": after", "the stream must continue past the oversized event")
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
	Middleware(authorizer, next, nil).ServeHTTP(rr, sseRequest())

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
	Middleware(authorizer, next, nil).ServeHTTP(rr, sseRequest())

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

func TestSSEStreamSniffedRejectionsAnswerWithRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "ambiguous result key",
			body: `{"jsonrpc":"2.0","id":44,"Result":{"tools":[]},"result":{"tools":[]}}`,
		},
		{
			name: "result that is not a clean response",
			body: `{"jsonrpc":"2.0","id":45,"result":{"tools":[]},"error":{"code":-1,"message":"x"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authorizer := &mockAuthorizer{results: map[string]mockResult{}}
			rr := runSSEStream(t, authorizer, "text/plain", tt.body)

			// A silent drop leaves the client waiting for its pending request. The
			// rejection must be answered with an error that carries the id.
			assert.Contains(t, rr.Body.String(), `"error"`, "the rejection must be answered")
			assert.Contains(t, rr.Body.String(), `"id":`, "the answer must carry the request id")
		})
	}
}

func TestListMembersMapToListFilters(t *testing.T) {
	t.Parallel()

	// Pins listMembers to the list methods of responseFilterForMethod in both
	// directions. Every list member maps to a list method, and every list method
	// has a member. The expected table is the one place a new list kind must be added.
	want := map[string]string{
		"tools":             string(mcp.MethodToolsList),
		"prompts":           string(mcp.MethodPromptsList),
		"resources":         string(mcp.MethodResourcesList),
		"resourceTemplates": string(mcp.MethodResourcesTemplatesList),
		"skills":            "skills/list",
	}
	assert.Equal(t, want, listMembers, "listMembers must match the expected list kinds")

	covered := make(map[responseFilterKind]bool)
	for member, method := range listMembers {
		kind := responseFilterForMethod(method)
		assert.NotEqual(t, responseFilterNone, kind,
			"list member %q maps to %q, which has no filter", member, method)
		covered[kind] = true
	}
	// Every list filter kind must be reached by a list member. The loop runs over
	// the enum in order up to responseFilterFindTool, which is not a list kind, so a
	// list kind added anywhere before it is checked.
	for kind := responseFilterTools; kind < responseFilterFindTool; kind++ {
		assert.True(t, covered[kind], "list filter kind %d has no list member in listMembers", kind)
	}
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

// TestSSEStreamDropsLoneCREventWithNoID checks that a lone CR event with no readable
// id is dropped rather than answered with an id-less error, which would end the session.
func TestSSEStreamDropsLoneCREventWithNoID(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	body := "data: {\"result\":{\"tools\":[{\"name\":\"secret\"}]}}\r: x\n\n"
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.NotContains(t, rr.Body.String(), `"error"`, "an error with no id must not be written")
}

func TestSSEStreamFiltersResultSplitAcrossDataLines(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// Each data line alone is not a result, but a client joins them into one. The
	// event is filtered, not dropped: its id is still answered.
	body := "data: {\"jsonrpc\":\"2.0\",\"id\":21,\ndata: \"result\":{\"tools\":[{\"name\":\"secret\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n"
	rr := runSSEStream(t, authorizer, "application/octet-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), `"id":21`)
}

func TestSSEStreamPassesResultFreeBodyThrough(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A non-MCP 2xx body with no result is not a list answer, so it passes through.
	body := `{"status":"ok"}`
	rr := runSSEStream(t, authorizer, "application/json", body)

	assert.Equal(t, body, rr.Body.String())
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

func TestSSEStreamRefusesHijackOnFilteredStream(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A takeover of a filtered stream must be refused before the underlying writer is
	// reached, so no backend bytes can skip the filter.
	var hijackErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _, hijackErr = http.NewResponseController(w).Hijack()
	})
	hr := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	Middleware(authorizer, next, nil).ServeHTTP(hr, sseRequest())

	require.ErrorIs(t, hijackErr, http.ErrNotSupported)
	assert.False(t, hr.hijacked, "the underlying writer must not be hijacked")
}

func TestSSEStreamAllowsHijackOfPassThroughResponse(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A 101 upgrade is not a filtered body, so its takeover reaches the underlying writer.
	var hijackErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
		_, _, hijackErr = http.NewResponseController(w).Hijack()
	})
	hr := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	Middleware(authorizer, next, nil).ServeHTTP(hr, sseRequest())

	require.NoError(t, hijackErr)
	assert.True(t, hr.hijacked, "a pass-through upgrade must reach the underlying writer")
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
	assert.Contains(t, rr.Body.String(), `"error"`, "the event must be answered with a fail-closed error")
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
	Middleware(authorizer, next, nil).ServeHTTP(rr, sseRequest())

	assert.Equal(t, http.StatusBadGateway, rr.Code, "a gzip coding in a second header line must be refused")
	assert.NotContains(t, rr.Body.String(), "secret")
}

func TestSSEStreamRequestDropsAcceptEncoding(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// The backend must not be asked for a coded body, which the filter cannot parse.
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	})
	req := sseRequest()
	req.Header.Set("Accept-Encoding", "gzip")
	Middleware(authorizer, next, nil).ServeHTTP(httptest.NewRecorder(), req)

	assert.Empty(t, got, "Accept-Encoding must be removed before the backend sees the request")
}

func TestSSEStreamFiltersFindToolAnswer(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// A find_tool answer comes back on the stream when the call was made over the POST
	// channel. Its shape alone must select the find_tool filter.
	const output = `{"tools":[` +
		`{"name":"weather","description":"Get weather","annotations":{"readOnlyHint":true}},` +
		`{"name":"secret","description":"restricted","annotations":{"readOnlyHint":true}}],` +
		`"token_metrics":{"baseline_tokens":20,"returned_tokens":10,"savings_percent":50}}`
	escaped, err := json.Marshal(output)
	require.NoError(t, err)
	result := `{"content":[{"type":"text","text":` + string(escaped) + `}],"structuredContent":` + output + `}`
	rr := runSSEStream(t, authorizer, "text/event-stream", sseStreamEvent(`{"jsonrpc":"2.0","id":61,"result":`+result+`}`))

	assert.NotContains(t, rr.Body.String(), "secret", "an unauthorized find_tool entry must be removed")
	assert.Contains(t, rr.Body.String(), "weather")
	assert.Contains(t, rr.Body.String(), `"id":61`)
}

func TestSSEStreamFiltersFindToolAnswerWithEscapedToolsKey(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The text carries the tools key with a JSON escape, so the raw bytes never spell
	// "tools". A client that decodes the text still reads the key as tools.
	const output = `{"tools":[{"name":"secret","description":"restricted","annotations":{"readOnlyHint":true}}]}`
	escaped, err := json.Marshal(output)
	require.NoError(t, err)
	result := `{"content":[{"type":"text","text":` + string(escaped) + `}]}`
	rr := runSSEStream(t, authorizer, "text/event-stream", sseStreamEvent(`{"jsonrpc":"2.0","id":62,"result":`+result+`}`))

	assert.NotContains(t, rr.Body.String(), "secret", "an escaped tools key must not bypass the find_tool filter")
	assert.Contains(t, rr.Body.String(), `"id":62`)
}

func TestSSEStreamFailsClosedOnToolsCallWithToolsList(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// A regular tool, not find_tool, whose answer carries a named tools list. The GET
	// stream cannot see the request, so the list shape selects the find_tool filter.
	// The answer is not a valid find_tool answer, so it fails closed with an error
	// that carries the request id, and no tool name reaches the client.
	const output = `{"tools":[{"name":"weather","description":"Get weather"},{"name":"secret","description":"restricted"}]}`
	result := `{"content":[{"type":"text","text":"listed"}],"structuredContent":` + output + `}`
	rr := runSSEStream(t, authorizer, "text/event-stream", sseStreamEvent(`{"jsonrpc":"2.0","id":63,"result":`+result+`}`))

	assert.NotContains(t, rr.Body.String(), "secret", "a tools list from a non-find_tool answer must not leak a name")
	assert.NotContains(t, rr.Body.String(), "weather", "a tools list from a non-find_tool answer must fail closed, not pass through")
	assert.Contains(t, rr.Body.String(), `"id":63`)
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamFailsClosedOnDeepResultBehindLoneCR(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// A lone CR splits the event into two pieces for a CR-honouring client. The second
	// piece nests its result past encoding/json's depth cap, so only per-piece checks see it.
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	payload := `{"jsonrpc":"2.0","id":63,"result":{"tools":[{"name":"secret","inputSchema":{"x":` + deep + `}}]}}`
	rr := runSSEStream(t, authorizer, "text/event-stream", "data: x\r\rdata: "+payload+"\n\n")

	assert.NotContains(t, rr.Body.String(), "secret", "a deep result behind a lone CR must not pass through")
}

func TestSSEStreamPassesPlainToolsCallAnswerUnchanged(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A tools/call answer whose text merely mentions tools is not a find_tool answer.
	// It must pass through, not fail closed with an error.
	event := `{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"Found 3 tools"}],"structuredContent":{"count":3}}}`
	rr := runSSEStream(t, authorizer, "text/event-stream", sseStreamEvent(event))

	assert.Contains(t, rr.Body.String(), `"count":3`, "a plain tools/call answer must pass through unchanged")
	assert.NotContains(t, rr.Body.String(), `"error"`, "a plain tools/call answer must not fail closed")
}

func TestSSEStreamFailsClosedOnMalformedFindToolAnswer(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// Names tools but is not a valid find_tool answer: it must fail closed, not pass through.
	result := `{"content":[],"structuredContent":{"tools":"secret"}}`
	rr := runSSEStream(t, authorizer, "text/event-stream", sseStreamEvent(`{"jsonrpc":"2.0","id":62,"result":`+result+`}`))

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), `"id":62`)
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamFailsClosedOnToolsStringHoldingJSONList(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The tools value is a string that holds a named tool list as JSON, so it must fail closed.
	result := `{"content":[],"structuredContent":{"tools":"[{\"name\":\"secret\"}]"}}`
	rr := runSSEStream(t, authorizer, "text/event-stream", sseStreamEvent(`{"jsonrpc":"2.0","id":63,"result":`+result+`}`))

	assert.NotContains(t, rr.Body.String(), "secret")
	assert.Contains(t, rr.Body.String(), `"id":63`)
}

func TestSSEStreamAnswersOversizedHeldBodyWithID(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// A held 2xx body past the cap ends the stream with one error that carries its id.
	pad := strings.Repeat("a", maxSniffedBodyBytes+1024)
	body := `{"jsonrpc":"2.0","id":50,"result":{"x":"` + pad + `"}}`
	rr := runSSEStream(t, authorizer, "application/json", body)

	assert.Contains(t, rr.Body.String(), `"id":50`, "the error must answer the request")
	assert.Contains(t, rr.Body.String(), `"error"`)
	assert.Less(t, rr.Body.Len(), 1024, "nothing past the error may be written")
}

func TestSSEStreamFailsClosedOnEscapedResultKeyPastDepthCap(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	// The result key is spelled with a JSON escape and a sibling nests past the depth cap,
	// so the frame does not decode. The key must still be recognised.
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":63,"r\u0065sult":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]},"x":` + deep + `}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.NotContains(t, rr.Body.String(), "secret", "an escaped result key must not leak the list")
	assert.Contains(t, rr.Body.String(), `"id":63`)
}

func TestSSEStreamFailsClosedOnDeepResultInHeldBody(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"secret": {authorized: false},
	}}
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	body := `{"jsonrpc":"2.0","id":64,"result":{"tools":[{"name":"secret","inputSchema":{"type":"object"}}]},"x":` + deep + `}`
	rr := runSSEStream(t, authorizer, "application/json", body)

	assert.NotContains(t, rr.Body.String(), "secret", "a held body past the depth cap must not pass through")
	assert.Contains(t, rr.Body.String(), `"id":64`)
	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestSSEStreamFiltersListEventOverCapWhenChunked(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
		"secret":  {authorized: false},
	}}
	// A 2 MiB list event arrives in 32 KiB writes, as a reverse proxy delivers it. Its
	// blank line comes last, so it must be filtered, not cut off at the cap.
	pad := strings.Repeat("a", 2<<20)
	event := sseStreamEvent(`{"jsonrpc":"2.0","id":65,"result":{"tools":[{"name":"weather","description":"` + pad +
		`","inputSchema":{"type":"object"}},{"name":"secret","inputSchema":{"type":"object"}}]}}`)
	var chunks []string
	for rest := event; len(rest) > 0; {
		n := min(32<<10, len(rest))
		chunks = append(chunks, rest[:n])
		rest = rest[n:]
	}
	rr := runSSEStream(t, authorizer, "text/event-stream", chunks...)

	assert.NotContains(t, rr.Body.String(), "secret", "a chunked oversized list must be filtered")
	assert.Contains(t, rr.Body.String(), "weather", "the allowed tool must still be sent")
	assert.Contains(t, rr.Body.String(), `"id":65`)
}

func TestSSEStreamPassesInitializeCapabilitiesThrough(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	// An initialize answer mentions tools only in its capabilities. It is not a find_tool
	// answer and must pass unchanged, or every session with a tools-capable backend breaks.
	body := sseStreamEvent(`{"jsonrpc":"2.0","id":66,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{"listChanged":true}}}}`)
	rr := runSSEStream(t, authorizer, "text/event-stream", body)

	assert.Equal(t, body, rr.Body.String())
}

func TestSSEStreamFailsClosedOnSiblingToolNames(t *testing.T) {
	t.Parallel()

	// A find_tool-shaped answer whose tool names sit outside the filtered tools list.
	// Each must fail closed so no unauthorized name reaches the client.
	const output = `{"tools":[{"name":"weather","description":"Get weather"}],` +
		`"token_metrics":{"baseline_tokens":20,"returned_tokens":10,"savings_percent":50}}`
	const secretMember = `"tools":[{"name":"secret","description":"restricted"}]`
	tests := []struct {
		name   string
		result string
	}{
		{
			name: "tools in _meta beside content",
			result: `{"content":[{"type":"text","text":` + mustJSONString(t, output) + `}],` +
				`"structuredContent":` + output + `,"_meta":{` + secretMember + `}}`,
		},
		{
			name: "tools on the text content item",
			result: `{"content":[{"type":"text","text":` + mustJSONString(t, output) + `,` + secretMember + `}],` +
				`"structuredContent":` + output + `}`,
		},
		{
			name: "tools beside tools in the output",
			result: `{"content":[{"type":"text","text":` + mustJSONString(t, output) + `}],"structuredContent":` +
				`{"tools":[{"name":"weather","description":"Get weather"}],"extra":{` + secretMember + `},` +
				`"token_metrics":{"baseline_tokens":20,"returned_tokens":10,"savings_percent":50}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authorizer := &mockAuthorizer{results: map[string]mockResult{
				"weather": {authorized: true},
				"secret":  {authorized: false},
			}}
			rr := runSSEStream(t, authorizer, "text/event-stream",
				sseStreamEvent(`{"jsonrpc":"2.0","id":68,"result":`+tc.result+`}`))

			assert.NotContains(t, rr.Body.String(), "secret", "an unauthorized name must not reach the client")
			assert.Contains(t, rr.Body.String(), `"error"`)
			assert.Contains(t, rr.Body.String(), `"id":68`)
		})
	}
}

func TestSSEStreamPassesAuthorizedToolWithToolsSchemaProperty(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"weather": {authorized: true},
	}}
	// An authorized tool whose inputSchema has a property named tools must not trip the
	// sibling check: the check runs on the answer with every tools list emptied.
	const output = `{"tools":[{"name":"weather","description":"Get weather","inputSchema":` +
		`{"type":"object","properties":{"tools":{"type":"array"}}},"annotations":{"readOnlyHint":true}}],` +
		`"token_metrics":{"baseline_tokens":20,"returned_tokens":10,"savings_percent":50}}`
	result := `{"content":[{"type":"text","text":` + mustJSONString(t, output) + `}],"structuredContent":` + output + `}`
	rr := runSSEStream(t, authorizer, "text/event-stream",
		sseStreamEvent(`{"jsonrpc":"2.0","id":69,"result":`+result+`}`))

	assert.Contains(t, rr.Body.String(), "weather", "an authorized tool must be kept")
	assert.NotContains(t, rr.Body.String(), `"error"`)
	assert.Contains(t, rr.Body.String(), `"id":69`)
}

func TestSSEStreamFailsClosedOnToolNamesOffFindToolShape(t *testing.T) {
	t.Parallel()

	// Each tool answer names a tool outside a readable find_tool text output. Before
	// the fix these passed through unfiltered. Each must now fail closed with an error
	// that carries the request id and no tool name.
	const secretList = `{"tools":[{"name":"secret","description":"restricted"}]}`
	nestedString := func() string {
		inner, err := json.Marshal(secretList)
		require.NoError(t, err)
		outer, err := json.Marshal(string(inner))
		require.NoError(t, err)
		return string(outer)
	}()
	tests := []struct {
		name   string
		result string
	}{
		{
			name: "non-text content item",
			result: `{"content":[{"type":"resource","resource":{"uri":"file:///x","text":` +
				mustJSONString(t, secretList) + `}}]}`,
		},
		{
			name:   "json array as text",
			result: `{"content":[{"type":"text","text":` + mustJSONString(t, "["+secretList+"]") + `}]}`,
		},
		{
			name:   "nested json string as text",
			result: `{"content":[{"type":"text","text":` + nestedString + `}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authorizer := &mockAuthorizer{results: map[string]mockResult{
				"secret": {authorized: false},
			}}
			rr := runSSEStream(t, authorizer, "text/event-stream",
				sseStreamEvent(`{"jsonrpc":"2.0","id":67,"result":`+tc.result+`}`))

			assert.NotContains(t, rr.Body.String(), "secret", "the tool name must not reach the client")
			assert.Contains(t, rr.Body.String(), `"error"`)
			assert.Contains(t, rr.Body.String(), `"id":67`)
		})
	}
}

// mustJSONString returns s as a JSON string literal for embedding in a test payload.
func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

// TestStreamFilterLogsErrorsAtWarn pins the log level split: filters built for the
// GET event stream log a filtering error at WARN, while the POST writer keeps ERROR.
func TestStreamFilterLogsErrorsAtWarn(t *testing.T) {
	t.Parallel()

	w := newSSEStreamFilterWriter(httptest.NewRecorder(), nil, sseRequest(), nil, nil)
	require.True(t, w.newFilter("tools/list").streamWarn, "GET stream filter must log at WARN")

	post := NewResponseFilteringWriter(nil, nil, sseRequest(), "tools/list", nil, nil)
	assert.False(t, post.streamWarn, "POST filter must keep ERROR")
}

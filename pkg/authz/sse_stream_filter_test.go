// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		// A 2xx body that is not JSON-RPC is dropped.
		{name: "2xx non-JSON-RPC body is dropped", status: http.StatusOK, body: "tools: secret", wantExact: ""},
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

	assert.LessOrEqual(t, rr.Body.Len(), maxSSEEventBytes, "an oversized event must not be written through")
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

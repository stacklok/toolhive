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
// handler writes each chunk in order, calling Flush after each one when
// flush is set, so tests can split events across writes.
func runSSEStream(t *testing.T, authorizer *mockAuthorizer, contentType string, chunks ...string) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
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

func TestSSEStreamPassesNonEventStreamThrough(t *testing.T) {
	t.Parallel()

	authorizer := &mockAuthorizer{results: map[string]mockResult{}}
	body := `{"jsonrpc":"2.0","id":5,"result":{"tools":[]}}`
	rr := runSSEStream(t, authorizer, "application/json", body)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, strings.EqualFold(rr.Body.String(), body), "a non-stream body is not parsed as SSE")
}

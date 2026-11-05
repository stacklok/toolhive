// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

const (
	listPrefix  = `{"jsonrpc":"2.0","id":1,"result":`
	allowedTool = `{"name":"allowed_tool","description":"visible"}`
	hiddenTool  = `{"name":"hidden_tool","description":"hidden"}`
	bothTools   = `[` + allowedTool + `,` + hiddenTool + `]`

	progressNotification = `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1,"progress":1}}`

	// withheldError is the JSON-RPC error the filter answers request 1 with
	// in place of a response it withholds.
	withheldError = `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"internal error"}}`
	// withheldErrorWithoutID is the one it answers a request without an id
	// with, such as the GET request that opens an event stream.
	withheldErrorWithoutID = `{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"}}`

	// longS is U+017F LATIN SMALL LETTER LONG S, which encoding/json matches
	// to "s" in member names.
	longS = "\u017f"
)

// toolsListTransport renders a JSON-RPC payload the way a backend could send
// it: as a JSON body, as an event stream, or as either without a
// Content-Type, which the filter sniffs.
type toolsListTransport struct {
	name        string
	contentType string
	render      func(payload string) string
}

var toolsListTransports = []toolsListTransport{
	{name: "json", contentType: "application/json", render: func(p string) string { return p }},
	{name: "sse", contentType: "text/event-stream", render: sseEvent},
	{name: "json without content type", render: func(p string) string { return p }},
	{name: "sse without content type", render: sseEvent},
}

func sseEvent(payload string) string {
	return "event: message\ndata: " + payload + "\n\n"
}

// serveThroughListFilter sends body through the tools/list filter, allowing
// only allowed_tool, and returns what the client receives.
func serveThroughListFilter(t *testing.T, contentType, body string) string {
	t.Helper()

	return serveThroughListMiddleware(t, []ToolMiddlewareOption{WithToolsFilter("allowed_tool")},
		http.StatusOK, contentType, body)
}

// serveThroughListMiddleware sends body with status through a tools/list
// filter built with opts and returns what the client receives.
func serveThroughListMiddleware(
	t *testing.T, opts []ToolMiddlewareOption, status int, contentType, body string,
) string {
	t.Helper()

	return serveListResponse(t, opts, status, contentType, body).Body.String()
}

// serveListResponse sends body with status through a tools/list filter built
// with opts, in answer to request 1, and returns the response the client
// receives.
func serveListResponse(
	t *testing.T, opts []ToolMiddlewareOption, status int, contentType, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	middleware, err := NewListToolsMappingMiddleware(opts...)
	require.NoError(t, err)

	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, writeErr := w.Write([]byte(body))
		require.NoError(t, writeErr)
	})

	request := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	recorder := httptest.NewRecorder()
	middleware(backend).ServeHTTP(recorder, request)
	return recorder
}

// assertWithheld checks that the client received the JSON-RPC error errorBody
// in place of a response the filter withheld: as the whole response, under
// HTTP 500, unless the backend labeled the response an event stream, whose
// status the filter forwards at once, so that the error is an event in place
// of the withheld one.
func assertWithheld(t *testing.T, recorder *httptest.ResponseRecorder, eventStream bool, errorBody string) {
	t.Helper()

	response := recorder.Result()
	require.NoError(t, response.Body.Close())
	assert.Empty(t, response.Header.Values("Content-Encoding"), "the backend's content coding must not reach the client")
	if eventStream {
		assert.Equal(t, http.StatusOK, response.StatusCode)
		assert.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
		assert.Equal(t, sseEvent(errorBody), recorder.Body.String())
		return
	}
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	assert.Equal(t, errorBody, recorder.Body.String())
}

// TestListToolsFilterWithholdsAmbiguousToolsLists covers responses that are,
// or that some client could read as, a tools list but that do not resolve to
// one unambiguous list. That includes any data that starts like a JSON object
// or array but that encoding/json cannot parse, whatever it holds: a client
// with a more lenient parser can read a list from it. Each must be withheld
// over every transport rather than passed through with the hidden tool in it,
// and the client answered with an error in its place.
func TestListToolsFilterWithholdsAmbiguousToolsLists(t *testing.T) {
	t.Parallel()

	const (
		nbsp = "\u00a0"
		bom  = "\ufeff"
	)
	nanList := listPrefix + `{"tools":` + bothTools + `},"x":NaN}`

	payloads := []struct {
		name    string
		payload string
	}{
		{"non-object tool", listPrefix + `{"tools":[` + allowedTool + `,` + hiddenTool + `,1]}}`},
		{"null tool", listPrefix + `{"tools":[` + allowedTool + `,` + hiddenTool + `,null]}}`},
		{"tools as object", listPrefix + `{"tools":{"0":` + hiddenTool + `}}}`},
		{"tools as string", listPrefix + `{"tools":"hidden_tool"}}`},
		{"tools as number", listPrefix + `{"tools":1,"hidden_tool":` + hiddenTool + `}}`},
		{"tools as null", listPrefix + `{"tools":null,"hidden_tool":` + hiddenTool + `}}`},
		{"case-folded null tools after tools", listPrefix + `{"tools":` + bothTools + `,"TOOLS":null}}`},
		{"case-folded tools before tools", listPrefix + `{"Tools":[` + hiddenTool + `],"tools":[` + allowedTool + `]}}`},
		{"case-folded tools only", listPrefix + `{"TOOLS":` + bothTools + `}}`},
		{"duplicate tools", listPrefix + `{"tools":` + bothTools + `,"tools":null}}`},
		{"long s spelling of tools", listPrefix + `{"tools":` + bothTools + `,"tool` + longS + `":null}}`},
		{"case-folded result", listPrefix + `{"tools":` + bothTools + `},"RESULT":{"tools":null}}`},
		{"long s spelling of result", listPrefix + `{"tools":` + bothTools + `},"re` + longS + `ult":{"tools":null}}`},
		{"duplicate result", listPrefix + `{"tools":` + bothTools + `},"result":"done"}`},
		{"case-folded result only", `{"jsonrpc":"2.0","id":1,"RESULT":{"tools":` + bothTools + `}}`},
		{"null result before case-folded result", `{"jsonrpc":"2.0","id":1,"result":null,"RESULT":{"tools":` + bothTools + `}}`},
		{"case-folded name in a tool", listPrefix + `{"tools":[{"name":"allowed_tool","NAME":"hidden_tool"}]}}`},
		{"duplicate name in a tool", listPrefix + `{"tools":[{"name":"hidden_tool","name":"allowed_tool"}]}}`},
		{"case-folded description in a tool", listPrefix +
			`{"tools":[{"name":"allowed_tool","description":"visible","De` + longS + `cription":"hidden_tool"}]}}`},
		{"case-folded id", `{"jsonrpc":"2.0","id":1,"ID":2,"result":{"tools":` + bothTools + `}}`},
		{"batch", `[` + listPrefix + `{"tools":` + bothTools + `}}]`},
		{"trailing data after the list", listPrefix + `{"tools":` + bothTools + `}}x`},
		{"tool without a name", listPrefix + `{"tools":[` + allowedTool + `,{"description":"hidden_tool"}]}}`},
		{"NaN after the list", nanList},
		{"Infinity after the list", listPrefix + `{"tools":` + bothTools + `},"x":Infinity}`},
		{"negative Infinity after the list", listPrefix + `{"tools":` + bothTools + `},"x":-Infinity}`},
		{"nesting deeper than encoding/json reads", listPrefix + `{"tools":` + bothTools + `},"x":` +
			strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`},
		{"no-break space before the list", nbsp + listPrefix + `{"tools":` + bothTools + `}}`},
		{"no-break space before a list with NaN", nbsp + nanList},
		{"byte order marks before a list with NaN", bom + bom + nanList},
		{"truncated list", listPrefix + `{"tools":` + bothTools},
		{"truncated tools/call result", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hidden_tool"}`},
		{"NaN in a tools/call result", `{"jsonrpc":"2.0","id":1,"result":{"content":[],"hidden_tool":NaN}}`},
	}

	for _, transport := range toolsListTransports {
		for _, tc := range payloads {
			t.Run(transport.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				recorder := serveListResponse(t, []ToolMiddlewareOption{WithToolsFilter("allowed_tool")},
					http.StatusOK, transport.contentType, transport.render(tc.payload))
				assertWithheld(t, recorder, transport.contentType == "text/event-stream", withheldError)
			})
		}
	}
}

// TestListToolsFilterEventStreamFraming covers event streams whose tools list
// only appears once lines and data values are assembled the way clients
// assemble them. A list the filter cannot resolve is withheld, and the client
// receives an error in its place.
func TestListToolsFilterEventStreamFraming(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	// Split the list where JSON allows white space, so that clients that
	// join the pieces with LF, or split lines at LF only, read a valid list.
	const splitAt = `{"jsonrpc":"2.0","id":1,`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	// toolHead and toolTail are the two lines of a tool a backend sends over
	// two data lines.
	const (
		toolHead = `{"name":"allowed_tool",`
		toolTail = `"inputSchema":{"type":"object"}}`
	)
	// A no-break space is not JSON white space, so the LF join of these data
	// values is not valid JSON, while a client that trims each value of
	// Unicode white space before joining, as the Go MCP SDK does, reads the
	// list.
	nbspSplitList := "event: message\ndata: " + list[:len(splitAt)] + "\u00a0\ndata: " + list[len(splitAt):] + "\n\n"

	tests := []struct {
		name        string
		contentType string
		body        string
		expected    string
	}{
		{
			name:        "list split across data lines",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + list[:len(splitAt)] + "\ndata: " + list[len(splitAt):] + "\n\n",
			expected:    "event: message\ndata: " + filtered + "\n\n",
		},
		{
			name:     "list split across data lines without content type",
			body:     "event: message\ndata: " + list[:len(splitAt)] + "\ndata: " + list[len(splitAt):] + "\n\n",
			expected: "event: message\ndata: " + filtered + "\n\n",
		},
		{
			name:        "list split across data lines with a no-break space",
			contentType: "text/event-stream",
			body:        nbspSplitList,
			expected:    sseEvent(withheldError),
		},
		{
			name:     "list split across data lines with a no-break space without content type",
			body:     nbspSplitList,
			expected: withheldError,
		},
		{
			name:        "LF lines ending in a CRLF blank line",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + list + "\r\n\r\n",
			expected:    "event: message\ndata: " + filtered + "\r\n\r\n",
		},
		{
			name:        "event the stream ends without terminating",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + list,
			expected:    "event: message\ndata: " + filtered,
		},
		{
			// A tool whose members span two data lines, as a backend that
			// pretty-prints its JSON sends it; the line break is white space
			// outside a string, and the kept tool keeps it.
			name:        "tool split across data lines",
			contentType: "text/event-stream",
			body: "event: message\ndata: " + listPrefix + `{"tools":[` + toolHead + "\ndata: " + toolTail + `,` +
				hiddenTool + `]}}` + "\n\n",
			expected: "event: message\ndata: " + listPrefix + `{"tools":[` + toolHead + "\ndata: " + toolTail + `]}}` + "\n\n",
		},
		{
			name:        "tool split across CRLF data lines",
			contentType: "text/event-stream",
			body: "event: message\r\ndata: " + listPrefix + `{"tools":[` + toolHead + "\r\ndata: " + toolTail + `,` +
				hiddenTool + `]}}` + "\r\n\r\n",
			expected: "event: message\r\ndata: " + listPrefix + `{"tools":[` + toolHead + "\r\ndata: " + toolTail + `]}}` +
				"\r\n\r\n",
		},
		{
			name:        "result member split across data lines",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + listPrefix + `{"tools":` + bothTools + `,"_meta":{"a":` + "\ndata: 1}}}\n\n",
			expected:    "event: message\ndata: " + listPrefix + `{"tools":[` + allowedTool + `],"_meta":{"a":` + "\ndata: 1}}}\n\n",
		},
		{
			// An empty data line inside a value comes back as a data line
			// with an empty value, never as a blank line, which would end
			// the event.
			name:        "empty data line inside a value",
			contentType: "text/event-stream",
			body:        "data:" + listPrefix + `{"tools":` + bothTools + `,"_meta":{"a":` + "\ndata:\ndata: 1}}}\n\n",
			expected:    "data: " + listPrefix + `{"tools":[` + allowedTool + `],"_meta":{"a":` + "\ndata: \ndata: 1}}}\n\n",
		},
		{
			name:        "case-folded tools in an event the stream ends without terminating",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + listPrefix + `{"TOOLS":` + bothTools + `}}`,
			expected:    sseEvent(withheldError),
		},
		{
			name:        "bare CR between fields",
			contentType: "text/event-stream",
			body:        "event: message\rdata: " + list + "\n\n",
			expected:    sseEvent(withheldError),
		},
		{
			name:        "bare CR inside the data",
			contentType: "text/event-stream",
			body:        "data: " + list[:len(splitAt)] + "\r" + list[len(splitAt):] + "\n\n",
			expected:    sseEvent(withheldError),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			received := serveThroughListFilter(t, tt.contentType, tt.body)
			assert.Equal(t, tt.expected, received)
			assert.NotContains(t, received, "hidden_tool")
		})
	}
}

// testLineSeparators are the characters, beside CR and LF, at which some
// clients end a line of an event stream, as Python's str.splitlines() does.
// NEL, U+2028 and U+2029 are the ones valid JSON can hold raw inside a
// string; the five control characters cannot appear there unescaped.
var testLineSeparators = []struct {
	name      string
	separator string
	rawInJSON bool
}{
	{name: "VT", separator: "\v"},
	{name: "FF", separator: "\f"},
	{name: "FS", separator: "\x1c"},
	{name: "GS", separator: "\x1d"},
	{name: "RS", separator: "\x1e"},
	{name: "NEL", separator: "\u0085", rawInJSON: true},
	{name: "LINE SEPARATOR", separator: "\u2028", rawInJSON: true},
	{name: "PARAGRAPH SEPARATOR", separator: "\u2029", rawInJSON: true},
}

// TestListToolsFilterWithholdsStreamsWithOtherLineSeparators covers event
// streams that hold one of testLineSeparators. A client that ends a line at
// it reads a tools list where the filter reads a comment, a field, or data
// that is not a list, so a stream that could carry a list under that
// reading is withheld, as one with a bare CR is, and the client receives an
// error in its place. A list whose own JSON holds one of those characters
// raw inside a string is withheld too, since that client reads a truncated
// value there; the escaped form of the character is text to every client,
// and a JSON body has no lines to split, so both are filtered as usual.
func TestListToolsFilterWithholdsStreamsWithOtherLineSeparators(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	// Each shape holds the separator where sep stands, in a line that ends
	// an event for the filter only at the LF after it.
	const sep = "<sep>"
	shapes := []struct {
		name string
		body string
	}{
		{name: "comment line", body: ": x" + sep + "data: " + list + "\n\n"},
		{name: "empty data value", body: "data:" + sep + "data: " + list + "\n\n"},
		{name: "two separators", body: "data: x" + sep + sep + "data: " + list + "\n\n"},
		{name: "separator before LF", body: "data: x" + sep + "\ndata: " + list + "\n\n"},
		{name: "id field", body: "id: 7" + sep + "data: " + list + "\n\n"},
	}
	// withText returns the list with text inside the description of the
	// tool the filter keeps, and withFiltered that list filtered.
	withText := func(text string) string {
		return listPrefix + `{"tools":[{"name":"allowed_tool","description":"a` + text + `b"},` + hiddenTool + `]}}`
	}
	withFiltered := func(text string) string {
		return listPrefix + `{"tools":[{"name":"allowed_tool","description":"a` + text + `b"}]}}`
	}

	for _, separator := range testLineSeparators {
		for _, shape := range shapes {
			t.Run(shape.name+" with "+separator.name, func(t *testing.T) {
				t.Parallel()

				body := strings.ReplaceAll(shape.body, sep, separator.separator)
				received := serveThroughListFilter(t, "text/event-stream", body)
				assert.Equal(t, sseEvent(withheldError), received)
				assert.NotContains(t, received, "hidden_tool")
			})
		}
		if !separator.rawInJSON {
			continue
		}
		t.Run("raw "+separator.name+" inside a description", func(t *testing.T) {
			t.Parallel()

			received := serveThroughListFilter(t, "text/event-stream", sseEvent(withText(separator.separator)))
			assert.Equal(t, sseEvent(withheldError), received)
		})
		t.Run("raw "+separator.name+" inside a description of a JSON body", func(t *testing.T) {
			t.Parallel()

			recorder := serveListResponse(t, []ToolMiddlewareOption{WithToolsFilter("allowed_tool")},
				http.StatusOK, "application/json", withText(separator.separator))
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, withFiltered(separator.separator)+"\n", recorder.Body.String())
		})
	}

	t.Run("escaped LINE SEPARATOR inside a description", func(t *testing.T) {
		t.Parallel()

		received := serveThroughListFilter(t, "text/event-stream", sseEvent(withText(`\u2028`)))
		assert.Equal(t, sseEvent(withFiltered(`\u2028`)), received)
	})
}

// TestListToolsFilterPassesOtherResultsWithRawLineSeparators covers events
// that are not tools lists and hold NEL, U+2028 or U+2029 raw inside a JSON
// string, as Python servers emit them. Splitting at the separator cuts the
// string, leaving a fragment that never closes, from which no client reads a
// list, so the event passes unchanged. Data that does close, such as a list
// holding NaN in an event that only the split creates, is still withheld.
func TestListToolsFilterPassesOtherResultsWithRawLineSeparators(t *testing.T) {
	t.Parallel()

	nanList := listPrefix + `{"tools":` + bothTools + `},"x":NaN}`
	for _, separator := range testLineSeparators {
		if !separator.rawInJSON {
			continue
		}
		t.Run("tool result with "+separator.name, func(t *testing.T) {
			t.Parallel()

			event := sseEvent(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"a` +
				separator.separator + `b"}]}}`)
			assert.Equal(t, event, serveThroughListFilter(t, "text/event-stream", event))
		})
		t.Run("notification with two "+separator.name, func(t *testing.T) {
			t.Parallel()

			event := sseEvent(`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"a` +
				separator.separator + separator.separator + `b"}}`)
			assert.Equal(t, event, serveThroughListFilter(t, "text/event-stream", event))
		})
		t.Run("list holding NaN in its own event after two "+separator.name, func(t *testing.T) {
			t.Parallel()

			body := "data: x" + separator.separator + separator.separator + "data: " + nanList + "\n\n"
			received := serveThroughListFilter(t, "text/event-stream", body)
			assert.Equal(t, sseEvent(withheldError), received)
			assert.NotContains(t, received, "hidden_tool")
		})
	}
}

func TestJSONContainerCloses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "object", data: `{"a":1}`, want: true},
		{name: "array after BOM and space", data: "\ufeff  [1,[2]]", want: true},
		{name: "closes before trailing bytes", data: `{"a":1}}`, want: true},
		{name: "cut inside a string", data: `{"a":"x`, want: false},
		{name: "brackets inside a string", data: `{"a":"}]"`, want: false},
		{name: "escaped quote inside a string", data: `{"a":"\"}"`, want: false},
		{name: "escaped backslash ends the string", data: `{"a":"\\"}`, want: true},
		{name: "unclosed nested", data: `{"a":[1,2}`, want: false},
		{name: "not a container", data: `"x"`, want: false},
		{name: "empty", data: ``, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, jsonContainerCloses([]byte(tt.data)))
		})
	}
}

// TestListToolsFilterStreamedResponses covers a list that arrives across
// several writes and flushes: nothing of it may reach the client before it is
// complete and filtered. A body without a Content-Type is held until the
// response ends, since a part of a list does not look like one.
func TestListToolsFilterStreamedResponses(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	const splitAt = `{"jsonrpc":"2.0","id":1,`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`

	tests := []struct {
		name        string
		contentType string
		chunks      []string
		expected    string
	}{
		{
			name:        "event split across flushes",
			contentType: "text/event-stream",
			chunks: []string{
				"event: message\ndata: " + list[:len(splitAt)] + "\n",
				"data: " + list[len(splitAt):] + "\n",
				"\n",
			},
			expected: sseEvent(filtered),
		},
		{
			name:     "event split across flushes without content type",
			chunks:   []string{"event: message\ndata: " + list[:len(splitAt)], list[len(splitAt):] + "\n\n"},
			expected: sseEvent(filtered),
		},
		{
			name:     "JSON body split across flushes without content type",
			chunks:   []string{list[:len(splitAt)], list[len(splitAt):]},
			expected: filtered + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			afterFlush, received := streamThroughListFilter(t, tt.contentType, tt.chunks)
			for _, beforeEnd := range afterFlush[:len(afterFlush)-1] {
				assert.Empty(t, beforeEnd, "an incomplete list must stay buffered")
			}
			assert.Equal(t, tt.expected, received)
		})
	}
}

// TestListToolsFilterDeliversEventsAtFlush checks when an event reaches the
// client: as soon as the blank line that ends it has been flushed, when that
// line ends in LF or CRLF. An event stream whose lines end in a bare CR alone
// or in RS alone, and an event stream without a Content-Type, are held until
// the response ends.
func TestListToolsFilterDeliversEventsAtFlush(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	lfEvent := "event: message\ndata: " + progressNotification + "\n\n"
	crlfEvent := "event: message\r\ndata: " + progressNotification + "\r\n\r\n"
	crEvent := "event: message\rdata: " + progressNotification + "\r\r"
	rsEvent := "event: message\x1edata: " + progressNotification + "\x1e\x1e"

	tests := []struct {
		name        string
		contentType string
		body        string
		atFlush     string
		atEnd       string
	}{
		{name: "LF lines", contentType: "text/event-stream", body: lfEvent, atFlush: lfEvent, atEnd: lfEvent},
		{name: "CRLF lines", contentType: "text/event-stream", body: crlfEvent, atFlush: crlfEvent, atEnd: crlfEvent},
		{
			name:        "tools list",
			contentType: "text/event-stream",
			body:        sseEvent(list),
			atFlush:     sseEvent(filtered),
			atEnd:       sseEvent(filtered),
		},
		{name: "media type in another case", contentType: "Text/Event-Stream", body: lfEvent, atFlush: lfEvent, atEnd: lfEvent},
		{name: "bare CR lines", contentType: "text/event-stream", body: crEvent, atEnd: crEvent},
		{name: "RS lines", contentType: "text/event-stream", body: rsEvent, atEnd: rsEvent},
		{name: "without content type", body: lfEvent, atEnd: lfEvent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			afterFlush, received := streamThroughListFilter(t, tt.contentType, []string{tt.body})
			assert.Equal(t, tt.atFlush, afterFlush[0], "received after the flush")
			assert.Equal(t, tt.atEnd, received, "received once the response ended")
		})
	}
}

// TestListToolsFilterReadsAStreamInPieces feeds an event stream to the filter
// in pieces, with a flush after each: one byte at a time, and in two pieces
// split after every byte. Line terminators and blank lines then arrive apart
// from the bytes before them, and the bytes kept after an event is delivered
// can already hold part of the next blank line. Each event must reach the
// client as soon as its blank line is complete, and the tools list only
// filtered and whole. The last event is shorter than the one before it, so
// that it ends within as many bytes as the filter has just delivered.
func TestListToolsFilterReadsAStreamInPieces(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`

	for _, terminator := range []string{"\n", "\r\n"} {
		end := terminator + terminator
		events := []struct{ sent, received string }{
			{sent: "data: " + progressNotification + end, received: "data: " + progressNotification + end},
			{
				sent:     "event: message" + terminator + "data: " + list + end,
				received: "event: message" + terminator + "data: " + filtered + end,
			},
			{sent: "data: {}" + end, received: "data: {}" + end},
		}
		stream := ""
		for _, event := range events {
			stream += event.sent
		}
		// deliveredWithin returns what the client should have received once
		// the first n bytes of the stream have been flushed.
		deliveredWithin := func(n int) string {
			delivered, sent := "", 0
			for _, event := range events {
				sent += len(event.sent)
				if n >= sent {
					delivered += event.received
				}
			}
			return delivered
		}

		t.Run(fmt.Sprintf("%q byte by byte", terminator), func(t *testing.T) {
			t.Parallel()

			chunks := make([]string, len(stream))
			for i := range stream {
				chunks[i] = stream[i : i+1]
			}
			afterFlush, received := streamThroughListFilter(t, "text/event-stream", chunks)
			for i, got := range afterFlush {
				require.Equal(t, deliveredWithin(i+1), got, "received after byte %d", i)
			}
			assert.Equal(t, deliveredWithin(len(stream)), received)
		})

		t.Run(fmt.Sprintf("%q in two pieces", terminator), func(t *testing.T) {
			t.Parallel()

			for split := range len(stream) + 1 {
				afterFlush, received := streamThroughListFilter(t, "text/event-stream",
					[]string{stream[:split], stream[split:]})
				require.Equal(t, deliveredWithin(split), afterFlush[0], "received after the first %d bytes", split)
				require.Equal(t, deliveredWithin(len(stream)), afterFlush[1], "received after the stream split at %d", split)
				require.Equal(t, afterFlush[1], received)
			}
		})
	}
}

// withHeldBounds lowers the bounds on what the filter holds of a body it
// cannot read yet, so that tests reach them with small bodies.
func withHeldBounds(event, body int) ToolMiddlewareOption {
	return func(config *toolMiddlewareConfig) error {
		config.maxHeldEventBytes = event
		config.maxHeldBodyBytes = body
		return nil
	}
}

// The bounds the held-body tests lower the filter's to.
const (
	testEventBound = 1 << 20
	testBodyBound  = 4 << 20
)

// event returns an event of n bytes whose data is not a tools list.
func sseEventOfSize(n int) string {
	return "data: " + strings.Repeat("x", n-len("data: \n\n")) + "\n\n"
}

// jsonBodyOfSize returns a JSON-RPC reply of n bytes that is not a tools list.
func jsonBodyOfSize(n int) string {
	const prefix, suffix = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"`, `"}]}}`
	return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
}

// TestListToolsFilterLimitsHeldBodies checks that the filter holds at most a
// bound of a body it cannot read yet, one bound for an event stream and a
// larger one for a JSON body or a body held until the response ends, and that
// it forwards what it can read before counting what it holds. On an event
// stream only the event that would make it hold more is withheld, and the
// stream goes on; any other response is withheld from then on.
func TestListToolsFilterLimitsHeldBodies(t *testing.T) {
	t.Parallel()

	progressEvent := sseEvent(progressNotification)
	// halves splits body into two writes, so that the filter holds the first
	// when the second arrives.
	halves := func(body string) []string {
		return []string{body[:len(body)/2], body[len(body)/2:]}
	}
	// smallEvents returns complete events of 1 KiB each, totaling n bytes.
	smallEvents := func(n int) string {
		return strings.Repeat(sseEventOfSize(1<<10), n/(1<<10))
	}

	tests := []struct {
		name        string
		contentType string
		chunks      []string
		expected    string
		// status is the status the client receives, when it differs from
		// the backend's.
		status int
	}{
		{
			name:        "event as long as the event bound",
			contentType: "text/event-stream",
			chunks:      append([]string{progressEvent}, halves(sseEventOfSize(testEventBound))...),
			expected:    progressEvent + sseEventOfSize(testEventBound),
		},
		{
			name:        "event longer than the event bound",
			contentType: "text/event-stream",
			chunks:      append(append([]string{progressEvent}, halves(sseEventOfSize(testEventBound+1))...), progressEvent),
			expected:    progressEvent + progressEvent,
		},
		{
			name:        "complete events as long as the event bound in one write",
			contentType: "text/event-stream",
			chunks:      []string{smallEvents(testEventBound)},
			expected:    smallEvents(testEventBound),
		},
		{
			name:        "complete events held with the start of the next past the event bound",
			contentType: "text/event-stream",
			chunks:      []string{smallEvents(testEventBound - 1<<10), sseEventOfSize(2 << 10)},
			expected:    smallEvents(testEventBound-1<<10) + sseEventOfSize(2<<10),
		},
		{
			name:        "JSON body longer than the event bound",
			contentType: "application/json",
			chunks:      halves(jsonBodyOfSize(testEventBound + 1)),
			expected:    jsonBodyOfSize(testEventBound + 1),
		},
		{
			name:        "JSON body as long as the body bound",
			contentType: "application/json",
			chunks:      halves(jsonBodyOfSize(testBodyBound)),
			expected:    jsonBodyOfSize(testBodyBound),
		},
		{
			name:        "JSON body longer than the body bound",
			contentType: "application/json",
			chunks:      halves(jsonBodyOfSize(testBodyBound + 1)),
			expected:    withheldErrorWithoutID,
			status:      http.StatusInternalServerError,
		},
		{
			name:     "body without content type longer than the event bound",
			chunks:   halves(sseEventOfSize(testEventBound + 1)),
			expected: sseEventOfSize(testEventBound + 1),
		},
		{
			name:     "body without content type longer than the body bound",
			chunks:   halves(sseEventOfSize(testBodyBound + 1)),
			expected: withheldErrorWithoutID,
			status:   http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, recorder := streamListResponse(t, tt.contentType, tt.chunks,
				withHeldBounds(testEventBound, testBodyBound))
			received := recorder.Body.String()
			assert.Equal(t, cmp.Or(tt.status, http.StatusOK), recorder.Code)
			assert.Equal(t, len(tt.expected), len(received))
			assert.True(t, received == tt.expected, "received body differs from the expected one")
		})
	}
}

// TestListToolsFilterKeepsAStreamOpenPastAnOversizedEvent checks that an
// event that would make the filter hold more of an event stream than it may
// is withheld alone: an error event takes its place at once when the start
// the filter holds of the event names the request it answers, with exactly
// one id beside a result or an error and no method, or else when the request
// that opened the stream has an id, and the events after it reach the client,
// read as events of their own under every rule, however the blank line that
// ends the withheld event is split across writes. An event that names no
// request on a stream a GET request opened is dropped without a replacement.
func TestListToolsFilterKeepsAStreamOpenPastAnOversizedEvent(t *testing.T) {
	t.Parallel()

	progressEvent := sseEvent(progressNotification)
	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	padding := strings.Repeat("x", testEventBound)
	// oversizedReply returns a reply to request id, as an event longer than
	// the event bound.
	oversizedReply := func(id string) string {
		return sseEvent(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[{"type":"text","text":"` + padding + `"}]}}`)
	}
	errorEvent := func(id string) string {
		return sseEvent(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32603,"message":"internal error"}}`)
	}
	replyEvent := func(id string) string {
		return sseEvent(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[]}}`)
	}
	halves := func(body string) []string {
		return []string{body[:len(body)/2], body[len(body)/2:]}
	}

	tests := []struct {
		name     string
		chunks   []string
		expected string
	}{
		{
			name:     "blank line split between a CR and its LF",
			chunks:   []string{"data: " + padding, "\r\n\r", "\n" + progressEvent},
			expected: progressEvent,
		},
		{
			name:     "blank line split after its first LF",
			chunks:   []string{"data: " + padding + "\n", "\n" + progressEvent},
			expected: progressEvent,
		},
		{
			name:     "oversized event then an event whose lines end in a bare CR",
			chunks:   []string{sseEventOfSize(testEventBound + 1), "data: " + list + "\r\r"},
			expected: "",
		},
		{
			name: "oversized list hiding a tool then a list",
			chunks: []string{
				"event: message\ndata: " + listPrefix + `{"tools":[` + hiddenTool + `,{"name":"pad","description":"` + padding + `"}]}}` + "\n\n",
				sseEvent(list),
			},
			expected: sseEvent(filtered),
		},
		{
			name:     "reply whose start names its request",
			chunks:   append(halves(oversizedReply("7")), progressEvent),
			expected: errorEvent("7") + progressEvent,
		},
		{
			name:     "reply whose start names its request by a string",
			chunks:   append(halves(oversizedReply(`"abc"`)), progressEvent),
			expected: errorEvent(`"abc"`) + progressEvent,
		},
		{
			name:     "reply whose start names its request then another reply",
			chunks:   append(halves(oversizedReply("5")), replyEvent("2")),
			expected: errorEvent("5") + replyEvent("2"),
		},
		{
			name: "request whose start names its method",
			chunks: append(halves(sseEvent(`{"jsonrpc":"2.0","id":3,"method":"sampling/createMessage",`+
				`"params":{"messages":[{"role":"user","content":{"type":"text","text":"`+padding+`"}}]}}`)), replyEvent("2")),
			expected: replyEvent("2"),
		},
		{
			name: "start with two ids",
			chunks: append(halves(sseEvent(`{"jsonrpc":"2.0","id":5,"id":6,"result":{"content":[{"type":"text","text":"`+padding+`"}]}}`)),
				progressEvent),
			expected: progressEvent,
		},
		{
			name: "start with an id in another case",
			chunks: append(halves(sseEvent(`{"jsonrpc":"2.0","ID":5,"result":{"content":[{"type":"text","text":"`+padding+`"}]}}`)),
				progressEvent),
			expected: progressEvent,
		},
		{
			name: "start whose id follows its result",
			chunks: append(halves(sseEvent(`{"jsonrpc":"2.0","result":{"content":[]},"id":5,"_meta":{"note":"`+padding+`"}}`)),
				progressEvent),
			expected: errorEvent("5") + progressEvent,
		},
		{
			name:     "start without a result or an error",
			chunks:   append(halves(sseEvent(`{"jsonrpc":"2.0","id":5,"params":{"text":"`+padding+`"}}`)), progressEvent),
			expected: progressEvent,
		},
		{
			name: "event whose id is not at the top level",
			chunks: append(halves(sseEvent(`{"jsonrpc":"2.0","result":{"id":7,"content":[{"type":"text","text":"`+padding+`"}]}}`)),
				progressEvent),
			expected: progressEvent,
		},
		{
			name:     "event in one write with its blank line",
			chunks:   []string{sseEventOfSize(testEventBound+1) + progressEvent},
			expected: progressEvent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, received := streamThroughListFilter(t, "text/event-stream", tt.chunks,
				withHeldBounds(testEventBound, testBodyBound))
			assert.Equal(t, len(tt.expected), len(received))
			assert.True(t, received == tt.expected, "received body differs from the expected one")
		})
	}

	// On a stream that answers a POST request, the error names the request
	// the start of the withheld event names, and otherwise the one the POST
	// made.
	posts := []struct {
		name     string
		request  string
		chunks   []string
		expected string
	}{
		{
			name:     "start that names no request on a stream a POST opened",
			request:  `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
			chunks:   append(halves(sseEventOfSize(testEventBound+1)), replyEvent("1")),
			expected: sseEvent(withheldError) + replyEvent("1"),
		},
		{
			name:     "start that names another request on a stream a POST opened",
			request:  `{"jsonrpc":"2.0","id":9,"method":"tools/call"}`,
			chunks:   append(halves(oversizedReply("4")), replyEvent("9")),
			expected: errorEvent("4") + replyEvent("9"),
		},
	}

	for _, tt := range posts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.request))
			_, recorder := streamListResponseTo(t, request, "text/event-stream", tt.chunks,
				withHeldBounds(testEventBound, testBodyBound))
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, tt.expected, recorder.Body.String())
		})
	}
}

// TestListToolsFilterForwardsCompleteEventsBeforeCounting checks that the
// complete events a handler has written without a flush are forwarded, not
// held against the event bound, when the next write would otherwise make the
// filter hold more than the bound.
func TestListToolsFilterForwardsCompleteEventsBeforeCounting(t *testing.T) {
	t.Parallel()

	events := strings.Repeat(sseEventOfSize(1<<10), testEventBound/(1<<10)-1)
	next := sseEventOfSize(2 << 10)
	tests := []struct {
		name   string
		chunks []string
	}{
		{name: "next event in one write", chunks: []string{events, next}},
		{name: "next event split in two writes", chunks: []string{events, next[:1<<10], next[1<<10:]}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"),
				withHeldBounds(testEventBound, testBodyBound))
			require.NoError(t, err)

			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				for _, chunk := range tt.chunks {
					_, writeErr := w.Write([]byte(chunk))
					require.NoError(t, writeErr)
				}
			})
			recorder := httptest.NewRecorder()
			middleware(backend).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sse", nil))

			assert.Equal(t, len(events)+len(next), recorder.Body.Len())
			assert.True(t, recorder.Body.String() == events+next, "received body differs from the one sent")
		})
	}
}

// TestListToolsFilterPassesAnUnreadBodyPastTheBounds checks that a body the
// filter passes through unread, a non-2xx body of an unsupported type, is not
// held against either bound, however it is written: in many writes without a
// flush, as a handler that never flushes writes it.
func TestListToolsFilterPassesAnUnreadBodyPastTheBounds(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", testBodyBound+1)
	middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"),
		withHeldBounds(testEventBound, testBodyBound))
	require.NoError(t, err)

	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		for off := 0; off < len(body); off += 32 << 10 {
			_, writeErr := w.Write([]byte(body[off:min(off+32<<10, len(body))]))
			require.NoError(t, writeErr)
		}
	})
	recorder := httptest.NewRecorder()
	middleware(backend).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, len(body), recorder.Body.Len())
	assert.True(t, recorder.Body.String() == body, "received body differs from the one sent")
}

// TestListToolsFilterBoundsAJSONBodyThroughAProxy checks the bound on a JSON
// body in the shape the transparent proxy delivers a backend reply: through
// httputil.ReverseProxy with a flush after every write. A reply as long as the
// bound reaches the client whole; one byte more is withheld and answered with
// an error.
func TestListToolsFilterBoundsAJSONBodyThroughAProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		size     int
		status   int
		expected string
	}{
		{
			name:     "as long as the body bound",
			size:     testBodyBound,
			status:   http.StatusOK,
			expected: jsonBodyOfSize(testBodyBound),
		},
		{
			name:     "longer than the body bound",
			size:     testBodyBound + 1,
			status:   http.StatusInternalServerError,
			expected: withheldError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := jsonBodyOfSize(tt.size)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				for off := 0; off < len(body); off += 32 << 10 {
					_, writeErr := w.Write([]byte(body[off:min(off+32<<10, len(body))]))
					require.NoError(t, writeErr)
				}
			}))
			t.Cleanup(backend.Close)

			target, err := url.Parse(backend.URL)
			require.NoError(t, err)
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.FlushInterval = -1
			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"),
				withHeldBounds(testEventBound, testBodyBound))
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/read"}`))
			middleware(proxy).ServeHTTP(recorder, request)

			assert.Equal(t, tt.status, recorder.Code)
			assert.Equal(t, len(tt.expected), recorder.Body.Len())
			assert.True(t, recorder.Body.String() == tt.expected, "received body differs from the expected one")
		})
	}
}

// TestListToolsFilterKeepsEventsAroundAWithheldOne checks that an event the
// filter withholds is withheld alone: the client receives an error event
// answering the request the event answers, named by the event's own id, in
// its place and the events before and after it, including an event whose
// start arrives in the same flush as the withheld one. A stream with a line
// that holds a bare CR or another separator only some clients split lines
// at, which clients split into events differently, is withheld up to the end
// of its last complete event with nothing in its place, since the GET
// request that opened the stream names no request to answer, and the events
// after that still reach the client. A body without a Content-Type is
// answered with an error in place of the whole response, since the filter
// forwards its status only once it has read the body.
func TestListToolsFilterKeepsEventsAroundAWithheldOne(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	const splitAt = `{"jsonrpc":"2.0","id":1,`
	progressEvent := sseEvent(progressNotification)
	withheldEvent := sseEvent(listPrefix + `{"TOOLS":` + bothTools + `}}`)
	errorEvent := sseEvent(withheldError)
	messageEvent := sseEvent(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"done"}}`)
	messageHead, messageTail := messageEvent[:len(messageEvent)/2], messageEvent[len(messageEvent)/2:]
	pingEvent := sseEvent(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	bareCREvent := "data: " + list[:len(splitAt)] + "\r" + list[len(splitAt):] + "\n\n"
	separatorEvent := "data: " + list[:len(splitAt)] + "\u2028" + list[len(splitAt):] + "\n\n"

	tests := []struct {
		name        string
		contentType string
		chunks      []string
		// afterFlush is what the client has received after each chunk's
		// flush, and expected what it has once the response has ended.
		afterFlush []string
		expected   string
		// status and receivedType are the status and Content-Type the client
		// receives, when they differ from the backend's.
		status       int
		receivedType string
	}{
		{
			name:        "withheld event followed by the start of another",
			contentType: "text/event-stream",
			chunks:      []string{progressEvent + withheldEvent + messageHead, messageTail, pingEvent},
			afterFlush: []string{
				progressEvent + errorEvent,
				progressEvent + errorEvent + messageEvent,
				progressEvent + errorEvent + messageEvent + pingEvent,
			},
			expected: progressEvent + errorEvent + messageEvent + pingEvent,
		},
		{
			name:        "withheld events between complete events",
			contentType: "text/event-stream",
			chunks:      []string{progressEvent + withheldEvent + messageEvent + withheldEvent + pingEvent},
			afterFlush:  []string{progressEvent + errorEvent + messageEvent + errorEvent + pingEvent},
			expected:    progressEvent + errorEvent + messageEvent + errorEvent + pingEvent,
		},
		{
			name:         "withheld event without content type",
			chunks:       []string{progressEvent + withheldEvent + messageHead, messageTail},
			afterFlush:   []string{"", ""},
			expected:     withheldErrorWithoutID,
			status:       http.StatusInternalServerError,
			receivedType: "application/json",
		},
		{
			name:        "stream with a line ending in a bare CR",
			contentType: "text/event-stream",
			chunks:      []string{progressEvent + bareCREvent, pingEvent},
			afterFlush:  []string{"", pingEvent},
			expected:    pingEvent,
		},
		{
			name:        "stream with a line holding a separator",
			contentType: "text/event-stream",
			chunks:      []string{progressEvent + separatorEvent, pingEvent},
			afterFlush:  []string{"", pingEvent},
			expected:    pingEvent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			afterFlush, recorder := streamListResponse(t, tt.contentType, tt.chunks)
			response := recorder.Result()
			require.NoError(t, response.Body.Close())
			assert.Equal(t, tt.afterFlush, afterFlush)
			assert.Equal(t, tt.expected, recorder.Body.String())
			assert.Equal(t, cmp.Or(tt.status, http.StatusOK), response.StatusCode)
			assert.Equal(t, cmp.Or(tt.receivedType, tt.contentType), response.Header.Get("Content-Type"))
			assert.NotContains(t, recorder.Body.String(), "hidden_tool")
		})
	}
}

// TestCompleteSSEEvents checks the complete part of an event stream, and that
// a read that resumes where an earlier read of the stream stopped finds the
// same events wherever the stream was split, including between the CR and LF
// of a line terminator and between the terminators of a blank line.
func TestCompleteSSEEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stream   string
		complete int
	}{
		{name: "LF event", stream: "event: message\ndata: a\n\n", complete: 24},
		{name: "CRLF event", stream: "event: message\r\ndata: a\r\n\r\n", complete: 27},
		{name: "CRLF line ending in an LF blank line", stream: "data: a\r\n\n", complete: 10},
		{name: "LF line ending in a CRLF blank line", stream: "data: a\n\r\n", complete: 10},
		{name: "blank lines first", stream: "\n\r\ndata: a\n", complete: 3},
		{name: "complete event and the start of another", stream: "data: a\n\ndata: b\n", complete: 9},
		{name: "two events", stream: "data: a\n\ndata: b\r\n\r\n", complete: 20},
		{name: "bare CR lines", stream: "data: a\r\rdata: b\r\r", complete: 0},
		{name: "bare CR before a line's CRLF", stream: "data: a\r\r\n", complete: 0},
		{name: "RS lines", stream: "data: a\x1e\x1edata: b\x1e\x1e", complete: 0},
		{name: "unterminated line", stream: "data: a", complete: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stream := []byte(tt.stream)
			require.Equal(t, tt.complete, completeSSEEvents(stream, 0))
			for split := range len(stream) + 1 {
				// The first read sees the stream up to split. The complete
				// part is then dropped, and the second read resumes after the
				// rest of what the first one read.
				first := completeSSEEvents(stream[:split], 0)
				second := completeSSEEvents(stream[first:], split-first)
				assert.Equal(t, tt.complete, first+second, "stream split after %d bytes", split)
			}
		})
	}
}

// streamThroughListFilter sends chunks through the tools/list filter,
// allowing only allowed_tool, with a flush after each chunk, the way the
// transparent proxy forwards a backend response. It returns what the client
// has received after each flush, and what it has received once the response
// has ended.
func streamThroughListFilter(
	t *testing.T, contentType string, chunks []string, opts ...ToolMiddlewareOption,
) ([]string, string) {
	t.Helper()

	afterFlush, recorder := streamListResponse(t, contentType, chunks, opts...)
	return afterFlush, recorder.Body.String()
}

// streamListResponse is streamThroughListFilter returning the response the
// client receives once the response has ended. The response answers a GET
// request without a body.
func streamListResponse(
	t *testing.T, contentType string, chunks []string, opts ...ToolMiddlewareOption,
) ([]string, *httptest.ResponseRecorder) {
	t.Helper()

	return streamListResponseTo(t, httptest.NewRequest(http.MethodGet, "/sse", nil), contentType, chunks, opts...)
}

// streamListResponseTo is streamListResponse answering request.
func streamListResponseTo(
	t *testing.T, request *http.Request, contentType string, chunks []string, opts ...ToolMiddlewareOption,
) ([]string, *httptest.ResponseRecorder) {
	t.Helper()

	middleware, err := NewListToolsMappingMiddleware(append([]ToolMiddlewareOption{WithToolsFilter("allowed_tool")}, opts...)...)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	afterFlush := make([]string, 0, len(chunks))
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		for _, chunk := range chunks {
			_, writeErr := w.Write([]byte(chunk))
			require.NoError(t, writeErr)
			flusher.Flush()
			afterFlush = append(afterFlush, recorder.Body.String())
		}
	})

	middleware(backend).ServeHTTP(recorder, request)
	return afterFlush, recorder
}

// TestListToolsFilterMatchesMediaTypesCaseInsensitively checks that a
// Content-Type in another case, or with parameters, is read as the media type
// it names rather than as an unrecognized one.
func TestListToolsFilterMatchesMediaTypesCaseInsensitively(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`

	tests := []struct {
		status      int
		contentType string
		body        string
		expected    string
	}{
		{status: http.StatusOK, contentType: "Application/JSON", body: list, expected: filtered + "\n"},
		{status: http.StatusOK, contentType: "application/json ; charset=UTF-8", body: list, expected: filtered + "\n"},
		{status: http.StatusOK, contentType: "Text/Event-Stream", body: sseEvent(list), expected: sseEvent(filtered)},
		// A body with a supported media type is read at any status, unlike
		// one whose Content-Type is not recognized.
		{status: http.StatusFound, contentType: "Application/JSON", body: list, expected: filtered + "\n"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d %s", tt.status, tt.contentType), func(t *testing.T) {
			t.Parallel()

			received := serveThroughListMiddleware(t, []ToolMiddlewareOption{WithToolsFilter("allowed_tool")},
				tt.status, tt.contentType, tt.body)
			assert.Equal(t, tt.expected, received)
		})
	}
}

// TestListToolsFilterReadsAmbiguousContentTypesAtTheEnd checks that a body
// whose Content-Type clients could read as either MCP media type, because the
// response carries more than one Content-Type or its one value names the
// other media type too, is held until the response ends and then filtered as
// whichever format it holds, at any status. A Content-Type that names one
// media type, with or without parameters, is still read as that media type
// as the body arrives.
// TestListToolsFilterReadsAJSONBodyInPieces verifies that a JSON body the
// backend writes and flushes in pieces reaches the client at the flush that
// completes it and not before, however the pieces fall: inside a string that
// holds braces or brackets, around an escape, or in a scalar body.
func TestListToolsFilterReadsAJSONBodyInPieces(t *testing.T) {
	t.Parallel()

	// bracesTool holds, inside a string, enough closing braces to close every
	// container open around it, behind an escaped quote and before an escaped
	// backslash.
	const bracesTool = `{"name":"allowed_tool","description":"}}}}]]{[ \"}}}}\" \\"}`
	bracesList := listPrefix + `{"tools":[` + bracesTool + `,` + hiddenTool + `]}}`
	bracesFiltered := listPrefix + `{"tools":[` + bracesTool + `]}}`
	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	// split returns body in two chunks, the second starting at index at.
	split := func(body string, at int) []string {
		return []string{body[:at], body[at:]}
	}

	tests := []struct {
		name string
		// chunks are written and flushed one at a time; the body is their
		// concatenation.
		chunks []string
		// atFlush is what the client has received after each flush.
		atFlush  []string
		expected string
	}{
		{
			name:     "list split across three writes",
			chunks:   []string{list[:20], list[20:50], list[50:]},
			atFlush:  []string{"", "", filtered + "\n"},
			expected: filtered + "\n",
		},
		{
			name:     "list split after a brace inside a string",
			chunks:   split(bracesList, strings.Index(bracesList, `"}}}}]]`)+2),
			atFlush:  []string{"", bracesFiltered + "\n"},
			expected: bracesFiltered + "\n",
		},
		{
			name:     "list split between a backslash and the quote it escapes",
			chunks:   split(bracesList, strings.Index(bracesList, `\"}}}}`)+1),
			atFlush:  []string{"", bracesFiltered + "\n"},
			expected: bracesFiltered + "\n",
		},
		{
			name:     "list split after an escaped backslash",
			chunks:   split(bracesList, strings.Index(bracesList, `\\"}`)+2),
			atFlush:  []string{"", bracesFiltered + "\n"},
			expected: bracesFiltered + "\n",
		},
		{
			name:     "white space after the list waits for the end of the response",
			chunks:   []string{list[:40], list[40:], "\n"},
			atFlush:  []string{"", filtered + "\n", filtered + "\n"},
			expected: filtered + "\n\n",
		},
		{
			name:     "scalar body split in two",
			chunks:   []string{`"just`, ` text"`},
			atFlush:  []string{"", `"just text"`},
			expected: `"just text"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Len(t, tt.atFlush, len(tt.chunks))
			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			var atFlush []string
			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				require.True(t, ok)
				for _, chunk := range tt.chunks {
					_, writeErr := w.Write([]byte(chunk))
					require.NoError(t, writeErr)
					flusher.Flush()
					atFlush = append(atFlush, recorder.Body.String())
				}
			})

			request := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			middleware(backend).ServeHTTP(recorder, request)

			assert.Equal(t, tt.atFlush, atFlush)
			assert.Equal(t, tt.expected, recorder.Body.String())
			assert.NotContains(t, recorder.Body.String(), "hidden_tool")
		})
	}
}

func TestListToolsFilterReadsAmbiguousContentTypesAtTheEnd(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`

	tests := []struct {
		name string
		// status is 200 unless set.
		status       int
		contentTypes []string
		body         string
		// atFlush is what the client has received once the backend has
		// written and flushed the whole body, before the response ends.
		atFlush  string
		expected string
	}{
		{
			name:         "JSON with an event-stream parameter on an event stream",
			contentTypes: []string{"application/json; x=text/event-stream"},
			body:         sseEvent(list),
			expected:     sseEvent(filtered),
		},
		{
			name:         "JSON then event-stream headers on an event stream",
			contentTypes: []string{"application/json", "text/event-stream"},
			body:         sseEvent(list),
			expected:     sseEvent(filtered),
		},
		{
			name:         "event-stream with a JSON parameter on a JSON body",
			contentTypes: []string{"text/event-stream; x=application/json"},
			body:         list,
			expected:     filtered + "\n",
		},
		{
			name:         "event-stream then JSON headers on a JSON body",
			contentTypes: []string{"text/event-stream", "application/json"},
			body:         list,
			expected:     filtered + "\n",
		},
		{
			name:         "upper-case JSON with an event-stream parameter on an event stream",
			contentTypes: []string{"Application/JSON; X=Text/Event-Stream"},
			body:         sseEvent(list),
			expected:     sseEvent(filtered),
		},
		{
			name:         "upper-case event-stream with a JSON parameter on a JSON body",
			contentTypes: []string{"TEXT/EVENT-STREAM; x=APPLICATION/JSON"},
			body:         list,
			expected:     filtered + "\n",
		},
		{
			name:         "JSON with an event-stream parameter on an event stream at a 3xx status",
			status:       http.StatusMultipleChoices,
			contentTypes: []string{"application/json; x=text/event-stream"},
			body:         sseEvent(list),
			expected:     sseEvent(filtered),
		},
		{
			name:         "JSON sequence at a 3xx status",
			status:       http.StatusMultipleChoices,
			contentTypes: []string{"application/json-seq"},
			body:         list,
			expected:     filtered + "\n",
		},
		{
			name:         "JSON with a charset",
			contentTypes: []string{"application/json; charset=utf-8"},
			body:         list,
			atFlush:      filtered + "\n",
			expected:     filtered + "\n",
		},
		{
			name:         "event stream",
			contentTypes: []string{"text/event-stream"},
			body:         sseEvent(list),
			atFlush:      sseEvent(filtered),
			expected:     sseEvent(filtered),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			var atFlush string
			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, contentType := range tt.contentTypes {
					w.Header().Add("Content-Type", contentType)
				}
				w.WriteHeader(cmp.Or(tt.status, http.StatusOK))
				_, writeErr := w.Write([]byte(tt.body))
				require.NoError(t, writeErr)
				flusher, ok := w.(http.Flusher)
				require.True(t, ok)
				flusher.Flush()
				atFlush = recorder.Body.String()
			})

			request := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			middleware(backend).ServeHTTP(recorder, request)

			assert.Equal(t, tt.atFlush, atFlush)
			assert.Equal(t, tt.expected, recorder.Body.String())
			assert.NotContains(t, recorder.Body.String(), "hidden_tool")
		})
	}
}

// TestListToolsFilterPassesOtherResponsesThrough checks that responses no
// client could read as a tools list reach the client byte for byte, and that
// a well-formed list is filtered.
func TestListToolsFilterPassesOtherResponsesThrough(t *testing.T) {
	t.Parallel()

	unchanged := []struct {
		name    string
		payload string
	}{
		{"no result", `{"jsonrpc":"2.0","id":1}`},
		{"result without tools", `{"jsonrpc":"2.0","id":1,"result":{"prompts":[],"Tool":[1]}}`},
		{"JSON-RPC error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`},
		{"non-object result", `{"jsonrpc":"2.0","id":1,"result":["tools",` + hiddenTool + `]}`},
		{"string result", `{"jsonrpc":"2.0","id":1,"result":"tools"}`},
		{"null result", `{"jsonrpc":"2.0","id":1,"result":null}`},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`},
		{"tools/call result", `{"jsonrpc":"2.0","id":2,"result":{"content":[],` +
			`"structuredContent":{"tools":` + bothTools + `},"isError":false}}`},
		{"initialize result", `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-11-25",` +
			`"capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"s","version":"1"}}}`},
		{"result followed by a no-break space", `{"jsonrpc":"2.0","id":1,"result":{}}` + "\u00a0"},
		{"plain text", `OK`},
		{"HTML page", `<!DOCTYPE html><html><body>hidden_tool</body></html>`},
		{"endpoint path", `/messages?sessionId=hidden_tool`},
	}

	for _, transport := range toolsListTransports {
		for _, tc := range unchanged {
			t.Run(transport.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				body := transport.render(tc.payload)
				assert.Equal(t, body, serveThroughListFilter(t, transport.contentType, body))
			})
		}
	}

	filtered := []struct {
		name     string
		payload  string
		expected string
	}{
		{
			name:     "well-formed list",
			payload:  listPrefix + `{"tools":` + bothTools + `}}`,
			expected: listPrefix + `{"tools":[` + allowedTool + `]}}`,
		},
		{
			// Only the members the filter reads are checked, so a schema with
			// case-variant property names is filtered like any other.
			name: "input schema with case-variant property names",
			payload: listPrefix + `{"tools":[{"name":"allowed_tool","inputSchema":{"type":"object",` +
				`"properties":{"Name":{},"name":{},"NAME":{}}}},` + hiddenTool + `]}}`,
			expected: listPrefix + `{"tools":[{"name":"allowed_tool","inputSchema":{"type":"object",` +
				`"properties":{"Name":{},"name":{},"NAME":{}}}}]}}`,
		},
		{
			name: "other members of the result and the message",
			payload: `{"jsonrpc":"2.0","id":"a","result":{"tools":` + bothTools +
				`,"nextCursor":"page-2","resultType":"complete","_meta":{"k":1}},"x-extra":true}`,
			expected: `{"jsonrpc":"2.0","id":"a","result":{"tools":[` + allowedTool +
				`],"nextCursor":"page-2","resultType":"complete","_meta":{"k":1}},"x-extra":true}`,
		},
		{
			name:     "no id and no jsonrpc",
			payload:  `{"result":{"tools":` + bothTools + `}}`,
			expected: `{"result":{"tools":[` + allowedTool + `]}}`,
		},
		{
			name: "numbers beyond float64 precision",
			payload: `{"jsonrpc":"2.0","id":9007199254740993,"result":{"tools":[{"name":"allowed_tool",` +
				`"inputSchema":{"type":"integer","maximum":9007199254740993}},` + hiddenTool + `]}}`,
			expected: `{"jsonrpc":"2.0","id":9007199254740993,"result":{"tools":[{"name":"allowed_tool",` +
				`"inputSchema":{"type":"integer","maximum":9007199254740993}}]}}`,
		},
		{
			name:     "white space inside a tool",
			payload:  listPrefix + `{"tools":[{ "name" : "allowed_tool", "description": "visible" },` + hiddenTool + `]}}`,
			expected: listPrefix + `{"tools":[{ "name" : "allowed_tool", "description": "visible" }]}}`,
		},
	}

	for _, transport := range toolsListTransports {
		for _, tc := range filtered {
			t.Run(transport.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				received := serveThroughListFilter(t, transport.contentType, transport.render(tc.payload))
				expected := transport.render(tc.expected)
				if strings.HasPrefix(transport.name, "json") {
					// The JSON path ends the filtered body with a newline.
					expected += "\n"
				}
				assert.Equal(t, expected, received)
			})
		}
	}
}

// TestListToolsFilterOverrideKeepsToolMembers checks that an override
// replaces a tool's name and description, that every other member of the
// tool keeps its place and its bytes, and that each tool the filter keeps is
// written from its own entry.
func TestListToolsFilterOverrideKeepsToolMembers(t *testing.T) {
	t.Parallel()

	const otherTool = `{"name":"other_tool"}`

	tests := []struct {
		name     string
		override ToolMiddlewareOption
		tools    string
		expected string
	}{
		{
			name:     "name and description",
			override: WithToolsOverride("actual_tool", "user_tool", "User description"),
			tools:    `{"name":"actual_tool","inputSchema":{"type":"object"},"description":"Original"},` + otherTool,
			expected: `{"name":"user_tool","inputSchema":{"type":"object"},"description":"User description"},` + otherTool,
		},
		{
			name:     "description added to a tool without one",
			override: WithToolsOverride("actual_tool", "user_tool", "User description"),
			tools:    `{"name":"actual_tool","annotations":{"readOnlyHint":true}},` + otherTool,
			expected: `{"name":"user_tool","annotations":{"readOnlyHint":true},"description":"User description"},` + otherTool,
		},
		{
			name:     "name only",
			override: WithToolsOverride("actual_tool", "user_tool", ""),
			tools:    `{"name":"actual_tool","description":"Original"},` + otherTool,
			expected: `{"name":"user_tool","description":"Original"},` + otherTool,
		},
		{
			name:     "name of another tool",
			override: WithToolsOverride("tool_a", "tool_b", ""),
			tools:    `{"name":"tool_a","inputSchema":{"a":1}},{"name":"tool_b","inputSchema":{"b":2}}`,
			expected: `{"name":"tool_b","inputSchema":{"a":1}},{"name":"tool_b","inputSchema":{"b":2}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			received := serveThroughListMiddleware(t, []ToolMiddlewareOption{tt.override}, http.StatusOK,
				"application/json", listPrefix+`{"tools":[`+tt.tools+`]}}`)
			assert.Equal(t, listPrefix+`{"tools":[`+tt.expected+`]}}`+"\n", received)
		})
	}
}

// TestListToolsFilterReadsEverySuccessStatus checks that a tools list without
// a supported Content-Type is filtered at every 2xx status, and that the same
// list at a 4xx or 5xx status, which clients treat as an error, passes
// through unread.
func TestListToolsFilterReadsEverySuccessStatus(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	renderings := []struct {
		name     string
		render   func(payload string) string
		filtered string
	}{
		{name: "json", render: func(p string) string { return p }, filtered: filtered + "\n"},
		{name: "sse", render: sseEvent, filtered: sseEvent(filtered)},
	}

	type statusCase struct {
		name        string
		status      int
		contentType string
		body        string
		expected    string
	}
	var tests []statusCase
	for _, status := range []int{
		http.StatusOK, http.StatusCreated, http.StatusAccepted,
		http.StatusNonAuthoritativeInfo, http.StatusPartialContent, http.StatusMultiStatus,
	} {
		for _, contentType := range []string{"", "application/json-seq"} {
			for _, rendering := range renderings {
				tests = append(tests, statusCase{
					name:        fmt.Sprintf("%d %q %s", status, contentType, rendering.name),
					status:      status,
					contentType: contentType,
					body:        rendering.render(list),
					expected:    rendering.filtered,
				})
			}
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		for _, rendering := range renderings {
			tests = append(tests, statusCase{
				name:     fmt.Sprintf("%d without content type %s", status, rendering.name),
				status:   status,
				body:     rendering.render(list),
				expected: rendering.render(list),
			})
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			received := serveThroughListMiddleware(t, []ToolMiddlewareOption{WithToolsFilter("allowed_tool")},
				tt.status, tt.contentType, tt.body)
			assert.Equal(t, tt.expected, received)
		})
	}
}

// TestListToolsFilterContentCoding covers bodies with a content coding, whose
// bytes the filter cannot read: the request reaches the backend without the
// client's Accept-Encoding, and a body that arrives with a coding anyway is
// withheld whenever the filter would have had to read it, with the client
// answered with an error instead of the backend's status and
// Content-Encoding.
func TestListToolsFilterContentCoding(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	gzippedList := gzipBytes(t, list)
	gzippedEvent := gzipBytes(t, sseEvent(list))
	gzippedResult := gzipBytes(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"done"}]}}`)
	gzippedError := gzipBytes(t, "internal error")

	tests := []struct {
		name string
		// method is the request method, POST unless set.
		method          string
		status          int
		contentType     string
		contentEncoding string
		chunks          [][]byte
		// withheld reports that the client is answered with an error;
		// otherwise it receives expected, with the backend's status and
		// Content-Encoding.
		withheld bool
		expected string
	}{
		{
			name:            "gzip JSON body",
			status:          http.StatusOK,
			contentType:     "application/json",
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedList},
			withheld:        true,
		},
		{
			name:            "gzip event stream across flushes",
			status:          http.StatusOK,
			contentType:     "text/event-stream",
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedEvent[:10], gzippedEvent[10:]},
			withheld:        true,
		},
		{
			name:            "gzip JSON body that is not a tools list",
			status:          http.StatusOK,
			contentType:     "application/json",
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedResult},
			withheld:        true,
		},
		{
			name:            "gzip body without content type",
			status:          http.StatusOK,
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedList},
			withheld:        true,
		},
		{
			name:            "gzip body without content type at another 2xx status",
			status:          http.StatusCreated,
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedList},
			withheld:        true,
		},
		{
			name:            "gzip body with an ambiguous content type at a non-2xx status",
			status:          http.StatusFound,
			contentType:     "application/json-seq",
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedList},
			withheld:        true,
		},
		{
			name:            "gzip JSON error body",
			status:          http.StatusInternalServerError,
			contentType:     "application/json",
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedList},
			withheld:        true,
		},
		{
			name:            "coding the filter does not know",
			status:          http.StatusOK,
			contentType:     "application/json",
			contentEncoding: "identity, br",
			chunks:          [][]byte{[]byte(list)},
			withheld:        true,
		},
		{
			name:            "identity coding",
			status:          http.StatusOK,
			contentType:     "application/json",
			contentEncoding: "identity",
			chunks:          [][]byte{[]byte(list)},
			expected:        filtered + "\n",
		},
		{
			// A non-2xx body without a supported Content-Type is passed
			// through unread whether or not it is encoded.
			name:            "gzip error body without supported content type",
			status:          http.StatusInternalServerError,
			contentType:     "text/plain",
			contentEncoding: "gzip",
			chunks:          [][]byte{gzippedError},
			expected:        string(gzippedError),
		},
		{
			// A response that declares a content coding but carries no
			// body is forwarded as it is: there is nothing to read.
			name:            "coded 204 without a body",
			status:          http.StatusNoContent,
			contentType:     "application/json",
			contentEncoding: "gzip",
		},
		{
			name:            "coded 202 without content type or body",
			status:          http.StatusAccepted,
			contentEncoding: "gzip",
		},
		{
			name:            "coded empty JSON body",
			status:          http.StatusOK,
			contentType:     "application/json",
			contentEncoding: "gzip",
		},
		{
			name:            "coded 304 to a GET request",
			method:          http.MethodGet,
			status:          http.StatusNotModified,
			contentType:     "application/json",
			contentEncoding: "gzip",
		},
		{
			// A flush before the first byte of a coded body forwards
			// nothing: the body is withheld when its first byte arrives.
			name:            "gzip event stream after a flush",
			status:          http.StatusOK,
			contentType:     "text/event-stream",
			contentEncoding: "gzip",
			chunks:          [][]byte{nil, gzippedEvent},
			withheld:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			var acceptEncodingSent bool
			backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, acceptEncodingSent = r.Header["Accept-Encoding"]
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				w.Header().Set("Content-Encoding", tt.contentEncoding)
				w.WriteHeader(tt.status)
				flusher, ok := w.(http.Flusher)
				require.True(t, ok)
				for _, chunk := range tt.chunks {
					_, writeErr := w.Write(chunk)
					require.NoError(t, writeErr)
					flusher.Flush()
				}
			})

			var requestBody io.Reader
			if cmp.Or(tt.method, http.MethodPost) == http.MethodPost {
				requestBody = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			}
			request := httptest.NewRequest(cmp.Or(tt.method, http.MethodPost), "/mcp", requestBody)
			request.Header.Set("Accept-Encoding", "gzip, br")
			recorder := httptest.NewRecorder()
			middleware(backend).ServeHTTP(recorder, request)

			assert.False(t, acceptEncodingSent, "the request to the backend must carry no Accept-Encoding")
			if tt.withheld {
				assertWithheld(t, recorder, false, withheldError)
				return
			}
			response := recorder.Result()
			require.NoError(t, response.Body.Close())
			assert.Equal(t, tt.status, response.StatusCode)
			assert.Equal(t, tt.contentEncoding, response.Header.Get("Content-Encoding"))
			assert.Equal(t, tt.expected, recorder.Body.String())
		})
	}
}

// TestListToolsFilterReadsTheFinalHeader checks that the filter reads a
// response by its final status and the header written with it, through a
// server that sends informational responses and trailers. An informational
// response before the final one, which can carry any header, is forwarded
// and decides nothing. A Content-Encoding the backend sets after the body, as
// a reverse proxy sets the trailers of the backend's response, does not reach
// the client as a header of a body the filter has read.
func TestListToolsFilterReadsTheFinalHeader(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	gzippedList := gzipBytes(t, list)

	tests := []struct {
		name string
		// informational is the Content-Type of a 103 Early Hints response
		// the backend sends before the final one, if any.
		informational   string
		contentType     string
		contentEncoding string
		// trailerEncoding is a Content-Encoding the backend announces as a
		// trailer and sets once it has written the body, if any.
		trailerEncoding string
		body            []byte
		// withheld reports that the client is answered with an error;
		// otherwise it receives expected under HTTP 200.
		withheld bool
		expected string
	}{
		{
			name:            "gzip JSON body after an informational event-stream header",
			informational:   "text/event-stream",
			contentType:     "application/json",
			contentEncoding: "gzip",
			body:            gzippedList,
			withheld:        true,
		},
		{
			name:            "gzip JSON body after an informational JSON header",
			informational:   "application/json",
			contentType:     "application/json",
			contentEncoding: "gzip",
			body:            gzippedList,
			withheld:        true,
		},
		{
			name:          "list without content type after an informational event-stream header",
			informational: "text/event-stream",
			body:          []byte(list),
			expected:      filtered + "\n",
		},
		{
			name:            "JSON body with a content coding set after it",
			contentType:     "application/json",
			trailerEncoding: "gzip",
			body:            gzippedList,
			expected:        string(gzippedList),
		},
		{
			name:            "body without content type with a content coding set after it",
			trailerEncoding: "gzip",
			body:            gzippedList,
			expected:        string(gzippedList),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.informational != "" {
					w.Header().Set("Content-Type", tt.informational)
					w.WriteHeader(http.StatusEarlyHints)
					clear(w.Header())
				}
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if tt.contentEncoding != "" {
					w.Header().Set("Content-Encoding", tt.contentEncoding)
				}
				if tt.trailerEncoding != "" {
					w.Header().Set("Trailer", "Content-Encoding")
				}
				w.WriteHeader(http.StatusOK)
				_, writeErr := w.Write(tt.body)
				assert.NoError(t, writeErr)
				assert.NoError(t, http.NewResponseController(w).Flush())
				if tt.trailerEncoding != "" {
					w.Header().Set("Content-Encoding", tt.trailerEncoding)
				}
			})
			server := httptest.NewServer(middleware(backend))
			t.Cleanup(server.Close)

			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL,
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			require.NoError(t, err)
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			t.Cleanup(client.CloseIdleConnections)
			response, err := client.Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())

			assert.Empty(t, response.Header.Values("Content-Encoding"),
				"no content coding may reach the client with a body the filter has read")
			if tt.withheld {
				assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
				assert.Equal(t, withheldError, string(body))
				return
			}
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, tt.expected, string(body))
		})
	}
}

// TestListToolsFilterHoldsTheStatusOverByteOrderMarks checks that byte order
// marks flushed on their own, which the filter drops, do not get the status
// of a response it holds forwarded, so that a body it then withholds is still
// answered with an error.
func TestListToolsFilterHoldsTheStatusOverByteOrderMarks(t *testing.T) {
	t.Parallel()

	const bom = "\ufeff"
	nanList := listPrefix + `{"tools":` + bothTools + `},"x":NaN}`

	for _, contentType := range []string{"application/json", ""} {
		t.Run(cmp.Or(contentType, "without content type"), func(t *testing.T) {
			t.Parallel()

			_, recorder := streamListResponse(t, contentType, []string{bom, bom + nanList})
			assertWithheld(t, recorder, false, withheldErrorWithoutID)
		})
	}
}

// TestListToolsFilterAnswersWithTheRequestID checks that the error answering a
// withheld response carries the id of the request the response answers, and
// none when the request has none, and that the backend still receives the
// whole request body.
func TestListToolsFilterAnswersWithTheRequestID(t *testing.T) {
	t.Parallel()

	withheldList := listPrefix + `{"TOOLS":` + bothTools + `}}`

	tests := []struct {
		name     string
		method   string
		body     string
		expected string
	}{
		{
			name:     "numeric id",
			method:   http.MethodPost,
			body:     `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`,
			expected: `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"internal error"}}`,
		},
		{
			name:     "string id",
			method:   http.MethodPost,
			body:     `{"jsonrpc":"2.0","id":"list-1","method":"tools/list"}`,
			expected: `{"jsonrpc":"2.0","id":"list-1","error":{"code":-32603,"message":"internal error"}}`,
		},
		{
			name:     "notification",
			method:   http.MethodPost,
			body:     `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			expected: withheldErrorWithoutID,
		},
		{
			name:     "body that is not a JSON-RPC message",
			method:   http.MethodPost,
			body:     `not json`,
			expected: withheldErrorWithoutID,
		},
		{
			name:     "GET request",
			method:   http.MethodGet,
			expected: withheldErrorWithoutID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			var received []byte
			backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var readErr error
				received, readErr = io.ReadAll(r.Body)
				require.NoError(t, readErr)
				w.Header().Set("Content-Type", "application/json")
				_, writeErr := w.Write([]byte(withheldList))
				require.NoError(t, writeErr)
			})

			recorder := httptest.NewRecorder()
			middleware(backend).ServeHTTP(recorder, httptest.NewRequest(tt.method, "/mcp", strings.NewReader(tt.body)))

			assert.Equal(t, tt.body, string(received), "the backend must receive the whole request body")
			assertWithheld(t, recorder, false, tt.expected)
		})
	}
}

// TestListToolsFilterAnswersAWithheldEventByItsID checks whose id the error
// event written in place of a withheld event carries: the withheld event's
// own when it is a response with one, the id the client is waiting on, and
// otherwise the id of the request that opened the stream. No error event is
// written when neither names a request: an error without an id, or with the
// id of a request the server made, is read by clients as the answer to a
// request of their own or ends their session. A withheld JSON body is
// answered under HTTP 500 with the request's id as before.
func TestListToolsFilterAnswersAWithheldEventByItsID(t *testing.T) {
	t.Parallel()

	withheld := func(id string) string {
		return sseEvent(`{"jsonrpc":"2.0","id":` + id + `,"result":{"tools":[1]}}`)
	}
	errorEvent := func(id string) string {
		return sseEvent(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32603,"message":"internal error"}}`)
	}
	reply := func(id string) string {
		return sseEvent(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[]}}`)
	}
	progressEvent := sseEvent(progressNotification)
	pingEvent := sseEvent(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	list := listPrefix + `{"tools":` + bothTools + `}}`
	const splitAt = `{"jsonrpc":"2.0","id":1,`
	bareCREvent := "data: " + list[:len(splitAt)] + "\r" + list[len(splitAt):] + "\n\n"
	separatorEvent := "data: " + list[:len(splitAt)] + "\u2028" + list[len(splitAt):] + "\n\n"
	const listRequest9 = `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`

	tests := []struct {
		name string
		// request is the body of the POST request the response answers; the
		// response answers a GET request without a body when it is empty.
		request string
		// contentType is the backend's Content-Type, text/event-stream when
		// it is empty.
		contentType string
		chunks      []string
		expected    string
	}{
		{
			name:     "responses with ids on a stream a GET opened",
			chunks:   []string{withheld("5") + withheld("6") + reply("2")},
			expected: errorEvent("5") + errorEvent("6") + reply("2"),
		},
		{
			name:     "response with a string id",
			chunks:   []string{withheld(`"abc"`)},
			expected: errorEvent(`"abc"`),
		},
		{
			name:     "response with two ids",
			chunks:   []string{progressEvent + sseEvent(`{"jsonrpc":"2.0","id":5,"id":6,"result":{"tools":[1]}}`) + progressEvent},
			expected: progressEvent + progressEvent,
		},
		{
			name:     "response with an id in another case",
			chunks:   []string{progressEvent + sseEvent(`{"jsonrpc":"2.0","ID":5,"result":{"tools":[1]}}`) + progressEvent},
			expected: progressEvent + progressEvent,
		},
		{
			name:     "response with a fractional id",
			chunks:   []string{progressEvent + withheld("5.5") + progressEvent},
			expected: progressEvent + progressEvent,
		},
		{
			name:     "response with a null id",
			chunks:   []string{progressEvent + withheld("null") + progressEvent},
			expected: progressEvent + progressEvent,
		},
		{
			name:     "response a nested value keeps from decoding on a stream a GET opened",
			chunks:   []string{progressEvent + sseEvent(`{"jsonrpc":"2.0","id":5,"result":{"tools":[NaN]}}`) + progressEvent},
			expected: progressEvent + errorEvent("5") + progressEvent,
		},
		{
			name:     "response a case-folded tool name keeps from decoding on a stream a GET opened",
			chunks:   []string{sseEvent(`{"jsonrpc":"2.0","id":5,"result":{"tools":[{"name":"allowed_tool","Name":"hidden_tool"}]}}`)},
			expected: errorEvent("5"),
		},
		{
			name: "request on a stream a GET opened",
			chunks: []string{progressEvent +
				sseEvent(`{"jsonrpc":"2.0","id":0,"method":"sampling/createMessage","result":{"tools":[1]}}`) + progressEvent},
			expected: progressEvent + progressEvent,
		},
		{
			name:     "run with a bare CR on a stream a GET opened",
			chunks:   []string{progressEvent + bareCREvent, pingEvent},
			expected: pingEvent,
		},
		{
			name:     "run with a bare CR on a stream a POST opened",
			request:  listRequest9,
			chunks:   []string{progressEvent + bareCREvent, pingEvent},
			expected: errorEvent("9") + pingEvent,
		},
		{
			name:     "run with a separator on a stream a POST opened",
			request:  listRequest9,
			chunks:   []string{progressEvent + separatorEvent, pingEvent},
			expected: errorEvent("9") + pingEvent,
		},
		{
			name:     "separator split across writes on a stream a POST opened",
			request:  listRequest9,
			chunks:   []string{"\xe2\x80", "\xa8data: " + list + "\n\n"},
			expected: errorEvent("9"),
		},
		{
			name:     "response with an id on a stream a POST opened",
			request:  listRequest9,
			chunks:   []string{withheld("4")},
			expected: errorEvent("4"),
		},
		{
			name:    "notification that is not valid JSON before the reply on a stream a POST opened",
			request: listRequest9,
			chunks: []string{sseEvent(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":9,"progress":NaN}}`) +
				reply("9")},
			expected: errorEvent("9") + reply("9"),
		},
		{
			name:        "JSON body answering a POST",
			request:     listRequest9,
			contentType: "application/json",
			chunks:      []string{`{"jsonrpc":"2.0","id":9,"result":{"tools":[1]}}`},
			expected:    `{"jsonrpc":"2.0","id":9,"error":{"code":-32603,"message":"internal error"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := func() *http.Request {
				if tt.request == "" {
					return httptest.NewRequest(http.MethodGet, "/sse", nil)
				}
				return httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.request))
			}()
			contentType := cmp.Or(tt.contentType, "text/event-stream")
			_, recorder := streamListResponseTo(t, request, contentType, tt.chunks)
			if contentType == "application/json" {
				assertWithheld(t, recorder, false, tt.expected)
				return
			}
			response := recorder.Result()
			require.NoError(t, response.Body.Close())
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, tt.expected, recorder.Body.String())
		})
	}
}

// TestWithheldEventID covers the id the error in place of a withheld event
// carries: the id of a response, the id of the request that opened the stream
// for an event that is not one message, and none for a request or a
// notification.
func TestWithheldEventID(t *testing.T) {
	t.Parallel()

	const request9 = `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`
	event := func(data string) []sseLine { return splitSSELines([]byte("data: " + data + "\n")) }
	response := func(id string) []sseLine { return event(`{"jsonrpc":"2.0","id":` + id + `,"result":{"tools":[1]}}`) }

	tests := []struct {
		name        string
		event       []sseLine
		requestBody string
		expected    jsonrpc2.ID
	}{
		{name: "response with an integer id", event: response("5"), requestBody: request9, expected: jsonrpc2.Int64ID(5)},
		{name: "response with a string id", event: response(`"abc"`), requestBody: request9, expected: jsonrpc2.StringID("abc")},
		{
			name:        "response with its id on the second data line",
			event:       splitSSELines([]byte("data: {\"jsonrpc\":\"2.0\",\ndata: \"id\":5,\"result\":{\"tools\":[1]}}\n")),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(5),
		},
		{
			name:        "error response with an id",
			event:       event(`{"jsonrpc":"2.0","id":3,"error":{"code":-32600,"message":"invalid"}}`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(3),
		},
		{
			name:        "error response without an id",
			event:       event(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"invalid"}}`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(9),
		},
		{
			name:        "request",
			event:       event(`{"jsonrpc":"2.0","id":0,"method":"sampling/createMessage","params":{}}`),
			requestBody: request9,
			expected:    jsonrpc2.ID{},
		},
		{name: "notification", event: event(progressNotification), requestBody: request9, expected: jsonrpc2.ID{}},
		{
			name:        "response with two ids",
			event:       event(`{"jsonrpc":"2.0","id":5,"id":6,"result":{"tools":[1]}}`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(9),
		},
		{
			name:        "response with an id in another case",
			event:       event(`{"jsonrpc":"2.0","ID":5,"result":{"tools":[1]}}`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(9),
		},
		{name: "response with a fractional id", event: response("5.5"), requestBody: request9, expected: jsonrpc2.Int64ID(9)},
		{name: "response with a null id", event: response("null"), requestBody: request9, expected: jsonrpc2.Int64ID(9)},
		{
			name:        "response a nested value keeps from decoding",
			event:       event(`{"jsonrpc":"2.0","id":5,"result":{"tools":[NaN]}}`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(5),
		},
		{
			name:        "response a case-folded tool name keeps from decoding",
			event:       event(`{"jsonrpc":"2.0","id":5,"result":{"tools":[{"name":"allowed_tool","Name":"hidden_tool"}]}}`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(5),
		},
		{
			name:        "batch",
			event:       event(`[{"jsonrpc":"2.0","id":5,"result":{"tools":[1]}}]`),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(9),
		},
		{
			name:        "event without a data line",
			event:       splitSSELines([]byte("event: message\n")),
			requestBody: request9,
			expected:    jsonrpc2.Int64ID(9),
		},
		{name: "no event", event: nil, requestBody: request9, expected: jsonrpc2.Int64ID(9)},
		{name: "no event on a stream a GET opened", event: nil, expected: jsonrpc2.ID{}},
		{
			name:     "response with an id in another case on a stream a GET opened",
			event:    event(`{"jsonrpc":"2.0","ID":5,"result":{"tools":[1]}}`),
			expected: jsonrpc2.ID{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, withheldEventID(tt.event, []byte(tt.requestBody)))
		})
	}
}

// TestHeldEventID covers the id the start of a withheld event names: the one
// id of a response's start, read up to where the start is cut, and none for a
// start that names a method, no result or error, two ids, an id spelled in
// another case, an id cut with the start, or no id at its top level.
func TestHeldEventID(t *testing.T) {
	t.Parallel()

	start := func(data string) []byte { return []byte("event: message\ndata: " + data) }

	tests := []struct {
		name     string
		event    []byte
		expected jsonrpc2.ID
	}{
		{
			name:     "response cut inside its result",
			event:    start(`{"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"xx`),
			expected: jsonrpc2.Int64ID(5),
		},
		{
			name:     "response with a string id",
			event:    start(`{"jsonrpc":"2.0","id":"abc","result":{"content":[{"type":"text","text":"xx`),
			expected: jsonrpc2.StringID("abc"),
		},
		{
			name:     "error response cut inside its error",
			event:    start(`{"jsonrpc":"2.0","id":5,"error":{"code":1,"message":"xx`),
			expected: jsonrpc2.Int64ID(5),
		},
		{
			name:     "response whose id follows its result",
			event:    start(`{"jsonrpc":"2.0","result":{"content":[]},"id":5,"_meta":{"note":"xx`),
			expected: jsonrpc2.Int64ID(5),
		},
		{
			name:     "response cut after its id",
			event:    start(`{"jsonrpc":"2.0","result":{},"id":5,`),
			expected: jsonrpc2.Int64ID(5),
		},
		{
			name:     "complete response",
			event:    start(`{"jsonrpc":"2.0","id":5,"result":{}}`),
			expected: jsonrpc2.Int64ID(5),
		},
		{
			name:     "response over two data lines",
			event:    []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":5,\"result\":{\"content\":[{\"text\":\"xx"),
			expected: jsonrpc2.Int64ID(5),
		},
		{name: "response cut inside its id", event: start(`{"jsonrpc":"2.0","result":{},"id":5`), expected: jsonrpc2.ID{}},
		{name: "response cut inside a string id", event: start(`{"jsonrpc":"2.0","result":{},"id":"ab`), expected: jsonrpc2.ID{}},
		{
			name:     "request",
			event:    start(`{"jsonrpc":"2.0","id":3,"method":"sampling/createMessage","params":{"messages":[{"text":"xx`),
			expected: jsonrpc2.ID{},
		},
		{name: "start without a result or an error", event: start(`{"jsonrpc":"2.0","id":5,"params":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start with two ids", event: start(`{"jsonrpc":"2.0","id":5,"id":6,"result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start with an id in another case", event: start(`{"jsonrpc":"2.0","ID":5,"result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start with a result in another case", event: start(`{"jsonrpc":"2.0","id":5,"Result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start with two results", event: start(`{"jsonrpc":"2.0","id":5,"result":{},"result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start with a fractional id", event: start(`{"jsonrpc":"2.0","id":5.5,"result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start with a null id", event: start(`{"jsonrpc":"2.0","id":null,"result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start that names a method cut in its value", event: start(`{"jsonrpc":"2.0","id":5,"result":{},"methoD":"`), expected: jsonrpc2.ID{}},
		{name: "start whose id is not at the top level", event: start(`{"jsonrpc":"2.0","result":{"id":7,"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start cut before its id", event: start(`{"jsonrpc":"2.0","result":{"content":[{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start that is not an object", event: start(`[{"jsonrpc":"2.0","id":5,"result":{"text":"xx`), expected: jsonrpc2.ID{}},
		{name: "start that is not JSON", event: start("xxxx"), expected: jsonrpc2.ID{}},
		{name: "start without a data line", event: []byte("event: message\n"), expected: jsonrpc2.ID{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, heldEventID(tt.event))
		})
	}
}

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

// TestSSEDataLines covers the split of a JSON value into the values of the
// data lines that carry it: every CRLF, CR or LF ends a line.
func TestSSEDataLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  string
		expected []string
	}{
		{name: "CRLF and CR", payload: "a\r\nb\rc", expected: []string{"a", "b", "c"}},
		{name: "no line break", payload: `{"a":1}`, expected: []string{`{"a":1}`}},
		{name: "empty line inside", payload: "{\"a\":\n\n1}", expected: []string{`{"a":`, "", "1}"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var lines []string
			for _, line := range sseDataLines([]byte(tt.payload)) {
				lines = append(lines, string(line))
			}
			assert.Equal(t, tt.expected, lines)
		})
	}
}

// TestReplaceSSEData covers the lines that take the place of an event's data
// lines: one per line of the payload, the first data line's terminator on the
// last of them, and the other lines of the event kept.
func TestReplaceSSEData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    string
		expected string
	}{
		{
			name:     "LF lines",
			event:    "event: message\ndata: x\nid: 7\ndata: y\n",
			expected: "event: message\ndata: a\ndata: b\ndata: c\nid: 7\n",
		},
		{
			name:     "unterminated data line",
			event:    "data: x",
			expected: "data: a\ndata: b\ndata: c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, writeSSELines(&out, replaceSSEData(splitSSELines([]byte(tt.event)), []byte("a\nb\nc"))...))
			assert.Equal(t, tt.expected, out.String())
		})
	}
}

// TestHasLineSeparator covers the characters, beside CR and LF, at which
// some clients end a line: each is found in a stream, and a stream whose
// lines end in LF or CR alone, or that holds a space, holds none.
func TestHasLineSeparator(t *testing.T) {
	t.Parallel()

	for _, separator := range testLineSeparators {
		t.Run(separator.name, func(t *testing.T) {
			t.Parallel()

			assert.True(t, hasLineSeparator([]byte("data: a"+separator.separator+"data: b")))
		})
	}

	none := []struct {
		name   string
		stream string
	}{
		{name: "LF", stream: "data: a\ndata: b"},
		{name: "CR", stream: "data: a\rdata: b"},
		{name: "space", stream: "data: a data: b"},
	}
	for _, tt := range none {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.False(t, hasLineSeparator([]byte(tt.stream)))
		})
	}
}

// TestFirstLineSplit covers the character the warning for a withheld stream
// names: the first bare CR or other line separator in the stream, whichever
// comes first, and none in a stream whose lines end in CRLF or LF.
func TestFirstLineSplit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stream   string
		expected rune
		found    bool
	}{
		{name: "bare CR before a separator", stream: "a\rb\u2028c", expected: '\r', found: true},
		{name: "separator before a bare CR", stream: "a\x1eb\rc", expected: '\x1e', found: true},
		{name: "separator after a CRLF", stream: "a\r\nb\u0085c", expected: '\u0085', found: true},
		{name: "CRLF and LF alone", stream: "a\r\nb\nc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			split, found := firstLineSplit([]byte(tt.stream))
			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.expected, split)
		})
	}
}

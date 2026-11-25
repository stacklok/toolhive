// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

const (
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`
	discoverRequest   = `{"jsonrpc":"2.0","id":"d-1","method":"server/discover"}`
	toolsListRequest  = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	// initializeWithInstructions is an InitializeResult that carries
	// instructions between other members, so a test can tell that their order
	// and bytes are kept.
	initializeWithInstructions = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25",` +
		`"instructions":"server text","capabilities":{"tools":{}},"serverInfo":{"name":"s","version":"1"}}}`
	initializeWithoutInstructions = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25",` +
		`"capabilities":{"tools":{}},"serverInfo":{"name":"s","version":"1"}}}`
)

// rewrittenInitialize returns initializeWithInstructions with text in place
// of the server's instructions.
func rewrittenInitialize(text string) string {
	encoded, _ := json.Marshal(text)
	return `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25",` +
		`"instructions":` + string(encoded) + `,"capabilities":{"tools":{}},"serverInfo":{"name":"s","version":"1"}}}`
}

// serveInstructions sends a request with body through the MCP parser and an
// instructions middleware for cfg to handler, and returns what the client
// receives. An empty body sends the GET that opens a legacy SSE stream, to a
// middleware for that transport.
func serveInstructions(
	t *testing.T, cfg instructions.Config, body string, handler http.Handler,
) *httptest.ResponseRecorder {
	t.Helper()
	middleware, err := NewInstructionsMiddleware(InstructionsMiddlewareParams{Instructions: cfg, LegacySSE: body == ""})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	if body != "" {
		req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	ParsingMiddleware(middleware(handler)).ServeHTTP(rec, req)
	return rec
}

// replyWith returns a handler that answers with status, contentType and body,
// writing body in the pieces it is split into at each "|" and flushing after
// each piece. It also sets a Content-Length for the whole body, as a backend
// that knows it does.
func replyWith(status int, contentType, body string, extraHeaders ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		pieces := strings.Split(body, "|")
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		for i := 0; i+1 < len(extraHeaders); i += 2 {
			w.Header().Set(extraHeaders[i], extraHeaders[i+1])
		}
		w.Header().Set("Content-Length", stringLen(strings.Join(pieces, "")))
		w.WriteHeader(status)
		for _, piece := range pieces {
			_, _ = w.Write([]byte(piece))
			w.(http.Flusher).Flush()
		}
	}
}

func stringLen(s string) string {
	b, _ := json.Marshal(len(s))
	return string(b)
}

// withheldBody is the JSON-RPC error that takes the place of a response the
// middleware withholds, for the request with id.
func withheldBody(id jsonrpc2.ID) string {
	return string(EncodeJSONRPCError(withheldResponse(id)))
}

// withheldEventFor is the event that takes the place of a withheld event.
func withheldEventFor(id jsonrpc2.ID) string {
	return "event: message\ndata: " + withheldBody(id) + "\n\n"
}

func TestInstructionsMiddleware_JSON(t *testing.T) {
	t.Parallel()

	appendCfg := instructions.Config{Mode: instructions.ModeAppend, Value: "policy"}
	withheld := withheldBody(jsonrpc2.Int64ID(1))
	tests := []struct {
		name        string
		cfg         instructions.Config
		request     string
		status      int
		contentType string
		headers     []string
		response    string
		wantStatus  int
		want        string
		// keepsLength is set when the middleware does not read the body, so
		// it keeps the backend's Content-Length.
		keepsLength bool
	}{
		{
			name:    "append to server instructions",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: initializeWithInstructions,
			want:     rewrittenInitialize("server text\n\npolicy"),
		},
		{
			name:    "prepend to server instructions",
			cfg:     instructions.Config{Mode: instructions.ModePrepend, Value: "policy"},
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: initializeWithInstructions,
			want:     rewrittenInitialize("policy\n\nserver text"),
		},
		{
			name:    "replace server instructions",
			cfg:     instructions.Config{Mode: instructions.ModeReplace, Value: "policy"},
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: initializeWithInstructions,
			want:     rewrittenInitialize("policy"),
		},
		{
			name:    "server without instructions gets the value appended at the end",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json; charset=utf-8",
			response: initializeWithoutInstructions + "\n",
			want: `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25",` +
				`"capabilities":{"tools":{}},"serverInfo":{"name":"s","version":"1"},"instructions":"policy"}}` + "\n",
		},
		{
			name:    "server/discover result",
			cfg:     appendCfg,
			request: discoverRequest, status: http.StatusOK, contentType: "application/json",
			response: `{"jsonrpc":"2.0","id":"d-1","result":{"resultType":"complete","supportedVersions":["2026-07-28"],` +
				`"capabilities":{},"instructions":"server text"}}`,
			want: `{"jsonrpc":"2.0","id":"d-1","result":{"resultType":"complete","supportedVersions":["2026-07-28"],` +
				`"capabilities":{},"instructions":"server text\n\npolicy"}}`,
		},
		{
			name:    "body written in pieces",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: initializeWithInstructions[:20] + "|" + initializeWithInstructions[20:],
			want:     rewrittenInitialize("server text\n\npolicy"),
		},
		{
			name:    "instructions that are not a string are replaced",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: `{"jsonrpc":"2.0","id":1,"result":{"instructions":{"x":1},"protocolVersion":"2025-11-25"}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"instructions":"policy","protocolVersion":"2025-11-25"}}`,
		},
		{
			name:    "error response passes through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unsupported version"}}`,
			want:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unsupported version"}}`,
		},
		{
			name:    "non-2xx status passes through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusInternalServerError, contentType: "application/json",
			response: initializeWithInstructions,
			want:     initializeWithInstructions, keepsLength: true,
		},
		{
			name:    "other method passes through",
			cfg:     appendCfg,
			request: toolsListRequest, status: http.StatusOK, contentType: "application/json",
			response: initializeWithInstructions,
			want:     initializeWithInstructions, keepsLength: true,
		},
		{
			name:    "duplicate member names are withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25",` +
				`"instructions":"policy-free","instructions":"b"}}`,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "instructions spelled in another case are withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response:   `{"jsonrpc":"2.0","id":1,"result":{"Instructions":"a","protocolVersion":"2025-11-25"}}`,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "a result that is not an object is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response:   `{"jsonrpc":"2.0","id":1,"result":"x"}`,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "response with another id is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response:   strings.Replace(initializeWithInstructions, `"id":1`, `"id":2`, 1),
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "compressed body is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			headers:    []string{"Content-Encoding", "br"},
			response:   initializeWithInstructions,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "body in another charset is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json; charset=utf-16le",
			response:   initializeWithInstructions,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "ambiguous Content-Type is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json; x=text/event-stream",
			response:   initializeWithInstructions,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "body that is neither JSON nor an event stream is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "text/plain",
			response:   initializeWithInstructions,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "body that is not JSON is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response:   `{"jsonrpc":"2.0","id":1,"result":`,
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "empty body is withheld",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response:   "",
			wantStatus: http.StatusInternalServerError, want: withheld,
		},
		{
			name:    "empty 202 is withheld",
			cfg:     appendCfg,
			request: discoverRequest, status: http.StatusAccepted,
			response:   "",
			wantStatus: http.StatusInternalServerError, want: withheldBody(jsonrpc2.StringID("d-1")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := serveInstructions(t, tt.cfg, tt.request,
				replyWith(tt.status, tt.contentType, tt.response, tt.headers...))

			wantStatus := cmp.Or(tt.wantStatus, tt.status)
			assert.Equal(t, wantStatus, rec.Code)
			assert.Equal(t, tt.want, rec.Body.String())
			switch {
			case tt.keepsLength:
				assert.Equal(t, stringLen(tt.want), rec.Header().Get("Content-Length"),
					"a body passed through unread keeps its Content-Length")
			case tt.wantStatus == http.StatusInternalServerError:
				sent := rec.Result().Header
				assert.Equal(t, "application/json", sent.Get("Content-Type"))
				assert.Empty(t, sent.Get("Content-Encoding"), "the error is not encoded")
				assert.Empty(t, sent.Get("Content-Length"))
			default:
				assert.Empty(t, rec.Header().Get("Content-Length"),
					"a body the middleware reads may change length, so it drops the backend's Content-Length")
			}
		})
	}
}

func TestInstructionsMiddleware_EventStream(t *testing.T) {
	t.Parallel()

	cfg := instructions.Config{Mode: instructions.ModeAppend, Value: "policy"}
	notification := `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"x"}}`
	otherResponse := `{"jsonrpc":"2.0","id":0,"result":{}}`
	duplicateResult := `{"jsonrpc":"2.0","id":5,"result":{"protocolVersion":"2024-11-05",` +
		`"serverInfo":{"name":"s","version":"1"},"capabilities":{},"instructions":"a","instructions":"b"}}`
	tests := []struct {
		name     string
		request  string
		headers  []string
		response string
		want     string
	}{
		{
			name:    "POST initialize answered on a stream",
			request: initializeRequest,
			response: "event: message\ndata: " + notification + "\n\n" +
				"event: message\r\nid: 7\r\ndata: " + initializeWithInstructions + "\r\n\r\n",
			want: "event: message\ndata: " + notification + "\n\n" +
				"event: message\r\nid: 7\r\ndata: " + rewrittenInitialize("server text\n\npolicy") + "\r\n\r\n",
		},
		{
			name:    "POST server/discover answered on a stream",
			request: discoverRequest,
			response: "event: message\ndata: " +
				`{"jsonrpc":"2.0","id":"d-1","result":{"supportedVersions":[],"capabilities":{}}}` + "\n\n",
			want: "event: message\ndata: " +
				`{"jsonrpc":"2.0","id":"d-1","result":{"supportedVersions":[],"capabilities":{},"instructions":"policy"}}` +
				"\n\n",
		},
		{
			name:    "event split across writes",
			request: initializeRequest,
			response: "event: message\ndata: " + initializeWithInstructions[:30] + "|" +
				initializeWithInstructions[30:] + "\n|\n",
			want: "event: message\ndata: " + rewrittenInitialize("server text\n\npolicy") + "\n\n",
		},
		{
			name:     "CRLF split across writes",
			request:  initializeRequest,
			response: "data: " + initializeWithInstructions + "\r|\n\r\n",
			want:     "data: " + rewrittenInitialize("server text\n\npolicy") + "\r\n\r\n",
		},
		{
			name:     "data split over several lines",
			request:  initializeRequest,
			response: "data: " + initializeWithInstructions[:24] + "\ndata: " + initializeWithInstructions[24:] + "\n\n",
			want:     "data: " + rewrittenInitialize("server text\n\npolicy") + "\n\n",
		},
		{
			name:     "last event without a blank line is read at the end",
			request:  initializeRequest,
			response: "data: " + initializeWithInstructions,
			want:     "data: " + rewrittenInitialize("server text\n\npolicy"),
		},
		{
			name:     "other method passes through",
			request:  toolsListRequest,
			response: "data: " + initializeWithInstructions + "\n\n",
			want:     "data: " + initializeWithInstructions + "\n\n",
		},
		{
			name:     "POST stream without the result ends with an error",
			request:  initializeRequest,
			response: "data: " + notification + "\n\n",
			want:     "data: " + notification + "\n\n" + withheldEventFor(jsonrpc2.Int64ID(1)),
		},
		{
			name:    "POST result that cannot be decoded is replaced by an error",
			request: initializeRequest,
			response: "data: " + strings.Replace(duplicateResult, `"id":5`, `"id":1`, 1) + "\n\n" +
				"data: " + notification + "\n\n",
			want: withheldEventFor(jsonrpc2.Int64ID(1)) + "data: " + notification + "\n\n",
		},
		{
			name:     "POST stream with a bare CR is withheld",
			request:  initializeRequest,
			response: "data: " + notification + "\n\n" + "data: " + initializeWithInstructions + "\r\r",
			want:     "data: " + notification + "\n\n" + withheldEventFor(jsonrpc2.Int64ID(1)),
		},
		{
			name:     "POST stream with a line separator is withheld",
			request:  initializeRequest,
			response: "data: " + strings.Replace(initializeWithInstructions, "server text", "server\u2028text", 1) + "\n\n",
			want:     withheldEventFor(jsonrpc2.Int64ID(1)),
		},
		{
			name:     "compressed POST stream is withheld",
			request:  initializeRequest,
			headers:  []string{"Content-Encoding", "gzip"},
			response: "data: " + initializeWithInstructions + "\n\n",
			want:     withheldBody(jsonrpc2.Int64ID(1)),
		},
		{
			name:    "GET stream rewrites the first initialize result only",
			request: "",
			response: "event: endpoint\ndata: /messages?session_id=abc\n\n" +
				"event: message\ndata: " + otherResponse + "\n\n" +
				"event: message\ndata: " + initializeWithInstructions + "\n\n" +
				"event: message\ndata: " + initializeWithInstructions + "\n\n",
			want: "event: endpoint\ndata: /messages?session_id=abc\n\n" +
				"event: message\ndata: " + otherResponse + "\n\n" +
				"event: message\ndata: " + rewrittenInitialize("server text\n\npolicy") + "\n\n" +
				"event: message\ndata: " + initializeWithInstructions + "\n\n",
		},
		{
			name:     "GET stream leaves an error response alone",
			request:  "",
			response: "data: " + `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}` + "\n\n",
			want:     "data: " + `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}` + "\n\n",
		},
		{
			name:     "GET initialize result that cannot be decoded is replaced by an error",
			request:  "",
			response: "data: " + duplicateResult + "\n\n" + "data: " + initializeWithInstructions + "\n\n",
			want:     withheldEventFor(jsonrpc2.Int64ID(5)) + "data: " + initializeWithInstructions + "\n\n",
		},
		{
			name:    "GET initialize result whose id cannot be named is dropped",
			request: "",
			response: "data: " + `{"jsonrpc":"2.0","id":1,"id":2,"result":{"serverInfo":{}}}` + "\n\n" +
				"data: " + notification + "\n\n",
			want: "data: " + notification + "\n\n",
		},
		{
			name:     "GET data that is not valid JSON is dropped",
			request:  "",
			response: "data: {\"jsonrpc\":\"2.0\",\"id\":NaN}\n\n" + "data: " + initializeWithInstructions + "\n\n",
			want:     "data: " + rewrittenInitialize("server text\n\npolicy") + "\n\n",
		},
		{
			name:     "GET stream with a bare CR is ended",
			request:  "",
			response: "event: endpoint\rdata: /messages\r\r" + "data: " + initializeWithInstructions + "\n\n",
			want:     "",
		},
		{
			name:     "compressed GET stream is withheld",
			request:  "",
			headers:  []string{"Content-Encoding", "gzip"},
			response: "event: endpoint\ndata: /messages\n\n",
			want:     withheldBody(jsonrpc2.ID{}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := serveInstructions(t, cfg, tt.request,
				replyWith(http.StatusOK, "text/event-stream", tt.response, tt.headers...))
			assert.Equal(t, tt.want, rec.Body.String())
		})
	}
}

// TestInstructionsMiddleware_Bound pins that the bound on what the
// middleware holds is checked before a write is taken in, so one write and
// several are treated alike, and that a response over it is withheld.
func TestInstructionsMiddleware_Bound(t *testing.T) {
	t.Parallel()

	bound := len(initializeWithInstructions) - 1
	tests := []struct {
		name        string
		request     string
		contentType string
		response    string
		want        string
	}{
		{name: "JSON body in one write", request: initializeRequest, contentType: "application/json",
			response: initializeWithInstructions, want: withheldBody(jsonrpc2.Int64ID(1))},
		{name: "JSON body in two writes", request: initializeRequest, contentType: "application/json",
			response: initializeWithInstructions[:10] + "|" + initializeWithInstructions[10:],
			want:     withheldBody(jsonrpc2.Int64ID(1))},
		{name: "event on a POST stream", request: initializeRequest, contentType: "text/event-stream",
			response: "data: " + initializeWithInstructions[:10] + "|" + initializeWithInstructions[10:] + "\n\n",
			want:     withheldEventFor(jsonrpc2.Int64ID(1))},
		{name: "event on a GET stream", request: "", contentType: "text/event-stream",
			response: "data: " + initializeWithInstructions + "\n\n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middleware, err := newInstructionsMiddleware(InstructionsMiddlewareParams{
				Instructions: instructions.Config{Value: "policy"}, LegacySSE: tt.request == "",
			}, bound)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodGet, "/sse", nil)
			if tt.request != "" {
				req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.request))
			}
			rec := httptest.NewRecorder()
			ParsingMiddleware(middleware(replyWith(http.StatusOK, tt.contentType, tt.response))).ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Body.String())
		})
	}
}

// recordingWriter is a ResponseWriter that records every status written to
// it, its flushes and write deadlines, and fails its writes once failWrites
// is set.
type recordingWriter struct {
	header http.Header
	// sent is the header as it stood when the final status was written.
	sent       http.Header
	statuses   []int
	body       strings.Builder
	flushes    int
	deadlines  int
	failWrites bool
}

func newRecordingWriter() *recordingWriter {
	return &recordingWriter{header: http.Header{}}
}

func (w *recordingWriter) Header() http.Header { return w.header }

func (w *recordingWriter) WriteHeader(statusCode int) {
	if w.sent == nil && !informationalStatus(statusCode) {
		w.sent = w.header.Clone()
	}
	w.statuses = append(w.statuses, statusCode)
}

func (w *recordingWriter) Write(data []byte) (int, error) {
	if w.failWrites {
		return 0, errors.New("client went away")
	}
	return w.body.Write(data)
}

func (w *recordingWriter) Flush() { w.flushes++ }

func (w *recordingWriter) SetWriteDeadline(time.Time) error {
	w.deadlines++
	return nil
}

// serveRecorded sends a POSTed initialize through the MCP parser and an
// instructions middleware to handler, writing to w.
func serveRecorded(t *testing.T, w http.ResponseWriter, handler http.HandlerFunc) {
	t.Helper()
	middleware, err := NewInstructionsMiddleware(InstructionsMiddlewareParams{Instructions: instructions.Config{Value: "policy"}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeRequest))
	ParsingMiddleware(middleware(handler)).ServeHTTP(w, req)
}

func TestInstructionsWriter_Statuses(t *testing.T) {
	t.Parallel()

	t.Run("informational status is forwarded before the held final one", func(t *testing.T) {
		t.Parallel()
		w := newRecordingWriter()
		serveRecorded(t, w, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Link", "</style.css>; rel=preload")
			rw.WriteHeader(http.StatusEarlyHints)
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write([]byte(initializeWithInstructions))
		})
		assert.Equal(t, []int{http.StatusEarlyHints, http.StatusOK}, w.statuses)
		assert.Equal(t, rewrittenInitialize("server text\n\npolicy"), w.body.String())
	})

	t.Run("second final status is dropped while the first is held", func(t *testing.T) {
		t.Parallel()
		w := newRecordingWriter()
		serveRecorded(t, w, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusOK)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte(initializeWithInstructions))
		})
		assert.Equal(t, []int{http.StatusOK}, w.statuses)
		assert.Equal(t, rewrittenInitialize("server text\n\npolicy"), w.body.String())
	})

	t.Run("second final status is forwarded once the first has been", func(t *testing.T) {
		t.Parallel()
		w := newRecordingWriter()
		serveRecorded(t, w, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(http.StatusOK)
			rw.WriteHeader(http.StatusCreated)
			_, _ = rw.Write([]byte("data: " + initializeWithInstructions + "\n\n"))
		})
		assert.Equal(t, []int{http.StatusOK, http.StatusCreated}, w.statuses,
			"net/http reports the superfluous call, as it would without the middleware")
	})

	t.Run("header changed after the status is not forwarded with it", func(t *testing.T) {
		t.Parallel()
		w := newRecordingWriter()
		serveRecorded(t, w, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusOK)
			rw.Header().Set("X-Late", "1")
			_, _ = rw.Write([]byte(initializeWithInstructions))
		})
		assert.Equal(t, []int{http.StatusOK}, w.statuses)
		assert.Equal(t, "application/json", w.sent.Get("Content-Type"))
		assert.Empty(t, w.sent.Get("X-Late"), "a change made after the status is not sent with it")
		assert.Equal(t, "1", w.header.Get("X-Late"), "the change is back in the map, for trailers")
	})
}

func TestInstructionsWriter_ResponseController(t *testing.T) {
	t.Parallel()

	w := newRecordingWriter()
	serveRecorded(t, w, func(rw http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(rw)
		require.NoError(t, rc.SetWriteDeadline(time.Now().Add(time.Minute)))
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = rw.Write([]byte("data: " + initializeWithInstructions + "\n\n"))
		require.NoError(t, rc.Flush())
	})
	assert.Equal(t, 1, w.deadlines, "SetWriteDeadline reaches the wrapped writer through Unwrap")
	assert.Equal(t, 1, w.flushes)
	assert.Equal(t, "data: "+rewrittenInitialize("server text\n\npolicy")+"\n\n", w.body.String())
}

func TestInstructionsWriter_ReturnsWriteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "rewritten JSON body", contentType: "application/json", body: initializeWithInstructions},
		{name: "rewritten event", contentType: "text/event-stream", body: "data: " + initializeWithInstructions + "\n\n"},
		{name: "withheld body", contentType: "text/plain", body: "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newRecordingWriter()
			w.failWrites = true
			var writeErr error
			serveRecorded(t, w, func(rw http.ResponseWriter, _ *http.Request) {
				rw.Header().Set("Content-Type", tt.contentType)
				_, writeErr = rw.Write([]byte(tt.body))
			})
			require.Error(t, writeErr, "the handler learns that the client went away")
		})
	}
}

// TestInstructionsWriter_WarnsOnce pins that a withheld response is logged
// at WARN once, with its first cause, even when it fails more than one way.
//
//nolint:paralleltest // Replaces the default slog logger.
func TestInstructionsWriter_WarnsOnce(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The alias makes the result fail to rewrite, and the JSON body then
	// fails to be the result: two causes, one line.
	rec := serveInstructions(t, instructions.Config{Value: "policy"}, initializeRequest,
		replyWith(http.StatusOK, "application/json",
			`{"jsonrpc":"2.0","id":1,"result":{"Instructions":"a","protocolVersion":"2025-11-25"}}`))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	lines := strings.Count(logs.String(), "withholding a response whose instructions cannot be set")
	assert.Equal(t, 1, lines, logs.String())
	assert.Contains(t, logs.String(), "the result cannot be rewritten")
	assert.Contains(t, logs.String(), "target=initialize")
}

// TestInstructionsMiddleware_HoldsOnlyIncompleteEvents pins that the events
// of a stream reach the client as they end, while an event that has not ended
// is held.
func TestInstructionsMiddleware_HoldsOnlyIncompleteEvents(t *testing.T) {
	t.Parallel()

	middleware, err := NewInstructionsMiddleware(InstructionsMiddlewareParams{
		Instructions: instructions.Config{Value: "policy"}, LegacySSE: true,
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	first := "event: endpoint\ndata: /messages\n\n"
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(first + "data: " + initializeWithInstructions[:10]))
		w.(http.Flusher).Flush()
		assert.Equal(t, first, rec.Body.String(), "a complete event is written, the rest is held")
		_, _ = w.Write([]byte(initializeWithInstructions[10:] + "\n\n"))
		assert.Equal(t, first+"data: "+rewrittenInitialize("server text\n\npolicy")+"\n\n", rec.Body.String())
		_, _ = w.Write([]byte("data: after\n"))
		assert.Contains(t, rec.Body.String(), "data: after\n", "after the rewrite the stream passes through")
	})
	ParsingMiddleware(middleware(handler)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))
}

func TestInstructionsMiddleware_ForwardsRequestWithoutAcceptEncoding(t *testing.T) {
	t.Parallel()

	var acceptEncoding []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding = append(acceptEncoding, r.Header.Get("Accept-Encoding"))
		w.WriteHeader(http.StatusAccepted)
	})
	middleware, err := NewInstructionsMiddleware(InstructionsMiddlewareParams{Instructions: instructions.Config{Value: "policy"}})
	require.NoError(t, err)

	for _, body := range []string{initializeRequest, toolsListRequest} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Accept-Encoding", "gzip")
		ParsingMiddleware(middleware(handler)).ServeHTTP(httptest.NewRecorder(), req)
	}
	assert.Equal(t, []string{"", "gzip"}, acceptEncoding,
		"only a request whose response is read is forwarded without Accept-Encoding")
}

func TestNewInstructionsMiddleware_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	_, err := NewInstructionsMiddleware(InstructionsMiddlewareParams{Instructions: instructions.Config{Mode: "after", Value: "policy"}})
	require.ErrorContains(t, err, "invalid instructions mode")
	_, err = NewInstructionsMiddleware(InstructionsMiddlewareParams{})
	require.ErrorContains(t, err, "must not be empty")
}

func TestCreateInstructionsMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		wantErr string
	}{
		{name: "valid", params: `{"instructions":{"mode":"prepend","value":"policy"},"legacy_sse":true}`},
		{name: "invalid parameters", params: `[]`, wantErr: "failed to unmarshal instructions middleware parameters"},
		{name: "invalid config", params: `{"instructions":{"mode":"after","value":"policy"}}`, wantErr: "failed to create instructions middleware"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			runner := mocks.NewMockMiddlewareRunner(ctrl)
			if tt.wantErr == "" {
				runner.EXPECT().AddMiddleware(InstructionsMiddlewareType, gomock.Any()).
					Do(func(_ string, mw types.Middleware) {
						require.IsType(t, &InstructionsMiddleware{}, mw)
						assert.NotNil(t, mw.Handler())
						assert.NoError(t, mw.Close())
					})
			}
			err := CreateInstructionsMiddleware(&types.MiddlewareConfig{
				Type:       InstructionsMiddlewareType,
				Parameters: json.RawMessage(tt.params),
			}, runner)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestInstructionsMiddleware_TransportScope pins which requests the
// middleware reads for each transport: GET streams only on legacy SSE, and
// POST responses only otherwise.
func TestInstructionsMiddleware_TransportScope(t *testing.T) {
	t.Parallel()

	stream := "data: " + initializeWithInstructions + "\n\n"
	tests := []struct {
		name      string
		legacySSE bool
		method    string
		want      string
	}{
		{name: "legacy SSE reads a GET stream", legacySSE: true, method: http.MethodGet,
			want: "data: " + rewrittenInitialize("server text\n\npolicy") + "\n\n"},
		{name: "legacy SSE leaves a POST alone", legacySSE: true, method: http.MethodPost, want: stream},
		{name: "Streamable HTTP reads a POST", method: http.MethodPost,
			want: "data: " + rewrittenInitialize("server text\n\npolicy") + "\n\n"},
		{name: "Streamable HTTP leaves a GET stream alone", method: http.MethodGet, want: stream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			middleware, err := NewInstructionsMiddleware(InstructionsMiddlewareParams{
				Instructions: instructions.Config{Value: "policy"}, LegacySSE: tt.legacySSE,
			})
			require.NoError(t, err)
			req := httptest.NewRequest(tt.method, "/mcp", nil)
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(tt.method, "/mcp", strings.NewReader(initializeRequest))
			}
			rec := httptest.NewRecorder()
			ParsingMiddleware(middleware(replyWith(http.StatusOK, "text/event-stream", stream))).ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Body.String())
		})
	}
}

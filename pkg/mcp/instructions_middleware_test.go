// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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

// serveInstructions sends a request with body (a GET when body is empty)
// through the MCP parser and an instructions middleware for cfg to handler,
// and returns what the client receives.
func serveInstructions(
	t *testing.T, cfg instructions.Config, body string, handler http.Handler,
) *httptest.ResponseRecorder {
	t.Helper()
	middleware, err := NewInstructionsMiddleware(cfg)
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

func TestInstructionsMiddleware_JSON(t *testing.T) {
	t.Parallel()

	appendCfg := instructions.Config{Mode: instructions.ModeAppend, Value: "policy"}
	tests := []struct {
		name        string
		cfg         instructions.Config
		request     string
		status      int
		contentType string
		headers     []string
		response    string
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
			name:    "ambiguous member names pass through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: `{"jsonrpc":"2.0","id":1,"result":{"Instructions":"a","protocolVersion":"2025-11-25",` +
				`"instructions":"b","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`,
			want: `{"jsonrpc":"2.0","id":1,"result":{"Instructions":"a","protocolVersion":"2025-11-25",` +
				`"instructions":"b","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`,
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
			name:    "response with another id passes through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: strings.Replace(initializeWithInstructions, `"id":1`, `"id":2`, 1),
			want:     strings.Replace(initializeWithInstructions, `"id":1`, `"id":2`, 1),
		},
		{
			name:    "non-2xx status passes through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusInternalServerError, contentType: "application/json",
			response:    initializeWithInstructions,
			want:        initializeWithInstructions,
			keepsLength: true,
		},
		{
			name:    "compressed body passes through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			headers:     []string{"Content-Encoding", "br"},
			response:    initializeWithInstructions,
			want:        initializeWithInstructions,
			keepsLength: true,
		},
		{
			name:    "body that is not JSON passes through",
			cfg:     appendCfg,
			request: initializeRequest, status: http.StatusOK, contentType: "application/json",
			response: `{"jsonrpc":"2.0","id":1,"result":`,
			want:     `{"jsonrpc":"2.0","id":1,"result":`,
		},
		{
			name:    "other method passes through",
			cfg:     appendCfg,
			request: toolsListRequest, status: http.StatusOK, contentType: "application/json",
			response:    initializeWithInstructions,
			want:        initializeWithInstructions,
			keepsLength: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := serveInstructions(t, tt.cfg, tt.request,
				replyWith(tt.status, tt.contentType, tt.response, tt.headers...))

			assert.Equal(t, tt.status, rec.Code)
			assert.Equal(t, tt.want, rec.Body.String())
			if tt.keepsLength {
				assert.Equal(t, stringLen(tt.want), rec.Header().Get("Content-Length"),
					"a body passed through unread keeps its Content-Length")
			} else {
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
	tests := []struct {
		name     string
		request  string
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
			name:     "data split over several lines",
			request:  initializeRequest,
			response: "data: " + initializeWithInstructions[:24] + "\ndata: " + initializeWithInstructions[24:] + "\n\n",
			want:     "data: " + rewrittenInitialize("server text\n\npolicy") + "\n\n",
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
			name:     "other method passes through",
			request:  toolsListRequest,
			response: "data: " + initializeWithInstructions + "\n\n",
			want:     "data: " + initializeWithInstructions + "\n\n",
		},
		{
			name:     "last event without a blank line is read at the end",
			request:  initializeRequest,
			response: "data: " + initializeWithInstructions,
			want:     "data: " + rewrittenInitialize("server text\n\npolicy"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := serveInstructions(t, cfg, tt.request, replyWith(http.StatusOK, "text/event-stream", tt.response))
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.want, rec.Body.String())
			assert.True(t, rec.Flushed, "flushes must reach the client")
		})
	}
}

// TestInstructionsMiddleware_HoldsOnlyIncompleteEvents pins that the events
// of a stream reach the client as they end, while an event that has not ended
// is held.
func TestInstructionsMiddleware_HoldsOnlyIncompleteEvents(t *testing.T) {
	t.Parallel()

	middleware, err := NewInstructionsMiddleware(instructions.Config{Value: "policy"})
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
	middleware, err := NewInstructionsMiddleware(instructions.Config{Value: "policy"})
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

	_, err := NewInstructionsMiddleware(instructions.Config{Mode: "after", Value: "policy"})
	require.ErrorContains(t, err, "invalid instructions mode")
	_, err = NewInstructionsMiddleware(instructions.Config{})
	require.ErrorContains(t, err, "must not be empty")
}

func TestCreateInstructionsMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		wantErr string
	}{
		{name: "valid", params: `{"mode":"prepend","value":"policy"}`},
		{name: "invalid parameters", params: `[]`, wantErr: "failed to unmarshal instructions middleware parameters"},
		{name: "invalid config", params: `{"mode":"after","value":"policy"}`, wantErr: "failed to create instructions middleware"},
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

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// InstructionsMiddlewareType is the type of the middleware that sets the
// instructions of initialize and server/discover results.
const InstructionsMiddlewareType = "instructions"

// maxInstructionsHeldBytes bounds what the instructions middleware holds of a
// response it cannot read yet: a JSON body that is not complete, or an event
// that has not ended. A response that would make it hold more is forwarded
// unchanged from then on.
const maxInstructionsHeldBytes = 16 << 20

// instructionsMember is the name of the result member that carries the
// instructions, in both InitializeResult and DiscoverResult.
const instructionsMember = "instructions"

// InstructionsMiddleware wraps the instructions middleware function.
type InstructionsMiddleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *InstructionsMiddleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*InstructionsMiddleware) Close() error {
	return nil
}

// CreateInstructionsMiddleware is the factory for the instructions middleware.
// Its parameters are an instructions.Config.
func CreateInstructionsMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params instructions.Config
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal instructions middleware parameters: %w", err)
	}
	middleware, err := NewInstructionsMiddleware(params)
	if err != nil {
		return fmt.Errorf("failed to create instructions middleware: %w", err)
	}
	runner.AddMiddleware(config.Type, &InstructionsMiddleware{middleware: middleware})
	return nil
}

// NewInstructionsMiddleware returns a middleware that applies cfg to the
// instructions of the successful initialize and server/discover results the
// wrapped handler returns. It must run inside ParsingMiddleware, whose parse
// of a POST names the request a response answers.
//
// On a POST, only the response to an initialize or server/discover request is
// read, as a JSON body or an event stream, and only the result whose id is the
// request's is rewritten. The response to any other request passes through
// unread. On the legacy SSE transport, the response to a POSTed initialize
// arrives on the event stream a GET opened instead, so on an event stream that
// answers a GET the first result shaped like an InitializeResult (it has
// protocolVersion, capabilities and serverInfo) is rewritten, and the rest of
// the stream passes through unread.
//
// A rewrite sets the instructions member of the result and keeps every other
// member as the server sent it. Error responses, responses with a non-2xx
// status, and every other message pass through unchanged. The middleware fails
// open: a response to initialize or server/discover that it cannot read (one
// with a content coding or a charset other than UTF-8, one that would make it
// hold more than maxInstructionsHeldBytes, or one that does not decode as one
// unambiguous JSON-RPC message, such as one with duplicate or case-variant
// member names) is forwarded unchanged and logged as a warning.
func NewInstructionsMiddleware(cfg instructions.Config) (types.MiddlewareFunction, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target, ok := instructionsTargetOf(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			rw := &instructionsWriter{ResponseWriter: w, config: &cfg, target: target}
			// The middleware reads the bytes of the response body, so it
			// forwards the request without the client's Accept-Encoding, as
			// the tool filter does (see NewListToolsMappingMiddleware).
			next.ServeHTTP(rw, WithoutAcceptEncoding(r))
			// An inner buffering middleware may write the body after the
			// handler returns, so the held bytes are written out only now.
			rw.finish()
		})
	}, nil
}

// instructionsTarget names the response of a request whose instructions the
// middleware sets.
type instructionsTarget struct {
	// id is the id of the initialize or server/discover request a POST
	// carries. It is not valid for a GET.
	id jsonrpc2.ID
	// method is the method of that request, or empty for a GET.
	method string
}

// instructionsTargetOf reports which response of r the middleware rewrites,
// and whether r has one: a POST that carries an initialize or server/discover
// request, or a GET, which may open the event stream of the legacy SSE
// transport.
func instructionsTargetOf(r *http.Request) (instructionsTarget, bool) {
	switch r.Method {
	case http.MethodGet:
		return instructionsTarget{}, true
	case http.MethodPost:
		parsed := GetParsedMCPRequest(r.Context())
		if parsed == nil || !parsed.IsRequest {
			return instructionsTarget{}, false
		}
		if parsed.Method != "initialize" && parsed.Method != "server/discover" {
			return instructionsTarget{}, false
		}
		switch id := parsed.ID.(type) {
		case int64:
			return instructionsTarget{id: jsonrpc2.Int64ID(id), method: parsed.Method}, true
		case string:
			return instructionsTarget{id: jsonrpc2.StringID(id), method: parsed.Method}, true
		}
	}
	return instructionsTarget{}, false
}

// instructionsBodyMode is how the instructions writer reads a response body.
type instructionsBodyMode int

const (
	// instructionsPassThrough forwards the body unread.
	instructionsPassThrough instructionsBodyMode = iota
	// instructionsJSON holds a JSON body until it is one complete value.
	instructionsJSON
	// instructionsEventStream reads an event stream event by event.
	instructionsEventStream
)

// instructionsWriter wraps the ResponseWriter of a request whose response may
// carry an initialize or server/discover result, and rewrites the
// instructions of that result as it passes through.
type instructionsWriter struct {
	http.ResponseWriter
	config *instructions.Config
	target instructionsTarget

	// wroteHeader is set once the wrapped handler has written its final
	// status, explicitly or with its first Write or Flush.
	wroteHeader bool
	// mode is how the body is read, decided by that status and the header.
	mode instructionsBodyMode
	// readable is set when the status is 2xx and the header names a JSON body
	// or an event stream, so a response to initialize or server/discover is
	// expected to carry its result.
	readable bool
	// done is set once the response the target names has been found, so the
	// rest of the body passes through unread.
	done bool
	// warned is set once the writer has logged why it forwards the response
	// unchanged, so that it logs the failure once.
	warned bool
	// buffer holds the start of the body the writer cannot read yet: a JSON
	// body that is not complete, or an event that has not ended.
	buffer []byte
	// scanned is the length of the prefix of buffer that has been read for
	// the end of an event without finding one (see completeSSEEvents).
	scanned int
	// jsonScan reads a JSON body for the end of its value as it arrives.
	jsonScan jsonBodyScan
}

// WriteHeader decides how the body is read from the final status and the
// header written with it, and forwards both. An informational status is
// forwarded as it arrives and decides nothing.
func (rw *instructionsWriter) WriteHeader(statusCode int) {
	if informationalStatus(statusCode) || rw.wroteHeader {
		rw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	rw.wroteHeader = true
	rw.mode = rw.bodyMode(statusCode)
	if rw.mode != instructionsPassThrough {
		// A rewrite changes the length of the body.
		rw.Header().Del("Content-Length")
	}
	rw.ResponseWriter.WriteHeader(statusCode)
}

// Write reads the body as it arrives. What the writer can read already, a
// complete JSON body or the complete events of a stream, is written at once;
// the rest is held, up to maxInstructionsHeldBytes.
func (rw *instructionsWriter) Write(data []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	if rw.mode == instructionsPassThrough || rw.done {
		return rw.ResponseWriter.Write(data)
	}
	rw.buffer = append(rw.buffer, data...)
	switch rw.mode {
	case instructionsJSON:
		if rw.jsonScan.isValid(rw.buffer) {
			rw.writeHeld(rw.rewriteJSON(rw.buffer))
		}
	case instructionsEventStream:
		if end := completeSSEEvents(rw.buffer, rw.scanned); end > 0 {
			events := rw.rewriteEvents(rw.buffer[:end])
			rest := rw.buffer[end:]
			if rw.done {
				// The rest passes through unread, after what is held.
				rw.writeHeld(append(events, rest...))
				return len(data), nil
			}
			rw.buffer = rest
			if _, err := rw.ResponseWriter.Write(events); err != nil {
				return len(data), err
			}
		}
		rw.scanned = len(rw.buffer)
	case instructionsPassThrough:
	}
	if len(rw.buffer) > maxInstructionsHeldBytes {
		rw.warn("the response would make the middleware hold more than it may", "max_held_bytes", maxInstructionsHeldBytes)
		rw.writeHeld(rw.buffer)
	}
	return len(data), nil
}

// Flush forwards a flush to the wrapped ResponseWriter. What is held stays
// held: it is not complete, so no client could read it yet. A flush before
// the wrapped handler has written a status writes 200 first, as net/http does.
func (rw *instructionsWriter) Flush() {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap returns the wrapped ResponseWriter, for http.ResponseController.
func (rw *instructionsWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// finish writes out what is held once the wrapped handler has returned. The
// last event of a stream is read as it stands, since no more of it will
// arrive.
func (rw *instructionsWriter) finish() {
	if len(rw.buffer) > 0 {
		switch rw.mode {
		case instructionsJSON:
			rw.writeHeld(rw.rewriteJSON(rw.buffer))
		case instructionsEventStream:
			rw.writeHeld(rw.rewriteEvents(rw.buffer))
		case instructionsPassThrough:
			rw.writeHeld(rw.buffer)
		}
	}
	if rw.readable && !rw.done && rw.target.method != "" {
		rw.warn("the response carries no result whose instructions the middleware could set")
	}
}

// bodyMode returns how a response with statusCode and the header the wrapped
// handler wrote is read. Only a 2xx JSON body that answers a POST, and a 2xx
// event stream, are read; a body the middleware cannot read in the clear is
// passed through.
func (rw *instructionsWriter) bodyMode(statusCode int) instructionsBodyMode {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return instructionsPassThrough
	}
	mediaType, ambiguous := responseMediaType(rw.Header())
	mode := func() instructionsBodyMode {
		switch {
		case ambiguous:
			return instructionsPassThrough
		case mediaType == "text/event-stream":
			return instructionsEventStream
		case mediaType == "application/json" && rw.target.method != "":
			return instructionsJSON
		}
		return instructionsPassThrough
	}()
	if mode == instructionsPassThrough {
		if ambiguous {
			rw.warn("the response has an ambiguous Content-Type")
		}
		return mode
	}
	rw.readable = true
	if reason := unreadableBody(rw.Header()); reason != "" {
		rw.warn("the response body cannot be read", "reason", reason)
		rw.readable = false
		return instructionsPassThrough
	}
	return mode
}

// writeHeld writes data to the wrapped ResponseWriter in place of what is
// held, which it drops, and passes the rest of the body through.
func (rw *instructionsWriter) writeHeld(data []byte) {
	rw.buffer = nil
	rw.scanned = 0
	rw.mode = instructionsPassThrough
	if _, err := rw.ResponseWriter.Write(data); err != nil {
		slog.Debug("failed to write a response body", "error", err)
	}
}

// rewriteJSON returns body, a complete JSON body, with the instructions of the
// result it carries set, or body as it is when it carries no such result.
func (rw *instructionsWriter) rewriteJSON(body []byte) []byte {
	rewritten, ok := rw.rewriteMessage(body)
	if !ok {
		return body
	}
	trimmed := bytes.TrimRight(body, " \t\r\n")
	return append(rewritten, body[len(trimmed):]...)
}

// rewriteEvents returns stream, which holds complete events, with the
// instructions of the result an event carries set. Once the response the
// target names has been found, the events after it are left as they are.
func (rw *instructionsWriter) rewriteEvents(stream []byte) []byte {
	var out bytes.Buffer
	var event []sseLine
	writeEvent := func(end ...sseLine) {
		if !rw.done && len(event) > 0 {
			if data := bytes.Join(sseEventData(event), sseLF); len(data) > 0 {
				if rewritten, ok := rw.rewriteMessage(data); ok {
					event = replaceSSEData(event, rewritten)
				}
			}
		}
		// writeSSELines only fails when its writer does; a bytes.Buffer does not.
		_ = writeSSELines(&out, event...)
		_ = writeSSELines(&out, end...)
		event = nil
	}
	for _, line := range splitSSELines(stream) {
		if isBlankSSELine(line.text) {
			writeEvent(line)
			continue
		}
		event = append(event, line)
	}
	writeEvent()
	return out.Bytes()
}

// rewriteMessage returns data, one JSON-RPC message, with the instructions of
// its result set, and reports whether it did so. It sets done once it finds
// the response the target names, even when that response is an error.
func (rw *instructionsWriter) rewriteMessage(data []byte) ([]byte, bool) {
	message, err := decodeMessage(data)
	if err != nil {
		return nil, false
	}
	response, ok := message.(*jsonrpc2.Response)
	if !ok {
		return nil, false
	}
	if rw.target.method == "" {
		// A GET: the response on the stream that answers initialize.
		if response.Error != nil || !initializeResultShape(response.Result) {
			return nil, false
		}
	} else if response.ID != rw.target.id {
		return nil, false
	}
	rw.done = true
	if response.Error != nil {
		return nil, false
	}

	rewritten, err := rewriteInstructions(rw.config, data, response.Result)
	if err != nil {
		rw.warn("the result could not be rewritten", "error", err)
		return nil, false
	}
	return rewritten, true
}

// warn logs why the middleware forwards a response unchanged. A GET stream
// need not carry an initialize result, so only a response to initialize or
// server/discover is logged.
func (rw *instructionsWriter) warn(msg string, args ...any) {
	if rw.target.method == "" || rw.warned {
		return
	}
	rw.warned = true
	args = append([]any{"method", rw.target.method}, args...)
	slog.Warn("forwarding a response without setting its instructions: "+msg, args...)
}

// initializeResultShape reports whether result is an object with the members
// every InitializeResult has.
func initializeResultShape(result json.RawMessage) bool {
	members, err := DecodeJSONObjectMembers(result)
	if err != nil {
		return false
	}
	found := 0
	for _, member := range members {
		switch member.Name {
		case "protocolVersion", "capabilities", "serverInfo":
			found++
		}
	}
	return found == 3
}

// rewriteInstructions returns message, a JSON-RPC response whose result is
// result, with the instructions of the result set as cfg says. Every other
// member of the message and of the result keeps the bytes the server sent.
// message has passed decodeMessage, so no object in it has two members a
// decoder could read as one.
//
// The server's instructions are the value of the result's instructions
// member. A value that is not a string carries none and is replaced.
func rewriteInstructions(cfg *instructions.Config, message []byte, result json.RawMessage) ([]byte, error) {
	resultMembers, err := DecodeJSONObjectMembers(result)
	if err != nil {
		return nil, fmt.Errorf("result: %w", err)
	}
	at := slices.IndexFunc(resultMembers, func(member JSONObjectMember) bool {
		return member.Name == instructionsMember
	})
	var server string
	if at >= 0 && json.Unmarshal(resultMembers[at].Value, &server) != nil {
		server = ""
	}
	value, err := json.Marshal(cfg.Apply(server))
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		resultMembers[at].Value = value
	} else {
		resultMembers = append(resultMembers, JSONObjectMember{Name: instructionsMember, Value: value})
	}
	encodedResult, err := EncodeJSONObjectMembers(nil, resultMembers, nil)
	if err != nil {
		return nil, err
	}

	envelope, err := DecodeJSONObjectMembers(bytes.Trim(bytes.TrimPrefix(message, UTF8BOM), " \t\r\n"))
	if err != nil {
		return nil, fmt.Errorf("message: %w", err)
	}
	return EncodeJSONObjectMembers(nil, envelope, map[string]json.RawMessage{"result": encodedResult})
}

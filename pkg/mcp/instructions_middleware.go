// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// InstructionsMiddlewareType is the type of the middleware that sets the
// instructions of initialize and server/discover results.
const InstructionsMiddlewareType = "instructions"

// defaultMaxInstructionsHeldBytes bounds what the instructions middleware
// holds of a response it cannot read yet: a JSON body that is not complete,
// or an event that has not ended. An initialize or server/discover result is
// far smaller, and a response that would make the middleware hold more is
// withheld.
const defaultMaxInstructionsHeldBytes = 1 << 20

// instructionsMember is the name of the result member that carries the
// instructions, in both InitializeResult and DiscoverResult.
const instructionsMember = "instructions"

// InstructionsMiddlewareParams are the parameters of the instructions
// middleware.
type InstructionsMiddlewareParams struct {
	// Instructions is what the middleware sets.
	Instructions instructions.Config `json:"instructions"`
	// LegacySSE is set when the endpoint serves the 2024-11-05 HTTP+SSE
	// transport, which answers a POSTed request on the event stream a GET
	// opened. The middleware then reads those streams and leaves POST
	// responses alone. Otherwise it reads the responses to POSTed initialize
	// and server/discover requests and leaves GET streams alone, since a
	// Streamable HTTP GET stream carries no response to either.
	LegacySSE bool `json:"legacy_sse,omitempty"`
}

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
// Its parameters are an InstructionsMiddlewareParams.
func CreateInstructionsMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params InstructionsMiddlewareParams
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

// NewInstructionsMiddleware returns a middleware that applies the configured
// instructions to the successful initialize and server/discover results the
// wrapped handler returns. It must run inside ParsingMiddleware, whose parse
// of a POST names the request a response answers.
//
// Unless params.LegacySSE is set, only the response to a POSTed initialize or
// server/discover request is read, as a JSON body or an event stream, and only
// the result whose id is the request's is rewritten. The response to any other
// request passes through unread. With params.LegacySSE set, the response to a
// POSTed initialize arrives on the event stream a GET opened instead, so on
// such a stream a result shaped like an InitializeResult (it has
// protocolVersion, capabilities and serverInfo) is rewritten, and every POST
// passes through unread.
//
// On an event stream, every event is read whatever its event type, since some
// clients dispatch every type as a message (the go-sdk legacy SSE client
// does), and its result is rewritten. Only an event without a type, or of
// type message, which every client dispatches, ends the search, so a result
// in an event of another type cannot let a later one through unread. After
// that event the rest of the stream passes through unread.
//
// A rewrite sets the instructions member of the result and keeps every other
// member as the server sent it. Error responses and responses with a non-2xx
// status pass through unchanged.
//
// The middleware fails closed, as the tool filter does, so that a server
// cannot keep the configured text from clients. A response it reads but
// cannot rewrite is withheld and answered with the JSON-RPC error
// withheldResponse returns, and the cause is logged as a warning: a body with
// a content coding, a charset other than UTF-8 or an ambiguous Content-Type, a
// 2xx answer to a POST that is neither JSON nor an event stream or that never
// carries the result, a body or an event larger than the bound on what the
// middleware holds, an event stream whose lines clients split differently,
// and a result that does not decode as one unambiguous JSON-RPC message. A
// JSON body is answered under HTTP 500 in place of the server's status. On an
// event stream the error takes the place of the event, or, when the stream
// cannot be read, is written in place of the rest of it. An event whose
// request cannot be named, or whose type some clients skip, is dropped
// instead, since those clients would read an error in its place as the
// answer.
func NewInstructionsMiddleware(params InstructionsMiddlewareParams) (types.MiddlewareFunction, error) {
	return newInstructionsMiddleware(params, defaultMaxInstructionsHeldBytes)
}

// newInstructionsMiddleware is NewInstructionsMiddleware with the bound on
// what the middleware holds of a response.
func newInstructionsMiddleware(params InstructionsMiddlewareParams, maxHeldBytes int) (types.MiddlewareFunction, error) {
	if err := params.Instructions.Validate(); err != nil {
		return nil, err
	}
	if maxHeldBytes < 1 {
		return nil, fmt.Errorf("the bound on a held response must be positive")
	}
	config := &instructionsMiddlewareConfig{instructions: params.Instructions, maxHeldBytes: maxHeldBytes}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target, ok := instructionsTargetOf(r, params.LegacySSE)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			rw := &instructionsWriter{ResponseWriter: w, config: config, target: target}
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

// instructionsMiddlewareConfig is what an instructions middleware applies.
type instructionsMiddlewareConfig struct {
	instructions instructions.Config
	// maxHeldBytes bounds what the middleware holds of a response it cannot
	// read yet (see defaultMaxInstructionsHeldBytes).
	maxHeldBytes int
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

// stream reports whether the target is the legacy SSE event stream a GET
// opened, rather than the response to a POST.
func (t instructionsTarget) stream() bool {
	return t.method == ""
}

// instructionsTargetOf reports which response of r the middleware rewrites,
// and whether r has one: on the legacy SSE transport a GET, which opens its
// event stream, and otherwise a POST that carries an initialize or
// server/discover request.
func instructionsTargetOf(r *http.Request, legacySSE bool) (instructionsTarget, bool) {
	if legacySSE {
		return instructionsTarget{}, r.Method == http.MethodGet
	}
	if r.Method != http.MethodPost {
		return instructionsTarget{}, false
	}
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
	return instructionsTarget{}, false
}

// instructionsBodyMode is how the instructions writer reads a response body.
type instructionsBodyMode int

const (
	// instructionsPassThrough forwards the body unread.
	instructionsPassThrough instructionsBodyMode = iota
	// instructionsJSON holds a JSON body until it is one complete value. The
	// status and header are held with it.
	instructionsJSON
	// instructionsEventStream reads an event stream event by event.
	instructionsEventStream
	// instructionsUnreadable withholds a body the middleware has to read but
	// cannot. The status and header are held until it is known whether there
	// is a body.
	instructionsUnreadable
)

// messageAction is what the instructions writer does with a message it reads.
type messageAction int

const (
	// messagePass forwards the message as it is.
	messagePass messageAction = iota
	// messageRewrite forwards the message with the instructions of its result
	// set.
	messageRewrite
	// messageWithhold withholds the message.
	messageWithhold
)

// instructionsWriter wraps the ResponseWriter of a request whose response may
// carry an initialize or server/discover result, and rewrites the
// instructions of that result as it passes through.
type instructionsWriter struct {
	http.ResponseWriter
	config *instructionsMiddlewareConfig
	target instructionsTarget

	// wroteHeader is set once the wrapped handler has written its final
	// status, explicitly or with its first Write or Flush.
	wroteHeader bool
	// status is that final status.
	status int
	// headerPending is set while the status and header are held (see
	// instructionsJSON and instructionsUnreadable).
	headerPending bool
	// header is the header the wrapped handler wrote with its status, which
	// is forwarded when the status is (see commitHeader).
	header http.Header
	// mode is how the body is read, decided by the status and the header.
	mode instructionsBodyMode
	// reason names why an unreadable body cannot be read.
	reason string
	// done is set once the response the target names has been found, or
	// withheld, so the rest of the body passes through unread.
	done bool
	// discard is set once the rest of the response is withheld.
	discard bool
	// warned is set once the writer has logged why it withholds a response,
	// so that it logs the failure once.
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
// header written with it. An informational status is forwarded as it arrives
// and decides nothing. The final status is forwarded at once unless the body
// it comes with has to be read whole first (see instructionsBodyMode).
func (rw *instructionsWriter) WriteHeader(statusCode int) {
	if informationalStatus(statusCode) {
		rw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if rw.wroteHeader {
		// A second final status is forwarded, for net/http to report, only
		// once the first one has been.
		if !rw.headerPending {
			rw.ResponseWriter.WriteHeader(statusCode)
		}
		return
	}
	rw.wroteHeader = true
	rw.status = statusCode
	rw.mode, rw.reason = rw.bodyMode(statusCode)
	if rw.mode != instructionsPassThrough {
		// A rewrite changes the length of the body.
		rw.Header().Del("Content-Length")
	}
	if rw.mode == instructionsJSON || rw.mode == instructionsUnreadable {
		rw.headerPending = true
		rw.header = rw.Header().Clone()
		return
	}
	rw.ResponseWriter.WriteHeader(statusCode)
}

// Write reads the body as it arrives. What the writer can read already, a
// complete JSON body or the complete events of a stream, is written at once;
// the rest is held, up to the bound on what the middleware holds.
func (rw *instructionsWriter) Write(data []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	switch {
	case rw.discard:
		return len(data), nil
	case rw.mode == instructionsPassThrough:
		return rw.ResponseWriter.Write(data)
	case len(data) == 0:
		return 0, nil
	case rw.mode == instructionsUnreadable:
		rw.warn("the response body cannot be read", "reason", rw.reason)
		return len(data), rw.withholdRest()
	case len(rw.buffer)+len(data) > rw.config.maxHeldBytes:
		rw.warn("the response would make the middleware hold more than it may",
			"max_held_bytes", rw.config.maxHeldBytes)
		return len(data), rw.withholdRest()
	}
	rw.buffer = append(rw.buffer, data...)
	if rw.mode == instructionsJSON {
		if !rw.jsonScan.isValid(rw.buffer) {
			return len(data), nil
		}
		return len(data), rw.answerJSON()
	}
	return len(data), rw.readEvents(false)
}

// Flush forwards a flush to the wrapped ResponseWriter. What is held stays
// held: it is not complete, so no client could read it yet, and a held status
// stays held. A flush before the wrapped handler has written a status writes
// 200 first, as net/http does.
func (rw *instructionsWriter) Flush() {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	if !rw.headerPending {
		rw.flush()
	}
}

// Unwrap returns the wrapped ResponseWriter, for http.ResponseController.
func (rw *instructionsWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// finish writes out what is held once the wrapped handler has returned. The
// last event of a stream is read as it stands, since no more of it will
// arrive. A 2xx answer to a POST that has not carried the result by then is
// withheld.
func (rw *instructionsWriter) finish() {
	if !rw.wroteHeader || rw.discard {
		return
	}
	var err error
	switch rw.mode {
	case instructionsJSON:
		err = rw.answerJSON()
	case instructionsEventStream:
		if len(rw.buffer) > 0 {
			err = rw.readEvents(true)
		}
	case instructionsUnreadable, instructionsPassThrough:
	}
	if err == nil && !rw.done && !rw.discard && rw.mode != instructionsPassThrough {
		if rw.target.stream() {
			// A stream that carried no initialize result, or the empty
			// body of a GET the middleware could not have read.
			err = rw.forwardHeld(nil)
		} else {
			rw.warn("the response carries no result whose instructions the middleware could set")
			err = rw.withholdRest()
		}
	}
	if err != nil {
		slog.Debug("failed to write a response", "error", err)
	}
}

// bodyMode returns how a response with statusCode and the header the wrapped
// handler wrote is read, and for a body that cannot be read, why. A 2xx answer
// to a POST is read as a JSON body or an event stream, and is withheld if it
// is neither; a 2xx event stream that answers a GET is read, and any other
// body passes through.
func (rw *instructionsWriter) bodyMode(statusCode int) (instructionsBodyMode, string) {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return instructionsPassThrough, ""
	}
	header := rw.Header()
	mediaType, ambiguous := responseMediaType(header)
	switch {
	case ambiguous:
		return instructionsUnreadable, "ambiguous Content-Type"
	case mediaType == "text/event-stream":
	case rw.target.stream():
		// A GET that opens no event stream carries no result.
		return instructionsPassThrough, ""
	case mediaType == "application/json":
	default:
		return instructionsUnreadable, fmt.Sprintf("Content-Type %q is neither JSON nor an event stream", mediaType)
	}
	if reason := unreadableBody(header); reason != "" {
		return instructionsUnreadable, reason
	}
	if mediaType == "text/event-stream" {
		return instructionsEventStream, ""
	}
	return instructionsJSON, ""
}

// answerJSON answers with the JSON body the writer holds: with the
// instructions of its result set when it is the response the target names,
// as it is when that response is an error, and withheld otherwise.
func (rw *instructionsWriter) answerJSON() error {
	body := rw.buffer
	rw.buffer = nil
	rewritten, action, _ := rw.readMessage(body, true)
	switch {
	case action == messageRewrite:
		trimmed := bytes.TrimRight(body, " \t\r\n")
		return rw.forwardHeld(append(rewritten, body[len(trimmed):]...))
	case action == messagePass && rw.done:
		return rw.forwardHeld(body)
	}
	rw.warn("the JSON body is not the result the request asked for, or cannot be rewritten")
	return rw.withholdRest()
}

// readEvents reads the complete events of the stream the writer holds, all
// of what it holds when terminal is set, writes them with the instructions
// of the result an event carries set, and keeps the rest.
//
// A line that ends in a bare CR, or in one of lineSeparators, is split into
// lines, and so into events, differently by different clients. The events
// that end before such a line are read as usual, and unless they carry the
// result the target names, the rest of the stream is withheld.
func (rw *instructionsWriter) readEvents(terminal bool) error {
	// A CR at the end of what is held may start a CRLF that has not arrived.
	held := rw.buffer
	if !terminal {
		held = bytes.TrimSuffix(held, sseCR)
	}
	split := lineSplitIndex(held)
	end := len(rw.buffer)
	switch {
	case split >= 0:
		end = completeSSEEvents(rw.buffer[:split], 0)
	case !terminal:
		end = completeSSEEvents(rw.buffer, rw.scanned)
	}
	if end > 0 {
		if err := rw.writeEvents(end); err != nil {
			return err
		}
	}
	if split >= 0 && !rw.done {
		rw.warn("the event stream has a line ending that clients split differently")
		return rw.withholdRest()
	}
	rw.scanned = len(rw.buffer)
	return nil
}

// writeEvents writes the first end bytes the writer holds, which end with a
// complete event, with the instructions of the result an event carries set,
// and keeps the rest. Once the response the target names has been found, the
// rest passes through unread.
func (rw *instructionsWriter) writeEvents(end int) error {
	events := rw.rewriteEvents(rw.buffer[:end])
	rw.buffer = rw.buffer[end:]
	if rw.done {
		events = append(events, rw.buffer...)
		rw.buffer = nil
		rw.mode = instructionsPassThrough
	}
	_, err := rw.ResponseWriter.Write(events)
	return err
}

// rewriteEvents returns stream, which holds complete events, with each event
// replaced by what readEvent returns for it. Once the response the target
// names has been found, the events after it are left as they are.
func (rw *instructionsWriter) rewriteEvents(stream []byte) []byte {
	var out bytes.Buffer
	var event []sseLine
	for _, line := range splitSSELines(stream) {
		if isBlankSSELine(line.text) {
			out.Write(rw.readEvent(event, line))
			event = nil
			continue
		}
		event = append(event, line)
	}
	out.Write(rw.readEvent(event))
	return out.Bytes()
}

// readEvent returns what takes the place of event and end, the blank line
// that ends it if it has one: the event as it is, the event with the
// instructions of its result set, the error event that takes the place of a
// result the writer withholds, or nothing when that result answers no
// request the writer can name or is in an event some clients skip (see
// dispatchedAsMessage). An error event in place of a skipped event would be
// read by those clients as the answer to their request.
func (rw *instructionsWriter) readEvent(event []sseLine, end ...sseLine) []byte {
	lines := slices.Concat(event, end)
	if rw.done || len(event) == 0 {
		return encodeSSELines(lines)
	}
	data := bytes.Join(sseEventData(event), sseLF)
	if len(bytes.TrimSpace(data)) == 0 {
		return encodeSSELines(lines)
	}
	message := dispatchedAsMessage(event)
	rewritten, action, id := rw.readMessage(data, message)
	switch action {
	case messageRewrite:
		return encodeSSELines(slices.Concat(replaceSSEData(event, rewritten), end))
	case messageWithhold:
		if !id.IsValid() || !message {
			return nil
		}
		return sseErrorEvent(withheldResponse(id))
	case messagePass:
	}
	return encodeSSELines(lines)
}

// readMessage decides on data, one message: it returns data with the
// instructions of its result set when it is the response the target names,
// or says to pass or withhold it, and the id of the request it answers. When
// ends is set, the message is one every client reads (a JSON body, or an
// event of no type or of type message), and finding that response, even an
// error, sets done.
func (rw *instructionsWriter) readMessage(data []byte, ends bool) ([]byte, messageAction, jsonrpc2.ID) {
	message, err := decodeMessage(data)
	if err != nil {
		return rw.readUndecodable(data, ends)
	}
	response, ok := message.(*jsonrpc2.Response)
	if !ok {
		return nil, messagePass, jsonrpc2.ID{}
	}
	if rw.target.stream() {
		// The response on the legacy SSE stream that answers initialize.
		if response.Error != nil || !initializeResultShape(response.Result) {
			return nil, messagePass, jsonrpc2.ID{}
		}
	} else if response.ID != rw.target.id {
		return nil, messagePass, jsonrpc2.ID{}
	}
	rw.done = ends
	if response.Error != nil {
		return nil, messagePass, response.ID
	}
	rewritten, err := rewriteInstructions(&rw.config.instructions, data, response.Result)
	if err != nil {
		rw.warn("the result cannot be rewritten", "error", err)
		return nil, messageWithhold, response.ID
	}
	return rewritten, messageRewrite, response.ID
}

// readUndecodable decides on data that does not decode as one unambiguous
// message, such as data with duplicate or case-variant member names, which
// clients read differently. Data that does not start like a JSON value is
// read by no client as a message and passes. Otherwise, in the response to a
// POST, data passes only when it names the id of another request (see
// heldDataID); any other data may be the result the request asked for and is
// withheld in its place. On the legacy SSE stream, data that a client could
// read as an InitializeResult, or that is not valid JSON, which lenient
// clients may still parse, is withheld, with the id it names if it names one.
// Only when ends is set (see readMessage) does withholding the result end the
// search for it.
func (rw *instructionsWriter) readUndecodable(data []byte, ends bool) ([]byte, messageAction, jsonrpc2.ID) {
	if !startsLikeJSON(data) {
		return nil, messagePass, jsonrpc2.ID{}
	}
	id := heldDataID(data)
	if rw.target.stream() {
		identified := mayCarryInitializeResult(data)
		if !identified && json.Valid(data) {
			return nil, messagePass, jsonrpc2.ID{}
		}
		rw.done = identified && ends
	} else {
		if id.IsValid() && id != rw.target.id {
			return nil, messagePass, jsonrpc2.ID{}
		}
		id = rw.target.id
		rw.done = ends
	}
	rw.warn("a message does not decode as one unambiguous JSON-RPC message")
	return nil, messageWithhold, id
}

// withholdRest withholds the rest of the response, and returns the error
// writing the JSON-RPC error in its place met. While the status is held, the
// error is the whole answer, under HTTP 500 and without the server's status,
// Content-Encoding or Content-Length. On an event stream it is an event that
// follows the ones already written, when the stream answers a POST; a legacy
// SSE stream names no request to answer, so nothing more is written to it.
func (rw *instructionsWriter) withholdRest() error {
	rw.discard = true
	rw.done = true
	rw.buffer = nil
	response := withheldResponse(rw.target.id)
	if rw.headerPending {
		rw.header.Del("Content-Encoding")
		rw.header.Del("Content-Length")
		var err error
		rw.commitHeader(func() {
			err = WriteJSONRPCError(rw.ResponseWriter, http.StatusInternalServerError, response)
		})
		return err
	}
	if rw.mode != instructionsEventStream || rw.target.stream() {
		return nil
	}
	_, err := rw.ResponseWriter.Write(sseErrorEvent(response))
	rw.flush()
	return err
}

// forwardHeld forwards the held status and header, when they are held, and
// then body, and passes the rest of the response through.
func (rw *instructionsWriter) forwardHeld(body []byte) error {
	rw.mode = instructionsPassThrough
	if rw.headerPending {
		rw.commitHeader(func() { rw.ResponseWriter.WriteHeader(rw.status) })
	}
	if len(body) == 0 {
		return nil
	}
	_, err := rw.ResponseWriter.Write(body)
	return err
}

// commitHeader calls write, which writes the held status, while the header
// map holds the header the wrapped handler wrote with that status, as the
// tool filter does (see toolFilterWriter.forwardHeader): what the handler
// changed in the map afterwards takes effect only as trailers.
func (rw *instructionsWriter) commitHeader(write func()) {
	rw.headerPending = false
	header := rw.Header()
	later := header.Clone()
	clear(header)
	maps.Copy(header, rw.header)
	write()
	clear(header)
	maps.Copy(header, later)
}

// flush forwards a transport flush to the wrapped ResponseWriter.
func (rw *instructionsWriter) flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// warn logs why the middleware withholds a response, once per response.
func (rw *instructionsWriter) warn(msg string, args ...any) {
	if rw.warned {
		return
	}
	rw.warned = true
	target := rw.target.method
	if rw.target.stream() {
		target = "legacy SSE stream"
	}
	args = append([]any{"target", target}, args...)
	slog.Warn("withholding a response whose instructions cannot be set: "+msg, args...)
}

// encodeSSELines returns lines as the bytes of an event stream.
func encodeSSELines(lines []sseLine) []byte {
	var out bytes.Buffer
	// writeSSELines only fails when its writer does; a bytes.Buffer does not.
	_ = writeSSELines(&out, lines...)
	return out.Bytes()
}

// dispatchedAsMessage reports whether every client dispatches event as a
// message: its event type, the value of its last event field read as the
// WHATWG grammar reads it (without one leading space), is empty or message.
// Clients that follow that grammar skip an event of any other type. The
// go-sdk trims all white space around the value, so it also dispatches every
// event this reports, while the reverse does not hold: an event typed
// "message " ends no search, since WHATWG clients skip it.
func dispatchedAsMessage(event []sseLine) bool {
	var name []byte
	for _, line := range event {
		field, value, _ := bytes.Cut(bytes.TrimSuffix(line.text, sseCR), []byte(":"))
		if string(field) == "event" {
			name = bytes.TrimPrefix(value, []byte(" "))
		}
	}
	return len(name) == 0 || string(name) == "message"
}

// lineSplitIndex returns the index of the first bare CR (see hasBareCR) or
// line separator (see lineSeparators) in stream, or -1 when it has none.
func lineSplitIndex(stream []byte) int {
	for i := range stream {
		if stream[i] == '\r' && (i+1 == len(stream) || stream[i+1] != '\n') {
			return i
		}
		for _, separator := range lineSeparators {
			if bytes.HasPrefix(stream[i:], separator) {
				return i
			}
		}
	}
	return -1
}

// startsLikeJSON reports whether data, after any byte order mark and white
// space, starts like a JSON object or array.
func startsLikeJSON(data []byte) bool {
	data = bytes.TrimLeft(bytes.TrimPrefix(data, UTF8BOM), " \t\r\n")
	return len(data) > 0 && (data[0] == '{' || data[0] == '[')
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

// mayCarryInitializeResult reports whether some client could read data, one
// JSON value that may spell a member twice or in another case, as an
// InitializeResult: a message, or a message in a batch, with a member a
// decoder could bind to result whose value has a member it could bind to
// protocolVersion or serverInfo.
func mayCarryInitializeResult(data []byte) bool {
	data = bytes.TrimLeft(bytes.TrimPrefix(data, UTF8BOM), " \t\r\n")
	if len(data) > 0 && data[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(data, &batch) != nil {
			return false
		}
		return slices.ContainsFunc(batch, func(message json.RawMessage) bool {
			return messageMayCarryInitializeResult(message)
		})
	}
	return messageMayCarryInitializeResult(data)
}

func messageMayCarryInitializeResult(message []byte) bool {
	members, err := DecodeJSONObjectMembers(message)
	if err != nil {
		return false
	}
	for _, member := range members {
		if !strings.EqualFold(member.Name, "result") {
			continue
		}
		resultMembers, err := DecodeJSONObjectMembers(member.Value)
		if err != nil {
			continue
		}
		for _, resultMember := range resultMembers {
			if strings.EqualFold(resultMember.Name, "protocolVersion") ||
				strings.EqualFold(resultMember.Name, "serverInfo") {
				return true
			}
		}
	}
	return false
}

// rewriteInstructions returns message, a JSON-RPC response whose result is
// result, with the instructions of the result set as cfg says. Every other
// member of the message and of the result keeps the bytes the server sent.
//
// The server's instructions are the value of the result's one instructions
// member; a value that is not a string carries none and is replaced. A result
// that spells instructions in another case, or twice, is read differently by
// clients and cannot be rewritten (see UniqueCanonicalMember).
func rewriteInstructions(cfg *instructions.Config, message []byte, result json.RawMessage) ([]byte, error) {
	resultMembers, err := DecodeJSONObjectMembers(result)
	if err != nil {
		return nil, fmt.Errorf("result: %w", err)
	}
	current, found, err := UniqueCanonicalMember(resultMembers, instructionsMember)
	if err != nil {
		return nil, err
	}
	var server string
	if found && json.Unmarshal(current, &server) != nil {
		server = ""
	}
	value, err := json.Marshal(cfg.Apply(server))
	if err != nil {
		return nil, err
	}
	if !found {
		resultMembers = append(resultMembers, JSONObjectMember{Name: instructionsMember, Value: value})
	}
	encodedResult, err := EncodeJSONObjectMembers(nil, resultMembers,
		map[string]json.RawMessage{instructionsMember: value})
	if err != nil {
		return nil, err
	}

	envelope, err := DecodeJSONObjectMembers(bytes.Trim(bytes.TrimPrefix(message, UTF8BOM), " \t\r\n"))
	if err != nil {
		return nil, fmt.Errorf("message: %w", err)
	}
	return EncodeJSONObjectMembers(nil, envelope, map[string]json.RawMessage{"result": encodedResult})
}

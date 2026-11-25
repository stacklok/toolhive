// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// InstructionsMiddlewareType is the type of the middleware that sets the
// instructions of initialize and server/discover results.
const InstructionsMiddlewareType = "instructions"

// defaultMaxInstructionsHeldBytes bounds what the instructions middleware
// holds of the response to a POST it cannot read yet: a JSON body that is not
// complete, or an event that has not ended. An initialize or server/discover
// result is far smaller, and a response that would make the middleware hold
// more is withheld.
const defaultMaxInstructionsHeldBytes = 1 << 20

// defaultMaxInstructionsStreamEventBytes bounds the size of an event the
// middleware reads on a stream a GET opened, which carries every response of
// a session, not only the one to initialize. It is the size the go-sdk lets
// one event reach by default, as the tool filter's bound is (see
// defaultMaxHeldEventBytes). An event over it is withheld alone.
const defaultMaxInstructionsStreamEventBytes = defaultMaxHeldEventBytes

// rawStreamTailBytes is how much of what a stream that is no longer read
// event by event has carried is kept, so that a member name split across
// writes is still found (see mayNameResultMember). It is longer than the
// longest such name written with a JSON escape for every character.
const rawStreamTailBytes = 128

// errInstructionsWithheld is what Write returns once a response is withheld
// whose wrapped handler may go on writing, such as an event stream, so that
// the handler stops and the request ends instead of leaving the client
// waiting on a connection that delivers nothing more.
var errInstructionsWithheld = errors.New("the response is withheld: its instructions cannot be set")

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
	// and server/discover requests, and the streams a GET with Last-Event-ID
	// resumes, which may replay them. It leaves any other GET stream alone,
	// since a Streamable HTTP server sends no response on it.
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
// Unless params.LegacySSE is set, the response to a POSTed initialize or
// server/discover request is read, as a JSON body or an event stream, and the
// result whose id is the request's is rewritten. On that stream every event
// is read whatever its event type, since some clients dispatch every type as
// a message, but only an event without a type, or of type message, which
// every client dispatches, ends the search; the rest of the stream then
// passes through unread. The response to any other POST passes through
// unread.
//
// A stream a GET opened answers no single request: on the legacy SSE
// transport (params.LegacySSE) it carries every response of the session, and
// on Streamable HTTP a GET with Last-Event-ID resumes a stream that may
// replay the result. Such a stream is read for as long as it lasts, and every
// result that has protocolVersion, serverInfo or supportedVersions, under any
// spelling a decoder binds, is rewritten, whatever its id or event type, so a
// decoy result cannot let the real one through. An event that names none of
// those members, as its bytes show (see mayNameResultMember), passes without
// being decoded. Any other GET passes through unread.
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
// middleware holds (on a stream a GET opened, only an event that may name a
// result member: any other is forwarded as it arrives), an event stream whose
// lines clients split differently,
// and a result that does not decode as one unambiguous JSON-RPC message. A
// whole body is answered with the error under HTTP 500, with its own
// Content-Length. On an event stream the error takes the place of the event.
// An event whose request cannot be named, or whose type some clients skip, is
// dropped instead, since those clients would read an error in its place as
// the answer. A stream that cannot be read further, and a GET answered with
// the error, end: Write returns errInstructionsWithheld so that the wrapped
// handler stops.
func NewInstructionsMiddleware(params InstructionsMiddlewareParams) (types.MiddlewareFunction, error) {
	return newInstructionsMiddleware(params, defaultMaxInstructionsHeldBytes, defaultMaxInstructionsStreamEventBytes)
}

// newInstructionsMiddleware is NewInstructionsMiddleware with the bounds on
// what the middleware holds of the response to a POST and of an event on a
// stream a GET opened.
func newInstructionsMiddleware(
	params InstructionsMiddlewareParams, maxHeldBytes, maxStreamEventBytes int,
) (types.MiddlewareFunction, error) {
	if err := params.Instructions.Validate(); err != nil {
		return nil, err
	}
	if maxHeldBytes < 1 || maxStreamEventBytes < 1 {
		return nil, fmt.Errorf("the bounds on a held response must be positive")
	}
	config := &instructionsMiddlewareConfig{
		instructions:        params.Instructions,
		maxHeldBytes:        maxHeldBytes,
		maxStreamEventBytes: maxStreamEventBytes,
	}
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
	// maxHeldBytes bounds what the middleware holds of the response to a POST
	// it cannot read yet (see defaultMaxInstructionsHeldBytes).
	maxHeldBytes int
	// maxStreamEventBytes bounds an event on a stream a GET opened (see
	// defaultMaxInstructionsStreamEventBytes).
	maxStreamEventBytes int
}

// instructionsTarget names the response of a request whose instructions the
// middleware sets.
type instructionsTarget struct {
	// id is the id of the initialize or server/discover request a POST
	// carries. It is not valid for a GET.
	id jsonrpc2.ID
	// method is the method of that request, or empty for a GET.
	method string
	// streamKind names the stream a GET opened, for the log, and is empty
	// for a POST.
	streamKind string
}

// stream reports whether the target is a stream a GET opened, which answers
// no single request, rather than the response to a POST.
func (t instructionsTarget) stream() bool {
	return t.streamKind != ""
}

// instructionsTargetOf reports which response of r the middleware rewrites,
// and whether r has one: on the legacy SSE transport a GET, which opens its
// event stream, and otherwise a POST that carries an initialize or
// server/discover request, or a GET with Last-Event-ID, which resumes a
// stream that may replay its result.
func instructionsTargetOf(r *http.Request, legacySSE bool) (instructionsTarget, bool) {
	if legacySSE {
		return instructionsTarget{streamKind: "legacy SSE stream"}, r.Method == http.MethodGet
	}
	if r.Method == http.MethodGet && r.Header.Get("Last-Event-ID") != "" {
		return instructionsTarget{streamKind: "resumed stream"}, true
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
	// started is set once the byte order marks an event stream starts with
	// have been dropped (see startEventStream).
	started bool
	// skipping is set while the writer drops the rest of an event over the
	// bound on a stream a GET opened, and forwarding while it forwards the
	// rest of one that names no result member, up to the blank line that
	// ends it (see releaseOversized). skipLine holds the start of the line
	// the bytes read so far end in (see lineStart).
	skipping   bool
	forwarding bool
	skipLine   []byte
	// raw is set once a stream a GET opened has a line that clients split
	// differently, so it can no longer be read event by event. Its bytes are
	// then forwarded as they arrive while their member names are checked
	// (see mayNameResultMember), and rawTail holds the last of them, as it
	// does for an oversized event the writer forwards.
	raw     bool
	rawTail []byte
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
		return len(data), rw.ended(nil)
	case rw.mode == instructionsPassThrough:
		return rw.ResponseWriter.Write(data)
	case len(data) == 0:
		return 0, nil
	case rw.mode == instructionsUnreadable:
		rw.warn("the response body cannot be read", "reason", rw.reason)
		return len(data), rw.ended(rw.withholdRest())
	case rw.mode == instructionsEventStream && rw.target.stream():
		return len(data), rw.ended(rw.writeStream(data))
	case len(rw.buffer)+len(data) > rw.config.maxHeldBytes:
		rw.warn("the response would make the middleware hold more than it may",
			"max_held_bytes", rw.config.maxHeldBytes)
		return len(data), rw.ended(rw.withholdRest())
	}
	rw.buffer = append(rw.buffer, data...)
	if rw.mode == instructionsJSON {
		if !rw.jsonScan.isValid(rw.buffer) {
			return len(data), nil
		}
		return len(data), rw.answerJSON()
	}
	return len(data), rw.ended(rw.readEvents(false))
}

// ended returns err, the error a write met, or, once the rest of a response
// whose wrapped handler may go on writing is withheld, errInstructionsWithheld,
// so that the handler stops. That is an event stream, or the answer to a GET.
// A whole body withheld in answer to a POST is the error the middleware wrote
// in full, and the handler, which finishes its body, is left to do so.
func (rw *instructionsWriter) ended(err error) error {
	if err != nil {
		return err
	}
	if rw.discard && (rw.mode == instructionsEventStream || rw.target.stream()) {
		return errInstructionsWithheld
	}
	return nil
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
	switch {
	case rw.mode == instructionsJSON:
		err = rw.answerJSON()
	case rw.mode == instructionsEventStream && rw.target.stream():
		err = rw.finishStream()
	case rw.mode == instructionsEventStream && len(rw.buffer) > 0:
		err = rw.readEvents(true)
	}
	if err == nil && !rw.done && !rw.discard && rw.mode != instructionsPassThrough {
		if rw.target.stream() {
			// A stream a GET opened, or the empty body of a GET the
			// middleware could not have read.
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
	if !rw.startEventStream(terminal) {
		return nil
	}
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
// rest passes through unread; on a stream a GET opened it never is.
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
	rewritten, action, id := rw.readEventMessage(event, data, message)
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

// readEventMessage decides on data, the data of event, as readMessage does
// for the response to a POST and readStreamMessage for a stream a GET opened.
// On such a stream, data whose bytes name no result member passes undecoded,
// whatever its size (see mayNameResultMember), and an event over the bound
// that may name one is withheld unread with the id the start of its data
// names, if any (see releaseOversized for one that arrives in many writes).
func (rw *instructionsWriter) readEventMessage(
	event []sseLine, data []byte, message bool,
) ([]byte, messageAction, jsonrpc2.ID) {
	if !rw.target.stream() {
		return rw.readMessage(data, message)
	}
	if !mayNameResultMember(data) {
		return nil, messagePass, jsonrpc2.ID{}
	}
	size := 0
	for _, line := range event {
		size += len(line.text) + len(line.term)
	}
	if size > rw.config.maxStreamEventBytes {
		rw.warn("an event is larger than the middleware reads",
			"max_event_bytes", rw.config.maxStreamEventBytes)
		return nil, messageWithhold, heldDataID(data)
	}
	return rw.readStreamMessage(data)
}

// readStreamMessage decides on data, one message on a stream a GET opened
// whose bytes may name a result member: it returns data with the instructions
// of its result set when the result has a member a decoder binds to
// protocolVersion, serverInfo or supportedVersions, whatever its id, or says
// to pass or withhold it, and the id of the request it answers. Data that does
// not decode as one unambiguous message is withheld when a client could still
// read it as such a result, or when it is not valid JSON, which lenient
// clients may still parse.
func (rw *instructionsWriter) readStreamMessage(data []byte) ([]byte, messageAction, jsonrpc2.ID) {
	message, err := decodeMessage(data)
	if err != nil {
		if !startsLikeJSON(data) || (json.Valid(data) && !mayCarryResult(data)) {
			return nil, messagePass, jsonrpc2.ID{}
		}
		rw.warn("a message that may carry the result does not decode as one unambiguous JSON-RPC message")
		return nil, messageWithhold, heldDataID(data)
	}
	response, ok := message.(*jsonrpc2.Response)
	if !ok || response.Error != nil || !resultMayCarry(response.Result) {
		return nil, messagePass, jsonrpc2.ID{}
	}
	rewritten, err := rewriteInstructions(&rw.config.instructions, data, response.Result)
	if err != nil {
		rw.warn("the result cannot be rewritten", "error", err)
		return nil, messageWithhold, response.ID
	}
	return rewritten, messageRewrite, response.ID
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
	if response.ID != rw.target.id {
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

// readUndecodable decides on data, in the response to a POST, that does not
// decode as one unambiguous message, such as data with duplicate or
// case-variant member names, which clients read differently. Data that does
// not start like a JSON value is read by no client as a message and passes,
// and so does data that names the id of another request (see heldDataID). Any
// other data may be the result the request asked for and is withheld in its
// place. Only when ends is set (see readMessage) does that end the search.
func (rw *instructionsWriter) readUndecodable(data []byte, ends bool) ([]byte, messageAction, jsonrpc2.ID) {
	if !startsLikeJSON(data) {
		return nil, messagePass, jsonrpc2.ID{}
	}
	if id := heldDataID(data); id.IsValid() && id != rw.target.id {
		return nil, messagePass, jsonrpc2.ID{}
	}
	rw.done = ends
	rw.warn("a message does not decode as one unambiguous JSON-RPC message")
	return nil, messageWithhold, rw.target.id
}

// writeStream reads data, the next bytes of a stream a GET opened, which is
// read for as long as it lasts. Complete events are written as readEvent
// returns them, and an event that has not ended is held up to the bound on an
// event, past which the rest of it is forwarded or skipped as it arrives (see
// releaseOversized).
// A line that clients split differently stops the reading of events: the
// stream is forwarded as it arrives from then on, unless what it carries
// names a member of the result, which ends the stream (see enterRaw).
func (rw *instructionsWriter) writeStream(data []byte) error {
	if rw.raw {
		return rw.writeRaw(data)
	}
	switch {
	case rw.skipping:
		if data = rw.skip(data); rw.discard || len(data) == 0 {
			return nil
		}
	case rw.forwarding:
		rest, err := rw.forwardRest(data)
		if err != nil || rw.discard || len(rest) == 0 {
			return err
		}
		data = rest
	}
	rw.buffer = append(rw.buffer, data...)
	if !rw.startEventStream(false) {
		return nil
	}
	// A CR at the end of what is held may start a CRLF that has not arrived.
	if split := lineSplitIndex(bytes.TrimSuffix(rw.buffer, sseCR)); split >= 0 {
		return rw.enterRaw(split)
	}
	if end := completeSSEEvents(rw.buffer, rw.scanned); end > 0 {
		if err := rw.writeEvents(end); err != nil {
			return err
		}
	}
	rw.scanned = len(rw.buffer)
	if len(rw.buffer) > rw.config.maxStreamEventBytes {
		return rw.releaseOversized()
	}
	return nil
}

// finishStream reads what is held of a stream a GET opened once the wrapped
// handler has returned, its last event as it stands.
func (rw *instructionsWriter) finishStream() error {
	if rw.raw || rw.skipping || rw.forwarding || len(rw.buffer) == 0 {
		return nil
	}
	rw.startEventStream(true)
	if split := lineSplitIndex(rw.buffer); split >= 0 {
		return rw.enterRaw(split)
	}
	return rw.writeEvents(len(rw.buffer))
}

// enterRaw stops reading a stream a GET opened event by event, once what the
// writer holds has a line, at split, that clients split differently. The
// events that end before it are read as usual. The rest is forwarded as it
// is, and the stream from then on as it arrives (see writeRaw), unless it
// names a member of the result: no reading of it can then be rewritten, and
// the stream ends.
func (rw *instructionsWriter) enterRaw(split int) error {
	if end := completeSSEEvents(rw.buffer[:split], 0); end > 0 {
		if err := rw.writeEvents(end); err != nil {
			return err
		}
	}
	held := rw.buffer
	rw.buffer = nil
	if mayNameResultMember(held) {
		rw.warn("the event stream has a line ending that clients split differently")
		return rw.withholdRest()
	}
	rw.raw = true
	rw.rawTail = rawStreamTail(held)
	_, err := rw.ResponseWriter.Write(held)
	return err
}

// writeRaw forwards data, the next bytes of a stream that is no longer read
// event by event, unless they name a member of the result together with the
// bytes before them, which ends the stream.
func (rw *instructionsWriter) writeRaw(data []byte) error {
	window := slices.Concat(rw.rawTail, data)
	if mayNameResultMember(window) {
		rw.warn("the event stream has a line ending that clients split differently")
		return rw.withholdRest()
	}
	rw.rawTail = rawStreamTail(window)
	_, err := rw.ResponseWriter.Write(data)
	return err
}

// releaseOversized lets go of the event the writer holds, which is over the
// bound on an event. An event whose bytes so far name no result member cannot
// yet be a result whose instructions the middleware sets: it is forwarded,
// and the rest of it as it arrives, while its bytes are still checked (see
// forwardRest), so that a large tool result reaches the client intact. Any
// other event is withheld and the rest of it skipped (see skip): an error
// event takes its place when its start names the request it answers and
// clients dispatch it as a message, and it is dropped otherwise. The stream
// goes on after it, as the tool filter's does.
func (rw *instructionsWriter) releaseOversized() error {
	held := rw.buffer
	rw.skipLine = lineStart(held[bytes.LastIndexByte(held, '\n')+1:])
	rw.buffer = nil
	rw.scanned = 0
	if !mayNameResultMember(held) {
		rw.forwarding = true
		rw.rawTail = rawStreamTail(held)
		_, err := rw.ResponseWriter.Write(held)
		return err
	}
	rw.warn("an event that may carry the result is larger than the middleware reads",
		"max_event_bytes", rw.config.maxStreamEventBytes)
	id := heldEventID(held)
	message := dispatchedAsMessage(splitSSELines(held))
	rw.skipping = true
	if !id.IsValid() || !message {
		return nil
	}
	_, err := rw.ResponseWriter.Write(sseErrorEvent(withheldResponse(id)))
	rw.flush()
	return err
}

// forwardRest forwards data, the next bytes of an oversized event that named
// no result member so far (see releaseOversized), up to the blank line that
// ends it, and returns the bytes after that line, which start the next event.
// Bytes that name a result member together with those before them end the
// stream, since the start of the event has already reached the client and it
// can no longer be withheld alone.
func (rw *instructionsWriter) forwardRest(data []byte) ([]byte, error) {
	n, ended := rw.eventRest(data)
	window := slices.Concat(rw.rawTail, data[:n])
	if mayNameResultMember(window) {
		rw.warn("an event larger than the middleware reads names a member of the result")
		return nil, rw.withholdRest()
	}
	rw.rawTail = rawStreamTail(window)
	if _, err := rw.ResponseWriter.Write(data[:n]); err != nil {
		return nil, err
	}
	if !ended {
		return nil, nil
	}
	rw.forwarding = false
	rw.rawTail = nil
	return data[n:], nil
}

// skip drops data, bytes of the event the writer skips (see releaseOversized),
// up to the blank line that ends it, and returns the bytes after that line,
// which start the next event; it returns nothing while the event has not
// ended. A line that clients split differently would let the event end where
// this does not look, so it ends the stream.
func (rw *instructionsWriter) skip(data []byte) []byte {
	if lineSplitIndex(bytes.TrimSuffix(data, sseCR)) >= 0 {
		rw.warn("the event stream has a line ending that clients split differently")
		_ = rw.withholdRest()
		return nil
	}
	n, ended := rw.eventRest(data)
	if !ended {
		return nil
	}
	rw.skipping = false
	return data[n:]
}

// eventRest reads data, the next bytes of the event the writer skips or
// forwards unread, for the blank line that ends it, from the start of the
// line the bytes before it end in (see lineStart), so that a blank line split
// across writes still ends the event. It returns how many bytes of data
// belong to the event, that line included, and whether the event ended in
// data.
func (rw *instructionsWriter) eventRest(data []byte) (int, bool) {
	prefix := len(rw.skipLine)
	end := 0
	for _, line := range splitSSELines(slices.Concat(rw.skipLine, data)) {
		end += len(line.text) + len(line.term)
		if line.term == nil {
			rw.skipLine = lineStart(line.text)
			return len(data), false
		}
		if isBlankSSELine(line.text) {
			rw.skipLine = nil
			return end - prefix, true
		}
	}
	rw.skipLine = nil
	return len(data), false
}

// startEventStream drops the byte order marks the event stream the writer
// holds starts with (see trimLeadingBOMs), once. Clients drop a leading mark
// before they read lines, so without this the first line's field name would
// start with it and the first event's type would be read as no type at all.
// The stream is forwarded without the marks, as the tool filter's is, so that
// every client reads the same events. It reports false while what is held
// may still be the start of a mark, unless terminal is set.
func (rw *instructionsWriter) startEventStream(terminal bool) bool {
	if rw.started {
		return true
	}
	rest := trimLeadingBOMs(rw.buffer)
	if !terminal && (bytes.HasPrefix(UTF8BOM, rest) || bytes.HasPrefix(latin1UTF8BOM, rest)) {
		return false
	}
	rw.buffer = rest
	rw.scanned = 0
	rw.started = true
	return true
}

// withholdRest withholds the rest of the response, and returns the error
// writing the JSON-RPC error in its place met. While the status is held, the
// error is the whole answer, under HTTP 500 with its own Content-Length and
// without the server's status or Content-Encoding, and is flushed, so that a
// client can read all of it however long the wrapped handler goes on. On an
// event stream it is an event that follows the ones already written, when
// the stream answers a POST; a stream a GET opened names no request to
// answer, so nothing more is written to it.
func (rw *instructionsWriter) withholdRest() error {
	rw.discard = true
	rw.done = true
	rw.buffer = nil
	response := withheldResponse(rw.target.id)
	if rw.headerPending {
		body := EncodeJSONRPCError(response)
		rw.header.Del("Content-Encoding")
		rw.header.Set("Content-Type", "application/json")
		rw.header.Set("Content-Length", strconv.Itoa(len(body)))
		rw.commitHeader(func() { rw.ResponseWriter.WriteHeader(http.StatusInternalServerError) })
		_, err := rw.ResponseWriter.Write(body)
		rw.flush()
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
		target = rw.target.streamKind
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

// resultMemberNames are the members, lower-cased, that make a result on a
// stream a GET opened one whose instructions the middleware sets: an
// InitializeResult has protocolVersion and serverInfo, and a DiscoverResult
// has supportedVersions.
var resultMemberNames = []string{"protocolversion", "serverinfo", "supportedversions"}

// resultMayCarry reports whether result is an object with a member a decoder
// could bind to one of resultMemberNames, under any spelling it folds to one
// of them.
func resultMayCarry(result json.RawMessage) bool {
	members, err := DecodeJSONObjectMembers(result)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(members, func(member JSONObjectMember) bool {
		return slices.ContainsFunc(resultMemberNames, func(name string) bool {
			return strings.EqualFold(member.Name, name)
		})
	})
}

// mayCarryResult reports whether some client could read data, one JSON value
// that may spell a member twice or in another case, as a result that
// resultMayCarry reports: a message, or a message in a batch, with a member a
// decoder could bind to result whose value resultMayCarry reports.
func mayCarryResult(data []byte) bool {
	data = bytes.TrimLeft(bytes.TrimPrefix(data, UTF8BOM), " \t\r\n")
	if len(data) > 0 && data[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(data, &batch) != nil {
			return false
		}
		return slices.ContainsFunc(batch, func(message json.RawMessage) bool {
			return messageMayCarryResult(message)
		})
	}
	return messageMayCarryResult(data)
}

func messageMayCarryResult(message []byte) bool {
	members, err := DecodeJSONObjectMembers(message)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(members, func(member JSONObjectMember) bool {
		return strings.EqualFold(member.Name, "result") && resultMayCarry(member.Value)
	})
}

// mayNameResultMember reports whether data, part of an event stream, could
// spell one of resultMemberNames for some decoder: whether it holds one once
// its JSON \u escapes are read and it is folded as decoders fold member names
// (Unicode case, and the long s and Kelvin sign that fold to s and k). Data
// for which it reports false names none of them, however it is decoded, so
// it carries no result whose instructions the middleware sets.
func mayNameResultMember(data []byte) bool {
	folded := bytes.ReplaceAll(bytes.ToLower(unescapeJSONUnicode(data)), []byte("\u017f"), []byte("s"))
	return slices.ContainsFunc(resultMemberNames, func(name string) bool {
		return bytes.Contains(folded, []byte(name))
	})
}

// unescapeJSONUnicode returns data with every \uXXXX escape replaced by the
// character it names. An escaped backslash before a u is read as an escape
// too, which can only make mayNameResultMember report a name that is not
// there.
func unescapeJSONUnicode(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u`)) {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+5 < len(data) && data[i+1] == 'u' {
			// Four hex digits fit in 16 bits, so the conversion cannot overflow.
			if r, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16); err == nil {
				out = utf8.AppendRune(out, rune(uint16(r)))
				i += 5
				continue
			}
		}
		out = append(out, data[i])
	}
	return out
}

// rawStreamTail returns a copy of the last rawStreamTailBytes of data, or of
// all of it.
func rawStreamTail(data []byte) []byte {
	return slices.Clone(data[max(0, len(data)-rawStreamTailBytes):])
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

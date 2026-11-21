// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// maxSSEEventBytes bounds how much unterminated data the filter buffers while it
// waits for the blank line that ends an event. An event still open past this
// size fails the stream closed. A complete event of any size is filtered.
// The filter splits events on LF only, so a stream whose lines end in a bare CR
// never completes an event. It is held until this cap or the end of the stream,
// then fails closed. A client that splits on bare CR still sees a complete event,
// so this is a stall, not a bypass.
const maxSSEEventBytes = 16 << 20

// errSSEEncodedBody closes a filtered stream whose body is content-coded.
var errSSEEncodedBody = errors.New("filtered event stream is content-coded and cannot be parsed")

// maxSniffedBodyBytes bounds a held 2xx body with a non-event-stream media type. Such a
// body is read only once the handler returns, so it is held whole until then.
const maxSniffedBodyBytes = 16 << 20

// errSSEEventTooLarge closes the stream when an unterminated event exceeds maxSSEEventBytes.
var errSSEEventTooLarge = errors.New("SSE event exceeds the filter's size limit")

// errSSEBodyTooLarge closes a held 2xx body that exceeds maxSniffedBodyBytes.
var errSSEBodyTooLarge = errors.New("held 2xx body exceeds the filter's size limit")

// errSSEBareCR is logged when an SSE event carries a lone CR line break the filter cannot split.
var errSSEBareCR = errors.New("event carries a bare CR line break that the filter cannot split")

// errSSEUnrecognizedMediaType is logged when a successful response with a
// Content-Type other than text/event-stream carries neither a JSON-RPC response
// nor an SSE result. Such a body is rejected with an error carrying its id where
// that id is readable, and dropped otherwise.
var errSSEUnrecognizedMediaType = errors.New(
	"successful response has a Content-Type other than text/event-stream and no readable JSON-RPC result")

// listMembers maps each list member of a result to the method whose filter applies.
var listMembers = map[string]string{
	"tools":             string(mcp.MethodToolsList),
	"prompts":           string(mcp.MethodPromptsList),
	"resources":         string(mcp.MethodResourcesList),
	"resourceTemplates": string(mcp.MethodResourcesTemplatesList),
	"skills":            "skills/list",
}

// sseStreamFilterWriter filters a long-lived GET text/event-stream. Unlike
// ResponseFilteringWriter, which buffers the whole response until the handler
// returns, this writer forwards each event as soon as it is complete: on the
// GET stream the handler only returns when the client disconnects.
//
// Each event is classified by the shape of its result, not by request method:
// a result carrying tools, prompts, resources, resourceTemplates or skills is
// filtered with the matching filter. A frame that is not a clean Response but
// still carries a result fails closed. Everything else, including a body with
// no result, passes through unchanged.
type sseStreamFilterWriter struct {
	http.ResponseWriter
	authorizer       authorizers.Authorizer
	request          *http.Request
	annotationCache  *AnnotationCache
	passThroughTools map[string]struct{}

	buffer      []byte
	decided     bool
	passThrough bool
	started     bool
	failed      bool
	failErr     error
	// sniff marks a successful body with another media type. The whole body is
	// buffered and read as JSON-RPC or SSE once complete (see writeSniffed).
	sniff bool
	// encoded marks a filtered stream whose body is content-coded, which cannot be parsed.
	encoded bool
	// committed records that the status line has been written.
	committed bool
}

// newSSEStreamFilterWriter wraps w for a GET stream served on behalf of r.
func newSSEStreamFilterWriter(
	w http.ResponseWriter, a authorizers.Authorizer, r *http.Request,
	annotationCache *AnnotationCache, passThroughTools map[string]struct{},
) *sseStreamFilterWriter {
	return &sseStreamFilterWriter{
		ResponseWriter:   w,
		authorizer:       a,
		request:          r,
		annotationCache:  annotationCache,
		passThroughTools: passThroughTools,
	}
}

// Unwrap exposes the underlying writer, so http.ResponseController can reach
// deadlines on the real connection.
func (w *sseStreamFilterWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Hijack refuses a connection takeover on a filtered stream. A hijacked connection
// would carry backend bytes past the filter in both directions. A pass-through
// response (a non-2xx, non-event-stream answer) is not filtered, so its takeover is
// allowed. Hijack is refused until the mode is decided. ReverseProxy hijacks a 101
// upgrade before it writes the status, so an upgrade through it is refused here.
func (w *sseStreamFilterWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !w.decided || !w.passThrough || !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

// WriteHeader forwards the status immediately, since the stream may stay open
// for a long time. Filtered modes strip Content-Length in markFiltered, because
// filtering can change the body size; a passed-through body keeps it.
func (w *sseStreamFilterWriter) WriteHeader(statusCode int) {
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		// An informational response (e.g. 103 Early Hints) precedes the final status.
		// Forward it without deciding the mode, so the final response still decides.
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	w.decide(statusCode)
	if w.encoded {
		w.refuseEncoded()
		return
	}
	w.committed = true
	w.ResponseWriter.WriteHeader(statusCode)
}

// refuseEncoded answers a content-coded filtered stream with 502, once. The body
// cannot be filtered, and forwarding it unfiltered would leak list answers.
func (w *sseStreamFilterWriter) refuseEncoded() {
	if !w.encoded || w.committed {
		return
	}
	w.committed = true
	// The backend's coding and body headers describe a body that is not sent.
	h := w.Header()
	h.Del("Content-Encoding")
	h.Del("Content-Type")
	h.Del("Content-Length")
	w.ResponseWriter.WriteHeader(http.StatusBadGateway)
}

// Write buffers the body and forwards every complete event. It returns the length
// of data as given, even when a leading byte order mark is stripped.
func (w *sseStreamFilterWriter) Write(data []byte) (int, error) {
	w.decide(http.StatusOK)
	if w.passThrough {
		return w.ResponseWriter.Write(data)
	}
	if w.failed {
		w.refuseEncoded()
		return 0, w.failErr
	}
	n := len(data)
	w.buffer = append(w.buffer, data...)
	if !w.started {
		// A byte order mark can open the stream, and a backend may split it across
		// writes. Hold back a partial BOM until it is complete, then strip it once.
		if len(w.buffer) < len(mcpparser.UTF8BOM) && bytes.HasPrefix(mcpparser.UTF8BOM, w.buffer) {
			return n, nil
		}
		w.started = true
		w.buffer = bytes.TrimPrefix(w.buffer, mcpparser.UTF8BOM)
	}
	if w.sniff {
		if len(w.buffer) > maxSniffedBodyBytes {
			return 0, w.failSniffed()
		}
		return n, nil
	}
	if err := w.drainCompleteEvents(); err != nil {
		return 0, err
	}
	return n, nil
}

// Flush forwards every complete event, then flushes the transport. An event
// still missing its terminating blank line stays buffered for a later call.
func (w *sseStreamFilterWriter) Flush() {
	// Decide before flushing: a flush commits the headers, and the mode decides
	// whether Cache-Control: private, no-store must be among them.
	w.decide(http.StatusOK)
	if w.failed {
		w.refuseEncoded()
		return
	}
	if w.sniff {
		// A sniffed body is decided only once it is complete, so flush the transport only.
		if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if err := w.drainCompleteEvents(); err != nil {
		// Usually the client has gone away. That is routine on a long-lived stream.
		slog.Debug("stopped writing filtered SSE stream", "error", err)
		return
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// finish runs once the wrapped handler has returned. No more data is coming,
// so any trailing partial event is filtered and written too.
func (w *sseStreamFilterWriter) finish() {
	if w.passThrough || w.failed || len(w.buffer) == 0 {
		return
	}
	if w.sniff {
		body := w.buffer
		w.buffer = nil
		w.writeSniffed(body)
		return
	}
	if err := w.drainCompleteEvents(); err != nil {
		slog.Debug("stopped writing filtered SSE stream", "error", err)
		return
	}
	trailing := w.buffer
	w.buffer = nil
	if len(trailing) == 0 {
		return
	}
	if err := w.writeLines(w.processLines(parseSSELines(trailing))); err != nil {
		slog.Debug("stopped writing filtered SSE stream", "error", err)
	}
}

// decide picks the mode once, from the Content-Type the handler set and the
// status it is answering with. An event stream is parsed and filtered as it
// arrives. A non-2xx body (an error) is passed through unchanged. A successful
// body with any other media type is sniffed (writeSniffed): it is held until the
// handler returns, then read as JSON-RPC or SSE, or dropped. A client that ignores
// Content-Type could read it as events, so it is never passed through unfiltered.
// Held bodies are buffered up to maxSniffedBodyBytes, so a long-lived mislabeled
// stream reaches the client only when it ends or hits that cap. Filtered modes
// carry caller-specific list answers, so they are marked uncacheable before
// their headers are committed, the same as the POST path.
func (w *sseStreamFilterWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	// A non-2xx answer is an error. It passes through unchanged whatever its media type,
	// so an error sent as text/event-stream is not filtered as a stream.
	if status < 200 || status >= 300 {
		w.passThrough = true
		return
	}
	mediaType, _, _ := mime.ParseMediaType(w.ResponseWriter.Header().Get("Content-Type"))
	if mediaType == "text/event-stream" {
		w.markFiltered()
		return
	}
	if status >= 200 && status < 300 {
		// Sniff it: a client may read it as events, so it is filtered like one or dropped.
		w.sniff = true
		w.markFiltered()
		return
	}
	w.passThrough = true
}

// markFiltered prepares the headers of a filtered mode. Filtering changes the body
// size, so Content-Length must go, and the response must not be cached. Both run
// before headers are committed, which happens on WriteHeader, Write or Flush.
func (w *sseStreamFilterWriter) markFiltered() {
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.Header().Set("Cache-Control", "private, no-store")
	for _, value := range w.ResponseWriter.Header().Values("Content-Encoding") {
		for _, coding := range strings.Split(value, ",") {
			if c := strings.TrimSpace(coding); c != "" && !strings.EqualFold(c, "identity") {
				w.encoded = true
				w.failed = true
				w.failErr = errSSEEncodedBody
			}
		}
	}
}

// writeSniffed handles a complete 2xx body whose media type is not an event
// stream. The body is read as one JSON-RPC response when it parses as one, and
// as SSE when its framing carries a result (sniffBodyShape decides). A clean
// JSON-RPC response with a list member is filtered and sent; any other clean
// response is sent unchanged. A body carrying no result in either shape is
// dropped, so nothing unfiltered reaches the client.
func (w *sseStreamFilterWriter) writeSniffed(body []byte) {
	if hasAmbiguousResultKey(body) {
		w.writeRejection(body, "rejected a 2xx body whose result key is duplicated or differs in case")
		return
	}
	message, _ := jsonrpc2.DecodeMessage(body)
	response, isResponse := message.(*jsonrpc2.Response)
	if !isResponse {
		// The shared classifier decides for a body that is not a clean Response.
		action, _ := classifyUnlabeledBody(body)
		switch action {
		case sniffRejectJSON:
			w.writeRejection(body, "rejected a 2xx JSON body carrying a result that is not a single JSON-RPC response")
			return
		case sniffRejectNamed:
			w.writeRejection(body, "dropped a 2xx body naming a result that could not be read")
			return
		case sniffPassThrough:
			if _, err := w.ResponseWriter.Write(body); err != nil {
				slog.Debug("stopped writing filtered SSE stream", "error", err)
			}
			return
		case sniffFilterSSE, sniffFilterJSON:
			// Filtered per event below.
		}
		if err := w.writeLines(w.processLines(parseSSELines(body))); err != nil {
			slog.Debug("stopped writing filtered SSE stream", "error", err)
		}
		return
	}
	method, err := resultMethod(response.Result)
	if err != nil {
		slog.Warn("rejected a 2xx JSON-RPC response whose result names lists in conflict", "error", err)
		// Answer with an error that carries the request id, so the client does not wait for a timeout.
		_, _ = w.ResponseWriter.Write(encodeErrorBody(response.ID))
		return
	}
	out := body
	if method != "" {
		filtered, ferr := w.newFilter(method).filterAndEncode(response)
		if ferr != nil {
			slog.Warn("answering a 2xx JSON-RPC response that failed to filter with an error", "error", ferr)
			out = encodeErrorBody(response.ID)
		} else {
			out = filtered
		}
	}
	if _, err := w.ResponseWriter.Write(out); err != nil {
		slog.Debug("stopped writing filtered SSE stream", "error", err)
	}
}

// drainCompleteEvents writes every event that is complete in the buffer and
// keeps the rest.
func (w *sseStreamFilterWriter) drainCompleteEvents() error {
	for {
		end := firstEventEnd(w.buffer)
		if end == 0 {
			break
		}
		chunk := w.buffer[:end]
		w.buffer = w.buffer[end:]
		if err := w.writeLines(w.processLines(parseSSELines(chunk))); err != nil {
			return err
		}
	}
	if len(w.buffer) > maxSSEEventBytes {
		// An unterminated event has grown past the cap. Nothing buffered is written out:
		// the client gets one error for it instead.
		return w.failWithEnvelope(errSSEEventTooLarge)
	}
	return nil
}

// failSniffed ends a held body that outgrew maxSniffedBodyBytes.
func (w *sseStreamFilterWriter) failSniffed() error {
	return w.failWithEnvelope(errSSEBodyTooLarge)
}

// failWithEnvelope stops the stream for good. It writes one JSON-RPC error that carries
// the id read from the held bytes, when one can be read, and drops the held bytes. The
// error is written as an event when the held bytes are event-stream framed.
func (w *sseStreamFilterWriter) failWithEnvelope(cause error) error {
	held := w.buffer
	w.buffer = nil
	w.failed = true
	w.failErr = cause
	slog.Warn("ending a filtered event stream with an error", "error", cause)
	if hasDataLine(held) {
		payload := bytes.Join(eventDataValues(parseSSELines(held)), []byte("\n"))
		body := encodeErrorBody(topLevelID(payload))
		_ = w.writeLines(errorEventLines(body))
		_, _ = w.ResponseWriter.Write([]byte("\n"))
		return cause
	}
	_, _ = w.ResponseWriter.Write(encodeErrorBody(topLevelID(held)))
	return cause
}

// writeLines writes each line with the terminator it was read with.
func (w *sseStreamFilterWriter) writeLines(lines []sseLine) error {
	for _, l := range lines {
		if _, err := w.ResponseWriter.Write(l.text); err != nil {
			return err
		}
		if _, err := w.ResponseWriter.Write(l.term); err != nil {
			return err
		}
	}
	return nil
}

// processLines filters each event in lines, choosing the filter from the shape of
// its result. Blank lines separate events and are kept as they are.
func (w *sseStreamFilterWriter) processLines(lines []sseLine) []sseLine {
	return processSSELines(lines, w.filterEvent)
}

// filterEvent filters one event's lines, choosing the filter from the shape of
// its result.
func (w *sseStreamFilterWriter) filterEvent(event []sseLine) []sseLine {
	if len(event) == 0 {
		return nil
	}
	dataValues, payload, loneCR := assembleSSEEvent(event)
	if loneCR {
		slog.Warn("rejected an SSE event with a bare CR line break", "error", errSSEBareCR)
		return errorEventLines(encodeErrorBody(frameID(event)))
	}
	if len(payload) == 0 {
		return event
	}
	// A duplicated or case-variant result key is rejected before decoding: the
	// client's decoder may pick a different key than this filter would, so the
	// body cannot be trusted to show the same list the client sees.
	if hasAmbiguousResultKey(payload) {
		return failClosed(event, frameID(event), "result key is duplicated or differs in case")
	}

	message, decodeErr := jsonrpc2.DecodeMessage(payload)
	response, isResponse := message.(*jsonrpc2.Response)
	if !isResponse {
		// Fail closed with the frame's own id, so the client's pending call is answered.
		// The shared handler below would answer with the empty request id of this GET.
		if mayCarryResult(payload, dataValues, decodeErr) {
			return failClosed(event, frameID(event), "dropped a frame carrying a result outside a clean Response")
		}
		// Frames that are not a clean Response and carry no result (e.g. notifications)
		// pass through the shared handling unchanged.
		return w.newFilter("").resolveAssembled(event, dataValues, payload)
	}

	method, err := resultMethod(response.Result)
	if err != nil {
		return failClosed(event, response.ID, err.Error())
	}
	if method == "" {
		// Not a list result (e.g. a tools/call answer): pass through.
		return event
	}
	return w.newFilter(method).resolveAssembled(event, dataValues, payload)
}

// newFilter returns a filter for one event, sharing this stream's authorizer and
// annotation cache. method is "" for an error-only writer.
func (w *sseStreamFilterWriter) newFilter(method string) *ResponseFilteringWriter {
	return NewResponseFilteringWriter(nil, w.authorizer, w.request, method, w.annotationCache, w.passThroughTools)
}

// failClosed replaces an event with a JSON-RPC error envelope. The reason is logged
// once here at Warn, and the envelope is encoded without a second log line. id is
// the request id when the frame was a clean Response, and empty otherwise. An
// event whose reason has no id is dropped.
func failClosed(event []sseLine, id jsonrpc2.ID, reason string) []sseLine {
	if id == (jsonrpc2.ID{}) {
		// No request id can be read: a notification or an off-spec frame. An error with
		// no id is one the go-sdk client rejects, and it ends the session. The event is
		// dropped instead, since no call is waiting on it.
		slog.Warn("dropped an SSE event with no readable id", "error", errors.New(reason))
		return nil
	}
	slog.Warn("failing closed on an SSE event", "error", errors.New(reason))
	return rebuildEventWithPayload(event, encodeErrorBody(id), true)
}

// resultMethod returns the response filter for a result: a list method, the find_tool
// filter for a find_tool answer, or "" when neither applies. A result that names
// tools but is not a valid find_tool answer is an error, so it fails closed. A
// result with no content or structuredContent member is not a find_tool answer
// and passes through.
func resultMethod(result json.RawMessage) (string, error) {
	method, err := listMethodForResult(result)
	if err != nil || method != "" {
		return method, err
	}
	if !mentionsToolsMember(result) || !hasFindToolEnvelope(result) {
		return "", nil
	}
	callResult, err := decodeFindToolCallResult(result)
	if err != nil {
		return "", fmt.Errorf("result names tools but is not a valid find_tool answer: %w", err)
	}
	if callResult.structuredContent == nil && len(callResult.outputs) == 0 {
		return "", nil
	}
	return optimizerdec.FindToolName, nil
}

// maxToolsSniffDepth bounds how many times a string is re-parsed as JSON while
// looking for a tools member. Each level is one encoding of the find_tool text.
const maxToolsSniffDepth = 32

// mentionsToolsMember reports whether a result names tools anywhere a client could
// read it: as an object key, or inside a string that is itself JSON, at any depth.
// It decodes before matching, so an escaped spelling such as "tools" is seen.
func mentionsToolsMember(result json.RawMessage) bool {
	var v any
	if json.Unmarshal(result, &v) != nil {
		return false
	}
	return mentionsToolsValue(v, 0)
}

func mentionsToolsValue(v any, depth int) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			// A tools value fails closed unless it is a list naming no tool, such as []
			// or ["hammer"]. A string or object holding tools (including a string of
			// JSON) counts.
			// listNamesNoTool defines which list shapes pass.
			if (strings.EqualFold(k, "tools") && !listNamesNoTool(val)) || mentionsToolsValue(val, depth) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if mentionsToolsValue(val, depth) {
				return true
			}
		}
	case string:
		// Plain text that merely mentions tools (such as "Found 3 tools") names no member,
		// so only a JSON-encoded tools key in the text counts, checked below.
		trimmed := strings.TrimSpace(t)
		if depth < maxToolsSniffDepth && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
			var inner any
			if json.Unmarshal([]byte(trimmed), &inner) == nil {
				return mentionsToolsValue(inner, depth+1)
			}
		}
	}
	return false
}

// hasFindToolEnvelope reports whether a result has a content or structuredContent
// member, the two members a find_tool answer carries. A result with neither, such as an
// initialize answer that only mentions tools in its capabilities, is not a find_tool answer.
func hasFindToolEnvelope(result json.RawMessage) bool {
	members, err := mcpparser.DecodeJSONObjectMembers(result)
	if err != nil {
		return false
	}
	for _, m := range members {
		if strings.EqualFold(m.Name, "content") || strings.EqualFold(m.Name, "structuredContent") {
			return true
		}
	}
	return false
}

// listMethodForResult returns the list method whose filter applies to a result.
// It returns "" when the result carries no list member. A result with more
// than one list member is an error: filtering one member would leave the
// others visible. A member whose name differs from a list name only in case is
// also an error, because a client that matches keys case-insensitively would
// read it as that list.
func listMethodForResult(result json.RawMessage) (string, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(result, &members); err != nil {
		return "", nil
	}
	var method string
	var matches int
	for member := range members {
		for name := range listMembers {
			if member != name && strings.EqualFold(member, name) {
				return "", errors.New("result carried a list member name that differs only in case")
			}
		}
		if m, ok := listMembers[member]; ok {
			method = m
			matches++
		}
	}
	if matches > 1 {
		return "", errors.New("result carried more than one list member")
	}
	return method, nil
}

// firstEventEnd returns the length of the prefix of buf that ends with the blank
// line closing its first complete event, or 0 if no event is complete yet.
func firstEventEnd(buf []byte) int {
	pos := 0
	for {
		idx := bytes.IndexByte(buf[pos:], '\n')
		if idx == -1 {
			return 0
		}
		line := buf[pos : pos+idx]
		pos += idx + 1
		if len(bytes.TrimRight(line, "\r")) == 0 {
			return pos
		}
	}
}

// hasAmbiguousResultKey reports whether a JSON object payload has a top-level key
// that is "result" in some other spelling, or "result" more than once. Go's decoder
// matches keys case-insensitively and keeps the last duplicate, while a client may
// read the exact "result" key. Such a payload cannot be filtered reliably, so the
// caller fails closed.
func hasAmbiguousResultKey(payload []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(payload))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	resultKeys := 0
	for dec.More() {
		// Malformed JSON is not a result a client can read, so it is not ambiguous here.
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := tok.(string)
		if !ok {
			return false
		}
		if strings.EqualFold(key, "result") {
			if key != "result" {
				return true
			}
			resultKeys++
			if resultKeys > 1 {
				return true
			}
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return false
		}
	}
	return false
}

// frameID returns the JSON-RPC id of an event's payload, read the way a client
// reads it (CR as a line break too), or an empty id when none can be read.
func frameID(event []sseLine) jsonrpc2.ID {
	var raw []byte
	for _, l := range event {
		raw = append(raw, l.text...)
		raw = append(raw, l.term...)
	}
	view := bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	payload := bytes.Join(eventDataValues(parseSSELines(view)), []byte("\n"))
	message, err := jsonrpc2.DecodeMessage(payload)
	if err != nil {
		return topLevelID(payload)
	}
	switch m := message.(type) {
	case *jsonrpc2.Response:
		return m.ID
	case *jsonrpc2.Request:
		return m.ID
	}
	return jsonrpc2.ID{}
}

// bodyID reads the id a client reads from a JSON-RPC body: the decoded message's id,
// the way encoding/json matches keys, so a duplicate or case-variant "id" resolves to the
// same value the client sees. It falls back to topLevelID when the body cannot be decoded.
func bodyID(body []byte) jsonrpc2.ID {
	if message, err := jsonrpc2.DecodeMessage(body); err == nil {
		switch m := message.(type) {
		case *jsonrpc2.Response:
			return m.ID
		case *jsonrpc2.Request:
			return m.ID
		}
	}
	return topLevelID(body)
}

// writeRejection answers a rejected 2xx body with an error that carries its id. When no
// id can be read, nothing is written: an error with no id ends the go-sdk client's session.
func (w *sseStreamFilterWriter) writeRejection(body []byte, reason string) {
	id := bodyID(body)
	if id == (jsonrpc2.ID{}) {
		// One log line per rejection: the reason, and that no id could be read.
		slog.Warn(reason, "error", errSSEUnrecognizedMediaType, "dropped", "no readable id")
		return
	}
	slog.Warn(reason, "error", errSSEUnrecognizedMediaType)
	_, _ = w.ResponseWriter.Write(encodeErrorBody(id))
}

// listNamesNoTool reports whether v is a list in which no entry is an object with a
// name key, compared case-insensitively (encoding/json matches keys this way, so a
// client reading "Name" sees a tool). Only such a list cannot leak a tool, so any
// other tools value, including a string, must fail closed.
func listNamesNoTool(v any) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for k := range obj {
			if strings.EqualFold(k, "name") {
				return false
			}
		}
	}
	return true
}

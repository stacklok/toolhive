// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// maxSSEEventBytes bounds how much of one event the filter buffers while it waits
// for the blank line that ends it. A larger event fails the stream closed.
const maxSSEEventBytes = 1 << 20

// errSSEEncodedBody closes a filtered stream whose body is content-coded.
var errSSEEncodedBody = errors.New("filtered event stream is content-coded and cannot be parsed")

// errSSEEventTooLarge closes the stream when one event exceeds maxSSEEventBytes.
var errSSEEventTooLarge = errors.New("SSE event exceeds the filter's size limit")

// errSSEUnrecognizedMediaType is logged when a successful response with a
// Content-Type other than text/event-stream carries neither a JSON-RPC response
// nor an SSE result. Such a body is dropped, since a client may read it as events.
var errSSEUnrecognizedMediaType = errors.New("successful response has a Content-Type other than text/event-stream and no JSON-RPC result")

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
// still carries a result fails closed. Everything else passes through unchanged.
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
	encoded   bool
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

// Hijack refuses a connection takeover. A hijacked connection, such as a 101
// upgrade, would carry backend bytes past the filter in both directions. Declaring
// Hijack here also stops http.ResponseController from reaching the underlying writer.
func (w *sseStreamFilterWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

// WriteHeader forwards the status immediately, since the stream may stay open
// for a long time. Filtered modes strip Content-Length in markFiltered, because
// filtering can change the body size; a passed-through body keeps it.
func (w *sseStreamFilterWriter) WriteHeader(statusCode int) {
	if statusCode >= 100 && statusCode < 200 {
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
	if w.committed {
		return
	}
	w.committed = true
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
		if len(w.buffer) > maxSSEEventBytes {
			return 0, w.fail(errSSEEventTooLarge)
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
// Held bodies are buffered up to maxSSEEventBytes, so a long-lived mislabeled
// stream reaches the client only when it ends or hits that cap. Filtered modes
// carry caller-specific list answers, so they are marked uncacheable before
// their headers are committed, the same as the POST path.
func (w *sseStreamFilterWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
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
	if ce := w.ResponseWriter.Header().Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		w.encoded = true
		w.failed = true
		w.failErr = errSSEEncodedBody
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
		slog.Warn("dropped a 2xx body whose result key is duplicated or differs in case", "error", errSSEUnrecognizedMediaType)
		return
	}
	message, _ := jsonrpc2.DecodeMessage(body)
	response, isResponse := message.(*jsonrpc2.Response)
	if !isResponse {
		switch sniffBodyShape(body) {
		case bodyShapeSSE:
		case bodyShapeJSON:
			slog.Warn("dropped a 2xx JSON body carrying a result that is not a single JSON-RPC response",
				"error", errSSEUnrecognizedMediaType)
			return
		default:
			// No result in either shape: pass it through, as the buffered path does.
			// A result split across data lines would still reach the client, so drop it.
			if bodyCarriesResultAnywhere(body) {
				slog.Warn("dropped a 2xx body whose result is split across data lines", "error", errSSEUnrecognizedMediaType)
				return
			}
			if _, err := w.ResponseWriter.Write(body); err != nil {
				slog.Debug("stopped writing filtered SSE stream", "error", err)
			}
			return
		}
		if err := w.writeLines(w.processLines(parseSSELines(body))); err != nil {
			slog.Debug("stopped writing filtered SSE stream", "error", err)
		}
		return
	}
	method, err := listMethodForResult(response.Result)
	if err != nil {
		// Answer with an error that carries the request id, so the client does not wait for a timeout.
		_, _ = w.ResponseWriter.Write(w.newFilter("").errorResponseBody(response.ID, err))
		return
	}
	out := body
	if method != "" {
		filtered, ferr := w.newFilter(method).filterAndEncode(response)
		if ferr != nil {
			slog.Warn("answering a 2xx JSON-RPC response that failed to filter with an error", "error", ferr)
			out = w.newFilter("").errorResponseBody(response.ID, ferr)
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
		if end > maxSSEEventBytes {
			return w.fail(errSSEEventTooLarge)
		}
		chunk := w.buffer[:end]
		w.buffer = w.buffer[end:]
		if err := w.writeLines(w.processLines(parseSSELines(chunk))); err != nil {
			return err
		}
	}
	if len(w.buffer) > maxSSEEventBytes {
		// An unterminated event has grown past the cap. Nothing buffered may be written out.
		return w.fail(errSSEEventTooLarge)
	}
	return nil
}

// fail stops the stream for good and drops what is buffered, so finish() cannot
// write it out.
func (w *sseStreamFilterWriter) fail(err error) error {
	w.failed = true
	w.failErr = err
	w.buffer = nil
	return err
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

// processLines filters each event in lines. Blank lines separate events and
// are kept as they are.
func (w *sseStreamFilterWriter) processLines(lines []sseLine) []sseLine {
	var out []sseLine
	var event []sseLine
	for _, l := range lines {
		if len(bytes.TrimRight(l.text, "\r")) == 0 {
			out = append(out, w.filterEvent(event)...)
			out = append(out, l)
			event = nil
			continue
		}
		event = append(event, l)
	}
	return append(out, w.filterEvent(event)...)
}

// filterEvent filters one event's lines, choosing the filter from the shape of
// its result.
func (w *sseStreamFilterWriter) filterEvent(event []sseLine) []sseLine {
	if len(event) == 0 {
		return nil
	}
	if eventHasLoneCR(event) {
		body := w.newFilter("").errorResponseBody(jsonrpc2.ID{}, errors.New("event carries a bare CR line break that the filter cannot split"))
		return errorEventLines(body)
	}
	dataValues := eventDataValues(event)
	if len(dataValues) == 0 {
		return event
	}
	payload := bytes.Join(dataValues, []byte("\n"))
	if len(payload) == 0 {
		return event
	}
	if hasAmbiguousResultKey(payload) {
		return w.failClosed(event, jsonrpc2.ID{}, "result key is duplicated or differs in case")
	}

	message, _ := jsonrpc2.DecodeMessage(payload)
	response, isResponse := message.(*jsonrpc2.Response)
	if !isResponse {
		// Reuse the shared handling for frames that are not a clean Response: it
		// fails closed on a result smuggled into the frame, probes each data value,
		// and passes routine frames (e.g. notifications) through.
		return w.newFilter("").resolveSSEEvent(event)
	}

	method, err := listMethodForResult(response.Result)
	if err != nil {
		return w.failClosed(event, response.ID, err.Error())
	}
	if method == "" {
		// Not a list result (e.g. a tools/call answer): pass through.
		return event
	}
	return w.newFilter(method).resolveSSEEvent(event)
}

// newFilter returns a filter for one event, sharing this stream's authorizer and
// annotation cache. method is "" for an error-only writer.
func (w *sseStreamFilterWriter) newFilter(method string) *ResponseFilteringWriter {
	return NewResponseFilteringWriter(nil, w.authorizer, w.request, method, w.annotationCache, w.passThroughTools)
}

// failClosed replaces an event with a JSON-RPC error envelope. errorResponseBody
// logs the failure, so it is not logged again here. id is the request id when
// the frame was a clean Response, and empty otherwise.
func (w *sseStreamFilterWriter) failClosed(event []sseLine, id jsonrpc2.ID, reason string) []sseLine {
	body := w.newFilter("").errorResponseBody(id, errors.New(reason))
	return rebuildEventWithPayload(event, body, true)
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

// parseSSELines splits raw on LF, keeping each line's terminator.
func parseSSELines(raw []byte) []sseLine {
	var lines []sseLine
	for len(raw) > 0 {
		idx := bytes.IndexByte(raw, '\n')
		if idx == -1 {
			lines = append(lines, sseLine{text: raw})
			break
		}
		lines = append(lines, sseLine{text: raw[:idx], term: raw[idx : idx+1]})
		raw = raw[idx+1:]
	}
	return lines
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
		tok, err := dec.Token()
		if err != nil {
			return true
		}
		key, ok := tok.(string)
		if !ok {
			return true
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
			return true
		}
	}
	return false
}

// bodyCarriesResultAnywhere reports whether a body could carry a result under any
// framing a client might read: the whole body, one data line, or the data lines
// joined together, which is how a client assembles one event's data.
func bodyCarriesResultAnywhere(body []byte) bool {
	if carriesResult(body) || sseCarriesResult(body) {
		return true
	}
	joined := bytes.Join(eventDataValues(parseSSELines(body)), []byte("\n"))
	return carriesResult(joined)
}

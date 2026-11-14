// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
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

// errSSEEventTooLarge closes the stream when one event exceeds maxSSEEventBytes.
var errSSEEventTooLarge = errors.New("SSE event exceeds the filter's size limit")

// errSSEUnrecognizedMediaType closes a successful response whose Content-Type is
// not text/event-stream. Such a body is not parsed, and it is not passed through
// either, because a client may still read it as an event stream.
var errSSEUnrecognizedMediaType = errors.New("successful response has a Content-Type other than text/event-stream")

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
	// buffered and treated as one JSON-RPC message (see writeSniffed).
	sniff bool
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
// deadlines and hijacking on the real connection.
func (w *sseStreamFilterWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// WriteHeader forwards the status immediately, since the stream may stay open
// for a long time. Content-Length is stripped because filtering can change the
// body size.
func (w *sseStreamFilterWriter) WriteHeader(statusCode int) {
	w.ResponseWriter.Header().Del("Content-Length")
	w.decide(statusCode)
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write buffers the body and forwards every complete event. It returns the length
// of data as given, even when a leading byte order mark is stripped.
func (w *sseStreamFilterWriter) Write(data []byte) (int, error) {
	w.decide(http.StatusOK)
	if w.passThrough {
		return w.ResponseWriter.Write(data)
	}
	if w.failed {
		return 0, w.failErr
	}
	n := len(data)
	if !w.started {
		// A byte order mark can open the stream. Strip it once, so the first field parses.
		w.started = true
		data = bytes.TrimPrefix(data, mcpparser.UTF8BOM)
	}
	if w.sniff {
		w.buffer = append(w.buffer, data...)
		if len(w.buffer) > maxSSEEventBytes {
			w.failed = true
			w.failErr = errSSEEventTooLarge
			w.buffer = nil
			return 0, errSSEEventTooLarge
		}
		return n, nil
	}
	w.buffer = append(w.buffer, data...)
	if len(w.buffer) > maxSSEEventBytes && completeEventEnd(w.buffer) == 0 {
		// Stop the stream for good: drop what is buffered so finish() cannot write it out.
		w.failed = true
		w.failErr = errSSEEventTooLarge
		w.buffer = nil
		return 0, errSSEEventTooLarge
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
	trailing := bytes.TrimPrefix(w.buffer, mcpparser.UTF8BOM)
	w.buffer = nil
	if len(trailing) == 0 {
		return
	}
	if err := w.writeLines(w.processLines(parseSSELines(trailing))); err != nil {
		slog.Debug("stopped writing filtered SSE stream", "error", err)
	}
}

// decide picks the mode once, from the Content-Type the handler set and the
// status it is answering with. An event stream is parsed and filtered. A
// non-2xx body (an error) is passed through unchanged. A successful body with
// any other media type fails closed, since a client that ignores Content-Type
// could read it as events. A parsed stream carries caller-specific list
// answers, so it is marked uncacheable before its headers are committed, the
// same as the POST path.
func (w *sseStreamFilterWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	mediaType, _, _ := mime.ParseMediaType(w.ResponseWriter.Header().Get("Content-Type"))
	if mediaType == "text/event-stream" {
		w.ResponseWriter.Header().Set("Cache-Control", "private, no-store")
		return
	}
	if status >= 200 && status < 300 {
		// Sniff it: a client may read it as events, so it is filtered like one or dropped.
		w.sniff = true
		w.ResponseWriter.Header().Set("Cache-Control", "private, no-store")
		return
	}
	w.passThrough = true
}

// writeSniffed handles a complete 2xx body whose media type is not an event
// stream. A clean JSON-RPC response with a list member is filtered and sent.
// Any other clean response is sent unchanged. Anything else (not JSON-RPC, or
// a result outside a clean response) is dropped, so nothing unfiltered reaches
// the client.
func (w *sseStreamFilterWriter) writeSniffed(body []byte) {
	message, _ := jsonrpc2.DecodeMessage(body)
	response, isResponse := message.(*jsonrpc2.Response)
	if !isResponse {
		slog.Warn("dropped a 2xx body that is not a JSON-RPC response", "error", errSSEUnrecognizedMediaType)
		return
	}
	method, err := listMethodForResult(response.Result)
	if err != nil {
		slog.Warn("dropped a 2xx JSON-RPC response with an ambiguous list", "error", err)
		return
	}
	out := body
	if method != "" {
		filtered, ferr := w.newFilter(method).filterAndEncode(response)
		if ferr != nil {
			slog.Warn("dropped a 2xx JSON-RPC response that failed to filter", "error", ferr)
			return
		}
		out = filtered
	}
	if _, err := w.ResponseWriter.Write(out); err != nil {
		slog.Debug("stopped writing filtered SSE stream", "error", err)
	}
}

// drainCompleteEvents writes every event that is complete in the buffer and
// keeps the rest.
func (w *sseStreamFilterWriter) drainCompleteEvents() error {
	end := completeEventEnd(w.buffer)
	if end == 0 {
		return nil
	}
	chunk := w.buffer[:end]
	w.buffer = w.buffer[end:]
	return w.writeLines(w.processLines(parseSSELines(chunk)))
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
	dataValues := eventDataValues(event)
	if len(dataValues) == 0 {
		return event
	}
	payload := bytes.Join(dataValues, []byte("\n"))
	if len(payload) == 0 {
		return event
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

// completeEventEnd returns the length of the prefix of buf that ends with the
// blank line closing its last complete event, or 0 if there is none.
func completeEventEnd(buf []byte) int {
	end := 0
	pos := 0
	for {
		idx := bytes.IndexByte(buf[pos:], '\n')
		if idx == -1 {
			return end
		}
		line := buf[pos : pos+idx]
		pos += idx + 1
		if len(bytes.TrimRight(line, "\r")) == 0 {
			end = pos
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

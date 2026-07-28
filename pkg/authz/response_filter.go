// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authz provides authorization utilities for MCP servers.
package authz

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

var errBug = errors.New("there's a bug")

// ResponseFilteringWriter wraps an http.ResponseWriter to intercept and filter responses
type ResponseFilteringWriter struct {
	http.ResponseWriter
	authorizer       authorizers.Authorizer
	request          *http.Request
	method           string
	buffer           *bytes.Buffer
	statusCode       int
	annotationCache  *AnnotationCache
	passThroughTools map[string]struct{}
}

// NewResponseFilteringWriter creates a new response filtering writer.
// The annotationCache parameter is optional; pass nil to disable annotation caching.
// The passThroughTools parameter is optional; tools whose names appear in this set
// bypass policy filtering because authorization is enforced elsewhere (e.g., inside
// the optimizer decorator for find_tool/call_tool).
func NewResponseFilteringWriter(
	w http.ResponseWriter, authorizer authorizers.Authorizer, r *http.Request, method string,
	annotationCache *AnnotationCache, passThroughTools map[string]struct{},
) *ResponseFilteringWriter {
	return &ResponseFilteringWriter{
		ResponseWriter:   w,
		authorizer:       authorizer,
		request:          r,
		method:           method,
		buffer:           &bytes.Buffer{},
		statusCode:       http.StatusOK,
		annotationCache:  annotationCache,
		passThroughTools: passThroughTools,
	}
}

// Write captures the response body for filtering
func (rfw *ResponseFilteringWriter) Write(data []byte) (int, error) {
	return rfw.buffer.Write(data)
}

// WriteHeader captures the status code
func (rfw *ResponseFilteringWriter) WriteHeader(statusCode int) {
	rfw.statusCode = statusCode
}

// FlushAndFilter processes the captured response and applies filtering if needed.
// Returns an error if filtering or writing fails.
func (rfw *ResponseFilteringWriter) FlushAndFilter() error {
	// Only successful responses can deliver a list result to a client, so
	// non-2xx responses pass through unfiltered: an error body isn't a list,
	// and rewriting it would only hurt debuggability. This deliberately
	// covers the whole 2xx range, not just 200/202: fetch-based MCP clients
	// (including the reference TypeScript transport) gate on response.ok,
	// which accepts 200-299, so a backend answering tools/list with e.g. 201
	// could otherwise smuggle an unfiltered list past the filter. A 204 has
	// no body and is passed through by the empty-response check below.
	if rfw.statusCode < http.StatusOK || rfw.statusCode >= http.StatusMultipleChoices {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rfw.buffer.Bytes()) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	// Check if this response needs filtering
	if !requiresResponseFiltering(rfw.method) {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rfw.buffer.Bytes()) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	rawResponse := rfw.buffer.Bytes()

	// Skip filtering for empty responses (common in SSE scenarios where actual data comes via SSE stream)
	if len(rawResponse) == 0 {
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse) //nolint:gosec // G705 - JSON-RPC response, not rendered as HTML
		return err
	}

	// Media type names are case-insensitive and parameters may be preceded by
	// whitespace (RFC 9110 section 8.3.1), so normalize before matching.
	// Without this, "Application/JSON" or "application/json ; charset=utf-8"
	// -- both valid labels for a JSON body that clients happily parse -- would
	// fall through to the default branch and skip filtering entirely.
	contentType := rfw.ResponseWriter.Header().Get("Content-Type")
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))

	switch mimeType {
	case "application/json":
		// Remove the upstream Content-Length header. The reverse proxy copies it
		// from the backend response via Header() (which we don't override), but
		// filtering changes the body size. Without this, Go's HTTP server detects
		// the mismatch and tears down the connection.
		rfw.ResponseWriter.Header().Del("Content-Length")
		return rfw.processJSONResponse(rawResponse)
	case "text/event-stream":
		// Same issue: filtering changes the SSE payload size.
		rfw.ResponseWriter.Header().Del("Content-Length")
		return rfw.processSSEResponse(rawResponse)
	default:
		// A successful response to a method whose result must be filtered,
		// yet labeled with neither MCP-supported media type. That could be an
		// accident, or a backend deliberately mislabeling the response to
		// smuggle an unfiltered list past the filter -- the same
		// disguised-result concern as #5257, handled for the tool filter in
		// pkg/mcp/tool_filter.go's processUnrecognizedMimeType. Sniff the
		// body under both supported shapes and, when it carries a JSON-RPC
		// result, process it exactly as if it had been labeled correctly
		// (which filters clean frames and fails closed on dirty ones). Only a
		// body carrying no result under either shape passes through.
		if carriesResult(rawResponse) {
			slog.Warn("response with unrecognized media type carries a JSON-RPC result; filtering as JSON",
				"method", rfw.method, "contentType", contentType)
			rfw.ResponseWriter.Header().Del("Content-Length")
			return rfw.processJSONResponse(rawResponse)
		}
		if sseCarriesResult(rawResponse) {
			slog.Warn("response with unrecognized media type carries a JSON-RPC result; filtering as SSE",
				"method", rfw.method, "contentType", contentType)
			rfw.ResponseWriter.Header().Del("Content-Length")
			return rfw.processSSEResponse(rawResponse)
		}
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, err := rfw.ResponseWriter.Write(rawResponse)
		return err
	}
}

// Flush implements http.Flusher if the underlying ResponseWriter supports it.
// This method is required for streaming support (SSE, streamable-http).
//
// We must delete the Content-Length header before flushing because
// httputil.ReverseProxy (with FlushInterval: -1) calls Flush() after copying
// the backend response. The first Flush() on the underlying writer triggers an
// implicit WriteHeader(200), sending headers to the wire. If the stale
// Content-Length is still present at that point, it's too late to remove it in
// FlushAndFilter().
func (rfw *ResponseFilteringWriter) Flush() {
	if flusher, ok := rfw.ResponseWriter.(http.Flusher); ok {
		rfw.ResponseWriter.Header().Del("Content-Length")
		flusher.Flush()
	}
}

func (rfw *ResponseFilteringWriter) processJSONResponse(rawResponse []byte) error {
	message, err := jsonrpc2.DecodeMessage(rawResponse)
	response, ok := message.(*jsonrpc2.Response)
	if err != nil || !ok {
		// Not a clean Response. The disguised-result bypass (#5257) is
		// transport-independent, so apply the same check here as on the SSE
		// path: if a non-Response, undecodable, or batch frame still carries a
		// result, fail closed rather than passing the smuggled list through.
		if carriesResult(rawResponse) {
			slog.Warn("JSON response carried a result outside a clean Response frame; dropping as a protocol violation",
				"method", rfw.method)
			// A batch is replaced wholesale by this single error envelope
			// even if only one of many elements carried the smuggled
			// result: JSON-RPC does define batch responses, but we don't
			// attempt a partial one here, and mcpparser.ParsingMiddleware
			// already rejects batch requests outright (see #5745), so a
			// batch response on this path is already off-spec traffic.
			// Fail-closed is the right side to err on regardless.
			return rfw.writeErrorResponse(rfw.requestID(),
				errors.New("dropped a frame carrying a result outside a clean Response"))
		}
		rfw.ResponseWriter.WriteHeader(rfw.statusCode)
		_, werr := rfw.ResponseWriter.Write(rawResponse)
		return werr
	}

	filteredData, err := rfw.filterAndEncode(response)
	if err != nil {
		return rfw.writeErrorResponse(response.ID, err)
	}

	rfw.ResponseWriter.WriteHeader(rfw.statusCode)
	_, err = rfw.ResponseWriter.Write(filteredData)
	return err
}

// filterAndEncode filters a Response and encodes the result for the wire.
func (rfw *ResponseFilteringWriter) filterAndEncode(response *jsonrpc2.Response) ([]byte, error) {
	filteredResponse, err := rfw.filterListResponse(response)
	if err != nil {
		return nil, err
	}
	return jsonrpc2.EncodeMessage(filteredResponse)
}

// sseLine pairs one scanned SSE line with the terminator that followed it in
// the raw body: "\r\n", "\n", "\r", or nil for a final line with none. SSE
// permits mixing all three within one stream, so each line keeps the
// terminator it actually had rather than the body being forced onto one
// stream-wide convention.
type sseLine struct {
	text []byte
	term []byte
}

// processSSEResponse rewrites a text/event-stream body per SSE framing rules
// rather than treating it as an arbitrary sequence of lines: within one
// event, all `data:` field values concatenate (joined by LF, per spec) into a
// single payload, and a blank line is structural — it dispatches the event
// rather than being emittable content in its own right.
//
// Lines are scanned one terminator at a time instead of sniffing a single
// separator convention for the whole body: sniffing disagrees with a client
// that (per spec) may see CR, LF, and CRLF mixed within one stream, and that
// disagreement is exactly the class of bug this rewrite exists to close.
// Every line is re-emitted with the same terminator it was scanned with, so
// the output is structurally identical to the input (minus a leading UTF-8
// BOM, stripped below); only `data:` payloads are rewritten.
func (rfw *ResponseFilteringWriter) processSSEResponse(rawResponse []byte) error {
	// Note: this routine is adapted from the one in pkg/mcp/tool_filter.go.
	// I don't see an obvious way to factor out the commonalities, so I'm
	// duplicating it here, but we should refactor response parsing
	// respecting mime types to a common routine.

	// A client strips a leading BOM per the WHATWG UTF-8 decode algorithm
	// before parsing lines, so strip it here too: otherwise the first line's
	// "data:" prefix wouldn't match and the event would pass through
	// unfiltered.
	rawResponse = bytes.TrimPrefix(rawResponse, []byte("\xEF\xBB\xBF"))

	var outputLines []sseLine
	var event []sseLine
	for len(rawResponse) > 0 {
		idx := bytes.IndexAny(rawResponse, "\r\n")
		var line, term []byte
		switch {
		case idx == -1:
			line, term, rawResponse = rawResponse, nil, nil
		case rawResponse[idx] == '\r' && idx+1 < len(rawResponse) && rawResponse[idx+1] == '\n':
			line, term, rawResponse = rawResponse[:idx], rawResponse[idx:idx+2], rawResponse[idx+2:]
		default:
			line, term, rawResponse = rawResponse[:idx], rawResponse[idx:idx+1], rawResponse[idx+1:]
		}

		if len(line) == 0 {
			outputLines = append(outputLines, rfw.resolveSSEEvent(event)...)
			outputLines = append(outputLines, sseLine{term: term})
			event = nil
			continue
		}
		event = append(event, sseLine{text: line, term: term})
	}
	// A trailing event with no closing blank line is never dispatched by a
	// spec-compliant client, but the backend did send these bytes, so write
	// them (filtered) rather than drop them — without fabricating a
	// terminator the backend didn't send. Consequence: if that event needed
	// filtering, the error envelope substituted here is never dispatched
	// either, so the client hangs on its own timeout instead of seeing the
	// error. Real backends terminate their events, so this is accepted, not
	// fixed.
	outputLines = append(outputLines, rfw.resolveSSEEvent(event)...)

	for _, l := range outputLines {
		if _, err := rfw.ResponseWriter.Write(l.text); err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
		if _, err := rfw.ResponseWriter.Write(l.term); err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
	}

	return nil
}

// resolveSSEEvent assembles the `data:` payload of one SSE event (the lines
// between two blank lines, exclusive) and returns the lines to emit in its
// place. An event with no `data:` field (e.g. a bare "event: ping") or whose
// assembled payload passes filterSSEEventData unchanged is returned as-is.
func (rfw *ResponseFilteringWriter) resolveSSEEvent(event []sseLine) []sseLine {
	if len(event) == 0 {
		return nil
	}

	var dataValues [][]byte
	for _, l := range event {
		// A bare "data" line with no colon, or one with a space before the
		// colon ("data :foo"), doesn't match the "data:" prefix below, so both
		// are excluded from assembly here, unlike strict WHATWG grammar (which
		// treats a bare "data" as a field with an empty value). That's safe
		// for two reasons: (1) the only effect on a spec-compliant client's
		// assembled buffer is one extra LF, which is JSON-insignificant
		// whitespace outside a string literal and can never turn JSON we'd
		// reject into JSON a client would accept; (2) for
		// "data :foo", bytes.Cut on the raw line yields before == "data " (not
		// "data"), so the go-sdk's own line parser ignores it too, exactly
		// like us and strict WHATWG.
		data, ok := bytes.CutPrefix(l.text, []byte("data:"))
		if !ok {
			continue
		}
		// Per the WHATWG EventSource grammar, exactly one leading space after
		// "data:" is part of the field delimiter, not the payload. Stripping
		// more (or a differently-positioned space) would corrupt a payload
		// split mid-string-literal across lines.
		data, _ = bytes.CutPrefix(data, []byte(" "))
		dataValues = append(dataValues, data)
	}
	if len(dataValues) == 0 {
		return event
	}

	assembled := bytes.Join(dataValues, []byte("\n"))
	if len(assembled) == 0 {
		// A client never dispatches an empty data buffer, so there's nothing
		// to classify or fail closed on.
		return event
	}
	replacement := rfw.filterSSEEventData(assembled)
	if replacement == nil {
		return event
	}

	out := make([]sseLine, 0, len(event))
	replaced := false
	for _, l := range event {
		if _, ok := bytes.CutPrefix(l.text, []byte("data:")); ok {
			if replaced {
				// Subsequent data: lines are already folded into the
				// assembled payload above; only the first line carries it.
				continue
			}
			// jsonrpc2.EncodeMessage and json.Marshal both escape newlines,
			// so replacement can never contain a raw line separator and is
			// always safe to emit as a single data: line.
			out = append(out, sseLine{text: append([]byte("data: "), replacement...), term: l.term})
			replaced = true
			continue
		}
		out = append(out, l)
	}
	return out
}

// filterSSEEventData classifies an event's assembled data payload and returns
// the payload to emit in its place. A nil replacement means the event's lines
// pass through unchanged.
func (rfw *ResponseFilteringWriter) filterSSEEventData(data []byte) []byte {
	message, decodeErr := jsonrpc2.DecodeMessage(data)
	response, isResponse := message.(*jsonrpc2.Response)
	switch {
	case isResponse:
		filteredData, ferr := rfw.filterAndEncode(response)
		if ferr != nil {
			// errorResponseBody logs ferr itself, so don't repeat it here.
			slog.Warn("emitting a JSON-RPC error in place of an SSE frame that failed to filter",
				"method", rfw.method)
			return rfw.errorResponseBody(response.ID, ferr)
		}
		return filteredData
	case carriesResult(data):
		// The frame is not a clean Response but still carries a result. This
		// covers a non-Response type (a request/notification frame smuggling a
		// result), a decode error (missing or invalid jsonrpc tag, a response
		// frame with no or a non-scalar id), and a batch array. All are
		// upstream-controlled shapes that would otherwise leak the very list
		// this filter scrubs (#5257). Fail closed with an explicit error
		// envelope rather than dropping the event silently: an event
		// dispatched with an empty data buffer isn't delivered to the
		// client's handler at all, leaving it hanging on its own timeout
		// (#6037).
		slog.Warn("SSE event carried a result outside a clean Response frame; failing closed as a protocol violation",
			"method", rfw.method)
		return rfw.errorResponseBody(rfw.requestID(),
			errors.New("dropped a frame carrying a result outside a clean Response"))
	case decodeErr != nil:
		// Genuinely undecodable and no smuggled result. Pass through unfiltered.
		slog.Warn("SSE event data could not be decoded as JSON-RPC; passing through unfiltered",
			"method", rfw.method, "error", decodeErr)
		return nil
	default:
		// Genuine non-Response frame (e.g. an interleaved notifications/*
		// message) with no result payload. Routine SSE traffic, so log at
		// Debug to keep the suspicious branches above from being buried.
		slog.Debug("SSE event data was not a JSON-RPC Response; passing through unfiltered",
			"method", rfw.method)
		return nil
	}
}

// requiresResponseFiltering reports whether the method needs response filtering.
// This covers the three MCP list operations and the optimizer's find_tool call,
// whose response embeds a filtered tool list inside a CallToolResult.
func requiresResponseFiltering(method string) bool {
	return method == string(mcp.MethodToolsList) ||
		method == string(mcp.MethodPromptsList) ||
		method == string(mcp.MethodResourcesList) ||
		method == optimizerdec.FindToolName
}

// carriesResult reports whether a data payload contains a JSON-RPC "result"
// field, either directly on an object or in any element of a batch array. A
// frame that is not a clean Response (a request/notification smuggling a
// result, a shape DecodeMessage rejects, or a batch array) but still carries a
// result must not pass through: it would leak the list the filter exists to
// scrub. See issue #5257.
//
// The payload may hold more than one top-level JSON value: an SSE event's
// assembled data can concatenate several messages (e.g. a notification and a
// response, one per data: line) into something DecodeMessage rejects as a
// single value. json.Decoder reads one JSON value at a time and stops at the
// first one it can't decode, rather than Unmarshal-ing the whole payload as
// one document, so a result-bearing value after the first is still caught
// instead of making the whole payload look undecodable. Stopping there
// (rather than trying to resync past the bad value) is safe: a payload the
// decoder can't parse value-by-value isn't a single parseable JSON document
// either, so no strict client can read past that point.
func carriesResult(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return false // EOF, or malformed JSON with nothing left to check
		}
		if valueCarriesResult(value) {
			return true
		}
	}
}

// valueCarriesResult applies carriesResult's check to a single JSON value.
func valueCarriesResult(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '{':
		return objectCarriesResult(trimmed)
	case '[':
		// A single level, not recursive: a JSON-RPC batch is a flat array of
		// message objects, so that's the whole legitimate surface. Recursing
		// through nested arrays is O(d^2) on encoding/json's own nesting cap
		// (10000), an easy amplification handle for something no client
		// actually flattens.
		var batch []json.RawMessage
		if json.Unmarshal(trimmed, &batch) != nil {
			return false
		}
		for _, el := range batch {
			elTrimmed := bytes.TrimSpace(el)
			if len(elTrimmed) == 0 || elTrimmed[0] != '{' {
				continue
			}
			if objectCarriesResult(elTrimmed) {
				return true
			}
		}
	}
	return false
}

// sseCarriesResult reports whether rawResponse contains an SSE "data:" line
// whose payload carries a JSON-RPC result. It is a lightweight detector, used
// only to decide whether a 2xx body with an unrecognized media type needs the
// full SSE processing path (which applies its own per-line filtering and
// fail-closed rules); it mirrors sniffSSEToolsList in pkg/mcp/tool_filter.go.
func sseCarriesResult(rawResponse []byte) bool {
	normalized := bytes.ReplaceAll(rawResponse, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	for _, line := range bytes.Split(normalized, []byte("\n")) {
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok && carriesResult(data) {
			return true
		}
	}
	return false
}

// objectCarriesResult reports whether a trimmed JSON object value has a
// "result" key present, including an explicit `"result":null` — RawMessage's
// UnmarshalJSON runs even for a JSON null, so probe.Result is non-nil (the
// 4 bytes "null") whenever the key exists at all. Treating a null result as
// carrying one is deliberate: it's the fail-closed direction.
func objectCarriesResult(trimmedObject json.RawMessage) bool {
	var probe struct {
		Result json.RawMessage `json:"result"`
	}
	return json.Unmarshal(trimmedObject, &probe) == nil && probe.Result != nil
}

// filterListResponse filters the list response based on authorization policies
func (rfw *ResponseFilteringWriter) filterListResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	if response.Error != nil {
		// A Response carrying both error and result is a protocol violation:
		// DecodeMessage populates both fields as given, and some clients
		// (e.g. the reference TS SDK's zod schema) silently strip the
		// unexpected "error" key and treat it as a successful result. Fail
		// closed rather than passing the smuggled list through under cover
		// of the error field. See #5257.
		if response.Result != nil {
			return nil, errors.New("response carried both error and result")
		}
		// If there's an error and no result, don't filter
		return response, nil
	}

	if response.Result == nil {
		// If there's no result, don't filter
		return response, nil
	}

	// Filter based on the method
	switch rfw.method {
	case string(mcp.MethodToolsList):
		return rfw.filterToolsResponse(response)
	case string(mcp.MethodPromptsList):
		return rfw.filterPromptsResponse(response)
	case string(mcp.MethodResourcesList):
		return rfw.filterResourcesResponse(response)
	case optimizerdec.FindToolName:
		return rfw.filterFindToolResponse(response)
	default:
		// Unknown method, just return as-is
		return response, nil
	}
}

// filterToolsResponse filters tools based on call_tool authorization
func (rfw *ResponseFilteringWriter) filterToolsResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Parse the result as a ListToolsResult
	var listResult mcp.ListToolsResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	// Populate annotation cache from tools/list response so that
	// subsequent tools/call requests can look up annotations.
	rfw.annotationCache.SetFromToolsList(listResult.Tools)

	// When the optimizer is enabled, its meta-tools (find_tool, call_tool) appear
	// in tools/list instead of real backend tools. These meta-tools won't match
	// any operator-written Cedar policy (which references real tool names), so
	// default-deny would filter them out — leaving the client with zero tools.
	// Authorization for the underlying backend tools is enforced by the authz
	// middleware: call_tool requests are intercepted and the inner tool_name
	// argument is authorized against Cedar policy before the request is served.
	// See: https://github.com/stacklok/toolhive/issues/4373
	passThrough := []mcp.Tool{}
	regular := []mcp.Tool{}
	for _, t := range listResult.Tools {
		if _, ok := rfw.passThroughTools[t.Name]; ok {
			passThrough = append(passThrough, t)
		} else {
			regular = append(regular, t)
		}
	}

	// filterToolsByPolicy checks each tool against the caller's Cedar policies
	// (injecting annotations into context for when-clause evaluation) and returns
	// only tools the caller is authorized to call.
	policyFiltered := filterToolsByPolicy(rfw.request.Context(), rfw.authorizer, regular)
	filteredTools := make([]mcp.Tool, 0, len(passThrough)+len(policyFiltered))
	filteredTools = append(filteredTools, passThrough...)
	filteredTools = append(filteredTools, policyFiltered...)

	// Create a new result with filtered tools
	filteredResult := mcp.ListToolsResult{
		PaginatedResult: listResult.PaginatedResult,
		Tools:           filteredTools,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// filterPromptsResponse filters prompts based on get_prompt authorization
func (rfw *ResponseFilteringWriter) filterPromptsResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Parse the result as a ListPromptsResult
	var listResult mcp.ListPromptsResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	// Note: instantiating the list ensures that no null value is sent over the wire.
	// This is basically defensive programming, but for clients.
	filteredPrompts := []mcp.Prompt{}
	for _, prompt := range listResult.Prompts {
		// Check if the user is authorized to get this prompt
		authorized, err := rfw.authorizer.AuthorizeWithJWTClaims(
			rfw.request.Context(),
			authorizers.MCPFeaturePrompt,
			authorizers.MCPOperationGet,
			prompt.Name,
			nil, // No arguments for the authorization check
		)
		if err != nil {
			slog.Warn("Authorization check failed for prompt, skipping",
				"prompt", prompt.Name, "error", err)
			continue
		}

		if authorized {
			filteredPrompts = append(filteredPrompts, prompt)
		} else {
			slog.Debug("Prompt denied by authorization policy",
				"prompt", prompt.Name)
		}
	}

	if denied := len(listResult.Prompts) - len(filteredPrompts); denied > 0 {
		slog.Debug("Authorization policy filtered prompts",
			"total", len(listResult.Prompts), "allowed", len(filteredPrompts), "denied", denied)
	}

	// Create a new result with filtered prompts
	filteredResult := mcp.ListPromptsResult{
		PaginatedResult: listResult.PaginatedResult,
		Prompts:         filteredPrompts,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// filterResourcesResponse filters resources based on read_resource authorization
func (rfw *ResponseFilteringWriter) filterResourcesResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Parse the result as a ListResourcesResult
	var listResult mcp.ListResourcesResult
	if err := json.Unmarshal(response.Result, &listResult); err != nil {
		// If we can't parse it as a list response, just return it as-is
		return response, nil
	}

	// Note: instantiating the list ensures that no null value is sent over the wire.
	// This is basically defensive programming, but for clients.
	filteredResources := []mcp.Resource{}
	for _, resource := range listResult.Resources {
		// Check if the user is authorized to read this resource
		authorized, err := rfw.authorizer.AuthorizeWithJWTClaims(
			rfw.request.Context(),
			authorizers.MCPFeatureResource,
			authorizers.MCPOperationRead,
			resource.URI,
			nil, // No arguments for the authorization check
		)
		if err != nil {
			slog.Warn("Authorization check failed for resource, skipping",
				"resource", resource.URI, "error", err)
			continue
		}

		if authorized {
			filteredResources = append(filteredResources, resource)
		} else {
			slog.Debug("Resource denied by authorization policy",
				"resource", resource.URI)
		}
	}

	if denied := len(listResult.Resources) - len(filteredResources); denied > 0 {
		slog.Debug("Authorization policy filtered resources",
			"total", len(listResult.Resources), "allowed", len(filteredResources), "denied", denied)
	}

	// Create a new result with filtered resources
	filteredResult := mcp.ListResourcesResult{
		PaginatedResult: listResult.PaginatedResult,
		Resources:       filteredResources,
	}

	// Marshal the filtered result back
	filteredResultData, err := json.Marshal(filteredResult)
	if err != nil {
		return nil, err
	}

	// Create a new response with the filtered result
	filteredResponse := &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResultData),
	}

	return filteredResponse, nil
}

// errorResponseBody logs the full filtering error server-side and encodes a
// JSON-RPC error response carrying a deliberately generic client-visible
// message: err can originate in policy evaluation and name tools or resources,
// so security.md forbids returning it verbatim (#6066). Both transports build
// their body here, so neither can leak it. id is echoed so the client can
// correlate the error with its request.
func (rfw *ResponseFilteringWriter) errorResponseBody(id jsonrpc2.ID, err error) []byte {
	slog.Error("error filtering response", "method", rfw.method, "error", err)
	errorResponse := &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(mcpparser.CodeInternalError, "internal error"),
	}

	body, encErr := jsonrpc2.EncodeMessage(errorResponse)
	if encErr != nil {
		// Unreachable in practice: errorResponse is always a well-formed
		// jsonrpc2.Response built from a valid ID and message above. Fall back
		// to a hardcoded valid JSON-RPC error body rather than writing nothing.
		slog.Error("failed to encode JSON-RPC error response", "error", encErr)
		return fmt.Appendf(nil, `{"jsonrpc":"2.0","error":{"code":%d,"message":"internal error"}}`,
			mcpparser.CodeInternalError)
	}
	return body
}

// writeErrorResponse writes an error response for the application/json path.
// The SSE path must NOT use this: WriteHeader is a no-op once Flush() has
// committed the headers, which is the case for any real SSE response. On the
// SSE path a filtering failure instead becomes the replacement payload of the
// event's data: line (see filterSSEEventData), written through
// processSSEResponse's normal line writer alongside every other line — no
// separate write path or WriteHeader call is needed there.
func (rfw *ResponseFilteringWriter) writeErrorResponse(id jsonrpc2.ID, err error) error {
	rfw.ResponseWriter.WriteHeader(http.StatusInternalServerError)
	_, writeErr := rfw.ResponseWriter.Write(rfw.errorResponseBody(id, err))
	return writeErr
}

// requestID recovers the JSON-RPC id of the original request so an error
// response written mid-filtering can still be correlated by the client.
// Falls back to an empty jsonrpc2.ID (which EncodeMessage omits from the
// wire entirely) if the request was never parsed or its id doesn't convert.
func (rfw *ResponseFilteringWriter) requestID() jsonrpc2.ID {
	parsed := mcpparser.GetParsedMCPRequest(rfw.request.Context())
	if parsed == nil {
		return jsonrpc2.ID{}
	}
	id, err := mcpparser.ConvertToJSONRPC2ID(parsed.ID)
	if err != nil {
		return jsonrpc2.ID{}
	}
	return id
}

// filterFindToolResponse filters the tools list embedded in a find_tool tools/call
// response. The response is a CallToolResult whose first text content item contains
// a JSON-encoded optimizer.FindToolOutput. Only tools the caller is authorized to
// call are retained.
//
// mcp.CallToolResult is used directly with its built-in UnmarshalJSON so that the
// Content interface slice is deserialized correctly into concrete types
// (TextContent, ImageContent, etc.) without a bespoke minimal struct.
//
// To identify which content item carries the find_tool output, each TextContent item
// is tentatively unmarshaled as optimizer.FindToolOutput. A successful unmarshal is a
// stronger signal than checking tc.Type == "text" alone — it confirms the item actually
// carries a find_tool result rather than an arbitrary text payload (e.g. an error string).
func (rfw *ResponseFilteringWriter) filterFindToolResponse(response *jsonrpc2.Response) (*jsonrpc2.Response, error) {
	// Use mcp.CallToolResult's built-in UnmarshalJSON for correct Content interface dispatch.
	var callResult mcp.CallToolResult
	if err := json.Unmarshal(response.Result, &callResult); err != nil || callResult.IsError {
		return response, nil
	}

	// Find the first TextContent item that successfully unmarshals as optimizer.FindToolOutput.
	textIdx := -1
	var output optimizer.FindToolOutput
	for i, c := range callResult.Content {
		tc, ok := c.(mcp.TextContent)
		if !ok {
			continue
		}
		if err := json.Unmarshal([]byte(tc.Text), &output); err == nil {
			textIdx = i
			break
		}
	}
	if textIdx == -1 {
		return response, nil
	}

	// Populate annotation cache before filtering, mirroring filterToolsResponse.
	// Subsequent call_tool requests use these annotations for Cedar when-clause evaluation
	// (e.g. resource.readOnlyHint). The cache is populated from the unfiltered list so
	// that annotations are available even for tools that Cedar will deny.
	rfw.annotationCache.SetFromToolsList(output.Tools)

	output.Tools = filterToolsByPolicy(rfw.request.Context(), rfw.authorizer, output.Tools)

	filteredText, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("re-encoding find_tool output: %w", err)
	}
	original := callResult.Content[textIdx].(mcp.TextContent)
	callResult.Content[textIdx] = mcp.TextContent{Type: original.Type, Text: string(filteredText)}

	filteredResult, err := json.Marshal(callResult)
	if err != nil {
		return nil, fmt.Errorf("re-encoding call result: %w", err)
	}

	return &jsonrpc2.Response{
		ID:     response.ID,
		Result: json.RawMessage(filteredResult),
	}, nil
}

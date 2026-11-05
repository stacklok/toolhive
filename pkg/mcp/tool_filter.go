// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// filteredToolNotFoundMessage is the generic message returned to a client
// that called a tool blocked by the tool filter. It deliberately matches what
// a client would see for a tool that doesn't exist at all: see the NOTE above
// the *toolCallFilter case in NewToolCallMappingMiddleware for why a filtered
// tool must not be distinguishable from a nonexistent one.
const filteredToolNotFoundMessage = "tool not found"

var errToolNameNotFound = errors.New("tool name not found")
var errBug = errors.New("there's a bug")
var errKeepBuffering = errors.New("keep buffering")
var errUnsupportedMimeType = errors.New("unsupported mime type")

// toolOverrideEntry is a struct that represents a tool override entry.
type toolOverrideEntry struct {
	ActualName          string
	OverrideName        string
	OverrideDescription string
}

// toolMiddlewareConfig is a helper struct used to configure the tool middleware,
// and it's meant to map from a tool's actual name to a config entry.
//
// The two separate structs are necessary because it must be possible to specify
// tool overrides without tool filtering.
//
// Assume a User only specified an override for a single tool out of a list of
// n tools; in such a case, it would become unclear whether the tool is the only
// one allowed or is the only one overridden.
//
// Sufficient information could be represented in a more complex structure, but
// this gets the job and is easy enough to understand.
type toolMiddlewareConfig struct {
	filterTools          map[string]struct{}
	actualToUserOverride map[string]toolOverrideEntry
	userToActualOverride map[string]toolOverrideEntry
	// maxHeldEventBytes and maxHeldBodyBytes bound what the tools list filter
	// holds of a response body it cannot read yet (see toolFilterWriter.Write):
	// the first bounds an event stream, the second every other body.
	maxHeldEventBytes int
	maxHeldBodyBytes  int
}

func (c *toolMiddlewareConfig) isToolInFilter(toolName string) bool {
	if len(c.filterTools) == 0 {
		return true
	}

	_, ok := c.filterTools[toolName]
	return ok
}

func (c *toolMiddlewareConfig) getToolCallActualName(toolName string) (string, bool) {
	if len(c.userToActualOverride) == 0 {
		return "", false
	}

	entry, ok := c.userToActualOverride[toolName]
	return entry.ActualName, ok
}

func (c *toolMiddlewareConfig) getToolListOverride(toolName string) (*toolOverrideEntry, bool) {
	if len(c.actualToUserOverride) == 0 {
		return nil, false
	}

	entry, ok := c.actualToUserOverride[toolName]
	return &entry, ok
}

// ToolMiddlewareOption is a function that can be used to configure the tool
// middleware.
type ToolMiddlewareOption func(*toolMiddlewareConfig) error

// SimpleTool represents a minimal tool with name and description.
// This is used by ApplyToolFiltering to work with tools in a generic way.
type SimpleTool struct {
	Name        string
	Description string
}

// ApplyToolFiltering applies filtering and overriding to a list of tools.
// This is the core logic used by both the HTTP middleware and other components
// that need to apply the same filtering/overriding behavior.
//
// Returns the filtered and overridden tools.
func ApplyToolFiltering(opts []ToolMiddlewareOption, tools []SimpleTool) ([]SimpleTool, error) {
	config := &toolMiddlewareConfig{
		filterTools:          make(map[string]struct{}),
		actualToUserOverride: make(map[string]toolOverrideEntry),
		userToActualOverride: make(map[string]toolOverrideEntry),
	}

	// Apply options to build config
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, err
		}
	}

	// Use the shared core logic
	return applyFilteringAndOverrides(config, tools), nil
}

// WithToolsFilter is a function that can be used to configure the tool
// middleware to use a filter list of tools.
func WithToolsFilter(toolsFilter ...string) ToolMiddlewareOption {
	return func(mw *toolMiddlewareConfig) error {
		for _, tf := range toolsFilter {
			if tf == "" {
				return fmt.Errorf("tool name cannot be empty")
			}

			mw.filterTools[tf] = struct{}{}
		}

		return nil
	}
}

// WithToolsOverride is a function that can be used to configure the tool
// middleware to use a map of tools to override the actual list of tools.
//
// If an empty string is provided for either overrideName or overrideDescription,
// that field will be left unchanged. An error is returned if actualName is empty.
func WithToolsOverride(actualName string, overrideName string, overrideDescription string) ToolMiddlewareOption {
	return func(mw *toolMiddlewareConfig) error {
		if actualName == "" {
			return fmt.Errorf("tool name cannot be empty")
		}

		if overrideName == "" && overrideDescription == "" {
			return fmt.Errorf("override name and description cannot both be empty")
		}

		entry := toolOverrideEntry{
			ActualName:          actualName,
			OverrideName:        overrideName,        // empty string means no override
			OverrideDescription: overrideDescription, // empty string means no override
		}
		mw.actualToUserOverride[actualName] = entry
		mw.userToActualOverride[overrideName] = entry

		return nil
	}
}

// NewListToolsMappingMiddleware creates an HTTP middleware that parses SSE responses
// and plain JSON objects to extract tool names from JSON-RPC messages containing
// tool lists or tool calls.
//
// The middleware looks for SSE events with:
// - event: message
// - data: {"jsonrpc":"2.0","id":X,"result":{"tools":[...]}}
//
// A response that is, or that a client could read as, a tools list is
// filtered only when it resolves unambiguously: see decodeToolsListResponse.
// One that does not is withheld rather than passed through, since passing it
// through would expose the tools the filter hides, and the client is answered
// with a JSON-RPC error in its place. Every other response is passed through
// unchanged.
//
// This middleware is designed to be used ONLY when tool filtering or
// override are enabled, and expects the list of tools to be "correct"
// (i.e. not empty and not containing nonexisting tools).
func NewListToolsMappingMiddleware(opts ...ToolMiddlewareOption) (types.MiddlewareFunction, error) {
	config := &toolMiddlewareConfig{
		filterTools:          make(map[string]struct{}),
		actualToUserOverride: make(map[string]toolOverrideEntry),
		userToActualOverride: make(map[string]toolOverrideEntry),
		maxHeldEventBytes:    defaultMaxHeldEventBytes,
		maxHeldBodyBytes:     defaultMaxHeldBodyBytes,
	}
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, err
		}
	}

	if len(config.filterTools) == 0 && len(config.actualToUserOverride) == 0 {
		return nil, fmt.Errorf("tools list for filtering or overriding is empty")
	}
	if config.maxHeldEventBytes < 1 || config.maxHeldBodyBytes < 1 {
		return nil, fmt.Errorf("the bounds on a held response body must be positive")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The filter reads the bytes of the response body, so it forwards
			// the request without the client's Accept-Encoding: Go's transport
			// then asks the backend for gzip on its own and decodes what it
			// asked for. A body that still arrives with a content coding is
			// withheld whenever the filter would have had to read it: see
			// WriteHeader. The request the caller holds keeps the header.
			r = WithoutAcceptEncoding(r)

			// Keep the body of the request, which the backend still reads,
			// so that a response the filter withholds can be answered with
			// an error that carries the request's id.
			var requestBody []byte
			if r.Method == http.MethodPost {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					writeBodyReadError(w, err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				requestBody = body
			}

			// NOTE: this middleware only checks the response body, whose
			// format at this point is not yet known and might be either a
			// JSON payload or an SSE stream.
			//
			// The way this is implemented is that we wrap the response writer
			// in order to buffer the response body. Once Flush() is called, we
			// process the buffer according to its content type and possibly
			// modify it before returning it to the client.
			rw := &toolFilterWriter{
				ResponseWriter: w,
				config:         config,
				statusCode:     http.StatusOK, // matches net/http's implicit status when WriteHeader is never called
				requestBody:    requestBody,
			}

			// Call the next handler
			next.ServeHTTP(rw, r)

			// Explicitly drain our buffered writer after the handler returns. An inner
			// buffering middleware (e.g. authz's response filter) may write the final
			// body into our buffer during its own post-ServeHTTP flush; without this
			// call that body is stranded and the client gets a 0-byte response. See #5797.
			//
			// This is the terminal drain, not a streaming Flush: there is no more data
			// coming, so a body with nothing to filter (wrong/missing content type,
			// truncated) is written through unchanged rather than dropped, while one
			// that could be read as a tools list but can't be filtered is withheld
			// and answered with an error.
			rw.finish()
		})
	}, nil
}

// NewToolCallMappingMiddleware creates an HTTP middleware that parses tool call
// requests and filters out tools that are not in the filter list.
//
// The middleware looks for JSON-RPC messages with:
// - method: tool/call
// - params: {"name": "tool_name"}
//
// This middleware is designed to be used ONLY when tool filtering or override
// is enabled, and expects the list of tools to be "correct" (i.e. not empty
// and not containing nonexisting tools).
func NewToolCallMappingMiddleware(opts ...ToolMiddlewareOption) (types.MiddlewareFunction, error) {
	config := &toolMiddlewareConfig{
		filterTools:          make(map[string]struct{}),
		actualToUserOverride: make(map[string]toolOverrideEntry),
		userToActualOverride: make(map[string]toolOverrideEntry),
	}
	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, err
		}
	}

	if len(config.filterTools) == 0 && len(config.actualToUserOverride) == 0 {
		return nil, fmt.Errorf("tools list for filtering or overriding is empty")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, ok := readToolCallBody(w, r)
			if !ok {
				return
			}

			// Restore the normalized request body for downstream handlers.
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))

			// Try to parse the request as a tool call request. If it succeeds,
			// check if the tool is in the filter. If it is not a tool call request,
			// just pass it through.
			var toolCallRequest toolCallRequest
			decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
			decoder.UseNumber()
			err := decoder.Decode(&toolCallRequest)
			if err == nil && json.Valid(bodyBytes) && toolCallRequest.Method == "tools/call" {
				fix := processToolCallRequest(config, toolCallRequest)

				switch fix := fix.(type) {

				// If the tool call request is allowed, and the tool name is not overridden,
				// we just pass it through unmodified.
				case *toolCallNoAction:
					next.ServeHTTP(w, r)
					return

				// NOTE: ideally, trying to call that was filtered out by config should be
				// equivalent to calling a nonexisting tool; in such cases and when the SSE
				// transport is used, the behaviour of the official Python SDK is to return
				// a 202 Accepted to THIS call and return an success message in the SSE
				// stream saying that the tool does not exist.
				//
				// It basically fails successfully.
				//
				// Unfortunately, implementing this behaviour is not trivial and requires
				// session management, as the SSE stream is managed by the proxy in an entirely
				// different thread of execution. As a consequence, the best thing we can
				// do that is still compliant with the spec is to return a JSON-RPC error
				// for this call, over HTTP 200, that looks the same as calling a tool that
				// doesn't exist (see filteredToolNotFoundMessage above).
				//
				// A client that only accepts an event stream (the legacy HTTP+SSE
				// transport, whose real response is delivered on a separate stream we
				// don't control from this middleware) can't be answered this way: the
				// body written here IS the only response this call gets, and it must be
				// something other than a stray JSON blob on a connection the client
				// expects to be text/event-stream. So that path keeps the 400, same as
				// the session-management limitation described above.
				case *toolCallFilter:
					if clientAcceptsJSON(r) {
						writeFilteredToolCallError(w, toolCallRequest.ID)
						return
					}
					w.WriteHeader(http.StatusBadRequest)
					return

				// In case of a tool name override, we need to fix the tool call request
				// and then forward it to the next handler.
				case *toolCallOverride:
					(*toolCallRequest.Params)["name"] = fix.Name()
					bodyBytes, err = json.Marshal(toolCallRequest)
					if err != nil {
						slog.Error("error marshalling tool call request",
							"error", err)
						next.ServeHTTP(w, r)
						return
					}

					r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
					r.ContentLength = int64(len(bodyBytes))

				// According to the current version of the MCP spec at
				// https://modelcontextprotocol.io/specification/2025-11-25/schema#calltoolrequest
				// this case can only happen if the request is malformed. The proxied MCP
				// server should be able to process the request, but since we detect it here
				// we short-circuit returning an error.
				case *toolCallBogus:
					w.WriteHeader(http.StatusBadRequest)
					return

				// This should never happen, but we handle it just in case.
				default:
					slog.Error("error processing tool call of a filtered tool",
						"error", err)
					next.ServeHTTP(w, r)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}, nil
}

func readToolCallBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeBodyReadError(w, err)
		return nil, false
	}
	// Validate before an override can erase invalid envelope evidence.
	if r.Method == http.MethodPost {
		if _, err := DecodeMessage(body); err != nil {
			WriteClassificationError(w, nil, err)
			return nil, false
		}
	}
	return bytes.TrimPrefix(body, UTF8BOM), true
}

// clientAcceptsJSON reports whether r's Accept header allows an
// application/json response. An empty/absent header is treated as
// accepting JSON, matching the common case of a client that doesn't bother
// to negotiate. Each comma-separated entry's media-type token (i.e.
// everything before any ";param=..." qualifier) is compared case-
// insensitively against "application/json", the type wildcard
// "application/*", and the full wildcard "*/*". Quality values (";q=") are
// not honored: a token present with any q-value counts as accepted.
func clientAcceptsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return true
	}

	for _, entry := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.Split(entry, ";")[0])
		if strings.EqualFold(mediaType, "application/json") ||
			strings.EqualFold(mediaType, "application/*") ||
			mediaType == "*/*" {
			return true
		}
	}

	return false
}

// writeFilteredToolCallError writes a JSON-RPC error response, over HTTP 200,
// for a tools/call request blocked by the tool filter. The error message is
// the generic filteredToolNotFoundMessage: see the NOTE in NewToolCallMappingMiddleware
// for why a filtered tool must look the same as one that doesn't exist.
//
// HTTP 200 (not 400) is deliberate: under MCP streamable HTTP, a validly
// received JSON-RPC request that fails at the application level rides back
// in a 200 response carrying a JSON-RPC error object, mirroring how
// WriteClassificationError's peers (e.g. writeRateLimited) shape their body,
// modulo status code.
func writeFilteredToolCallError(w http.ResponseWriter, id any) {
	body := filteredToolCallErrorBody(id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G104: writing a JSON-RPC error response to an HTTP client
	_, _ = w.Write(body)
}

// filteredToolCallErrorBody renders the JSON-RPC error body for a filtered
// tool call, modeled on classificationErrorBody: the body is marshaled first
// (with a hand-crafted fallback on marshal failure) so the caller only writes
// headers/status once a valid body is ready.
//
// MCP narrows base JSON-RPC 2.0 here: schema/2025-11-25 types the error
// response as `id?: RequestId` where RequestId = string | number, so an
// absent id is encoded by omitting the "id" key, never as null. See
// session.HasJSONRPCID.
func filteredToolCallErrorBody(id any) []byte {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    CodeInvalidParams,
			"message": filteredToolNotFoundMessage,
		},
	}
	if session.HasJSONRPCID(id) {
		resp["id"] = id
	}

	body, err := json.Marshal(resp)
	if err != nil {
		// This should never happen with simple map types, but return a
		// hand-crafted fallback to guarantee a valid JSON-RPC error.
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"tool not found"}}`)
	}
	return body
}

// defaultMaxHeldEventBytes is the most of an event stream the tool filter
// holds while it cannot read it yet: an event that has not ended, or a stream
// it holds until the response ends. It is the size the Go MCP SDK lets one
// event reach by default.
const defaultMaxHeldEventBytes = 16 << 20

// defaultMaxHeldBodyBytes is the most of any other body the tool filter holds
// while it cannot read it yet: a JSON body that is not complete, or a body it
// holds until the response ends because its Content-Type is missing or
// ambiguous. It is the size of a backend reply the virtual MCP server reads
// (see pkg/vmcp/client), so the filter withholds no reply that it accepts.
const defaultMaxHeldBodyBytes = 100 << 20

// toolFilterWriter wraps http.ResponseWriter to capture and process SSE responses
type toolFilterWriter struct {
	http.ResponseWriter
	buffer     []byte
	config     *toolMiddlewareConfig
	statusCode int
	// scanned is the length of the prefix of buffer that drainBuffer has
	// already read without finding the end of an event in it, so that
	// reading an event stream resumes after it (see completeSSEEvents).
	scanned int
	// discard is set once the filter has withheld the rest of the response
	// (see withholdRest): because its body is one the filter cannot read
	// (see unreadable), would make the filter hold more of it than it may
	// (see Write), or could carry a tools list the filter cannot resolve
	// while its status is still held, or because the filter itself failed
	// (see drainBuffer). The rest of that body is dropped.
	discard bool
	// unreadable names why the filter cannot read the bytes of the body,
	// when the header shows that it cannot (see unreadableBody): such a body
	// is withheld as soon as a byte of it arrives (see Write), while a
	// response that carries no body is forwarded as it is.
	unreadable string
	// skipping is set while the filter drops the bytes of an event that
	// would make it hold more of an event stream than it may (see
	// withholdHeld), up to the blank line that ends the event; the stream
	// goes on after it (see skip).
	skipping bool
	// skipLine is the start of the line the dropped bytes end in, at most
	// two bytes, which is all that tells whether that line is blank once it
	// ends (see lineStart).
	skipLine []byte
	// bodyStarted is set once a body byte has been written to the wrapped
	// ResponseWriter. Until then the buffer holds the start of the body the
	// client reads.
	bodyStarted bool
	// jsonScan reads the JSON body the buffer holds for the end of its value
	// as the body arrives (see jsonBodyScan). It starts over whenever the
	// buffer does.
	jsonScan jsonBodyScan
	// wroteHeader is set once the wrapped handler has written the status,
	// explicitly or with its first Write.
	wroteHeader bool
	// headerPending is set while that status and the header have not been
	// forwarded to the wrapped ResponseWriter (see WriteHeader).
	headerPending bool
	// header is the header the wrapped handler wrote with its status: the
	// one the filter reads the body by, and the one it forwards (see
	// forwardHeader).
	header http.Header
	// requestBody is the body of the request the response answers, which
	// holds the id of the error that answers a withheld response (see
	// errorResponse and withheldEventID).
	requestBody []byte
}

// WriteHeader captures the status code and decides when to forward it.
//
// An informational status, which comes before the final one, is forwarded at
// once with the header it is written with, as net/http forwards it, and
// decides nothing: the filter decides on the final status and the header
// written with it. That header is the one the filter reads the body by and
// the one it forwards: as http.ResponseWriter documents, what the wrapped
// handler changes in the header map afterwards takes effect only as trailers
// (see forwardHeader).
//
// Content-Length is stripped because applying tool filters or overrides may
// change the body size (e.g. a longer override description). Without this,
// net/http rejects the rewritten body with "http: wrote more than the declared
// Content-Length" and the client receives only the headers. Removing the
// header lets Go fall back to chunked transfer encoding.
//
// The status and header of an event stream are forwarded at once, so that its
// events reach the client as the filter reads them. Those of any other body
// are forwarded only once the filter writes the body, so that a body the
// filter withholds can be answered with an error instead (see withholdRest).
// A body whose bytes the filter cannot read, as the header shows (see
// unreadableBody), is withheld as soon as a byte of it arrives, whenever the
// filter would have had to read it (see Write); its status and header are
// held until then, those of an event stream included, so that a response
// that carries no body is forwarded as it is. A later call for a final status
// reaches the wrapped ResponseWriter as it is once the status has been
// forwarded, or the response answered; while the filter holds the status, it
// is ignored, as net/http ignores a second status.
func (rw *toolFilterWriter) WriteHeader(statusCode int) {
	if informationalStatus(statusCode) {
		rw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if rw.wroteHeader {
		if !rw.headerPending {
			rw.ResponseWriter.WriteHeader(statusCode)
		}
		return
	}
	rw.wroteHeader = true
	rw.headerPending = true
	rw.statusCode = statusCode
	rw.Header().Del("Content-Length")
	rw.header = rw.Header().Clone()

	mimeType, ambiguous := responseMediaType(rw.header)
	if reason := unreadableBody(rw.header); reason != "" &&
		(rw.successResponse() || ambiguous || isSupportedMimeType(mimeType)) {
		rw.unreadable = reason
		return
	}
	if mimeType == "text/event-stream" {
		rw.commitHeader()
	}
}

// Write captures the response body for Flush and finish to process. Like
// net/http, it writes the status first when the wrapped handler has not.
//
// The first byte of a body the filter cannot read (see unreadable) withholds
// the response: the middleware asks the backend for a body it can read, and
// the client is answered with an error in place of one it cannot.
//
// The filter holds at most a bound of a body it has not been able to read
// yet (see heldBound). When a write would make it hold more, it first writes
// out what it holds and can read already, as Flush would: the complete events
// of an event stream, or a body it passes through unread. Data that would
// still make it hold more than the bound fails closed: on an event stream
// whose status has been forwarded, the event it belongs to is withheld, and
// the stream goes on after it; any other response is withheld from then on,
// as a body the filter cannot read is (see withholdHeld).
func (rw *toolFilterWriter) Write(data []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	for rest := data; len(rest) > 0 && !rw.discard; {
		if rw.skipping {
			rest = rw.skip(rest)
			continue
		}
		if rw.unreadable != "" {
			slog.Warn("withholding a response body the tool filter cannot read", "reason", rw.unreadable)
			rw.withholdRest()
			break
		}
		hold, bound := rw.heldBound()
		if len(rw.buffer)+len(rest) > bound {
			rw.drainBuffer(false)
			if rw.discard {
				break
			}
		}
		if held := len(rw.buffer) + len(rest); held > bound {
			slog.Warn("withholding what would make the tool filter hold more of a response body than it may",
				"hold", hold, "held_bytes", held, "max_held_bytes", bound)
			rw.withholdHeld()
			continue
		}
		rw.buffer = append(rw.buffer, rest...)
		break
	}
	return len(data), nil
}

// heldBound returns the bound on what the filter holds of the response body
// while it cannot read it yet, and names the hold it bounds. An event stream
// is bounded by the maxHeldEventBytes of the config, since an event that has
// not ended is the only part of it the filter holds, and every other body by
// its maxHeldBodyBytes: a JSON body that is not complete, or a body held until
// the response ends because its Content-Type is missing or ambiguous.
func (rw *toolFilterWriter) heldBound() (string, int) {
	switch mimeType, _ := responseMediaType(rw.header); mimeType {
	case "text/event-stream":
		return "event stream", rw.config.maxHeldEventBytes
	case "application/json":
		return "JSON body", rw.config.maxHeldBodyBytes
	default:
		return "body held until the response ends", rw.config.maxHeldBodyBytes
	}
}

// Flush processes the buffered data that is ready, writes the result to the
// underlying ResponseWriter, and forwards the flush downstream. This is the
// interim/streaming path: a downstream handler or reverse proxy may call this
// mid-response, so a partial JSON body, an SSE event whose closing blank line
// has not arrived yet, a 2xx body without a supported Content-Type, or any
// body whose Content-Type is ambiguous (see responseMediaType), is left
// buffered for a later call to complete, up to the bound on what the filter
// holds (see Write).
// When nothing could be written yet the transport flush is not forwarded --
// there's nothing new to push over the wire, and forwarding it could send a
// still-incomplete frame, or a status the filter holds (see WriteHeader). Use
// finish() for the one-time terminal drain after the wrapped handler has
// returned.
//
// A flush before the wrapped handler has written a status writes 200 first,
// as net/http does, so that the status and header are read and held like any
// other, rather than committed by the wrapped ResponseWriter with the header
// map as it stands.
func (rw *toolFilterWriter) Flush() {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	if rw.skipping || !rw.drainBuffer(false) {
		return
	}
	rw.flush()
}

// flush forwards a transport flush to the wrapped ResponseWriter.
func (rw *toolFilterWriter) flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// finish performs the terminal drain of any remaining buffered data after the
// wrapped handler has returned. Unlike Flush, there is no more data coming:
// an SSE event the stream ended without a blank line is processed like any
// other event (some clients dispatch it at end of stream), an incomplete body
// that cannot be a tools list (one that does not start like a JSON object or
// array) is written through unchanged rather than held forever, and an
// unrecognized/missing Content-Type on a response with nothing to filter is
// likewise passed through as-is, and the status and header of a response
// without a body are forwarded, a Content-Encoding they declare included. It
// does not forward a transport flush, since returning from ServeHTTP already
// completes the response.
//
// A body that is, or could be read as, a tools list but cannot be filtered,
// and a body the filter cannot read, are not passed through: the client is
// answered with an error in their place -- see drainBuffer and Write.
func (rw *toolFilterWriter) finish() {
	rw.drainBuffer(true)
}

// drainBuffer processes the buffered response body according to its MIME
// type and writes the result to the underlying ResponseWriter. It reports
// whether the caller may safely forward a transport-level flush: false means
// nothing was written, because nothing is complete yet and more is expected
// (Flush, non-terminal), because the response has been withheld already, or
// because what is complete could be read as a tools list we could not safely
// filter and nothing could take its place (fail closed, in both Flush and
// finish).
//
// When terminal is false (Flush, mid-stream), whatever processBuffer reports
// as incomplete is left buffered for a later call. When terminal is true
// (finish, called once ServeHTTP has returned), there won't be a later call,
// so an incomplete body that cannot be a tools list is written through
// unchanged instead of being stranded.
//
// A filtering failure -- a response that is, or could be read by a client
// as, a tools list (whether correctly labeled or sniffed, see
// processUnrecognizedMimeType) but that the filter cannot resolve
// unambiguously, e.g. a tool that is not an object, a tool missing its
// required name, a case-variant or duplicate spelling of a member the filter
// reads, or data that starts like a JSON object or array but that
// encoding/json cannot parse -- is never passed through, in either mode:
// doing so would leak the unfiltered list. In an event stream only the part
// that fails is withheld: one event (or every complete event read with a line
// that holds a bare CR or another separator only some clients end a line at,
// see processEventStream). An error event answering the request the event
// answers takes its place when the filter can name that request (see
// withheldEvent), and the events before and after it are still written. Any
// other body is answered with an error in place of the whole response (see
// withholdRest), since the filter forwards its status only once it writes the
// body.
func (rw *toolFilterWriter) drainBuffer(terminal bool) bool {
	if rw.discard {
		return false
	}

	// Drop every byte order mark the body starts with before MIME dispatch, so
	// every response path reads the body as a client that drops them all does,
	// the client receives the body without them, and the result does not
	// depend on how the marks are split across writes and flushes. Dropping
	// them moves the bytes already read, so reading starts over. A body that
	// holds nothing but such marks so far has nothing to write, and so does
	// not get its held status forwarded either.
	if !rw.bodyStarted {
		if trimmed := trimLeadingBOMs(rw.buffer); len(trimmed) < len(rw.buffer) {
			rw.buffer = trimmed
			rw.scanned = 0
			rw.jsonScan = jsonBodyScan{}
		}
	}
	if len(rw.buffer) == 0 {
		if terminal {
			rw.commitHeader()
		}
		return !rw.headerPending
	}

	mimeType, ambiguous := responseMediaType(rw.header)
	// Only a non-2xx body whose Content-Type names no supported media type is
	// passed through unread (see processUnrecognizedMimeType); any other body
	// has to be read.
	sniff := rw.successResponse() || ambiguous

	var b bytes.Buffer
	consumed, err := processBuffer(rw.config, rw.buffer, &rw.jsonScan, rw.scanned, mimeType, sniff, terminal, rw.withheldEvent, &b)

	switch {
	case errors.Is(err, errKeepBuffering):
		if !terminal {
			slog.Debug("keep buffering", "buffered_bytes", len(rw.buffer))
			rw.scanned = len(rw.buffer)
			return false
		}
		slog.Warn("response ended with an incomplete buffered body; writing it through unchanged",
			"buffered_bytes", len(rw.buffer))
		rw.writeThrough(rw.buffer)
		rw.keepRest(len(rw.buffer))
		return true

	case errors.Is(err, errUnsupportedMimeType):
		// processBuffer only returns this once it has ruled out the buffer being
		// a tools list: either the response has nothing to filter (a non-2xx
		// error body), or it's a 2xx body that doesn't look like a tools list
		// under either supported wire format. Either way, passing it through
		// unchanged cannot leak an unfiltered list.
		rw.writeThrough(rw.buffer)
		rw.keepRest(len(rw.buffer))
		return true

	case errors.Is(err, errBug):
		// A failure of the filter itself: nothing it read is written through.
		slog.Error("error filtering buffered response; refusing to pass through unfiltered", "error", err)
		rw.withholdRest()
		return true

	case err != nil:
		// A response, or an event of one, that is, or could be read as, a
		// tools list but failed to filter -- e.g. a malformed tool entry. Fail
		// closed: writing the raw, unfiltered data through would defeat the
		// filter entirely. processBuffer has left that data out of b, which
		// holds the rest of what it consumed, with an error event in place of
		// each withheld event of an event stream that answers a request (see
		// withheldEvent). The response's shape is the backend's doing and is
		// logged as a warning.
		slog.Warn("withholding a response that could carry a tools list the filter cannot resolve",
			"error", err)
		if rw.headerPending {
			rw.withholdRest()
			return true
		}
		rw.writeThrough(b.Bytes())
		rw.keepRest(consumed)
		return b.Len() > 0

	default:
		slog.Debug("flushing buffer", "bytes", b.Len(), "consumed_bytes", consumed)
		rw.writeThrough(b.Bytes())
		// Keep whatever processBuffer left for a later call (an SSE event
		// that isn't complete yet), dropping only the bytes it consumed.
		rw.keepRest(consumed)
		return true
	}
}

// unreadableBody returns why the filter cannot read the bytes of a body the
// response header describes, or an empty string when it can: a content coding
// (e.g. br), whose bytes are not the body's, or a charset the filter does
// not read, in which the bytes are not the text a client reads (see
// unreadableCharset). The middleware forwards the request without the
// client's Accept-Encoding, so the transport decodes the gzip it asks for on
// its own, and a coding that still arrives is one the transport does not
// decode.
func unreadableBody(header http.Header) string {
	if coding := contentCoding(header); coding != "" {
		return "content coding " + coding
	}
	if charset := unreadableCharset(header); charset != "" {
		return "charset " + charset
	}
	return ""
}

// contentCoding returns the content codings a response header declares, or an
// empty string when it declares none other than identity.
func contentCoding(header http.Header) string {
	var codings []string
	for _, value := range header.Values("Content-Encoding") {
		for _, coding := range strings.Split(value, ",") {
			coding = strings.TrimSpace(coding)
			if coding != "" && !strings.EqualFold(coding, "identity") {
				codings = append(codings, coding)
			}
		}
	}
	return strings.Join(codings, ", ")
}

// supportedMimeTypes are the two media types MCP responses use.
var supportedMimeTypes = []string{"application/json", "text/event-stream"}

// isSupportedMimeType reports whether mimeType is one of the two media types
// MCP responses use, which processBuffer reads whatever the status code.
func isSupportedMimeType(mimeType string) bool {
	return slices.Contains(supportedMimeTypes, mimeType)
}

// responseMediaType returns the media type a response's Content-Type names,
// lower-cased and without parameters, and reports whether that label is
// ambiguous.
//
// Media type names are case-insensitive and parameters may be preceded by
// whitespace (RFC 9110 section 8.3.1), and clients accept such labels, so the
// media type is normalized rather than left to the sniff path. Clients do not
// all read the label the same way, though: some take the media type before
// the first ';', others pick a format by whichever supported media type the
// header starts with or contains anywhere, and fetch joins repeated headers
// with ", ". So the label is ambiguous, and no media type is returned, unless
// the response carries exactly one Content-Type and it names no supported
// media type other than its own; a media type that contains one, such as
// application/json-seq, names it too. The body of an ambiguous response is
// held until the response ends and read as either format, whatever the
// status code (see processUnrecognizedMimeType).
func responseMediaType(header http.Header) (string, bool) {
	values := header.Values("Content-Type")
	if len(values) == 0 {
		return "", false
	}
	if len(values) > 1 {
		return "", true
	}

	value := strings.ToLower(values[0])
	mediaType := strings.TrimSpace(strings.Split(value, ";")[0])
	for _, supported := range supportedMimeTypes {
		if supported != mediaType && strings.Contains(value, supported) {
			return "", true
		}
	}
	return mediaType, false
}

// readableCharsets are the charsets the filter reads a body in: it reads the
// bytes of a body as UTF-8, as MCP clients do, and US-ASCII is a subset of it.
var readableCharsets = []string{"utf-8", "utf8", "us-ascii", "ascii"}

// unreadableCharset returns the charset a response's Content-Type declares
// when it is not one the filter reads a body in (see readableCharsets), or an
// empty string when the response declares none or a readable one. A client
// that decodes the body by the declared charset reads different text from it
// than the filter does. Every Content-Type value is read, as a client may read
// any of them; the charset is the one its parameters name, however the value
// quotes or encodes it (RFC 2231), lower-cased, since charset names are
// case-insensitive. A value that names a charset but that cannot be parsed,
// as one with two charsets cannot, is returned as it is: which charset a
// client reads it as is undecided.
func unreadableCharset(header http.Header) string {
	for _, value := range header.Values("Content-Type") {
		_, params, err := mime.ParseMediaType(value)
		if err != nil {
			if strings.Contains(strings.ToLower(value), "charset") {
				return value
			}
			continue
		}
		charset, ok := params["charset"]
		if !ok {
			continue
		}
		if charset = strings.ToLower(charset); !slices.Contains(readableCharsets, charset) {
			return charset
		}
	}
	return ""
}

// successResponse reports whether the response's status code is in the 2xx
// range, whose bodies the filter reads even without a supported Content-Type
// (see processUnrecognizedMimeType).
func (rw *toolFilterWriter) successResponse() bool {
	return rw.statusCode >= http.StatusOK && rw.statusCode < http.StatusMultipleChoices
}

// writeThrough writes data to the underlying ResponseWriter, after the status
// and header when they have not been forwarded yet.
func (rw *toolFilterWriter) writeThrough(data []byte) {
	rw.commitHeader()
	if len(data) > 0 {
		rw.bodyStarted = true
	}
	_, err := rw.ResponseWriter.Write(data)
	if err != nil {
		slog.Error("error writing buffer", "error", err)
	}
}

// commitHeader forwards the status and header the wrapped handler wrote to the
// underlying ResponseWriter, if they have not been forwarded yet.
func (rw *toolFilterWriter) commitHeader() {
	if rw.headerPending {
		rw.headerPending = false
		rw.forwardHeader(func() { rw.ResponseWriter.WriteHeader(rw.statusCode) })
	}
}

// forwardHeader calls write, which writes a status to the underlying
// ResponseWriter, while the header map holds the header the wrapped handler
// wrote with its status (see WriteHeader) instead of what the map holds now.
// As http.ResponseWriter documents, what the handler changes in the map after
// writing its status takes effect only as trailers, which is how a reverse
// proxy delivers the trailers of the backend's response. So the map holds
// those changes again once the status has been written, and the underlying
// ResponseWriter sends whichever of them are trailers it may send.
func (rw *toolFilterWriter) forwardHeader(write func()) {
	header := rw.Header()
	later := header.Clone()
	clear(header)
	maps.Copy(header, rw.header)
	write()
	clear(header)
	maps.Copy(header, later)
}

// informationalStatus reports whether statusCode is an informational status,
// which comes before the final status of a response: any 1xx status but 101
// Switching Protocols, after which no other status follows.
func informationalStatus(statusCode int) bool {
	return statusCode >= http.StatusContinue && statusCode < http.StatusOK &&
		statusCode != http.StatusSwitchingProtocols
}

// withholdRest withholds the rest of the response, together with what the
// filter holds of it, and answers the request with a JSON-RPC error in its
// place, so that the client learns that its request failed. While the status
// has not been forwarded, the error is the whole response, under HTTP 500 as
// the authorization middleware answers a response it cannot filter, and
// without the backend's Content-Encoding. On an event stream, whose status is
// forwarded at once, it is an event after the ones already written when the
// stream answers a request with an id (see withheldEvent), flushed at once so
// that a client on a stream the backend holds open reads it, and nothing is
// written otherwise. Any other response has had its body written already, and
// nothing is added to it.
func (rw *toolFilterWriter) withholdRest() {
	rw.discard = true
	rw.dropHeld()
	if rw.headerPending {
		rw.headerPending = false
		rw.header.Del("Content-Encoding")
		rw.forwardHeader(func() {
			err := WriteJSONRPCError(rw.ResponseWriter, http.StatusInternalServerError, rw.errorResponse())
			if err != nil {
				slog.Error("error writing the error that answers a withheld response", "error", err)
			}
		})
		return
	}
	if mimeType, _ := responseMediaType(rw.header); mimeType == "text/event-stream" {
		if event := rw.withheldEvent(nil); event != nil {
			rw.writeThrough(event)
			rw.flush()
		}
	}
}

// withholdHeld withholds what the filter holds of the body together with the
// data that would make it hold more than it may (see Write). On an event
// stream whose status has been forwarded, what it holds is the start of one
// event: that event alone is withheld, an error event takes its place at
// once, and the stream goes on after it (see skip), so that a client on a
// stream the backend holds open keeps receiving the events after it. The
// error names the request the event answers when the start of the event
// does (see heldEventID), or else when the request body does; when neither
// does, the event is dropped without a replacement, since an error without an
// id is read by clients as the answer to a request of their own (see
// withheldEventID). Any other response is withheld from then on (see
// withholdRest).
func (rw *toolFilterWriter) withholdHeld() {
	if mimeType, _ := responseMediaType(rw.header); mimeType != "text/event-stream" || rw.headerPending {
		rw.withholdRest()
		return
	}
	id := heldEventID(rw.buffer)
	if !id.IsValid() {
		id = requestID(rw.requestBody)
	}
	rw.skipping = true
	rw.skipLine = lineStart(rw.buffer[bytes.LastIndexByte(rw.buffer, '\n')+1:])
	rw.dropHeld()
	if id.IsValid() {
		rw.writeThrough(sseErrorEvent(withheldResponse(id)))
		rw.flush()
	}
}

// dropHeld drops what the filter holds of the body, so that reading starts
// over with the next bytes it keeps.
func (rw *toolFilterWriter) dropHeld() {
	rw.buffer = nil
	rw.scanned = 0
	rw.jsonScan = jsonBodyScan{}
}

// keepRest keeps what processBuffer left of the buffer for a later call and
// drops the consumed bytes before it, which have been written or withheld.
// What is kept has been read without finding the end of an event in it, and
// a JSON scan starts over with it once anything was consumed, since the body
// the scan read is gone.
func (rw *toolFilterWriter) keepRest(consumed int) {
	rw.buffer = append(rw.buffer[:0], rw.buffer[consumed:]...)
	rw.scanned = len(rw.buffer)
	if consumed > 0 {
		rw.jsonScan = jsonBodyScan{}
	}
}

// skip drops data, bytes of the event the filter is skipping (see
// withholdHeld), up to the blank line that ends it, and returns the bytes
// after that line, which start the next event; it returns nothing while the
// event has not ended. The blank line is read as drainBuffer reads one (see
// isBlankSSELine), from the start of the line the dropped bytes end in, so
// that a blank line split across writes still ends the event.
func (rw *toolFilterWriter) skip(data []byte) []byte {
	prefix := len(rw.skipLine)
	end := 0
	for _, line := range splitSSELines(slices.Concat(rw.skipLine, data)) {
		end += len(line.text) + len(line.term)
		if line.term == nil {
			rw.skipLine = lineStart(line.text)
			return nil
		}
		if isBlankSSELine(line.text) {
			rw.skipping = false
			rw.skipLine = nil
			return data[end-prefix:]
		}
	}
	rw.skipLine = nil
	return nil
}

// lineStart returns what tells whether a line that has not ended yet is
// blank once it ends (see isBlankSSELine): the line itself while it is empty
// or a lone CR, and otherwise two of its bytes, after which no line is blank.
func lineStart(text []byte) []byte {
	return slices.Clone(text[:min(len(text), 2)])
}

// withheldEvent returns the event that takes the place of event, one the
// filter withholds from an event stream: an event whose data is the JSON-RPC
// error that answers the request the withheld event answers (see
// withheldEventID). It returns nil when no request with an id is there to
// answer, and nothing takes the place of the withheld event. event is nil for
// a run of events withheld together (see processEventStream) and for the
// rest of a stream (see withholdRest).
func (rw *toolFilterWriter) withheldEvent(event []sseLine) []byte {
	id := withheldEventID(event, rw.requestBody)
	if !id.IsValid() {
		return nil
	}
	return sseErrorEvent(withheldResponse(id))
}

// sseErrorEvent returns an event whose data is response.
func sseErrorEvent(response *jsonrpc2.Response) []byte {
	return fmt.Appendf(nil, "event: message\ndata: %s\n\n", EncodeJSONRPCError(response))
}

// errorResponse returns the JSON-RPC error that answers the request in place
// of a response the filter withholds (see withheldResponse). It carries the
// id of the request, so that the client can tell which of its requests
// failed, and no id when the request has none, as a notification or the GET
// request that opens an event stream do.
func (rw *toolFilterWriter) errorResponse() *jsonrpc2.Response {
	return withheldResponse(requestID(rw.requestBody))
}

// withheldResponse returns the JSON-RPC error, answering the request id, that
// takes the place of what the filter withholds. As in the authorization
// middleware, its message is generic.
func withheldResponse(id jsonrpc2.ID) *jsonrpc2.Response {
	return &jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(CodeInternalError, "internal error"),
	}
}

// withheldEventID returns the id of the request that the error written in
// place of a withheld event answers, or an absent id when that error would
// answer no request of the client's, so that none is written.
//
// A withheld event that is a response with an id answers the request with
// that id, the one the client is waiting on. A withheld event that is a
// request or a notification answers none: an error with no id, or with the
// id of a request the server made, is read by clients as the answer to a
// request of their own or ends their session. No withheld event decodes as a
// request today, since an event is withheld for what its result holds and a
// request carries none, but one that did would get no error either way. A
// response the strict decoder rejects for a member below its top level, such
// as a tool whose name is spelled in another case, still answers by the one
// id its top-level members name (see heldDataID), the id the client waits on.
// Any other event, one whose data is not one message, a batch, or an error
// response without an id, answers the request that opened the stream, whose
// body requestBody holds: a POST request names its id, and a GET request
// names none.
func withheldEventID(event []sseLine, requestBody []byte) jsonrpc2.ID {
	data := bytes.Join(sseEventData(event), sseLF)
	message, err := decodeMessage(data)
	if err == nil {
		switch message := message.(type) {
		case *jsonrpc2.Response:
			if message.ID.IsValid() {
				return message.ID
			}
		case *jsonrpc2.Request:
			return jsonrpc2.ID{}
		}
	}
	if id := heldDataID(data); id.IsValid() {
		return id
	}
	return requestID(requestBody)
}

// heldEventID returns the id the start of an event names, read from the
// data the event has so far (see heldDataID).
func heldEventID(event []byte) jsonrpc2.ID {
	return heldDataID(bytes.Join(sseEventData(splitSSELines(event)), sseLF))
}

// heldDataID returns the id the top-level members of data name, when they
// leave no doubt about it: data, read as JSON up to where it is cut or where
// its object closes, holds exactly one top-level member named id, with a
// string or an integer value, no member named method, one member named
// result or error, and no member whose name is one of those four spelled in
// another case. Any other data names no id: a request's error would answer a
// request of the server, data without a result or an error may be a request
// whose method has not arrived, and data that spells one of those members
// twice or in another case is read differently by clients (see
// DecodeMessage). An integer the data is cut in is not read either, since
// more digits may follow. The error that takes the place of an event the
// filter cannot decode in full, or that it withholds from a stream the
// request did not open (see withholdHeld), can so still name the request the
// event answered.
func heldDataID(data []byte) jsonrpc2.ID {
	members, ok := heldMembers(data)
	if !ok {
		return jsonrpc2.ID{}
	}
	var id jsonrpc2.ID
	counts := make(map[string]int)
	for _, member := range members {
		for _, name := range []string{"id", "method", "result", "error"} {
			if member.name != name && strings.EqualFold(member.name, name) {
				return jsonrpc2.ID{}
			}
		}
		counts[member.name]++
		if member.name == "id" {
			id = idToken(member.value)
		}
	}
	if counts["id"] != 1 || counts["method"] != 0 || counts["result"] > 1 || counts["error"] > 1 ||
		counts["result"]+counts["error"] == 0 {
		return jsonrpc2.ID{}
	}
	return id
}

// heldMember is a top-level member of the object a withheld event's data
// opens with: its name and its value, a scalar token or the delimiter that
// opens an object or array value.
type heldMember struct {
	name  string
	value any
}

// heldMembers returns the top-level members of the object data opens with,
// read up to where data is cut or where the object closes, and reports
// whether data opens an object at all. A member whose value is read in full
// carries that value; a top-level member whose value is cut, an integer that
// more digits may follow or a string that has not closed, still counts by
// its name with a nil value, so a start cut inside the value of a method or
// of a case-folded spelling of one of the four names is read as holding that
// member, while idToken rejects the nil value so a cut id value names no id.
func heldMembers(data []byte) ([]heldMember, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	var (
		members []heldMember
		pending *heldMember
		// pendingEnd is the offset in data after the value of pending: a
		// number that ends data may be cut, while any byte after a number
		// ends it.
		pendingEnd int64
		name       string
	)
	for depth, expectKey := 1, true; ; {
		token, err := decoder.Token()
		if err != nil {
			return cutMembers(members, pending, pendingEnd, int64(len(data)), depth, expectKey, name), true
		}
		if pending != nil {
			members = append(members, *pending)
			pending = nil
		}
		if delim, ok := token.(json.Delim); ok {
			if depth == 1 && !expectKey {
				members = append(members, heldMember{name: name, value: delim})
			}
			if depth += nesting(delim); depth == 0 {
				return members, true
			}
			expectKey = depth == 1
			continue
		}
		if depth != 1 {
			continue
		}
		if expectKey {
			name, _ = token.(string)
			expectKey = false
			continue
		}
		pending = &heldMember{name: name, value: token}
		pendingEnd = decoder.InputOffset()
		expectKey = true
	}
}

// cutMembers finalizes the top-level members of a held object whose data
// ran out mid-read: a pending member whose value was read in full is kept
// unless it is a number the cut may extend, and a member whose value is cut,
// read at the top level with its name already known, still counts by that
// name with a nil value.
func cutMembers(
	members []heldMember, pending *heldMember, pendingEnd, dataLen int64, depth int, expectKey bool, name string,
) []heldMember {
	if pending != nil {
		if _, number := pending.value.(json.Number); !number || pendingEnd < dataLen {
			members = append(members, *pending)
		}
		return members
	}
	if depth == 1 && !expectKey {
		members = append(members, heldMember{name: name, value: nil})
	}
	return members
}

// nesting returns the change in depth a delimiter token makes: one deeper
// for a delimiter that opens an object or array, one shallower for one that
// closes it.
func nesting(delim json.Delim) int {
	if delim == '{' || delim == '[' {
		return 1
	}
	return -1
}

// idToken returns the request id a JSON token holds: an integer or a string.
// Any other token, a fraction included, holds none.
func idToken(value any) jsonrpc2.ID {
	switch id := value.(type) {
	case json.Number:
		if n, err := id.Int64(); err == nil {
			return jsonrpc2.Int64ID(n)
		}
	case string:
		return jsonrpc2.StringID(id)
	}
	return jsonrpc2.ID{}
}

// requestID returns the id of the JSON-RPC request in body, or an absent id
// when body holds no request. The body is the backend's to answer, so one
// that holds no request is read without a warning.
func requestID(body []byte) jsonrpc2.ID {
	if len(body) == 0 {
		return jsonrpc2.ID{}
	}
	message, err := decodeMessage(body)
	if err != nil {
		return jsonrpc2.ID{}
	}
	if request, ok := message.(*jsonrpc2.Request); ok {
		return request.ID
	}
	return jsonrpc2.ID{}
}

type toolCallRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  *map[string]any `json:"params,omitempty"`
}

// processBuffer processes the buffered response body according to its
// declared MIME type, writes the result to w, and returns how many leading
// bytes of buffer it consumed; the caller keeps the rest buffered. When part
// of what it consumed is, or could be read as, a tools list that cannot be
// filtered, it leaves that part out of w, writes the rest, and returns an
// error with the count (see processEventStream). An error that wraps errBug
// is a failure of the filter itself, after which nothing in w may be used.
// scan is the scan of buffer as a JSON body, which an earlier call has read
// as far as buffer went then (see processJSONBuffer).
// from is the length of a prefix of buffer that an earlier call has read
// without finding the end of an event in it (see processEventStream).
// sniff indicates whether a body without a supported MIME type has to be
// read before it can safely be passed through: it's set for a 2xx response
// and for an ambiguous Content-Type (see responseMediaType), and only
// consulted for an unrecognized MIME type -- see processUnrecognizedMimeType.
// terminal reports that the response has ended, so no more data will follow.
// withheld returns what is written in place of an event withheld from an
// event stream, or nothing (see writeWithheld).
func processBuffer(
	config *toolMiddlewareConfig,
	buffer []byte,
	scan *jsonBodyScan,
	from int,
	mimeType string,
	sniff bool,
	terminal bool,
	withheld func(event []sseLine) []byte,
	w io.Writer,
) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}

	switch mimeType {
	case "application/json":
		return processJSONBuffer(config, buffer, scan, terminal, w)
	case "text/event-stream":
		return processEventStream(config, buffer, from, terminal, withheld, w)
	default:
		return processUnrecognizedMimeType(config, buffer, mimeType, sniff, terminal, w)
	}
}

// processJSONBuffer filters a JSON-RPC response body if it carries a tools
// list, otherwise passes it through unchanged (e.g. a tools/call result, or
// any other JSON-RPC message this middleware isn't meant to touch).
//
// A body that is, or could be read as, a tools list but that
// decodeToolsListResponse rejects is an error, and so is a body that ends
// without being one JSON value while some client could still read a list from
// it (see mayCarryToolsList): one that starts with a complete value that
// could be read as a tools list, or one that starts like a JSON object or
// array at all. Any other body that is not a complete JSON value yet is
// incomplete: more may follow, or, once the response has ended, it cannot
// carry a list.
//
// Whether the body is one complete JSON value is read from scan, which reads
// only the bytes that arrived since the previous call (see jsonBodyScan), so
// a body that arrives in many writes is read once rather than validated whole
// at every flush.
func processJSONBuffer(
	config *toolMiddlewareConfig,
	buffer []byte,
	scan *jsonBodyScan,
	terminal bool,
	w io.Writer,
) (int, error) {
	if !scan.isValid(buffer) {
		if terminal && mayCarryToolsList(buffer) {
			return len(buffer), errors.New("response body that could carry a tools list is not a single JSON value")
		}
		return 0, fmt.Errorf("%w: body is not a complete JSON value", errKeepBuffering)
	}

	if !isToolsListCandidate(buffer) {
		if _, err := w.Write(buffer); err != nil {
			return 0, fmt.Errorf("%w: %w", errBug, err)
		}
		return len(buffer), nil
	}

	message, err := decodeToolsListResponse(buffer)
	if err != nil {
		return len(buffer), err
	}
	if err := processToolsListResponse(config, message, w); err != nil {
		return len(buffer), err
	}
	return len(buffer), nil
}

// processUnrecognizedMimeType handles a body whose Content-Type is missing,
// ambiguous (see responseMediaType), or isn't one of the two MCP-supported
// types (application/json, text/event-stream).
//
// sniff is false only for a non-2xx response whose Content-Type names
// neither supported type. Such a response has nothing to filter -- the tool
// filter only ever acts on a tools/list result, which a non-2xx status can't
// carry -- so it's always safe to pass through.
//
// A 2xx response, on the other hand, is spec-required to declare one of the
// two supported Content-Types. A missing or wrong header there could be an
// accident, or a backend deliberately omitting/mislabeling it to smuggle an
// unfiltered tools list past the filter (the same disguised-result concern
// handled in pkg/authz for #5257). So the raw body is sniffed the same way a
// correctly-labeled response would be processed: if it could be read as a
// tools list under either supported shape, it's filtered (or, on failure,
// rejected) exactly as it would be under the correct Content-Type. Only once
// neither shape could carry a tools list is it safe to pass through
// unchanged. A body whose Content-Type is ambiguous is sniffed the same way
// at any status, as a body labeled with either supported type is read
// whatever the status.
//
// Such a body is only sniffed once the response has ended. Until then it
// stays buffered, because a part of a tools list the backend writes across
// several flushes does not look like one and would otherwise be passed
// through piece by piece. A backend that streams a response labels it
// text/event-stream without naming application/json, so only a response
// that is outside the spec or labeled ambiguously is held back this way.
func processUnrecognizedMimeType(
	config *toolMiddlewareConfig,
	buffer []byte,
	mimeType string,
	sniff bool,
	terminal bool,
	w io.Writer,
) (int, error) {
	if !sniff {
		return 0, fmt.Errorf("%w: %s", errUnsupportedMimeType, mimeType)
	}

	if !terminal {
		return 0, fmt.Errorf("%w: a body without a supported content type is read once the response ends",
			errKeepBuffering)
	}

	if mayCarryToolsList(buffer) {
		return processJSONBuffer(config, buffer, &jsonBodyScan{}, terminal, w)
	}

	if eventStreamMayCarryToolsList(buffer) {
		// Nothing takes the place of a withheld event here: the status of
		// this body has not been forwarded, so a body that fails to filter
		// is answered with an error as a whole.
		return processEventStream(config, buffer, 0, terminal, nil, w)
	}

	return 0, fmt.Errorf("%w: %s", errUnsupportedMimeType, mimeType)
}

// processEventStream filters the tools lists carried by a text/event-stream
// body. It works on whole events, the way clients read them: the data lines
// of an event, joined with LF, are one payload, so a tools list split across
// several data lines is filtered like any other. An event whose data is a
// tools list is re-emitted with one data line per line of the filtered list,
// which a client joins back with LF; every other line keeps its bytes and its
// terminator.
//
// Only complete events are processed: unless terminal, an event whose
// closing blank line (LF or CRLF terminated, the one kind every client
// agrees on) has not arrived yet stays buffered, and the returned count stops
// before it. Reading for that blank line resumes at from, the length of a
// prefix of buffer that an earlier call has read without finding one (see
// completeSSEEvents); what the returned count leaves has been read in full.
// At the end of the response the last event is processed as it stands, since
// some clients dispatch an event the stream ended without terminating.
//
// A blank line that ends in a bare CR ends an event under the WHATWG grammar
// but not for clients that split lines at LF only, which read on into the
// bytes after it, and a blank line that ends in one of lineSeparators ends
// an event only for the clients that split lines at those too. So the events
// of a stream whose lines end in a bare CR or in one of those characters
// alone stay buffered until the response ends.
//
// A stream that holds a bare CR or one of lineSeparators is read as
// different lines, and so as different events, by clients that follow the
// WHATWG grammar, by clients that split lines at LF only, and by clients
// that split lines at those characters as well (see
// eventStreamMayCarryToolsList). Such a stream passes through unchanged when
// no event could carry a tools list under any of those readings, and is
// otherwise withheld up to the returned count, since it has no event
// boundaries every client agrees on.
// Any other event whose data could carry a tools list but is not one
// unambiguous JSON value (see filterSSEEvent) is withheld alone, together with
// the blank line that ends it, and the events around it are written. Either
// way what withheld returns is written in place of what is withheld (see
// writeWithheld), for the withheld event, or for no event when the stream is
// withheld up to the count; the returned error says what was withheld, and
// the count still covers it.
func processEventStream(
	config *toolMiddlewareConfig,
	buffer []byte,
	from int,
	terminal bool,
	withheld func(event []sseLine) []byte,
	w io.Writer,
) (int, error) {
	consumed := len(buffer)
	if !terminal {
		consumed = completeSSEEvents(buffer, from)
	}
	if consumed == 0 {
		return 0, fmt.Errorf("%w: no complete event", errKeepBuffering)
	}
	stream := buffer[:consumed]

	if hasBareCR(stream) || hasLineSeparator(stream) {
		if eventStreamMayCarryToolsList(stream) {
			if err := writeWithheld(w, withheld, nil); err != nil {
				return 0, err
			}
			// The stream holds a bare CR or a line separator, so the first of
			// them is there to name. Length only, not the stream: it is the
			// full pre-filter payload (e.g. the unfiltered tools/list), and
			// this error is logged upstream.
			split, _ := firstLineSplit(stream)
			return consumed, fmt.Errorf(
				"%d-byte event stream that could carry a tools list holds U+%04X, at which only some clients end a line",
				consumed, split)
		}
		if _, err := w.Write(stream); err != nil {
			return 0, fmt.Errorf("%w: %w", errBug, err)
		}
		return consumed, nil
	}

	var (
		event    []sseLine
		failures []error
	)
	for _, line := range splitSSELines(stream) {
		if !isBlankSSELine(line.text) {
			event = append(event, line)
			continue
		}
		if err := writeSSEEvent(config, w, withheld, event, line); err != nil {
			failures = append(failures, err)
		}
		event = nil
	}
	if err := writeSSEEvent(config, w, withheld, event); err != nil {
		failures = append(failures, err)
	}
	return consumed, errors.Join(failures...)
}

// writeSSEEvent writes one event, as filterSSEEvent resolves it, to w,
// followed by end, the blank line that ends the event if it has one. An event
// that filterSSEEvent cannot resolve is not written, and neither is end: what
// withheld returns for the event is written in their place (see
// writeWithheld).
func writeSSEEvent(
	config *toolMiddlewareConfig, w io.Writer, withheld func(event []sseLine) []byte, event []sseLine, end ...sseLine,
) error {
	lines, err := filterSSEEvent(config, event)
	if err != nil {
		if writeErr := writeWithheld(w, withheld, event); writeErr != nil {
			return writeErr
		}
		return err
	}
	if err := writeSSELines(w, lines...); err != nil {
		return err
	}
	return writeSSELines(w, end...)
}

// writeWithheld writes to w what withheld returns for event, the event that
// takes the place of the withheld one. It writes nothing when withheld is nil,
// for a body whose status has not been forwarded (see
// processUnrecognizedMimeType), or returns nothing, when no request with an
// id is there to answer (see withheldEvent).
func writeWithheld(w io.Writer, withheld func(event []sseLine) []byte, event []sseLine) error {
	if withheld == nil {
		return nil
	}
	data := withheld(event)
	if len(data) == 0 {
		return nil
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("%w: %w", errBug, err)
	}
	return nil
}

// processToolsListResponse writes a tools list response to w, followed by a
// newline, with the tools that are not in the filter list left out and the
// overrides applied to the rest. Everything else in the response keeps the
// bytes the backend sent: see toolsListMessage.
func processToolsListResponse(
	config *toolMiddlewareConfig,
	message toolsListMessage,
	w io.Writer,
) error {
	tools := make([]json.RawMessage, 0, len(message.items))
	for _, item := range message.items {
		// Apply the shared filtering/override logic one tool at a time, so
		// that each tool that is kept is written back from its own bytes.
		processed := applyFilteringAndOverrides(config, []SimpleTool{{
			Name:        item.name,
			Description: item.description,
		}})
		if len(processed) == 0 {
			continue
		}
		tool, err := item.withOverride(processed[0])
		if err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
		tools = append(tools, tool)
	}

	body, err := message.encode(tools)
	if err != nil {
		return fmt.Errorf("%w: %w", errBug, err)
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("%w: %w", errBug, err)
	}
	return nil
}

// applyFilteringAndOverrides is the core logic for filtering and overriding tools.
// This implements the exact same logic as before but is now extracted for reuse.
func applyFilteringAndOverrides(config *toolMiddlewareConfig, tools []SimpleTool) []SimpleTool {
	result := make([]SimpleTool, 0, len(tools))
	for _, tool := range tools {
		description := tool.Description

		// If the tool is overridden, we need to use the override name and description.
		if entry, ok := config.getToolListOverride(tool.Name); ok {
			if entry.OverrideName != "" {
				tool.Name = entry.OverrideName
			}
			if entry.OverrideDescription != "" {
				description = entry.OverrideDescription
			}
		}

		// If the tool is in the filter, we add it to the filtered tools list.
		// Note that lookup is done using the user-known name (tool.Name after override).
		if config.isToolInFilter(tool.Name) {
			result = append(result, SimpleTool{
				Name:        tool.Name,
				Description: description,
			})
		}
	}
	return result
}

// toolCallFix mimics a sum type in Go. The actual types represent the
// possible manipulations to perform on the tool call request, namely:
// - filter the tool call request
// - override the tool call request
// - return a bogus tool call request
// - do nothing
//
// The actual types are not exported, and the only way to get a value of a specific type
// is to use a type assertion.
//
// Technical note: it might be tempting to build this into toolMiddlewareConfig, but this
// would leave out the case in which the request is malformed, scenario that does not
// belong to the logic implementing config.
type toolCallFixAction interface{}

// toolCallFilter is a struct that represents a tool call filter, i.e.
// the tool call request is not allowed.
type toolCallFilter struct{}

// toolCallOverride is a struct that represents a tool call override, i.e.
// the tool call request is allowed, but the tool name is overridden.
type toolCallOverride struct {
	actualName string
}

// Name returns the actual name of the tool.
func (t *toolCallOverride) Name() string {
	return t.actualName
}

// toolCallBogus is a struct that represents a bogus tool call request, i.e.
// the tool call request is not allowed and the tool name is not overridden.
type toolCallBogus struct{}

// toolCallNoAction is a struct that represents a tool call no action, i.e.
// the tool call request is allowed and the tool name is not overridden.
type toolCallNoAction struct{}

// processToolCallRequest processes a tool call request checking if the tool
// is in the filter list. Note that the tool name received in the toolCallRequest
// is going to be the user-provided name, which might be different from the actual
// tool name.
func processToolCallRequest(
	config *toolMiddlewareConfig,
	toolCallRequest toolCallRequest,
) toolCallFixAction {
	// NOTE: the spec does not allow for nil params.
	if toolCallRequest.Params == nil {
		return &toolCallBogus{}
	}

	// NOTE: the spec does not allow for name to be missing.
	toolName, ok := (*toolCallRequest.Params)["name"].(string)
	if !ok {
		return &toolCallBogus{}
	}

	// NOTE: the spec does not allow for empty tool names.
	if toolName == "" {
		return &toolCallBogus{}
	}

	// If the tool is not in the filter list, return an error.
	// Note that the tool name we use here is the user-provided name, which
	// might be different from the actual tool name, but filters are expressed
	// in terms of tool names as known to the user, so this is correct.
	if !config.isToolInFilter(toolName) {
		return &toolCallFilter{}
	}

	// If the tool is allowed by the filter, and has an override, return the
	// actual name to fix the tool call request.
	if actualName, ok := config.getToolCallActualName(toolName); ok {
		return &toolCallOverride{actualName: actualName}
	}

	// If the tool is allowed by the filter, and does not have an override,
	// return an empty string and no error, signaling the fact that the tool
	// call request is ok as is.
	return &toolCallNoAction{}
}

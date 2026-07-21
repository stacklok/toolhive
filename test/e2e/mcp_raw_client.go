// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sync/atomic"
	"time"
)

// The following mirror the reserved wire constants defined in
// pkg/mcp/revision.go. They are duplicated here rather than imported: this
// client must stay independent of any MCP library (production or SDK) so it
// can emit traffic no conforming implementation would ever produce.
const (
	// MCPVersionModern is the Modern (stateless) MCP protocol version.
	MCPVersionModern = "2026-07-28"

	// MetaKeyProtocolVersion is the reserved _meta key carrying the per-request
	// protocol version on Modern requests.
	MetaKeyProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	// MetaKeyClientInfo is the reserved _meta key carrying client implementation
	// info on Modern requests. Optional: a Modern request may omit it.
	MetaKeyClientInfo = "io.modelcontextprotocol/clientInfo"
	// MetaKeyClientCapabilities is the reserved _meta key carrying client
	// capabilities on Modern requests.
	MetaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"

	// HeaderMCPProtocolVersion is the HTTP header carrying the Modern protocol
	// version.
	HeaderMCPProtocolVersion = "MCP-Protocol-Version"
	// HeaderMCPSessionID is the HTTP header carrying the Legacy session id.
	HeaderMCPSessionID = "Mcp-Session-Id"
)

// requestIDCounter assigns default JSON-RPC ids so distinct RawRequests get
// distinct ids unless a test overrides one via WithID (e.g. to collide two
// ids on purpose).
var requestIDCounter atomic.Int64

// RawRequest builds a single JSON-RPC request for MCP-over-HTTP, or carries
// a raw pre-built body that bypasses the builder entirely. Construct one
// with NewLegacyRequest, NewLegacyInitializeRequest, or NewModernRequest,
// then chain the With*/Set*/Delete* methods to adjust it. The zero value is
// not usable.
//
// Finish building before sending: Send/SendRaw only read a RawRequest, so a
// fully-built one is safe to share read-only across goroutines (e.g. one
// RawRequest fanned out to many concurrent senders), but mutating it with
// Set*/With* concurrently with a send is a data race.
type RawRequest struct {
	method  string
	id      any
	params  map[string]any
	meta    map[string]any // nil means no _meta object at all
	headers map[string]string
	body    []byte // when set, overrides method/id/params/meta entirely
}

// NewLegacyRequest builds a Legacy (2025-11-25) JSON-RPC request for the
// given method. params is cloned; the caller's map is never mutated by the
// returned RawRequest.
func NewLegacyRequest(method string, params map[string]any) (*RawRequest, error) {
	return newRawRequest(method, params)
}

// NewLegacyInitializeRequest builds a standard Legacy "initialize" request.
func NewLegacyInitializeRequest(clientName, clientVersion string) *RawRequest {
	r, _ := newRawRequest("initialize", map[string]any{ // method is a fixed non-empty literal; newRawRequest cannot fail here.
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": clientVersion,
		},
	})
	return r
}

// NewModernRequest builds a Modern (2026-07-28) JSON-RPC request: it sets the
// MCP-Protocol-Version header and the reserved _meta keys protocolVersion and
// clientCapabilities (clientInfo is optional per the draft schema and is
// omitted by default). Each can be independently overridden or removed via
// SetHeader/DeleteHeader and SetMeta/DeleteMeta.
func NewModernRequest(method string, params map[string]any) (*RawRequest, error) {
	r, err := newRawRequest(method, params)
	if err != nil {
		return nil, err
	}
	r.headers[HeaderMCPProtocolVersion] = MCPVersionModern
	r.meta = map[string]any{
		MetaKeyProtocolVersion:    MCPVersionModern,
		MetaKeyClientCapabilities: map[string]any{},
	}
	return r, nil
}

func newRawRequest(method string, params map[string]any) (*RawRequest, error) {
	if method == "" {
		return nil, errors.New("mcp_raw_client: method must not be empty")
	}
	return &RawRequest{
		method:  method,
		id:      requestIDCounter.Add(1),
		params:  maps.Clone(params),
		headers: map[string]string{},
	}, nil
}

// WithID overrides the JSON-RPC id, e.g. to collide two requests' ids or to
// use a value outside the int64-safe float64 range.
func (r *RawRequest) WithID(id any) *RawRequest {
	r.id = id
	return r
}

// WithSessionID sets the Mcp-Session-Id header, including a foreign/spoofed
// session id.
func (r *RawRequest) WithSessionID(sessionID string) *RawRequest {
	return r.SetHeader(HeaderMCPSessionID, sessionID)
}

// WithClientInfo sets the _meta clientInfo object.
func (r *RawRequest) WithClientInfo(name, version string) *RawRequest {
	return r.SetMeta(MetaKeyClientInfo, map[string]any{"name": name, "version": version})
}

// WithRawBody replaces the entire request body with body, sent verbatim.
// Use this for truncated, oversized, or garbage bodies. body is cloned;
// mutating the caller's slice afterward has no effect on the request.
func (r *RawRequest) WithRawBody(body []byte) *RawRequest {
	r.body = bytes.Clone(body)
	return r
}

// SetHeader sets an arbitrary HTTP header on the request.
func (r *RawRequest) SetHeader(key, value string) *RawRequest {
	r.headers[key] = value
	return r
}

// DeleteHeader removes a header, e.g. to omit MCP-Protocol-Version entirely.
func (r *RawRequest) DeleteHeader(key string) *RawRequest {
	delete(r.headers, key)
	return r
}

// SetMeta sets a params._meta key to an arbitrary value, including a
// non-object value for a key the spec requires to be an object (to trigger
// error paths). It lazily creates the _meta object if none exists yet.
func (r *RawRequest) SetMeta(key string, value any) *RawRequest {
	if r.meta == nil {
		r.meta = map[string]any{}
	}
	r.meta[key] = value
	return r
}

// DeleteMeta removes a params._meta key, e.g. to make clientInfo absent.
func (r *RawRequest) DeleteMeta(key string) *RawRequest {
	delete(r.meta, key)
	return r
}

// marshal renders the JSON-RPC wire body for r, or returns r.body verbatim
// if WithRawBody was used.
func (r *RawRequest) marshal() ([]byte, error) {
	if r.body != nil {
		return r.body, nil
	}

	wire := map[string]any{
		"jsonrpc": "2.0",
		"method":  r.method,
	}
	if r.id != nil {
		wire["id"] = r.id
	}

	params := maps.Clone(r.params)
	if r.meta != nil {
		if params == nil {
			params = map[string]any{}
		}
		params["_meta"] = r.meta
	}
	if params != nil {
		wire["params"] = params
	}

	return json.Marshal(wire)
}

// NewBatchBody renders a JSON-RPC batch: a JSON array of the wire bodies of
// reqs, in order. Combine with RawMCPClient.SendRaw to send it. Each element
// is validated the same as a standalone RawRequest; a deliberately malformed
// batch payload (bad JSON, wrong array shape) must be hand-built and sent
// via SendRaw directly.
func NewBatchBody(reqs ...*RawRequest) ([]byte, error) {
	parts := make([]json.RawMessage, 0, len(reqs))
	for i, r := range reqs {
		b, err := r.marshal()
		if err != nil {
			return nil, fmt.Errorf("mcp_raw_client: marshal batch element %d: %w", i, err)
		}
		parts = append(parts, b)
	}
	return json.Marshal(parts)
}

// RawRPCError is the JSON-RPC "error" object of a RawResponse.
type RawRPCError struct {
	Code    int64
	Message string
	Data    json.RawMessage
}

// RawResponse is the parsed result of sending a single (non-batch) JSON-RPC
// request over MCP-over-HTTP. For batch responses (a JSON array), inspect
// Body directly instead of ID/Result/Error.
type RawResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte // raw response body, always populated
	ID         any    // echoed "id"; nil if absent, JSON null, or unparseable
	Result     json.RawMessage
	Error      *RawRPCError
}

// RawMCPClient sends raw MCP-over-HTTP JSON-RPC requests with byte-level
// control over the wire shape. It wraps a single *http.Client (and its
// connection pool), shared and safe for concurrent use across goroutines —
// construct one RawMCPClient per test suite, not one per request.
type RawMCPClient struct {
	httpClient *http.Client
}

// NewRawMCPClient creates a RawMCPClient. Every request made through it is
// bounded by timeout, so a stuck proxy fails fast instead of hanging the
// test.
func NewRawMCPClient(timeout time.Duration) (*RawMCPClient, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("mcp_raw_client: timeout must be positive, got %s", timeout)
	}
	return &RawMCPClient{
		httpClient: &http.Client{
			Timeout: timeout,
			// Raised from the net/http default of 2 idle conns/host: adversarial/
			// concurrency tests fan many goroutines out to the same proxy host,
			// and the default would force constant reconnection.
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}, nil
}

// Send marshals req and POSTs it to url. No Accept header is set by
// default; a live streamable-HTTP MCP endpoint may require
// "Accept: application/json, text/event-stream" — set it via
// req.SetHeader("Accept", ...) when hitting a real proxy.
func (c *RawMCPClient) Send(ctx context.Context, url string, req *RawRequest) (*RawResponse, error) {
	body, err := req.marshal()
	if err != nil {
		return nil, fmt.Errorf("mcp_raw_client: marshal request: %w", err)
	}
	return c.SendRaw(ctx, url, req.headers, body)
}

// SendRaw POSTs body verbatim to url with the given headers — for batches
// built with NewBatchBody, or any payload (truncated, oversized, garbage)
// not expressible via RawRequest.
func (c *RawMCPClient) SendRaw(ctx context.Context, url string, headers map[string]string, body []byte) (*RawResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp_raw_client: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp_raw_client: do request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mcp_raw_client: read response body: %w", err)
	}

	result := &RawResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		Body:       respBody,
	}
	populateEnvelope(respBody, result)
	return result, nil
}

// wireError is the shape of a JSON-RPC "error" object on the wire.
type wireError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// wireResponse is the shape of a single JSON-RPC response object. ID is kept
// as raw JSON so decodeID can preserve large integers exactly.
type wireResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

// populateEnvelope best-effort parses body as a single JSON-RPC response
// object into out. A malformed or non-object body (e.g. a batch array, or
// deliberately garbled test input) leaves ID/Result/Error at their zero
// values; out.Body still carries the raw bytes for the caller to inspect.
func populateEnvelope(body []byte, out *RawResponse) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return
	}
	out.ID = decodeID(w.ID)
	out.Result = w.Result
	if w.Error != nil {
		out.Error = &RawRPCError{Code: w.Error.Code, Message: w.Error.Message, Data: w.Error.Data}
	}
}

// decodeID decodes a raw JSON-RPC id, returning a json.Number for numeric
// ids (so large int64 values round-trip exactly, unlike the float64 that
// encoding/json's default decodes numbers into) or a string for string ids.
// An absent or null id decodes to nil.
func decodeID(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}

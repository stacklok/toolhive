// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Revision identifies which MCP protocol era a request belongs to.
type Revision int

const (
	// RevisionLegacy is the current 2025-11-25 MCP revision: session-based,
	// initialize handshake, Mcp-Session-Id.
	RevisionLegacy Revision = iota
	// RevisionModern is the 2026-07-28 MCP revision: stateless, no initialize,
	// protocol metadata carried per-request in _meta.
	RevisionModern
)

// MCPVersionModern is the single Modern (stateless) protocol version this
// build understands.
const MCPVersionModern = "2026-07-28"

// metaKeyProtocolVersion is the reserved _meta key that carries the per-request
// protocol version on Modern (stateless) MCP requests, per the draft MCP schema's
// RequestMetaObject.
const metaKeyProtocolVersion = "io.modelcontextprotocol/protocolVersion"

// metaKeyClientInfo is the reserved _meta key that carries the per-request client
// implementation info on Modern (stateless) MCP requests, per the draft MCP
// schema's RequestMetaObject.
const metaKeyClientInfo = "io.modelcontextprotocol/clientInfo"

// metaKeyClientCapabilities is the reserved _meta key that carries the per-request
// client capabilities on Modern (stateless) MCP requests, per the draft MCP
// schema's RequestMetaObject.
const metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"

// reservedModernMetaKeys are the _meta keys a Legacy client never sets. The
// presence of any one of them — independent of whether its value is
// well-formed — is itself a claim of the Modern revision, and must not be
// silently downgraded to Legacy. Only a malformed/absent protocolVersion
// alongside one of these keys turns into a rejection, never a downgrade.
var reservedModernMetaKeys = []string{metaKeyProtocolVersion, metaKeyClientInfo, metaKeyClientCapabilities}

// The following JSON-RPC error codes are defined by the draft MCP spec
// (schema/draft/schema.ts) for the stateless "Modern" revision. They are
// declared as local literals, matching this repo's existing convention for
// JSON-RPC codes (e.g. streamable_proxy.go's -32603). The only SDK with
// equivalents, modelcontextprotocol/go-sdk, is reachable solely as a
// transitive dependency (via toolhive-core's mcpcompat) and is pinned to an
// older draft snapshot (2026-06-30, CodeHeaderMismatch = -32001) with no
// equivalents at all for the other two codes below, so importing it would
// risk wiring in stale values. Revisit once the go-sdk dependency is bumped
// to a revision that matches MCPVersionModern.
//
// These are exported so other packages (e.g. the HTTP layer) can reference
// the same wire values instead of hardcoding or redeclaring them.
const (
	// CodeHeaderMismatch signals a mismatch between the MCP-Protocol-Version
	// header and the _meta protocol version (schema.ts HeaderMismatchError).
	// Also reused by RequestHeaderMismatchError for the Mcp-Method/Mcp-Name
	// headers: keep both error types' Code() in sync with this constant.
	CodeHeaderMismatch int64 = -32020
	// CodeMissingClientCapability signals that _meta is missing a client
	// capability required for the request (schema.ts MissingRequiredClientCapabilityError).
	CodeMissingClientCapability int64 = -32021
	// CodeUnsupportedProtocolVersion signals that the _meta protocol version
	// is not one this server supports (schema.ts UnsupportedProtocolVersionError).
	CodeUnsupportedProtocolVersion int64 = -32022
	// CodeInvalidParams is the standard JSON-RPC Invalid Params code, used
	// as a fallback when the draft spec defines no dedicated code for the failure.
	CodeInvalidParams int64 = -32602
	// CodeInvalidRequest is the standard JSON-RPC Invalid Request code. ToolHive
	// uses it to reject a message whose shape is not a valid single request for
	// the negotiated protocol version — currently a JSON-RPC batch, which was
	// removed from MCP in the 2025-06-18 revision (see batch.go).
	CodeInvalidRequest int64 = -32600
)

// HeaderMismatchError indicates the MCP-Protocol-Version HTTP header did not match
// the io.modelcontextprotocol/protocolVersion carried in the request's _meta field.
// Neither value wins: this is a hard rejection of the request, not a signal to
// proceed with either value.
type HeaderMismatchError struct {
	// Header is the MCP-Protocol-Version header value.
	Header string
	// Body is the _meta protocol version value.
	Body string
}

func (e *HeaderMismatchError) Error() string {
	return fmt.Sprintf("MCP-Protocol-Version header %q does not match _meta protocol version %q", e.Header, e.Body)
}

// Code implements CodedError.
func (*HeaderMismatchError) Code() int64 { return CodeHeaderMismatch }

// Data implements CodedError.
func (e *HeaderMismatchError) Data() map[string]any {
	return map[string]any{"header": e.Header, "body": e.Body}
}

// requestHeaderMismatchReason distinguishes why a Mcp-Method/Mcp-Name header
// check failed, so Error() can describe the actual problem instead of always
// reporting a value "mismatch" — the wire code (CodeHeaderMismatch, -32020)
// is the same in every case; only the human-readable message differs.
type requestHeaderMismatchReason int

const (
	// headerMismatchReasonValue is the default: the header and body both
	// carry a value, but they disagree.
	headerMismatchReasonValue requestHeaderMismatchReason = iota
	// headerMismatchReasonMissing means the header was required but absent.
	headerMismatchReasonMissing
	// headerMismatchReasonMalformed means the header value itself could not
	// be decoded (e.g. invalid base64 in a Mcp-Name sentinel).
	headerMismatchReasonMalformed
)

// RequestHeaderMismatchError indicates a Modern (2026-07-28) request header
// contradicted, was missing from, or was malformed relative to the value
// carried in the request body. This is the generalized sibling of
// HeaderMismatchError for the Mcp-Method/Mcp-Name headers: HeaderMismatchError
// is specific to MCP-Protocol-Version, while this type covers any other
// header/body consistency check and names which header failed.
type RequestHeaderMismatchError struct {
	// Header is the name of the header that failed the check (e.g. "Mcp-Method", "Mcp-Name").
	Header string
	// HeaderValue is the (decoded, where applicable) value carried by the header.
	HeaderValue string
	// BodyValue is the value the header was compared against in the request body.
	BodyValue string

	// reason is unexported: it only shapes Error()'s message, not Data()'s
	// three-key shape or Code()'s wire value.
	reason requestHeaderMismatchReason
}

func (e *RequestHeaderMismatchError) Error() string {
	switch e.reason {
	case headerMismatchReasonValue:
		return fmt.Sprintf("%s header %q does not match request body value %q", e.Header, e.HeaderValue, e.BodyValue)
	case headerMismatchReasonMissing:
		return fmt.Sprintf("%s header is missing (required for this request)", e.Header)
	case headerMismatchReasonMalformed:
		return fmt.Sprintf("%s header value %q is malformed", e.Header, e.HeaderValue)
	default:
		return fmt.Sprintf("%s header %q does not match request body value %q", e.Header, e.HeaderValue, e.BodyValue)
	}
}

// Code implements CodedError.
func (*RequestHeaderMismatchError) Code() int64 { return CodeHeaderMismatch }

// Data implements CodedError.
func (e *RequestHeaderMismatchError) Data() map[string]any {
	return map[string]any{"header": e.Header, "headerValue": e.HeaderValue, "bodyValue": e.BodyValue}
}

// UnsupportedVersionError indicates the _meta protocol version named a Modern
// revision this server does not support.
type UnsupportedVersionError struct {
	// Requested is the _meta protocol version the client asked for.
	Requested string
	// Supported lists the protocol versions this server supports.
	Supported []string
}

func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported MCP protocol version %q (supported: %v)", e.Requested, e.Supported)
}

// Code implements CodedError.
func (*UnsupportedVersionError) Code() int64 { return CodeUnsupportedProtocolVersion }

// Data implements CodedError.
func (e *UnsupportedVersionError) Data() map[string]any {
	return map[string]any{"supported": e.Supported, "requested": e.Requested}
}

// MissingClientCapabilityError indicates a Modern request's _meta is missing
// clientCapabilities (clientInfo is optional per the draft schema and is not
// checked here).
//
// The draft types MissingRequiredClientCapabilityError.data.requiredCapabilities
// as a ClientCapabilities object, not a list of names. The classifier cannot
// compute per-method required capabilities (that check is deferred to the
// caller/handler layer), so RequiredCapabilities is populated best-effort and
// may be an empty object.
type MissingClientCapabilityError struct {
	// RequiredCapabilities is the ClientCapabilities object the request was
	// missing, if known.
	RequiredCapabilities map[string]any
}

func (e *MissingClientCapabilityError) Error() string {
	return fmt.Sprintf("request _meta is missing required client capabilities: %v", e.RequiredCapabilities)
}

// Code implements CodedError.
func (*MissingClientCapabilityError) Code() int64 { return CodeMissingClientCapability }

// Data implements CodedError.
func (e *MissingClientCapabilityError) Data() map[string]any {
	return map[string]any{"requiredCapabilities": e.RequiredCapabilities}
}

// MissingModernMetadataError indicates the request carried a Modern signal
// via one of the reserved io.modelcontextprotocol/* _meta keys — with no
// MCP-Protocol-Version header present at all — but _meta carried no valid
// protocolVersion. (When a header IS present, a missing or invalid body
// protocolVersion is a header/body mismatch instead; see HeaderMismatchError.)
// The draft spec defines no dedicated error code for this reserved-key-only
// case, so it falls back to the standard JSON-RPC Invalid Params code.
type MissingModernMetadataError struct{}

func (*MissingModernMetadataError) Error() string {
	return "request carries a Modern signal but no valid io.modelcontextprotocol/protocolVersion in _meta"
}

// Code implements CodedError.
func (*MissingModernMetadataError) Code() int64 { return CodeInvalidParams }

// Data implements CodedError.
func (*MissingModernMetadataError) Data() map[string]any { return map[string]any{} }

// ClassifyRevision determines whether a single MCP request is Legacy or Modern,
// and whether it is valid.
//
// When err != nil, the caller MUST reject the request; the returned Revision is
// then purely informational — it records that the request claimed Modern, not
// that classification succeeded.
//
// method == "initialize" always classifies Legacy immediately, unconditionally —
// Modern never sends initialize (it is the Legacy session-start marker by
// definition) — which also guards against a spoofed Modern _meta on a Legacy
// call.
//
// Otherwise, a request "signals" Modern if the MCP-Protocol-Version header is
// exactly MCPVersionModern, OR _meta carries ANY of the reserved
// io.modelcontextprotocol/* keys (protocolVersion, clientInfo,
// clientCapabilities) — presence of the key is the signal, independent of
// whether its value is well-formed, since a Legacy client never sets these
// keys at all. A request with no signal anywhere classifies Legacy, the safe
// default. A request with a signal is never silently downgraded: it either
// classifies Modern with a nil error, or Modern with an error the caller must
// reject on.
//
// A non-empty MCP-Protocol-Version header that names some OTHER version (not
// MCPVersionModern) and carries no reserved _meta key is, by design, not a
// Modern signal: the request body's _meta is authoritative for the protocol
// version, and an unrecognized header value alone classifies Legacy rather
// than erroring.
//
// Given a Modern signal, checks run in this order:
//  1. meta[metaKeyProtocolVersion] must be a non-empty string. If it is not,
//     and protoHeader is non-empty, the body has nothing valid to match against
//     the header: this is a header/body mismatch (*HeaderMismatchError). If
//     protoHeader is empty — the signal came only from a reserved _meta key,
//     so there is no header to mismatch against — the request is malformed
//     (*MissingModernMetadataError).
//  2. that string must equal MCPVersionModern, or it names an unsupported
//     version (*UnsupportedVersionError).
//  3. if protoHeader is non-empty it must equal the body version, or the two
//     conflict (*HeaderMismatchError, a hard rejection — neither value wins).
//  4. _meta must carry clientCapabilities (clientInfo is optional per the
//     draft schema), or the client capabilities are missing
//     (*MissingClientCapabilityError, with per-method requirements deferred to
//     the caller).
//
// A request that passes all four classifies Modern with a nil error.
func ClassifyRevision(method string, meta map[string]any, protoHeader string) (Revision, error) {
	if method == "initialize" {
		return RevisionLegacy, nil
	}

	if !hasModernSignal(meta, protoHeader) {
		return RevisionLegacy, nil
	}

	bodyVersion, hasBodyVersion := stringMetaValue(meta, metaKeyProtocolVersion)
	if !hasBodyVersion {
		if protoHeader != "" {
			return RevisionModern, &HeaderMismatchError{Header: protoHeader, Body: ""}
		}
		// TODO: this always returns -32602, but ClassifyRevision is transport-agnostic
		// and cannot tell "stdio, no header concept" (where -32602 is correct) apart from
		// "HTTP, header omitted" (where the draft's Server Validation rules make a missing
		// MCP-Protocol-Version header itself a -32020 HeaderMismatch condition). Revisit
		// once the classifier is wired into the HTTP request path and can be given
		// transport context.
		return RevisionModern, &MissingModernMetadataError{}
	}

	if bodyVersion != MCPVersionModern {
		return RevisionModern, &UnsupportedVersionError{Requested: bodyVersion, Supported: []string{MCPVersionModern}}
	}

	if protoHeader != "" && protoHeader != bodyVersion {
		return RevisionModern, &HeaderMismatchError{Header: protoHeader, Body: bodyVersion}
	}

	if !hasObjectMetaValue(meta, metaKeyClientCapabilities) {
		return RevisionModern, &MissingClientCapabilityError{RequiredCapabilities: map[string]any{}}
	}

	return RevisionModern, nil
}

// nameRequiredMethods are the Modern (2026-07-28) methods for which the draft
// spec's "Server Validation" section requires a Mcp-Name header naming the
// body's tool/resource/prompt identifier. Methods outside this set (e.g.
// tools/list) have no per-request name to check, so Mcp-Name is never
// required for them — though if a client sends one anyway, it is still
// validated for consistency.
var nameRequiredMethods = map[string]bool{
	"tools/call":     true,
	"resources/read": true,
	"prompts/get":    true,
}

// ValidateHeaderConsistency enforces the Modern (2026-07-28) Mcp-Method and
// Mcp-Name request headers against the corresponding parsed request body
// fields (Method and ResourceID).
//
// Caller contract: only call this for a request ClassifyRevision has already
// classified Modern. Mcp-Method/Mcp-Name are HTTP-only fields — populated
// solely by ParsingMiddleware reading real HTTP headers (see parser.go) —
// so there is no stdio/HTTP transport ambiguity here to excuse a Legacy
// request from this check; the caller must simply not invoke this function
// for Legacy traffic in the first place.
//
// Mcp-Method is required on every Modern request: a missing or empty header
// is itself a rejection (*RequestHeaderMismatchError), not a silent no-op,
// and a present-but-different value is a mismatch.
//
// Mcp-Name is required only for the methods in nameRequiredMethods
// (tools/call, resources/read, prompts/get) — for any other method, an
// absent Mcp-Name header is fine. When present, it is decoded via
// decodeSentinelName before comparison against ResourceID, since the draft
// spec allows it to be sentinel-encoded; a decode failure or mismatch is a
// rejection.
func ValidateHeaderConsistency(parsed *ParsedMCPRequest) error {
	if parsed.MCPMethodHeader == "" {
		return &RequestHeaderMismatchError{Header: "Mcp-Method", reason: headerMismatchReasonMissing}
	}
	if parsed.MCPMethodHeader != parsed.Method {
		return &RequestHeaderMismatchError{Header: "Mcp-Method", HeaderValue: parsed.MCPMethodHeader, BodyValue: parsed.Method}
	}

	if parsed.MCPNameHeader == "" {
		if nameRequiredMethods[parsed.Method] {
			return &RequestHeaderMismatchError{Header: "Mcp-Name", reason: headerMismatchReasonMissing}
		}
		return nil
	}

	decoded, err := decodeSentinelName(parsed.MCPNameHeader)
	if err != nil {
		return &RequestHeaderMismatchError{
			Header:      "Mcp-Name",
			HeaderValue: parsed.MCPNameHeader,
			BodyValue:   parsed.ResourceID,
			reason:      headerMismatchReasonMalformed,
		}
	}
	if decoded != parsed.ResourceID {
		return &RequestHeaderMismatchError{Header: "Mcp-Name", HeaderValue: decoded, BodyValue: parsed.ResourceID}
	}

	return nil
}

// ExtractMeta pulls the "_meta" object out of raw JSON-RPC request params, for
// use with ClassifyRevision. It is deliberately tolerant: absent params, params
// that don't decode as a JSON object, or a "_meta" value that isn't itself an
// object all yield a nil map rather than an error. Only a well-formed object
// "_meta" is returned.
func ExtractMeta(params json.RawMessage) map[string]any {
	if len(params) == 0 {
		return nil
	}
	var paramsMap map[string]any
	if err := json.Unmarshal(params, &paramsMap); err != nil {
		return nil
	}
	return metaFromParamsMap(paramsMap)
}

// metaFromParamsMap reports the "_meta" value of an already-decoded JSON-RPC
// params map, if it decodes as a JSON object. A missing key or a wrong-typed
// value (e.g. a string or number) both yield nil.
func metaFromParamsMap(paramsMap map[string]any) map[string]any {
	meta, ok := paramsMap["_meta"].(map[string]any)
	if !ok {
		return nil
	}
	return meta
}

// hasModernSignal reports whether the request signals the Modern revision:
// either the header exactly names MCPVersionModern, or _meta carries any of
// the reserved Modern-only keys (regardless of whether their values are
// well-formed).
func hasModernSignal(meta map[string]any, protoHeader string) bool {
	if protoHeader == MCPVersionModern {
		return true
	}
	for _, key := range reservedModernMetaKeys {
		if _, ok := meta[key]; ok {
			return true
		}
	}
	return false
}

// stringMetaValue reports the non-empty string value of meta[key], if present.
func stringMetaValue(meta map[string]any, key string) (string, bool) {
	raw, ok := meta[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// hasObjectMetaValue reports whether meta[key] is present and decodes as a JSON
// object; see objectMetaValue for the shape this covers.
func hasObjectMetaValue(meta map[string]any, key string) bool {
	_, ok := objectMetaValue(meta, key)
	return ok
}

// objectMetaValue reports the value of meta[key] if it decodes as a JSON
// object (map[string]any) — the shape clientInfo and clientCapabilities take
// in _meta. A missing key or a wrong-typed value (e.g. a string or number)
// both count as "not present".
func objectMetaValue(meta map[string]any, key string) (map[string]any, bool) {
	raw, ok := meta[key]
	if !ok {
		return nil, false
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return obj, true
}

// sentinelPrefix and sentinelSuffix wrap a base64-encoded Mcp-Name header
// value per the draft MCP spec. This is NOT RFC 2047 encoded-word syntax
// (there is no encoding-letter field); both markers are literal and
// case-sensitive.
const (
	sentinelPrefix = "=?base64?"
	sentinelSuffix = "?="
)

// decodeSentinelName decodes a Mcp-Name header value that may be wrapped in
// the draft spec's base64 sentinel format (=?base64?<payload>?=). A value
// that isn't wrapped in the sentinel markers is returned unchanged, since it
// is already the plain name/uri. A wrapped value whose payload fails to
// base64-decode is reported as an error.
func decodeSentinelName(v string) (string, error) {
	if !strings.HasPrefix(v, sentinelPrefix) || !strings.HasSuffix(v, sentinelSuffix) {
		return v, nil
	}

	payload := strings.TrimSuffix(strings.TrimPrefix(v, sentinelPrefix), sentinelSuffix)
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("decoding base64 sentinel Mcp-Name payload: %w", err)
	}
	return string(decoded), nil
}

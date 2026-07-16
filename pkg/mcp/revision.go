// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import "fmt"

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
// (schema/draft/schema.ts) for the stateless "Modern" revision. The go-sdk
// keeps its equivalents unexported, so they are declared locally here rather
// than imported.
const (
	// jsonRPCCodeHeaderMismatch signals a mismatch between the MCP-Protocol-Version
	// header and the _meta protocol version (schema.ts HeaderMismatchError).
	jsonRPCCodeHeaderMismatch int64 = -32020
	// jsonRPCCodeMissingClientCapability signals that _meta is missing a client
	// capability required for the request (schema.ts MissingRequiredClientCapabilityError).
	jsonRPCCodeMissingClientCapability int64 = -32021
	// jsonRPCCodeUnsupportedProtocolVersion signals that the _meta protocol version
	// is not one this server supports (schema.ts UnsupportedProtocolVersionError).
	jsonRPCCodeUnsupportedProtocolVersion int64 = -32022
	// jsonRPCCodeInvalidParams is the standard JSON-RPC Invalid Params code, used
	// as a fallback when the draft spec defines no dedicated code for the failure.
	jsonRPCCodeInvalidParams int64 = -32602
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
func (*HeaderMismatchError) Code() int64 { return jsonRPCCodeHeaderMismatch }

// Data implements CodedError.
func (e *HeaderMismatchError) Data() map[string]any {
	return map[string]any{"header": e.Header, "body": e.Body}
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
func (*UnsupportedVersionError) Code() int64 { return jsonRPCCodeUnsupportedProtocolVersion }

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
func (*MissingClientCapabilityError) Code() int64 { return jsonRPCCodeMissingClientCapability }

// Data implements CodedError.
func (e *MissingClientCapabilityError) Data() map[string]any {
	return map[string]any{"requiredCapabilities": e.RequiredCapabilities}
}

// MissingModernMetadataError indicates the request carried a Modern signal —
// either the MCP-Protocol-Version header or one of the reserved
// io.modelcontextprotocol/* _meta keys — but _meta carried no valid
// protocolVersion. The draft spec defines no dedicated error code for this
// case, so it falls back to the standard JSON-RPC Invalid Params code.
type MissingModernMetadataError struct {
	// Header is the MCP-Protocol-Version header value, when the header was (at
	// least partly) the source of the Modern signal. Empty when the signal came
	// only from a reserved _meta key.
	Header string
}

func (*MissingModernMetadataError) Error() string {
	return "request carries a Modern signal but no valid io.modelcontextprotocol/protocolVersion in _meta"
}

// Code implements CodedError.
func (*MissingModernMetadataError) Code() int64 { return jsonRPCCodeInvalidParams }

// Data implements CodedError.
func (e *MissingModernMetadataError) Data() map[string]any {
	data := map[string]any{}
	if e.Header != "" {
		data["header"] = e.Header
	}
	return data
}

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
//  1. meta[metaKeyProtocolVersion] must be a non-empty string, or the request
//     is malformed (*MissingModernMetadataError).
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
		return RevisionModern, &MissingModernMetadataError{Header: protoHeader}
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
// object (map[string]any) — the shape clientCapabilities takes in _meta. A
// missing key or a wrong-typed value (e.g. a string or number) both count as
// "not present".
func hasObjectMetaValue(meta map[string]any, key string) bool {
	raw, ok := meta[key]
	if !ok {
		return false
	}
	_, ok = raw.(map[string]any)
	return ok
}

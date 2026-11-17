// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"fmt"
	"net/http"

	"golang.org/x/exp/jsonrpc2"
)

// UTF8BOM is the three-byte UTF-8 byte order mark. Clients following the
// WHATWG UTF-8 decode algorithm strip one leading BOM before parsing, so code
// decoding or sniffing JSON-RPC messages from the wire must do the same.
var UTF8BOM = []byte("\xEF\xBB\xBF")

// latin1UTF8BOM is the six bytes a UTF-8 byte order mark becomes when it is
// decoded as Latin-1 and encoded as UTF-8 again. The event stream parser of
// the TypeScript MCP SDK drops this sequence from the start of a stream it has
// already decoded as UTF-8, where a real mark has become U+FEFF, so a client
// can read past both forms before the first field name.
var latin1UTF8BOM = []byte("\xC3\xAF\xC2\xBB\xC2\xBF")

// trimLeadingBOMs returns data without the byte order marks it starts with, in
// either form (see UTF8BOM and latin1UTF8BOM) and in any order. A client may
// drop more than one leading mark before parsing a body (its decoder drops one
// and its parser another), so code that has to read a body the way any client
// could read it drops them all.
func trimLeadingBOMs(data []byte) []byte {
	for {
		switch {
		case bytes.HasPrefix(data, UTF8BOM):
			data = data[len(UTF8BOM):]
		case bytes.HasPrefix(data, latin1UTF8BOM):
			data = data[len(latin1UTF8BOM):]
		default:
			return data
		}
	}
}

// ConvertToJSONRPC2ID converts an interface{} ID to jsonrpc2.ID
func ConvertToJSONRPC2ID(id interface{}) (jsonrpc2.ID, error) {
	if id == nil {
		return jsonrpc2.ID{}, nil
	}

	switch v := id.(type) {
	case string:
		return jsonrpc2.StringID(v), nil
	case int:
		return jsonrpc2.Int64ID(int64(v)), nil
	case int64:
		return jsonrpc2.Int64ID(v), nil
	case float64:
		// JSON numbers are often unmarshaled as float64
		return jsonrpc2.Int64ID(int64(v)), nil
	default:
		return jsonrpc2.ID{}, fmt.Errorf("unsupported ID type: %T", id)
	}
}

// WithoutAcceptEncoding returns a copy of r, made as Request.Clone makes one,
// whose headers carry no Accept-Encoding, and leaves r as it was. A filter
// that reads the bytes of a response body forwards the copy: without the
// header, Go's transport asks the backend for gzip on its own and decodes
// what it asked for, so the filter reads the body in the clear whether or
// not the backend compresses it, and a body that still arrives with a
// content coding is one the transport could not decode. The caller's request
// keeps the client's header for the middleware around the filter.
func WithoutAcceptEncoding(r *http.Request) *http.Request {
	out := r.Clone(r.Context())
	out.Header.Del("Accept-Encoding")
	return out
}

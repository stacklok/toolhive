// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ CodedError = (*HeaderMismatchError)(nil)
var _ CodedError = (*RequestHeaderMismatchError)(nil)
var _ CodedError = (*UnsupportedVersionError)(nil)
var _ CodedError = (*MissingClientCapabilityError)(nil)
var _ CodedError = (*MissingModernMetadataError)(nil)

func validModernMeta() map[string]any {
	return map[string]any{
		metaKeyProtocolVersion:    MCPVersionModern,
		metaKeyClientInfo:         map[string]any{"name": "test-client", "version": "1.0.0"},
		metaKeyClientCapabilities: map[string]any{},
	}
}

func TestClassifyRevision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		meta        map[string]any
		protoHeader string
		expectedRev Revision
		checkErr    func(t *testing.T, err error)
	}{
		{
			name:        "modern: valid meta, no header",
			method:      "tools/call",
			meta:        validModernMeta(),
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "modern: valid meta, matching header",
			method:      "tools/call",
			meta:        validModernMeta(),
			protoHeader: MCPVersionModern,
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "modern: mismatched header",
			method:      "tools/call",
			meta:        validModernMeta(),
			protoHeader: "2025-11-25",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, "2025-11-25", mismatchErr.Header)
				assert.Equal(t, MCPVersionModern, mismatchErr.Body)
				assert.Equal(t, map[string]any{"header": "2025-11-25", "body": MCPVersionModern}, mismatchErr.Data())
			},
		},
		{
			name:   "modern: unsupported future body version",
			method: "tools/call",
			meta: map[string]any{
				metaKeyProtocolVersion: "2099-01-01",
			},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var unsupportedErr *UnsupportedVersionError
				require.ErrorAs(t, err, &unsupportedErr)
				assert.Equal(t, CodeUnsupportedProtocolVersion, unsupportedErr.Code())
				data := unsupportedErr.Data()
				assert.Equal(t, "2099-01-01", data["requested"])
				assert.Equal(t, []string{MCPVersionModern}, data["supported"])
			},
		},
		{
			name:        "modern header but meta absent entirely is a header mismatch",
			method:      "tools/call",
			meta:        nil,
			protoHeader: MCPVersionModern,
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, MCPVersionModern, mismatchErr.Header)
				assert.Empty(t, mismatchErr.Body)
			},
		},
		{
			name:        "modern header but meta missing protocol version key is a header mismatch",
			method:      "tools/call",
			meta:        map[string]any{"other": "value"},
			protoHeader: MCPVersionModern,
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
			},
		},
		{
			name:        "modern header but body version wrong-typed is a header mismatch",
			method:      "tools/call",
			meta:        map[string]any{metaKeyProtocolVersion: 42},
			protoHeader: MCPVersionModern,
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
			},
		},
		{
			name:        "modern header but body version empty string is a header mismatch",
			method:      "tools/call",
			meta:        map[string]any{metaKeyProtocolVersion: ""},
			protoHeader: MCPVersionModern,
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
			},
		},
		{
			name:   "modern: clientInfo omitted is valid",
			method: "tools/call",
			meta: map[string]any{
				metaKeyProtocolVersion:    MCPVersionModern,
				metaKeyClientCapabilities: map[string]any{},
			},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:   "modern: missing clientCapabilities",
			method: "tools/call",
			meta: map[string]any{
				metaKeyProtocolVersion: MCPVersionModern,
				metaKeyClientInfo:      map[string]any{"name": "test-client"},
			},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var missingCapErr *MissingClientCapabilityError
				require.ErrorAs(t, err, &missingCapErr)
				assert.Equal(t, CodeMissingClientCapability, missingCapErr.Code())
			},
		},
		{
			name:        "legacy: absent meta",
			method:      "tools/call",
			meta:        nil,
			protoHeader: "",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "legacy: meta missing protocol version key",
			method:      "tools/call",
			meta:        map[string]any{"other": "value"},
			protoHeader: "",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "legacy: unrecognized header version, no reserved meta key",
			method:      "tools/call",
			meta:        map[string]any{"other": "value"},
			protoHeader: "2099-01-01",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			// logLevel is a reserved key that must be STRIPPED on egress but is
			// NOT a Modern signal (go-sdk's validateRequestMeta gates purely on
			// protocolVersion; SEP-2577 deprecates logLevel). Guards the split
			// between what StripReservedMeta removes and modernSignalMetaKeys: if
			// hasModernSignal ever iterated the strip set again, this request
			// would be misdetected Modern and rejected instead of classified
			// Legacy.
			name:        "legacy: logLevel reserved key alone is not a Modern signal",
			method:      "tools/call",
			meta:        map[string]any{MetaKeyLogLevel: "debug"},
			protoHeader: "",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "modern signal: reserved protocolVersion key wrong-typed",
			method:      "tools/call",
			meta:        map[string]any{metaKeyProtocolVersion: 42},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var missingMetaErr *MissingModernMetadataError
				require.ErrorAs(t, err, &missingMetaErr)
				assert.Equal(t, CodeInvalidParams, missingMetaErr.Code())
			},
		},
		{
			name:        "modern signal: reserved protocolVersion key empty string",
			method:      "tools/call",
			meta:        map[string]any{metaKeyProtocolVersion: ""},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var missingMetaErr *MissingModernMetadataError
				require.ErrorAs(t, err, &missingMetaErr)
				assert.Equal(t, CodeInvalidParams, missingMetaErr.Code())
			},
		},
		{
			name:        "modern signal via clientCapabilities key, no protocolVersion",
			method:      "tools/call",
			meta:        map[string]any{metaKeyClientCapabilities: map[string]any{}},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var missingMetaErr *MissingModernMetadataError
				require.ErrorAs(t, err, &missingMetaErr)
				assert.Equal(t, CodeInvalidParams, missingMetaErr.Code())
			},
		},
		{
			name:   "modern signal via clientInfo key, broken protocolVersion",
			method: "tools/call",
			meta: map[string]any{
				metaKeyClientInfo:      map[string]any{"name": "test-client"},
				metaKeyProtocolVersion: "",
			},
			protoHeader: "",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var missingMetaErr *MissingModernMetadataError
				require.ErrorAs(t, err, &missingMetaErr)
				assert.Equal(t, CodeInvalidParams, missingMetaErr.Code())
			},
		},
		{
			// #6188: the ChatGPT connector negotiates 2025-11-25 and sets
			// reserved keys without a _meta protocolVersion. The explicit
			// non-Modern header is its negotiated declaration and outranks the
			// stray key; rejecting this broke every tools/call it sent. Note
			// the same request WITHOUT the reserved key is classified Legacy by
			// hasModernSignal, so rejecting it was incoherent, not strict.
			name:   "legacy: reserved key with an explicit non-modern header",
			method: "tools/call",
			meta: map[string]any{
				metaKeyClientCapabilities: map[string]any{},
			},
			protoHeader: "2025-11-25",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			// Modern enforcement is untouched: a Modern header with no _meta
			// protocolVersion is still the malformed shape go-sdk sends for a
			// removed RPC, and still -32020.
			name:   "modern header with reserved key but no body version is still a mismatch",
			method: "tools/call",
			meta: map[string]any{
				metaKeyClientCapabilities: map[string]any{},
			},
			protoHeader: MCPVersionModern,
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, MCPVersionModern, mismatchErr.Header)
				assert.Empty(t, mismatchErr.Body)
			},
		},
		{
			// An unrecognised header is still non-Modern, so it is classified
			// Legacy for the same reason. Version validity is the transport's
			// job (isSupportedMCPVersion), not the classifier's.
			name:   "legacy: reserved key with an unrecognised header",
			method: "tools/call",
			meta: map[string]any{
				metaKeyClientInfo: map[string]any{"name": "openai-mcp"},
			},
			protoHeader: "1999-01-01",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "legacy: initialize with nil meta",
			method:      "initialize",
			meta:        nil,
			protoHeader: "",
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:        "legacy: initialize wins over spoofed modern meta and header",
			method:      "initialize",
			meta:        validModernMeta(),
			protoHeader: MCPVersionModern,
			expectedRev: RevisionLegacy,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rev, err := ClassifyRevision(tt.method, tt.meta, tt.protoHeader)

			assert.Equal(t, tt.expectedRev, rev)
			tt.checkErr(t, err)
		})
	}
}

func TestExtractMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params json.RawMessage
		want   map[string]any
	}{
		{
			name:   "well-formed object _meta is returned",
			params: json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`),
			want:   map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		},
		{
			name:   "empty object _meta is returned",
			params: json.RawMessage(`{"_meta":{}}`),
			want:   map[string]any{},
		},
		{
			name:   "nil params yields nil",
			params: nil,
			want:   nil,
		},
		{
			name:   "empty params yields nil",
			params: json.RawMessage(``),
			want:   nil,
		},
		{
			name:   "params without _meta yields nil",
			params: json.RawMessage(`{"other":"value"}`),
			want:   nil,
		},
		{
			name:   "params as JSON array yields nil",
			params: json.RawMessage(`["_meta"]`),
			want:   nil,
		},
		{
			name:   "params as JSON scalar yields nil",
			params: json.RawMessage(`42`),
			want:   nil,
		},
		{
			name:   "malformed params bytes yield nil",
			params: json.RawMessage(`{not json`),
			want:   nil,
		},
		{
			name:   "_meta as string yields nil",
			params: json.RawMessage(`{"_meta":"not-an-object"}`),
			want:   nil,
		},
		{
			name:   "_meta as number yields nil",
			params: json.RawMessage(`{"_meta":42}`),
			want:   nil,
		},
		{
			name:   "_meta as array yields nil",
			params: json.RawMessage(`{"_meta":[1,2,3]}`),
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ExtractMeta(tt.params))
		})
	}
}

func sentinelEncode(name string) string {
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(name)) + "?="
}

func TestValidateHeaderConsistency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		parsed   *ParsedMCPRequest
		checkErr func(t *testing.T, err error)
	}{
		{
			name: "missing Mcp-Method header is rejected",
			parsed: &ParsedMCPRequest{
				Method:     "tools/call",
				ResourceID: "my-tool",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *RequestHeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, "Mcp-Method", mismatchErr.Header)
			},
		},
		{
			name: "method not in the name-required set needs no Mcp-Name",
			parsed: &ParsedMCPRequest{
				Method:          "tools/list",
				MCPMethodHeader: "tools/list",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name: "method in the name-required set missing Mcp-Name is rejected",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *RequestHeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, "Mcp-Name", mismatchErr.Header)
			},
		},
		{
			name: "Mcp-Method matches body method",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   "my-tool",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name: "Mcp-Method mismatches body method",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "resources/read",
				MCPNameHeader:   "my-tool",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *RequestHeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, int64(-32020), mismatchErr.Code())
				assert.Equal(t, "Mcp-Method", mismatchErr.Header)
				assert.Equal(t, "resources/read", mismatchErr.HeaderValue)
				assert.Equal(t, "tools/call", mismatchErr.BodyValue)
			},
		},
		{
			name: "Mcp-Name plain string matches ResourceID",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   "my-tool",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name: "Mcp-Name plain string mismatches ResourceID",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   "other-tool",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *RequestHeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, "Mcp-Name", mismatchErr.Header)
				assert.Equal(t, "other-tool", mismatchErr.HeaderValue)
				assert.Equal(t, "my-tool", mismatchErr.BodyValue)
			},
		},
		{
			name: "Mcp-Name sentinel-encoded decodes to matching ResourceID",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   sentinelEncode("my-tool"),
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name: "Mcp-Name sentinel-encoded decodes to mismatching value",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   sentinelEncode("other-tool"),
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *RequestHeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, "Mcp-Name", mismatchErr.Header)
				assert.Equal(t, "other-tool", mismatchErr.HeaderValue)
				assert.Equal(t, "my-tool", mismatchErr.BodyValue)
			},
		},
		{
			name: "Mcp-Name sentinel wrapper with invalid base64 payload",
			parsed: &ParsedMCPRequest{
				Method:          "tools/call",
				ResourceID:      "my-tool",
				MCPMethodHeader: "tools/call",
				MCPNameHeader:   "=?base64?not-valid-base64!!?=",
			},
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *RequestHeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, "Mcp-Name", mismatchErr.Header)
				assert.Equal(t, "=?base64?not-valid-base64!!?=", mismatchErr.HeaderValue)
				assert.Equal(t, "my-tool", mismatchErr.BodyValue)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHeaderConsistency(tt.parsed)
			tt.checkErr(t, err)
		})
	}
}

func TestDecodeSentinelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "non-sentinel value passes through unchanged",
			input: "my-tool",
			want:  "my-tool",
		},
		{
			name:  "empty value passes through unchanged",
			input: "",
			want:  "",
		},
		{
			name:  "valid sentinel decodes the base64 payload",
			input: sentinelEncode("my-tool"),
			want:  "my-tool",
		},
		{
			name:    "sentinel wrapper with invalid base64 payload errors",
			input:   "=?base64?not-valid-base64!!?=",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeSentinelName(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestEncodeSentinelName pins EncodeSentinelName as the exact mirror of
// decodeSentinelName: every case must round-trip back to the original value.
func TestEncodeSentinelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantSame bool // true if the value must pass through unchanged
	}{
		{name: "plain ASCII name unchanged", input: "my-tool", wantSame: true},
		{name: "URI with colon and slash unchanged", input: "file:///tmp/foo.txt", wantSame: true},
		{name: "empty value is encoded", input: "", wantSame: false},
		{name: "accented character encoded", input: "café-résumé", wantSame: false},
		{name: "CJK character encoded", input: "工具", wantSame: false},
		{name: "CR in value encoded", input: "bad\rname", wantSame: false},
		{name: "LF in value encoded", input: "bad\nname", wantSame: false},
		{name: "value already shaped like a sentinel is escaped", input: sentinelEncode("my-tool"), wantSame: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := EncodeSentinelName(tt.input)
			if tt.wantSame {
				assert.Equal(t, tt.input, got)
			} else {
				assert.NotEqual(t, tt.input, got)
			}

			decoded, err := decodeSentinelName(got)
			require.NoError(t, err)
			assert.Equal(t, tt.input, decoded, "must round-trip through decodeSentinelName")
		})
	}
}

// TestStripReservedMeta pins the copy-before-mutate contract and the two halves
// of the strip predicate: every ReservedMetaPrefix key goes EXCEPT the
// passthroughMetaKeys, and nothing outside the prefix is touched.
func TestStripReservedMeta(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, StripReservedMeta(nil))
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, StripReservedMeta(map[string]any{}))
	})

	t.Run("removes reserved keys, preserves the rest", func(t *testing.T) {
		t.Parallel()
		in := map[string]any{
			// Request-side reserved keys.
			metaKeyProtocolVersion:    MCPVersionModern,
			metaKeyClientInfo:         map[string]any{"name": "x"},
			metaKeyClientCapabilities: map[string]any{},
			MetaKeyLogLevel:           "debug",
			// Response/notification-side reserved keys: a backend must not be able
			// to speak for vMCP on the way back to the client (#5986).
			"io.modelcontextprotocol/serverInfo":     map[string]any{"name": "attacker"},
			"io.modelcontextprotocol/subscriptionId": "sub-1",
			// An unknown future reserved key must go too -- that is the whole point
			// of matching a namespace rather than a fixed list.
			"io.modelcontextprotocol/futureThing": "whatever",
			// Not reserved: ordinary caller/backend metadata.
			"progressToken": "tok-1",
			"traceparent":   "00-abc-def-01",
			"custom":        42,
			// Reserved-adjacent, must NOT match: the second label differs, so the
			// trailing slash in ReservedMetaPrefix is what saves it. This is
			// registry provenance metadata, not per-hop control.
			"io.modelcontextprotocol.registry/publisher-provided": map[string]any{"x": 1},
		}
		got := StripReservedMeta(in)

		for k := range in {
			if strings.HasPrefix(k, ReservedMetaPrefix) {
				assert.NotContains(t, got, k, "reserved key %q must be stripped", k)
			}
		}
		assert.Equal(t, "tok-1", got["progressToken"])
		assert.Equal(t, "00-abc-def-01", got["traceparent"])
		assert.Equal(t, 42, got["custom"])
		assert.Contains(t, got, "io.modelcontextprotocol.registry/publisher-provided",
			"the registry namespace is not this predicate's to strip")
	})

	t.Run("passthrough keys survive despite the reserved prefix", func(t *testing.T) {
		t.Parallel()
		// related-task is a 2025-11-25 MUST on task-related requests AND responses
		// (tasks/result carries the task id nowhere else), so the strip must not
		// eat it even though it sits under the reserved prefix.
		in := map[string]any{
			metaKeyProtocolVersion:                             MCPVersionModern,
			"io.modelcontextprotocol/related-task":             map[string]any{"taskId": "t-1"},
			"io.modelcontextprotocol/model-immediate-response": true,
		}
		got := StripReservedMeta(in)
		assert.NotContains(t, got, metaKeyProtocolVersion)
		assert.Equal(t, map[string]any{"taskId": "t-1"}, got["io.modelcontextprotocol/related-task"])
		assert.Equal(t, true, got["io.modelcontextprotocol/model-immediate-response"])
	})

	t.Run("does not mutate the caller's map", func(t *testing.T) {
		t.Parallel()
		in := map[string]any{metaKeyProtocolVersion: MCPVersionModern, "custom": 1}
		_ = StripReservedMeta(in)
		assert.Contains(t, in, metaKeyProtocolVersion, "caller's map must be untouched")
		assert.Len(t, in, 2)
	})

	t.Run("no reserved keys returns a copy, not the original", func(t *testing.T) {
		t.Parallel()
		in := map[string]any{"custom": 1}
		got := StripReservedMeta(in)
		require.Equal(t, in, got)
		got["custom"] = 2
		assert.Equal(t, 1, in["custom"], "returned value must be a copy")
	})
}

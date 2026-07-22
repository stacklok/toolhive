// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ CodedError = (*HeaderMismatchError)(nil)
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
			name:   "modern signal via reserved key with non-modern header is a header mismatch",
			method: "tools/call",
			meta: map[string]any{
				metaKeyClientCapabilities: map[string]any{},
			},
			protoHeader: "2025-11-25",
			expectedRev: RevisionModern,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				var mismatchErr *HeaderMismatchError
				require.ErrorAs(t, err, &mismatchErr)
				assert.Equal(t, CodeHeaderMismatch, mismatchErr.Code())
				assert.Equal(t, "2025-11-25", mismatchErr.Header)
				assert.Empty(t, mismatchErr.Body)
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

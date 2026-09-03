// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

const (
	testServerName    = "toolhive-vmcp"
	testServerVersion = "0.1.0"
)

// TestModernEnvelopeCommonFields asserts the invariants that apply to every
// Modern result: resultType:"complete", _meta.serverInfo, and Cacheable
// present on the four lists + resources/read but absent on tools/call and
// prompts/get.
func TestModernEnvelopeCommonFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		build         func(t *testing.T) any
		wantCacheable bool
	}{
		{
			name: "tools/list",
			build: func(t *testing.T) any {
				t.Helper()
				result, err := newModernToolsList([]vmcp.Tool{{
					Name:        "greet",
					Description: "say hi",
					InputSchema: map[string]any{"type": "object"},
				}}, testServerName, testServerVersion, "")
				require.NoError(t, err)
				return result
			},
			wantCacheable: true,
		},
		{
			name: "resources/list",
			build: func(*testing.T) any {
				return newModernResourcesList([]vmcp.Resource{
					{Name: "info", URI: "embedded:info", MimeType: "text/plain"},
				}, testServerName, testServerVersion, "")
			},
			wantCacheable: true,
		},
		{
			name: "resources/templates/list",
			build: func(*testing.T) any {
				return newModernResourceTemplatesList([]vmcp.ResourceTemplate{
					{Name: "logs", URITemplate: "file:///logs/{date}.txt"},
				}, testServerName, testServerVersion, "")
			},
			wantCacheable: true,
		},
		{
			name: "prompts/list",
			build: func(*testing.T) any {
				return newModernPromptsList([]vmcp.Prompt{
					{Name: "code_review", Arguments: []vmcp.PromptArgument{{Name: "Code", Required: true}}},
				}, testServerName, testServerVersion, "")
			},
			wantCacheable: true,
		},
		{
			name: "tools/call",
			build: func(*testing.T) any {
				return newModernCallToolResult(&vmcp.ToolCallResult{
					Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "hello"}},
				}, testServerName, testServerVersion, false)
			},
			wantCacheable: false,
		},
		{
			name: "resources/read",
			build: func(*testing.T) any {
				return newModernReadResourceResult(&vmcp.ResourceReadResult{
					Contents: []vmcp.ResourceContent{{URI: "embedded:info", MimeType: "text/plain", Text: "hi"}},
				}, testServerName, testServerVersion)
			},
			wantCacheable: true,
		},
		{
			name: "prompts/get",
			build: func(*testing.T) any {
				return newModernGetPromptResult(&vmcp.PromptGetResult{
					Messages: []vmcp.PromptMessage{{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "hi"}}},
				}, testServerName, testServerVersion)
			},
			wantCacheable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.build(t))
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))

			require.Equal(t, modernResultTypeComplete, decoded["resultType"], "resultType")

			meta, ok := decoded["_meta"].(map[string]any)
			require.True(t, ok, "_meta must be present")
			serverInfo, ok := meta[modernServerInfoKey].(map[string]any)
			require.True(t, ok, "_meta.%s must be present", modernServerInfoKey)
			require.Equal(t, testServerName, serverInfo["name"])
			require.Equal(t, testServerVersion, serverInfo["version"])

			_, hasTTL := decoded["ttlMs"]
			_, hasScope := decoded["cacheScope"]
			require.Equal(t, tt.wantCacheable, hasTTL, "ttlMs presence")
			require.Equal(t, tt.wantCacheable, hasScope, "cacheScope presence")
			if tt.wantCacheable {
				require.InDelta(t, 0, decoded["ttlMs"], 0)
				require.Equal(t, "private", decoded["cacheScope"],
					"vMCP results are admission-filtered per identity; \"public\" would leak across identities")
			}
		})
	}
}

// TestModernResultMetaPreservesBackendMeta asserts _meta on tools/call,
// resources/read, and prompts/get carries BOTH the backend's own result.Meta
// keys AND serverInfo. Overwriting _meta with only serverInfo would silently
// discard whatever the backend attached (progress tokens, trace ids, ...),
// diverging from the SDK path's preservation via conversion.ToMCPMeta
// (serve_handlers.go). The no-backend-meta case (nil result.Meta) is already
// covered by TestModernEnvelopeCommonFields, which only asserts serverInfo.
func TestModernResultMetaPreservesBackendMeta(t *testing.T) {
	t.Parallel()

	backendMeta := map[string]any{"progressToken": "tok-1", "traceId": "abc"}

	tests := []struct {
		name  string
		build func() any
	}{
		{
			name: "tools/call",
			build: func() any {
				return newModernCallToolResult(&vmcp.ToolCallResult{Meta: backendMeta}, testServerName, testServerVersion, false)
			},
		},
		{
			name: "resources/read",
			build: func() any {
				return newModernReadResourceResult(&vmcp.ResourceReadResult{Meta: backendMeta}, testServerName, testServerVersion)
			},
		},
		{
			name: "prompts/get",
			build: func() any {
				return newModernGetPromptResult(&vmcp.PromptGetResult{Meta: backendMeta}, testServerName, testServerVersion)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.build())
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))

			meta, ok := decoded["_meta"].(map[string]any)
			require.True(t, ok, "_meta must be present")
			require.Equal(t, "tok-1", meta["progressToken"], "backend meta key must survive")
			require.Equal(t, "abc", meta["traceId"], "backend meta key must survive")

			serverInfo, ok := meta[modernServerInfoKey].(map[string]any)
			require.True(t, ok, "_meta.%s must still be present alongside backend meta", modernServerInfoKey)
			require.Equal(t, testServerName, serverInfo["name"])
			require.Equal(t, testServerVersion, serverInfo["version"])

			// backendMeta itself must be untouched (copy before mutating caller input).
			require.Len(t, backendMeta, 2, "the caller's Meta map must not be mutated")
		})
	}
}

// TestModernResultMetaOverwritesSpoofedServerInfo is a regression case for
// newModernResultMeta's clone-then-set order: if a backend result.Meta
// already contains the "io.modelcontextprotocol/serverInfo" key (e.g. an
// untrusted or misbehaving backend spoofing vMCP's own identity), the real
// vMCP serverInfo must still win on the wire. One builder is enough to pin
// the shared newModernResultMeta logic all three call/read/get builders use.
func TestModernResultMetaOverwritesSpoofedServerInfo(t *testing.T) {
	t.Parallel()

	spoofed := map[string]any{
		modernServerInfoKey: map[string]any{"name": "attacker-server", "version": "666"},
	}

	raw, err := json.Marshal(newModernCallToolResult(&vmcp.ToolCallResult{Meta: spoofed}, testServerName, testServerVersion, false))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	meta, ok := decoded["_meta"].(map[string]any)
	require.True(t, ok, "_meta must be present")
	serverInfo, ok := meta[modernServerInfoKey].(map[string]any)
	require.True(t, ok)
	require.Equal(t, testServerName, serverInfo["name"], "vMCP's real serverInfo must overwrite a backend-supplied one")
	require.Equal(t, testServerVersion, serverInfo["version"])
}

// TestReservedResponseMetaStrippedByBothHelpers is the #5986 regression pin:
// the Legacy and Modern response builders must both route backend _meta through
// the SAME mcpparser.StripReservedMeta, so a backend cannot set a reserved
// io.modelcontextprotocol/* key on a result and have it reach the client as if
// vMCP had set it.
//
// It compares the two HELPERS, not the two end-to-end paths -- the outputs are
// deliberately not equal (Modern always mints its own serverInfo, Legacy never
// does, since the 2025-11-25 revision has no serverInfo _meta key). It also
// cannot speak for Legacy resources/read, which drops backend result _meta
// wholesale today (coreResourceHandler returns bare []mcp.ResourceContents)
// while Modern preserves it -- a pre-existing divergence, out of scope here.
func TestReservedResponseMetaStrippedByBothHelpers(t *testing.T) {
	t.Parallel()

	// One fixture, fed to both builders: a hostile backend spoofing vMCP's
	// identity and asserting protocol control, alongside metadata it is
	// legitimately allowed to attach.
	backendMeta := map[string]any{
		"io.modelcontextprotocol/serverInfo":         map[string]any{"name": "attacker", "version": "666"},
		"io.modelcontextprotocol/protocolVersion":    "1999-01-01",
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "spoofed-client"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/logLevel":           "debug",
		"io.modelcontextprotocol/subscriptionId":     "sub-1",
		"io.modelcontextprotocol/futureThing":        "whatever",
		// Reserved but relay-semantic: MUST survive (see passthroughMetaKeys).
		"io.modelcontextprotocol/related-task": map[string]any{"taskId": "t-1"},
		// Non-reserved backend metadata: MUST survive.
		"progressToken": "tok-1",
		"traceparent":   "00-abc-def-01",
		"custom":        "keep-me",
	}
	snapshot := maps.Clone(backendMeta)

	assertClean := func(t *testing.T, meta map[string]any, wantServerInfo bool) {
		t.Helper()
		for k := range meta {
			if k == mcpparser.ReservedMetaPrefix+"related-task" {
				continue
			}
			if k == modernServerInfoKey && wantServerInfo {
				continue
			}
			require.False(t, strings.HasPrefix(k, mcpparser.ReservedMetaPrefix),
				"backend-set reserved key %q must not reach the client", k)
		}
		require.Equal(t, "tok-1", meta["progressToken"], "non-reserved backend meta must survive")
		require.Equal(t, "00-abc-def-01", meta["traceparent"])
		require.Equal(t, "keep-me", meta["custom"])
		require.Equal(t, map[string]any{"taskId": "t-1"}, meta[mcpparser.ReservedMetaPrefix+"related-task"],
			"end-to-end task correlation must be relayed, not terminated")
	}

	t.Run("modern builder strips and re-stamps its own serverInfo", func(t *testing.T) {
		t.Parallel()

		got := newModernResultMeta(backendMeta, testServerName, testServerVersion)
		assertClean(t, got, true)

		serverInfo, ok := got[modernServerInfoKey].(modernServerInfo)
		require.True(t, ok, "vMCP must stamp its own serverInfo")
		require.Equal(t, testServerName, serverInfo.Name, "vMCP's serverInfo must beat the backend's")
		require.Equal(t, testServerVersion, serverInfo.Version)
	})

	t.Run("legacy builder strips and stamps nothing", func(t *testing.T) {
		t.Parallel()

		got := conversion.ToMCPMeta(backendMeta)
		require.NotNil(t, got)
		// ToMCPMeta hoists progressToken out of the map, so put it back before
		// running the shared assertions.
		flat := maps.Clone(got.AdditionalFields)
		flat["progressToken"] = got.ProgressToken
		assertClean(t, flat, false)
	})

	t.Run("the caller's map is never mutated", func(t *testing.T) {
		t.Parallel()

		// Compare against a snapshot, not just the length: a swap-one-add-one
		// mutation would keep the count identical.
		require.Equal(t, snapshot, backendMeta, "neither builder may mutate the domain result's map")
	})
}

// TestModernEnvelopeEmptyCollections asserts that an empty domain slice
// marshals to a JSON array ([]), never null.
func TestModernEnvelopeEmptyCollections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field string
		build func(t *testing.T) any
	}{
		{
			name:  "tools/list",
			field: "tools",
			build: func(t *testing.T) any {
				t.Helper()
				result, err := newModernToolsList(nil, testServerName, testServerVersion, "")
				require.NoError(t, err)
				return result
			},
		},
		{
			name:  "resources/list",
			field: "resources",
			build: func(*testing.T) any { return newModernResourcesList(nil, testServerName, testServerVersion, "") },
		},
		{
			name:  "resources/templates/list",
			field: "resourceTemplates",
			build: func(*testing.T) any {
				return newModernResourceTemplatesList(nil, testServerName, testServerVersion, "")
			},
		},
		{
			name:  "prompts/list",
			field: "prompts",
			build: func(*testing.T) any { return newModernPromptsList(nil, testServerName, testServerVersion, "") },
		},
		{
			name:  "tools/call content",
			field: "content",
			build: func(*testing.T) any {
				return newModernCallToolResult(&vmcp.ToolCallResult{}, testServerName, testServerVersion, false)
			},
		},
		{
			name:  "resources/read contents",
			field: "contents",
			build: func(*testing.T) any {
				return newModernReadResourceResult(&vmcp.ResourceReadResult{}, testServerName, testServerVersion)
			},
		},
		{
			name:  "prompts/get messages",
			field: "messages",
			build: func(*testing.T) any {
				return newModernGetPromptResult(&vmcp.PromptGetResult{}, testServerName, testServerVersion)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.build(t))
			require.NoError(t, err)

			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.JSONEq(t, "[]", string(decoded[tt.field]), "%s must marshal as [] not null", tt.field)
		})
	}
}

// TestModernCallToolResult covers tools/call-specific behavior not shared
// with the other builders: isError passthrough and conditional
// structuredContent.
func TestModernCallToolResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		domainResult   *vmcp.ToolCallResult
		wantIsError    bool
		wantStructured bool
	}{
		{
			name:         "success",
			domainResult: &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "ok"}}},
		},
		{
			name: "isError true passthrough",
			domainResult: &vmcp.ToolCallResult{
				Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "boom"}},
				IsError: true,
			},
			wantIsError: true,
		},
		{
			name:           "structuredContent present when set",
			domainResult:   &vmcp.ToolCallResult{StructuredContent: map[string]any{"count": float64(1)}},
			wantStructured: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(newModernCallToolResult(tt.domainResult, testServerName, testServerVersion, false))
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))

			gotIsError, _ := decoded["isError"].(bool)
			require.Equal(t, tt.wantIsError, gotIsError)

			_, hasStructured := decoded["structuredContent"]
			require.Equal(t, tt.wantStructured, hasStructured)
		})
	}
}

// TestModernCompleteTolerateNil asserts newModernComplete doesn't panic on a
// nil result -- defense-in-depth against a plausible aggregator bug, not an
// expected input (see its doc comment). newModernCallToolResult,
// newModernReadResourceResult, and newModernGetPromptResult carry no
// equivalent guard: their callers in modern_dispatch.go dereference
// result.BackendID before invoking the builder, so a nil result already
// panics upstream.
func TestModernCompleteTolerateNil(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		newModernComplete(nil, "other-server", "9.9.9")
	})
}

// TestModernComplete pins the completion/complete wire shape from
// newModernComplete against the SDK's CompleteResult/CompletionResultDetails
// (protocol.go:653,660 in go-sdk@v1.7.0-pre.3): a "completion" object with
// values/total/hasMore, resultType, and _meta.serverInfo -- no Cacheable,
// matching the SDK (CompleteResult never embeds it). JSONEq's exact-match
// means an errant Cacheable field would fail these cases on its own, without
// a separate assertion.
func TestModernComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result *vmcp.CompletionResult
		want   string
	}{
		{
			name:   "nil result marshals an empty completion",
			result: nil,
			want: `{
				"resultType": "complete",
				"completion": {"values": []},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:   "nil Values slice marshals as [] not null",
			result: &vmcp.CompletionResult{},
			want: `{
				"resultType": "complete",
				"completion": {"values": []},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:   "values/total/hasMore pass through",
			result: &vmcp.CompletionResult{Values: []string{"a", "b"}, Total: 5, HasMore: true},
			want: `{
				"resultType": "complete",
				"completion": {"values": ["a", "b"], "total": 5, "hasMore": true},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(newModernComplete(tt.result, testServerName, testServerVersion))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(raw))
		})
	}
}

// TestModernWriters covers the three HTTP write helpers. writeModernError's
// status derives from the JSON-RPC code (404 for method-not-found, 400 for
// invalid-params, 200 for everything else -- mirroring go-sdk's
// extractErrorStatus); writeModernDenied always changes the HTTP status to
// 403 for a POLICY reason -- that's the signal the audit middleware's
// determineOutcome keys off to log outcome:"denied", unrelated to the
// protocol-level mapping above.
func TestModernWriters(t *testing.T) {
	t.Parallel()

	t.Run("writeModernResult", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		writeModernResult(rec, "req-1", map[string]any{"ok": true})

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))

		var decoded struct {
			JSONRPC string         `json:"jsonrpc"`
			ID      string         `json:"id"`
			Result  map[string]any `json:"result"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
		require.Equal(t, "2.0", decoded.JSONRPC)
		require.Equal(t, "req-1", decoded.ID)
		require.Equal(t, true, decoded.Result["ok"])
	})

	// writeModernResult must ALWAYS emit "id", unlike writeModernError/
	// writeModernDenied below -- a result response's id is required by the
	// MCP schema, not optional. This pins that a future refactor doesn't
	// "helpfully" make it conditional to match its error-writing siblings.
	t.Run("writeModernResult always emits id, even absent", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		writeModernResult(rec, nil, map[string]any{"ok": true})

		var asMap map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &asMap))
		_, hasID := asMap["id"]
		require.True(t, hasID, `"id" key must be present on a result response even when id is nil`)
	})

	// writeModernError's "id" key follows transportsession.HasJSONRPCID: MCP
	// narrows base JSON-RPC 2.0 so an absent id is encoded by omitting the
	// key, never as "id":null (schema/2025-11-25 types the error response id
	// as optional string|number). A map decode (not a tagged struct) is the
	// only way to tell an absent key apart from a present null.
	t.Run("writeModernError id presence", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			id         any
			wantIDKey  bool
			wantIDEcho string // substring the body must contain, verbatim, when wantIDKey
		}{
			{name: "nil id is omitted", id: nil, wantIDKey: false},
			{name: "zero id is still present", id: int64(0), wantIDKey: true, wantIDEcho: `"id":0`},
			{name: "present id is present", id: "req-2", wantIDKey: true, wantIDEcho: `"id":"req-2"`},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				rec := httptest.NewRecorder()
				writeModernError(rec, tt.id, jsonRPCCodeInternalError, "some message")

				var asMap map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &asMap))
				_, hasID := asMap["id"]
				require.Equal(t, tt.wantIDKey, hasID, `"id" key presence`)
				if tt.wantIDKey {
					// A map decode would coerce a numeric id to float64,
					// losing the verbatim-echo guarantee -- check the raw
					// wire bytes instead.
					require.Contains(t, rec.Body.String(), tt.wantIDEcho)
				}
			})
		}
	})

	t.Run("writeModernError", func(t *testing.T) {
		t.Parallel()

		codeToStatus := []struct {
			code       int
			wantStatus int
		}{
			{jsonRPCCodeMethodNotFound, http.StatusNotFound},
			{jsonRPCCodeInvalidParams, http.StatusBadRequest},
			{jsonRPCCodeInvalidRequest, http.StatusOK},
			{jsonRPCCodeInternalError, http.StatusOK},
		}

		for _, tc := range codeToStatus {
			rec := httptest.NewRecorder()
			writeModernError(rec, "req-2", tc.code, "some message")

			require.Equal(t, tc.wantStatus, rec.Code, "code %d", tc.code)
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))

			var decoded struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
			require.Equal(t, tc.code, decoded.Error.Code)
			require.Equal(t, "some message", decoded.Error.Message)
		}
	})

	t.Run("writeModernDenied", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		writeModernDenied(rec, "req-3", "denied by policy")

		require.Equal(t, http.StatusForbidden, rec.Code, "a denial must change the HTTP status for audit outcome:\"denied\"")
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))

		var decoded struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
		require.Equal(t, int(mcpparser.JSONRPCCodeDenied), decoded.Error.Code)
		require.Equal(t, "denied by policy", decoded.Error.Message)
	})

	// writeModernDenied's "id" key follows the same HasJSONRPCID rule as
	// writeModernError above.
	t.Run("writeModernDenied id presence", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			id         any
			wantIDKey  bool
			wantIDEcho string
		}{
			{name: "nil id is omitted", id: nil, wantIDKey: false},
			{name: "zero id is still present", id: int64(0), wantIDKey: true, wantIDEcho: `"id":0`},
			{name: "present id is present", id: "req-3", wantIDKey: true, wantIDEcho: `"id":"req-3"`},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				rec := httptest.NewRecorder()
				writeModernDenied(rec, tt.id, "denied by policy")

				var asMap map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &asMap))
				_, hasID := asMap["id"]
				require.Equal(t, tt.wantIDKey, hasID, `"id" key presence`)
				if tt.wantIDKey {
					require.Contains(t, rec.Body.String(), tt.wantIDEcho)
				}
			})
		}
	})
}

// TestWriteModernEnvelopeMarshalFallback verifies the defensive fallback
// drops the "id" key (never "id":null) when the envelope fails to marshal.
// A channel is not JSON-marshalable, forcing json.Marshal to fail.
func TestWriteModernEnvelopeMarshalFallback(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	writeModernEnvelope(rec, http.StatusOK, map[string]any{"bad": make(chan int)})

	const want = `{"jsonrpc":"2.0","error":{"code":-32603,"message":"Internal error"}}`
	require.Equal(t, want, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `"id"`)
}

// TestModernDiscover asserts server/discover's capability-flags shape: a
// capability field is present iff the corresponding admitted list was
// non-empty, resources/templates fold into the single "resources" flag, and
// no descriptor arrays ever appear on the wire.
func TestModernDiscover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                             string
		hasTools, hasResources, hasTemplates, hasPrompts bool
		want                                             string
	}{
		{
			name: "nothing admitted -- only the static completions capability advertised",
			want: `{
				"resultType": "complete",
				"ttlMs": 0,
				"cacheScope": "private",
				"supportedVersions": ["2026-07-28", "2025-11-25"],
				"capabilities": {"completions": {}},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:     "tools only",
			hasTools: true,
			want: `{
				"resultType": "complete",
				"ttlMs": 0,
				"cacheScope": "private",
				"supportedVersions": ["2026-07-28", "2025-11-25"],
				"capabilities": {"tools": {}, "completions": {}},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:         "resources only",
			hasResources: true,
			want: `{
				"resultType": "complete",
				"ttlMs": 0,
				"cacheScope": "private",
				"supportedVersions": ["2026-07-28", "2025-11-25"],
				"capabilities": {"resources": {}, "completions": {}},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:         "templates only still sets resources flag",
			hasTemplates: true,
			want: `{
				"resultType": "complete",
				"ttlMs": 0,
				"cacheScope": "private",
				"supportedVersions": ["2026-07-28", "2025-11-25"],
				"capabilities": {"resources": {}, "completions": {}},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:       "prompts only",
			hasPrompts: true,
			want: `{
				"resultType": "complete",
				"ttlMs": 0,
				"cacheScope": "private",
				"supportedVersions": ["2026-07-28", "2025-11-25"],
				"capabilities": {"prompts": {}, "completions": {}},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
		{
			name:         "everything admitted",
			hasTools:     true,
			hasResources: true,
			hasTemplates: true,
			hasPrompts:   true,
			want: `{
				"resultType": "complete",
				"ttlMs": 0,
				"cacheScope": "private",
				"supportedVersions": ["2026-07-28", "2025-11-25"],
				"capabilities": {"tools": {}, "resources": {}, "prompts": {}, "completions": {}},
				"_meta": {"io.modelcontextprotocol/serverInfo": {"name": "toolhive-vmcp", "version": "0.1.0"}}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(newModernDiscover(
				tt.hasTools, tt.hasResources, tt.hasTemplates, tt.hasPrompts, testServerName, testServerVersion,
			))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(raw))
		})
	}
}

// TestModernDescriptorFieldMapping pins the domain->wire descriptor mapping
// (the list-item shape, not the envelope wrapping it): tool name/description/
// inputSchema (including the RawInputSchema round-trip)/outputSchema/
// annotations, resource uri/name/mimeType, resource template uriTemplate,
// and prompt arguments[].required with omitempty.
func TestModernDescriptorFieldMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func(t *testing.T) any
		want  string
	}{
		{
			name: "tool with annotations and output schema",
			build: func(t *testing.T) any {
				t.Helper()
				readOnly := true
				wireTool, err := modernToolFromDomain(vmcp.Tool{
					Name:        "greet",
					Description: "say hi",
					InputSchema: map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
					},
					OutputSchema: map[string]any{"type": "object"},
					Annotations:  &vmcp.ToolAnnotations{ReadOnlyHint: &readOnly},
				})
				require.NoError(t, err)
				return wireTool
			},
			want: `{
				"name": "greet",
				"description": "say hi",
				"inputSchema": {"type": "object", "properties": {"name": {"type": "string"}}},
				"outputSchema": {"type": "object"},
				"annotations": {"readOnlyHint": true}
			}`,
		},
		{
			// Pins the known documented behavior at the mapping site: a
			// tool with no annotations still emits "annotations":{} because
			// mcpcompat's Tool.MarshalJSON writes the field unconditionally.
			name: "tool with no annotations still emits annotations:{}",
			build: func(t *testing.T) any {
				t.Helper()
				wireTool, err := modernToolFromDomain(vmcp.Tool{
					Name:        "noop",
					InputSchema: map[string]any{"type": "object"},
				})
				require.NoError(t, err)
				return wireTool
			},
			want: `{"name": "noop", "inputSchema": {"type": "object"}, "annotations": {}}`,
		},
		{
			name: "resource",
			build: func(*testing.T) any {
				return modernResourceFromDomain(vmcp.Resource{
					Name: "info", URI: "embedded:info", Description: "info doc", MimeType: "text/plain",
				})
			},
			want: `{"uri":"embedded:info","name":"info","description":"info doc","mimeType":"text/plain"}`,
		},
		{
			name: "resource template",
			build: func(*testing.T) any {
				return modernResourceTemplateFromDomain(vmcp.ResourceTemplate{
					Name: "logs", URITemplate: "file:///logs/{date}.txt", Description: "daily logs", MimeType: "text/plain",
				})
			},
			want: `{"uriTemplate":"file:///logs/{date}.txt","name":"logs","description":"daily logs","mimeType":"text/plain"}`,
		},
		{
			name: "prompt arguments[].required with omitempty",
			build: func(*testing.T) any {
				return modernPromptFromDomain(vmcp.Prompt{
					Name:        "code_review",
					Description: "do a code review",
					Arguments: []vmcp.PromptArgument{
						{Name: "Code", Required: true},
						{Name: "Language", Description: "optional hint"},
					},
				})
			},
			want: `{
				"name": "code_review",
				"description": "do a code review",
				"arguments": [
					{"name": "Code", "required": true},
					{"name": "Language", "description": "optional hint"}
				]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.build(t))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(raw))
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// identityKey keys a []string test corpus by its own element value.
func identityKey(s string) string { return s }

// makeKeys builds n keys that sort lexicographically in generation order, so a
// test can assert exact page contents without depending on sort subtleties.
func makeKeys(n int) []string {
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, fmt.Sprintf("k%05d", i))
	}
	return keys
}

// drainPages walks paginateModern to exhaustion, returning every key delivered
// and the number of pages it took. It fails if pagination does not terminate
// within a generous bound, so a cursor that fails to advance surfaces as a clear
// failure rather than an infinite loop.
func drainPages(t *testing.T, corpus []string) ([]string, int) {
	t.Helper()

	// Non-nil so an empty corpus compares equal to makeKeys(0) rather than
	// tripping on nil-vs-empty, which is not a distinction under test here.
	seen := []string{}
	cursor := ""
	for pages := 1; pages <= 100; pages++ {
		page, next, err := paginateModern(corpus, identityKey, cursorKindTools, cursor)
		require.NoError(t, err)
		seen = append(seen, page...)
		if next == "" {
			return seen, pages
		}
		cursor = next
	}
	t.Fatal("pagination did not terminate within 100 pages; cursor is not advancing")
	return nil, 0
}

// TestPaginateModern_DeliversEveryItemExactlyOnce is the central assertion of the
// pagination half: walking the cursors must yield the complete set, in order,
// with no gap and no duplicate. That is the property the whole scheme exists to
// provide, and the one a broken page boundary silently violates.
func TestPaginateModern_DeliversEveryItemExactlyOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     int
		wantPages int
	}{
		{name: "empty set is a single empty page", total: 0, wantPages: 1},
		{name: "single item fits one page", total: 1, wantPages: 1},
		{name: "exactly one full page emits no cursor", total: modernPageSize, wantPages: 1},
		{name: "one item beyond a page spills to a second", total: modernPageSize + 1, wantPages: 2},
		{name: "the regression case: 1100 items", total: 1100, wantPages: 2},
		{name: "multiple full pages plus a tail", total: modernPageSize*2 + 7, wantPages: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			corpus := makeKeys(tt.total)
			seen, pages := drainPages(t, corpus)

			assert.Equal(t, tt.wantPages, pages, "unexpected page count")
			assert.Equal(t, corpus, seen, "every item must be delivered exactly once, in sorted order")

			distinct := make(map[string]struct{}, len(seen))
			for _, k := range seen {
				distinct[k] = struct{}{}
			}
			assert.Len(t, distinct, tt.total, "duplicates indicate overlapping pages")
		})
	}
}

// TestPaginateModern_PageBoundaries pins the boundary behaviour the walk above
// exercises only indirectly: page size is capped, and a nextCursor appears if and
// only if items actually remain.
func TestPaginateModern_PageBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("first page is capped at the page size and carries a cursor", func(t *testing.T) {
		t.Parallel()
		page, next, err := paginateModern(makeKeys(1100), identityKey, cursorKindTools, "")
		require.NoError(t, err)
		assert.Len(t, page, modernPageSize)
		assert.NotEmpty(t, next, "items remain, so a cursor is required")
	})

	t.Run("final page carries no cursor", func(t *testing.T) {
		t.Parallel()
		_, next, err := paginateModern(makeKeys(1100), identityKey, cursorKindTools, "")
		require.NoError(t, err)
		page, next2, err := paginateModern(makeKeys(1100), identityKey, cursorKindTools, next)
		require.NoError(t, err)
		assert.Len(t, page, 100)
		assert.Empty(t, next2, "nothing remains, so emitting a cursor would force a wasted round trip")
	})

	t.Run("caller slice is not reordered", func(t *testing.T) {
		t.Parallel()
		// Deliberately unsorted, mirroring the aggregator's unordered fan-out.
		corpus := []string{"zeta", "alpha", "mu"}
		original := slices.Clone(corpus)

		page, _, err := paginateModern(corpus, identityKey, cursorKindTools, "")
		require.NoError(t, err)

		assert.Equal(t, original, corpus, "paginateModern must not sort the caller's slice in place")
		assert.Equal(t, []string{"alpha", "mu", "zeta"}, page, "the page itself must be sorted")
	})
}

// TestPaginateModern_CursorValidation covers the cursor as untrusted input. A
// cursor is client-controlled, so every malformed or mismatched shape must be
// rejected rather than reinterpreted into a plausible-looking page.
func TestPaginateModern_CursorValidation(t *testing.T) {
	t.Parallel()

	validOtherKind, err := encodeModernCursor(cursorKindPrompts, "k00001")
	require.NoError(t, err)
	emptyKey, err := encodeModernCursor(cursorKindTools, "")
	require.NoError(t, err)

	tests := []struct {
		name    string
		cursor  string
		wantErr bool
	}{
		{name: "not base64", cursor: "!!!not-base64!!!", wantErr: true},
		{name: "base64 of non-JSON", cursor: base64.RawURLEncoding.EncodeToString([]byte("plain")), wantErr: true},
		{name: "cursor minted for a different list verb", cursor: validOtherKind, wantErr: true},
		{name: "cursor with an empty key", cursor: emptyKey, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := paginateModern(makeKeys(10), identityKey, cursorKindTools, tt.cursor)
			require.ErrorIs(t, err, errInvalidModernCursor)
		})
	}

	t.Run("cursor naming a since-removed item resumes at the next key", func(t *testing.T) {
		t.Parallel()
		// "k00002" is gone from the corpus; the scan must continue from the next
		// key above it rather than erroring or restarting.
		cursor, err := encodeModernCursor(cursorKindTools, "k00002")
		require.NoError(t, err)

		page, _, err := paginateModern([]string{"k00001", "k00003", "k00004"}, identityKey, cursorKindTools, cursor)
		require.NoError(t, err)
		assert.Equal(t, []string{"k00003", "k00004"}, page)
	})
}

// TestModernCursor_RoundTripAndOpacity checks the codec, and that the token does
// not leak its payload in plaintext -- clients are required to treat it as
// opaque, so it must not invite parsing.
func TestModernCursor_RoundTripAndOpacity(t *testing.T) {
	t.Parallel()

	encoded, err := encodeModernCursor(cursorKindResources, "file:///a/b.txt")
	require.NoError(t, err)

	got, err := decodeModernCursor(cursorKindResources, encoded)
	require.NoError(t, err)
	assert.Equal(t, "file:///a/b.txt", got)

	assert.NotContains(t, encoded, "file:///", "the encoded cursor must not carry its key in plaintext")
}

// TestModernRequestCursor covers cursor extraction from request params, including
// the shapes that must read as "first page" rather than erroring.
func TestModernRequestCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params string
		want   string
	}{
		{name: "absent params", params: "", want: ""},
		{name: "empty object", params: `{}`, want: ""},
		{name: "null cursor", params: `{"cursor":null}`, want: ""},
		{name: "non-string cursor", params: `{"cursor":42}`, want: ""},
		{name: "malformed params", params: `{`, want: ""},
		{name: "present cursor", params: `{"cursor":"abc"}`, want: "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, modernRequestCursor(json.RawMessage(tt.params)))
		})
	}
}

// TestDispatchModernList_Pagination drives the four list verbs through
// dispatchModern to prove the wire result actually carries nextCursor and that a
// bad cursor is rejected as -32602 rather than -32603. The per-verb table also
// pins that each verb pages on its own unique key, so a cursor from one cannot
// silently page another.
func TestDispatchModernList_Pagination(t *testing.T) {
	t.Parallel()

	const total = modernPageSize + 5

	tools := make([]vmcp.Tool, 0, total)
	resources := make([]vmcp.Resource, 0, total)
	templates := make([]vmcp.ResourceTemplate, 0, total)
	prompts := make([]vmcp.Prompt, 0, total)
	for i := range total {
		id := fmt.Sprintf("k%05d", i)
		tools = append(tools, vmcp.Tool{Name: id, InputSchema: map[string]any{"type": "object"}})
		resources = append(resources, vmcp.Resource{URI: "file:///" + id, Name: id})
		templates = append(templates, vmcp.ResourceTemplate{URITemplate: "file:///{x}/" + id, Name: id})
		prompts = append(prompts, vmcp.Prompt{Name: id})
	}
	fakeCore := &modernFakeCore{tools: tools, resources: resources, templates: templates, prompts: prompts}

	tests := []struct {
		method     string
		itemsField string
	}{
		{method: "tools/list", itemsField: "tools"},
		{method: "resources/list", itemsField: "resources"},
		{method: "resources/templates/list", itemsField: "resourceTemplates"},
		{method: "prompts/list", itemsField: "prompts"},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()

			// Page 1: capped, and carries a cursor.
			_, body := dispatchModernTest(t.Context(), t, fakeCore, false, &mcpparser.ParsedMCPRequest{
				ID: 1, Method: tt.method, Params: json.RawMessage(`{}`),
			})
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "expected a result envelope, got %v", body)
			items, ok := result[tt.itemsField].([]any)
			require.True(t, ok, "expected %q in the result", tt.itemsField)
			assert.Len(t, items, modernPageSize)

			cursor, ok := result["nextCursor"].(string)
			require.True(t, ok, "a nextCursor must be present while items remain")
			require.NotEmpty(t, cursor)

			// Page 2: the tail, and no further cursor.
			params, err := json.Marshal(map[string]any{"cursor": cursor})
			require.NoError(t, err)
			_, body2 := dispatchModernTest(t.Context(), t, fakeCore, false, &mcpparser.ParsedMCPRequest{
				ID: 2, Method: tt.method, Params: params,
			})
			result2, ok := body2["result"].(map[string]any)
			require.True(t, ok, "expected a result envelope, got %v", body2)
			items2, ok := result2[tt.itemsField].([]any)
			require.True(t, ok)
			assert.Len(t, items2, 5, "the tail page must hold the remaining items")
			assert.NotContains(t, result2, "nextCursor", "the final page must omit nextCursor entirely")
		})
	}

	t.Run("invalid cursor is invalid params, not internal error", func(t *testing.T) {
		t.Parallel()

		rec, body := dispatchModernTest(t.Context(), t, fakeCore, false, &mcpparser.ParsedMCPRequest{
			ID: 3, Method: "tools/list", Params: json.RawMessage(`{"cursor":"!!!bogus!!!"}`),
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok, "expected a JSON-RPC error envelope, got %v", body)
		assert.Equal(t, float64(jsonRPCCodeInvalidParams), errObj["code"])
		assert.Equal(t, "invalid cursor", errObj["message"],
			"the message must not describe the cursor encoding clients are told to treat as opaque")
	})
}

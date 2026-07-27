// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// identityKey keys a []string test corpus by its own element value.
func identityKey(s string) string { return s }

// makeKeys builds n distinct keys that sort lexicographically in generation
// order, so a test can assert exact page contents without depending on sort
// subtleties.
//
// Corpora built ONLY from this helper are what hid the duplicate-key drop: it
// mints strictly unique keys, so no test built on it can exercise a collision.
// Anything asserting a completeness property must also be run against
// makeKeysWithDuplicates below.
func makeKeys(n int) []string {
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, fmt.Sprintf("k%05d", i))
	}
	return keys
}

// makeKeysWithDuplicates builds an adversarial corpus of n items whose keys
// deliberately collide at positions that matter. Resources, resource templates
// and prompts are plain-appended across backends with no conflict resolution, so
// this is the realistic shape rather than a synthetic edge case: two backends
// exposing the same URI produce exactly this.
//
// The collisions are placed by SORTED position, which is the subtle part and the
// reason a first attempt at this fixture was useless. paginateModern sorts before
// paging, so a distinctively-named duplicate key like "dup-boundary" does not stay
// where it was generated — it sorts among the "d" keys, nowhere near the page
// boundary, and the boundary case goes untested while looking tested. Collisions
// must therefore be introduced by overwriting a key with its own neighbour's
// value, which keeps the corpus sorted and pins the duplicate to the intended
// index.
//
// Two shapes are built:
//   - a run spanning the page-1 boundary (indices pageSize-2 .. pageSize+2 share
//     one key), so a page ends part-way through a run of equal keys. This is the
//     shape that silently dropped an item.
//   - a three-way tie well inside page 1.
func makeKeysWithDuplicates(n int) []string {
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, fmt.Sprintf("k%05d", i))
	}
	// Three-way tie inside page 1: indices 10, 11, 12 all take index 10's key.
	for _, i := range []int{11, 12} {
		if i < n {
			keys[i] = keys[10]
		}
	}
	// A run straddling the page-1 boundary: every index from pageSize-2 to
	// pageSize+2 takes the key at pageSize-2, so the run begins on page 1 and
	// continues onto page 2.
	base := modernPageSize - 2
	if base >= 0 && base < n {
		for i := base + 1; i <= modernPageSize+2 && i < n; i++ {
			keys[i] = keys[base]
		}
	}
	return keys
}

// drainPages walks paginateModern to exhaustion, returning every key delivered
// and the number of pages it took. It fails if pagination does not terminate
// within a generous bound, so a cursor that fails to advance surfaces as a clear
// failure rather than an infinite loop.
func drainPages(t *testing.T, corpus []string) ([]string, int) {
	t.Helper()

	// SHUFFLE before paging. This closes a blind spot that hid a second defect
	// class: makeKeys returns pre-sorted keys, so deleting the sort from
	// paginateModern entirely left this test green -- meaning the deterministic
	// total order the whole keyset scheme rests on was unverified. The aggregator's
	// real output is unordered (concurrent backend fan-out), so an unsorted input
	// is also the realistic one. Deterministic seed keeps failures reproducible.
	shuffled := slices.Clone(corpus)
	rng := rand.New(rand.NewPCG(1, 2))
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	corpus = shuffled

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

// TestPaginateModern_StaticSetDeliveredExactlyOnce is the central assertion of
// the pagination half: walking the cursors over an UNCHANGING set must yield the
// complete set, in order, with no gap and no duplicate. That is the property the
// whole scheme exists to provide, and the one a broken page boundary silently
// violates.
//
// The name says "static set" deliberately. The exactly-once guarantee holds only
// while the underlying set does not change mid-walk: with keyset pagination an
// item inserted BELOW the cursor after a page was served is never reported, since
// the scan only moves forward. That is inherent to the scheme (go-sdk's server
// behaves identically) and is explicitly out of scope per SEP-2567's "cursor
// stability and snapshot consistency" note -- conformant, not a bug. Do not widen
// this name back to an unqualified promise it cannot keep.
func TestPaginateModern_StaticSetDeliveredExactlyOnce(t *testing.T) {
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
			// makeKeys is generated in sorted order, so the corpus doubles as the
			// expected delivery order -- and because drainPages shuffles its input,
			// matching it proves the paginator restored a deterministic total order
			// rather than passing the input through.
			assert.Equal(t, corpus, seen, "every item must be delivered exactly once, in sorted order")

			distinct := make(map[string]struct{}, len(seen))
			for _, k := range seen {
				distinct[k] = struct{}{}
			}
			assert.Len(t, distinct, tt.total, "duplicates indicate overlapping pages")
		})
	}
}

// TestPaginateModern_DuplicateKeysAreNotDropped is the regression test for the
// blocking defect: with a bare "strictly above lastKey" scan, two items sharing a
// key at a page boundary caused BOTH to be skipped, so an item appeared on no
// page at all. A 1100-item resource corpus with one duplicated URI delivered 1099.
//
// Key uniqueness holds only for tools (ResolveToolConflicts keys a map). Resource
// URIs, resource-template URI templates and prompt names are plain-appended
// across backends with no conflict resolution, so collisions are ordinary -- two
// backends exposing file:///README.md is enough. The paginator therefore must not
// depend on uniqueness, and this test is what holds it to that.
//
// The corpus is adversarial by construction (see makeKeysWithDuplicates): a pair
// straddling the page-1 boundary, a run spanning it, and a three-way tie. Total
// count is asserted, because "delivered exactly n" is the property that fails
// when a collision eats an item.
func TestPaginateModern_DuplicateKeysAreNotDropped(t *testing.T) {
	t.Parallel()

	for _, total := range []int{modernPageSize + 100, modernPageSize + 1, modernPageSize * 2} {
		t.Run(fmt.Sprintf("run-spanning-boundary/total=%d", total), func(t *testing.T) {
			t.Parallel()

			corpus := makeKeysWithDuplicates(total)
			require.Len(t, corpus, total, "fixture must produce the requested item count")

			// Sanity-check the fixture actually collides; a non-colliding corpus
			// would make this test silently vacuous, which is how the original
			// defect survived.
			counts := map[string]int{}
			for _, k := range corpus {
				counts[k]++
			}
			boundaryKey := fmt.Sprintf("k%05d", modernPageSize-2)
			assert.Greater(t, counts[boundaryKey], 1,
				"fixture must duplicate a key spanning the page boundary")
			assert.Equal(t, 3, counts[fmt.Sprintf("k%05d", 10)],
				"fixture must include a three-way key tie")

			// The run must genuinely straddle the boundary: some copies on page 1,
			// some on page 2. Otherwise this test cannot catch the drop.
			sortedCorpus := slices.Clone(corpus)
			slices.Sort(sortedCorpus)
			onPage1 := 0
			for i := 0; i < modernPageSize && i < len(sortedCorpus); i++ {
				if sortedCorpus[i] == boundaryKey {
					onPage1++
				}
			}
			assert.Greater(t, onPage1, 0, "run must start on page 1")
			assert.Less(t, onPage1, counts[boundaryKey], "run must continue onto page 2")

			seen, pages := drainPages(t, corpus)

			// The headline: nothing is lost.
			assert.Len(t, seen, total,
				"a duplicated key must not drop items; every item must be delivered")
			assert.Greater(t, pages, 1, "corpus must actually paginate for this test to mean anything")

			// And the multiset matches exactly -- same keys, same multiplicities.
			wantSorted := slices.Clone(corpus)
			slices.Sort(wantSorted)
			gotSorted := slices.Clone(seen)
			slices.Sort(gotSorted)
			assert.Equal(t, wantSorted, gotSorted,
				"delivered multiset must equal the corpus, including duplicate multiplicities")
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

	validOtherKind, err := encodeModernCursor(cursorKindPrompts, "k00001", 1)
	require.NoError(t, err)
	emptyKey, err := encodeModernCursor(cursorKindTools, "", 1)
	require.NoError(t, err)
	negativeDelivered, err := encodeModernCursor(cursorKindTools, "k00001", -1)
	require.NoError(t, err)

	// A cursor over the length cap, rejected before any decode work happens.
	oversized := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"k":"tools","l":"` + strings.Repeat("A", modernCursorMaxLen*2) + `"}`))
	require.Greater(t, len(oversized), modernCursorMaxLen)

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!not-base64!!!"},
		{name: "base64 of non-JSON", cursor: base64.RawURLEncoding.EncodeToString([]byte("plain"))},
		{name: "cursor minted for a different list verb", cursor: validOtherKind},
		{name: "cursor with an empty key", cursor: emptyKey},
		{name: "cursor with a negative delivered count", cursor: negativeDelivered},
		{name: "cursor longer than the length cap", cursor: oversized},
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
		cursor, err := encodeModernCursor(cursorKindTools, "k00002", 1)
		require.NoError(t, err)

		page, _, err := paginateModern([]string{"k00001", "k00003", "k00004"}, identityKey, cursorKindTools, cursor)
		require.NoError(t, err)
		assert.Equal(t, []string{"k00003", "k00004"}, page)
	})

	t.Run("delivered count larger than the run resumes at the next key", func(t *testing.T) {
		t.Parallel()
		// Claims 9 copies of "dup" were delivered but only 2 exist: must not
		// overshoot past "zed".
		cursor, err := encodeModernCursor(cursorKindTools, "dup", 9)
		require.NoError(t, err)

		page, _, err := paginateModern([]string{"dup", "dup", "zed"}, identityKey, cursorKindTools, cursor)
		require.NoError(t, err)
		assert.Equal(t, []string{"zed"}, page)
	})
}

// TestModernCursor_RoundTrip checks the codec. It deliberately does NOT claim to
// test opacity: base64 provides encoding, not secrecy, and encodeModernCursor's
// own comment says it is "emphatically NOT encryption". Opacity is a rule imposed
// on CLIENTS by the spec, not a property the server can enforce, so asserting the
// key is absent from the token would be near-vacuous theatre.
func TestModernCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	encoded, err := encodeModernCursor(cursorKindResources, "file:///a/b.txt", 3)
	require.NoError(t, err)

	gotKey, gotDelivered, err := decodeModernCursor(cursorKindResources, encoded)
	require.NoError(t, err)
	assert.Equal(t, "file:///a/b.txt", gotKey)
	assert.Equal(t, 3, gotDelivered)
}

// TestModernRequestCursor covers cursor extraction from request params, including
// the shapes that must read as "first page" and the shapes that must error.
func TestModernRequestCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		want    string
		wantErr bool
	}{
		{name: "absent params", params: "", want: ""},
		{name: "empty object", params: `{}`, want: ""},
		{name: "null cursor", params: `{"cursor":null}`, want: ""},
		{name: "malformed params", params: `{`, want: ""},
		{name: "present cursor", params: `{"cursor":"abc"}`, want: "abc"},
		// An empty-string cursor is "first page", not an error: vMCP never mints
		// one (nextCursor is omitted entirely at end-of-results), so it can only
		// mean "no cursor supplied". Matches go-sdk's server.
		{name: "empty string cursor is first page", params: `{"cursor":""}`, want: ""},
		// Present but not a string: must be an error, never a silent restart at
		// page one. A client with a serialization bug needs a diagnosable -32602.
		{name: "number cursor is rejected", params: `{"cursor":42}`, wantErr: true},
		{name: "object cursor is rejected", params: `{"cursor":{"k":"v"}}`, wantErr: true},
		{name: "array cursor is rejected", params: `{"cursor":["a"]}`, wantErr: true},
		{name: "bool cursor is rejected", params: `{"cursor":true}`, wantErr: true},
		// Duplicate JSON keys resolve last-wins, which is Go's behaviour
		// everywhere and is why decoding into json.RawMessage matters: there is no
		// partial-decode state to mishandle. Whichever value comes last is the one
		// type-checked, so the same input is accepted or rejected purely on that
		// value -- not on the presence of the duplicate.
		{name: "duplicate cursor key takes the last value", params: `{"cursor":42,"cursor":"abc"}`, want: "abc"},
		{name: "duplicate cursor key rejects a bad last value", params: `{"cursor":"abc","cursor":42}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := modernRequestCursor(json.RawMessage(tt.params))
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidModernCursor)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// paginationFakeCore builds a fresh fake core holding `total` items of every
// kind. Each subtest gets its own instance rather than sharing one: modernFakeCore
// carries write-capable fields (checkCalled, callCalled, gotCtx), so a shared
// value would be a data race the moment a parallel subtest exercised a verb that
// writes them.
//
// Resource NAMES deliberately collide across all items while URIs stay distinct,
// and template names likewise. That makes the choice of pagination key
// observable: keying resources on Name instead of URI collapses the corpus and
// fails, where a fixture with unique names would have accepted either key.
func paginationFakeCore(total int) *modernFakeCore {
	tools := make([]vmcp.Tool, 0, total)
	resources := make([]vmcp.Resource, 0, total)
	templates := make([]vmcp.ResourceTemplate, 0, total)
	prompts := make([]vmcp.Prompt, 0, total)
	for i := range total {
		id := fmt.Sprintf("k%05d", i)
		tools = append(tools, vmcp.Tool{Name: id, InputSchema: map[string]any{"type": "object"}})
		resources = append(resources, vmcp.Resource{URI: "file:///" + id, Name: "same-name"})
		templates = append(templates, vmcp.ResourceTemplate{URITemplate: "file:///{x}/" + id, Name: "same-name"})
		prompts = append(prompts, vmcp.Prompt{Name: id})
	}
	return &modernFakeCore{tools: tools, resources: resources, templates: templates, prompts: prompts}
}

// TestDispatchModernList_Pagination drives the four list verbs through
// dispatchModern to prove the wire result actually carries nextCursor and that a
// bad cursor is rejected as -32602 rather than -32603.
func TestDispatchModernList_Pagination(t *testing.T) {
	t.Parallel()

	const total = modernPageSize + 5

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
			fakeCore := paginationFakeCore(total)

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

	// Both an undecodable string cursor and a cursor of the wrong JSON type must
	// surface as -32602. The non-string case is the one that used to fall through
	// to "first page", silently serving page 1 to a client with a serialization
	// bug instead of giving it a diagnosable error.
	for _, tt := range []struct {
		name   string
		params string
	}{
		{name: "undecodable string cursor", params: `{"cursor":"!!!bogus!!!"}`},
		{name: "non-string cursor", params: `{"cursor":42}`},
	} {
		t.Run("invalid cursor is invalid params, not internal error: "+tt.name, func(t *testing.T) {
			t.Parallel()

			rec, body := dispatchModernTest(t.Context(), t, paginationFakeCore(10), false, &mcpparser.ParsedMCPRequest{
				ID: 3, Method: "tools/list", Params: json.RawMessage(tt.params),
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code)

			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, "expected a JSON-RPC error envelope, got %v", body)
			assert.Equal(t, float64(jsonRPCCodeInvalidParams), errObj["code"])
			assert.Equal(t, "invalid cursor", errObj["message"],
				"the message must not describe the cursor encoding clients are told to treat as opaque")

			_, isResult := body["result"]
			assert.False(t, isResult, "a rejected cursor must not also serve a page")
		})
	}
}

// TestDispatchModernList_CursorNotReplayableAcrossVerbs is the end-to-end twin of
// the kind check. Without it the kind field is self-consistently verified --
// encode and decode both read the same site-local constant, so mutating one verb
// to mint another verb's kind passes every unit test while, in production, a
// prompts/list cursor would be accepted by tools/list.
//
// This replays a real cursor from one verb against every other verb through
// dispatchModern, which is the only way to catch that class of mistake.
func TestDispatchModernList_CursorNotReplayableAcrossVerbs(t *testing.T) {
	t.Parallel()

	const total = modernPageSize + 5
	verbs := []string{"tools/list", "resources/list", "resources/templates/list", "prompts/list"}

	// Mint one real cursor per verb, through the dispatcher.
	cursors := make(map[string]string, len(verbs))
	for _, verb := range verbs {
		_, body := dispatchModernTest(t.Context(), t, paginationFakeCore(total), false, &mcpparser.ParsedMCPRequest{
			ID: 1, Method: verb, Params: json.RawMessage(`{}`),
		})
		result, ok := body["result"].(map[string]any)
		require.True(t, ok, "%s: expected a result, got %v", verb, body)
		cursor, ok := result["nextCursor"].(string)
		require.True(t, ok, "%s: expected a nextCursor", verb)
		cursors[verb] = cursor
	}

	for _, minted := range verbs {
		for _, replayed := range verbs {
			if minted == replayed {
				continue
			}
			t.Run(minted+" cursor rejected by "+replayed, func(t *testing.T) {
				t.Parallel()

				params, err := json.Marshal(map[string]any{"cursor": cursors[minted]})
				require.NoError(t, err)

				rec, body := dispatchModernTest(t.Context(), t, paginationFakeCore(total), false,
					&mcpparser.ParsedMCPRequest{ID: 9, Method: replayed, Params: params})

				assert.Equal(t, http.StatusBadRequest, rec.Code)
				errObj, ok := body["error"].(map[string]any)
				require.True(t, ok, "expected an error envelope, got %v", body)
				assert.Equal(t, float64(jsonRPCCodeInvalidParams), errObj["code"])

				_, isResult := body["result"]
				assert.False(t, isResult, "a cross-verb cursor must not serve a page")
			})
		}
	}
}

// TestModernList_EndOfResultsOmitsCursor pins the draft-only pagination
// amendment: end-of-results MUST be signalled by OMITTING nextCursor, never by
// emitting an empty string. Per the draft's Implementation Guidelines, "an empty
// string is a valid cursor and thus MUST NOT be treated as the end of results" --
// so a client receiving `nextCursor: ""` would re-request with `cursor: ""`,
// which reads as "first page", and loop forever being served page one.
//
// This is a wire-serialization guarantee (the `omitempty` struct tags in
// modern_envelope.go), so it is asserted on the marshalled JSON rather than on
// the Go struct, where the zero value is indistinguishable from a dropped field.
func TestModernList_EndOfResultsOmitsCursor(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"tools/list", "resources/list", "resources/templates/list", "prompts/list"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			// A single short page: there is no next page, so no cursor may appear.
			rec, body := dispatchModernTest(t.Context(), t, paginationFakeCore(1), false,
				&mcpparser.ParsedMCPRequest{ID: 1, Method: method, Params: json.RawMessage(`{}`)})

			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "expected a result envelope, got %v", body)

			assert.NotContains(t, result, "nextCursor",
				"end-of-results must omit nextCursor entirely, not emit an empty string")
			assert.NotContains(t, rec.Body.String(), `"nextCursor"`,
				"the key must be absent from the wire bytes, not merely empty")
		})
	}
}

// TestModernPageSize_MatchesSDKDefaultBoundary pins the page-size value
// behaviourally rather than by reading the constant, so it fails on a real
// behaviour change instead of tautologically agreeing with whatever the constant
// says.
//
// modernPageSize is a hand-copied duplicate of go-sdk's DefaultPageSize (1000),
// which is what the Legacy/SDK path applies to the same split. mcpcompat does not
// re-export the constant, so nothing links the two: if go-sdk changes its
// default, Legacy follows and Modern does not.
//
// This pins the Modern half — a 1001-item corpus must produce exactly a 1000-item
// first page and a 1-item second. The Legacy half is NOT pinned here; doing so
// needs a harness that can hold a client on Legacy, which this package lacks
// (see the constant's doc comment). Asserting only the reachable half, and saying
// so, beats asserting a pairing this test cannot observe.
func TestModernPageSize_MatchesSDKDefaultBoundary(t *testing.T) {
	t.Parallel()

	const sdkDefaultPageSize = 1000
	require.Equal(t, sdkDefaultPageSize, modernPageSize,
		"modernPageSize must track go-sdk's DefaultPageSize; if go-sdk changed, update both and re-check Legacy")

	fakeCore := paginationFakeCore(sdkDefaultPageSize + 1)

	_, body := dispatchModernTest(t.Context(), t, fakeCore, false, &mcpparser.ParsedMCPRequest{
		ID: 1, Method: "tools/list", Params: json.RawMessage(`{}`),
	})
	result, ok := body["result"].(map[string]any)
	require.True(t, ok, "expected a result envelope, got %v", body)
	items, ok := result["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, items, sdkDefaultPageSize, "the first page must hold exactly the SDK default page size")

	cursor, ok := result["nextCursor"].(string)
	require.True(t, ok, "one item remains, so a cursor is required")

	params, err := json.Marshal(map[string]any{"cursor": cursor})
	require.NoError(t, err)
	_, body2 := dispatchModernTest(t.Context(), t, fakeCore, false, &mcpparser.ParsedMCPRequest{
		ID: 2, Method: "tools/list", Params: params,
	})
	result2, ok := body2["result"].(map[string]any)
	require.True(t, ok)
	items2, ok := result2["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, items2, 1, "the tail page must hold the single remaining item")
}

// TestPaginateModern_DuplicateShapes covers the remaining two shapes of the
// duplicate-key defect, whose loss profile differs sharply and whose rule is
// easy to state wrongly.
//
// The general rule: every item whose key equals the LAST key delivered on a page
// was permanently dropped. So loss scaled with the size of the duplicate run
// straddling a page boundary, and a duplicate NOT at a boundary was harmless --
// which is exactly why the bug was easy to miss.
//
//   - single duplicate exactly at the boundary: 1 item lost
//   - duplicate mid-page: 0 lost (harmless, but must stay correct)
//   - run of 50 at the boundary: 50 lost
//   - every key identical: an entire page lost, and page 2 came back empty
func TestPaginateModern_DuplicateShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		corpus func() []string
	}{
		{
			name: "single duplicate exactly at the page boundary",
			corpus: func() []string {
				keys := makeKeys(modernPageSize + 100)
				keys[modernPageSize] = keys[modernPageSize-1]
				return keys
			},
		},
		{
			name: "duplicate mid-page is harmless but must stay correct",
			corpus: func() []string {
				keys := makeKeys(modernPageSize + 100)
				keys[500] = keys[499]
				return keys
			},
		},
		{
			name: "run of 50 copies straddling the boundary",
			corpus: func() []string {
				keys := makeKeys(modernPageSize + 100)
				for i := modernPageSize - 25; i < modernPageSize+25; i++ {
					keys[i] = keys[modernPageSize-25]
				}
				return keys
			},
		},
		{
			name: "every key identical",
			corpus: func() []string {
				keys := make([]string, modernPageSize+100)
				for i := range keys {
					keys[i] = "same"
				}
				return keys
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			corpus := tt.corpus()
			seen, _ := drainPages(t, corpus)

			assert.Len(t, seen, len(corpus),
				"every item must be delivered; a duplicated key must never drop one")

			wantSorted := slices.Clone(corpus)
			slices.Sort(wantSorted)
			gotSorted := slices.Clone(seen)
			slices.Sort(gotSorted)
			assert.Equal(t, wantSorted, gotSorted,
				"delivered multiset must match the corpus, duplicate multiplicities included")
		})
	}
}

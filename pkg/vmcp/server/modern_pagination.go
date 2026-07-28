// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Client-facing list pagination for the Modern (2026-07-28) dispatcher.
//
// WHY THIS IS NOT A PORT OF THE LEGACY BEHAVIOR. vMCP already follows
// pagination cursors in two places, but both are UPSTREAM -- vMCP acting as a
// client, walking a backend's pages to assemble the aggregated view
// (pkg/vmcp/client, pkg/vmcp/session/internal/backend, via
// pkg/vmcp/internal/pagination.ListAll; #5851). The giveaway that none of it is
// reusable here is the shape of the entry point: ListAll takes a `fetch`
// CALLBACK and drives it until the upstream stops yielding cursors, whereas this
// file takes a SLICE already fully in hand. ListAll holds no encode/decode logic
// and never mints a cursor -- it only consumes them. This is the opposite
// direction: vMCP as a SERVER, splitting its own already-aggregated list into
// pages for a downstream client. On the Legacy path the SDK's session-scoped
// feature store does that split; dispatchModern bypasses the SDK, so it must do
// it itself.
//
// Cursor TOKENS are consequently not interchangeable between the two revisions
// even though page BOUNDARIES are (see modernPageSize): go-sdk's Legacy server
// mints padded base64.URLEncoding over gob, this mints RawURLEncoding over JSON.
// Neither side ever needs to read the other's -- a client re-lists after
// negotiating, and the opacity rule forbids it from trying.
//
// THE STATELESS CONSTRAINT, AND WHY THE SPEC PERMITS THIS. Modern has no
// sessions, so a cursor may not denote server-held iteration state -- there is
// nowhere to keep it, and any two requests may land on different replicas.
//
// The governing text is the DRAFT pagination page
// (docs/specification/draft/server/utilities/pagination.mdx), not the 2025-11-25
// one: they differ, and the draft is what 2026-07-28 ships. Its normative rules:
//
//   - the cursor is "an opaque string token, representing a position in the
//     result set" (Pagination Model);
//   - clients "MUST treat cursors as opaque tokens" -- no assumptions about
//     format, no parsing, no modifying (Implementation Guidelines 3);
//   - servers "SHOULD provide stable cursors and handle invalid cursors
//     gracefully" (Implementation Guidelines 1);
//   - "Invalid cursors SHOULD result in an error with code -32602" (Error
//     Handling).
//
// That opacity MUST, on its own, licenses this design: if a client may not
// interpret the token, the server is free to encode position INTO it rather than
// remembering it. No further justification is needed.
//
// ONE DRAFT-ONLY AMENDMENT, and it is load-bearing here. The 2025-11-25 bullet
// "Don't persist cursors across sessions" is GONE, replaced by: "Don't make any
// determination based on cursor value other than whether a non-null value was
// provided (e.g. an empty string is a valid cursor and thus MUST NOT be treated
// as the end of results)." The reason for the deletion is recorded in
// docs/seps/2567-sessionless-mcp.md under "Consequential spec edits" -- note that
// SEP is a list of directed edits, i.e. rationale, NOT the normative surface;
// the draft page above is.
//
// The consequence here: end-of-results MUST be signalled by OMITTING nextCursor,
// never by emitting an empty string, because a conformant client treats "" as a
// valid cursor and re-requests with it. Hence `omitempty` on every list result's
// NextCursor field (modern_envelope.go) -- without it a client loops forever on
// page one.
//
// A NOTE ON A TEMPTING ARGUMENT, flagged so nobody re-derives it as doctrine:
// one could argue a self-describing cursor is *required* now, since with sessions
// gone a server-held cursor has no protocol lifetime bounding it. The engineering
// half is true and worth knowing -- a stateful scheme would have to invent its own
// TTL and eviction. But it is NOT a spec consequence: the deleted bullet was
// advice to CLIENTS about persistence, and removing it withdraws a client-side
// caveat rather than creating a server-side obligation. SEP-2567 explicitly
// declines to speak here, putting "cursor stability and snapshot consistency"
// out of scope. Treat the TTL point as our reasoning; treat opacity as the
// authority.
//
// AGGREGATION ACROSS BACKENDS needs no per-backend positions. core.List* returns
// the complete, admission-filtered set in one call, so the cursor encodes a
// position in the AGGREGATED ordering; it never names a backend, and adding or
// removing one cannot invalidate it. Each page re-runs the aggregation -- the same
// per-request fan-out dispatchModernDiscover already documents, not a new one.
// Re-sorting per page is O(n log n) per request, measured at 0.76ms for n=1100,
// i.e. dwarfed by that fan-out. Do not "optimise" it with a cache.

// modernPageSize is the maximum number of items in one Modern list page.
//
// It matches go-sdk's DefaultPageSize (server.go:36-37, value 1000), which is
// what the Legacy/SDK path uses for this same split: vMCP never calls mcpcompat's
// WithPageSize, so the SDK default is in force there. Keeping the two equal means
// a client sees the same page boundaries whichever revision it negotiates.
//
// This is a COPY, not a reference -- mcpcompat does not re-export the constant
// (raising that upstream is tracked separately). Provenance is not detection: if
// go-sdk changes its default, the Legacy path follows automatically and this one
// does not, silently falsifying the equality claimed above.
//
// KNOWN GAP, stated rather than papered over: nothing currently detects that
// drift. The Modern side IS pinned behaviourally in-package (a 1001-item corpus
// must yield a 1000-item first page), but not end-to-end:
// test/integration/vmcp's Over1000Tools regression exercises the Legacy split
// only -- its helpers are mcpcompat-backed, and mcpcompat cannot request
// 2026-07-28 (#5911) -- so no in-tree integration test pins the Modern split
// end-to-end. (A per-request Legacy/Modern pinning mechanism now exists in
// test/e2e's RawMCPClient, but the integration-tier helpers are still
// mcpcompat-backed.)
//
// So: treat the equality claimed above as an invariant maintained by REVIEW, not
// by CI, and re-check it on any go-sdk bump. This deliberately does not name a
// mechanism the missing Modern end-to-end coverage should use; whether that is a
// pinning harness, a loud failure at negotiation, or something else is
// undecided, and this comment must not go stale by presuming one.
const modernPageSize = 1000

// modernCursorMaxLen caps an inbound cursor before it is decoded.
//
// A legitimate cursor is tens of bytes (kind, one key, a small count). The
// request body cap is 8MB and list verbs are not rate-limited
// (ratelimit/decorator.go covers CallTool only), so without this a client could
// hand over a well-formed multi-megabyte cursor that decodes successfully --
// measured at ~295ms of CPU for a 6MB key -- and repeat it unmetered. Rejecting
// on LENGTH before decoding keeps that O(1). 512 is generous: it accommodates a
// ~370-byte key, far beyond any real URI.
const modernCursorMaxLen = 512

// Cursor kinds, one per paginated list verb. The kind is carried inside the
// cursor and checked on decode so a cursor minted for one list cannot be
// replayed against another: without it, a tools cursor sent to prompts/list
// would be silently reinterpreted as a prompt name and return a plausible but
// meaningless page.
const (
	cursorKindTools             = "tools"
	cursorKindResources         = "resources"
	cursorKindResourceTemplates = "resourceTemplates"
	cursorKindPrompts           = "prompts"
)

// errInvalidModernCursor marks a cursor that is over-long, malformed, not a JSON
// string, or well-formed but minted for a different list verb. Callers map it to
// -32602, which the draft pagination page's Error Handling section requires
// ("Invalid cursors SHOULD result in an error with code -32602") and which
// schema.ts classifies under invalid-params ("Pagination: Invalid or expired
// cursor values"). go-sdk does the same, returning ErrInvalidParams for a cursor
// it cannot decode (server.go:2091-2094).
var errInvalidModernCursor = errors.New("invalid pagination cursor")

// modernCursor is the decoded cursor payload: a position in the sorted aggregated
// list, fully self-described so no server state is needed to interpret it.
//
// The JSON field names are single letters only to keep the token short; nothing
// outside this file may depend on them, since the token is opaque by spec.
type modernCursor struct {
	Kind    string `json:"k"`
	LastKey string `json:"l"`
	// Delivered is how many items sharing LastKey have already been sent, counted
	// across every page so far. It is what makes the paginator safe against
	// DUPLICATE keys -- see paginateModern for why a bare "strictly above
	// LastKey" scan silently loses items.
	Delivered int `json:"d,omitempty"`
}

// encodeModernCursor builds the opaque token handed to the client as nextCursor.
// base64url keeps it safe in any JSON string; it is emphatically NOT encryption
// or authentication, and nothing secret may go in it. A client can trivially
// decode and forge one, which is harmless: a cursor only selects a position
// within a set the caller is already entitled to (admission filtering runs in
// core.List* before this file sees anything) and carries no identity, backend, or
// capability of its own.
func encodeModernCursor(kind, lastKey string, delivered int) (string, error) {
	payload, err := json.Marshal(modernCursor{Kind: kind, LastKey: lastKey, Delivered: delivered})
	if err != nil {
		return "", fmt.Errorf("encoding pagination cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// decodeModernCursor recovers a position from a client-supplied cursor, rejecting
// anything that is not a well-formed cursor for wantKind.
//
// Every failure collapses to errInvalidModernCursor rather than surfacing the
// decode detail: the cursor is client-controlled input, and echoing back why it
// failed to parse tells a caller about the internal encoding it is required to
// treat as opaque.
func decodeModernCursor(wantKind, cursor string) (string, int, error) {
	// Length first, so an oversized cursor costs nothing to reject.
	if len(cursor) > modernCursorMaxLen {
		return "", 0, errInvalidModernCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", 0, errInvalidModernCursor
	}
	var decoded modernCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", 0, errInvalidModernCursor
	}
	// A syntactically valid decode is not enough: the kind must match the verb
	// being served, an empty LastKey would make the scan below return the whole
	// list (silently turning a page request into a full-list request), and a
	// negative Delivered would rewind.
	if decoded.Kind != wantKind || decoded.LastKey == "" || decoded.Delivered < 0 {
		return "", 0, errInvalidModernCursor
	}
	return decoded.LastKey, decoded.Delivered, nil
}

// paginateModern returns the page of items following cursor, plus the cursor for
// the page after it (empty when the returned page is the last one).
//
// KEY UNIQUENESS IS NOT ASSUMED, and this is the subtle part. An earlier version
// documented these keys as globally unique "since the aggregator's conflict
// resolver has already de-duplicated them across backends", and skipped forward
// with a bare `key > lastKey`. At the time that precondition held for exactly
// ONE of the four callers (Tool.Name; resource URIs, template strings and
// prompt names were plain-appended across backends with no de-duplication).
// The aggregator has since been fixed to resolve all four (#6060:
// aggregator/capability_conflicts.go), but this paginator is generic and MUST
// NOT depend on that caller invariant — its signature cannot enforce it, and a
// future caller may not uphold it. The collision-safety below stays.
//
// With a bare `>` scan, a collision at a page boundary permanently DROPS an item:
// the page ends on the first copy, the cursor names that key, and the scan then
// skips every copy of it, so the second appears on no page at all. Measured
// before this fix, a 1100-resource set with one duplicated URI delivered 1099.
//
// So the cursor carries (LastKey, Delivered) and the scan resumes *within* a run
// of equal keys rather than past it. Items sharing a key are interchangeable for
// this purpose -- Delivered is a position in the run, not an identity -- which
// keeps the walk correct without a secondary sort key this generic signature
// cannot see. A Delivered larger than the run (a copy vanished between pages)
// degrades to "resume at the next key" rather than failing.
//
// keyOf must still yield the item's natural identity; it just no longer has to be
// unique.
//
// items is never mutated: it is cloned before sorting, because it comes straight
// from core.List* and the caller's slice must not be reordered underneath it.
func paginateModern[T any](items []T, keyOf func(T) string, kind, cursor string) ([]T, string, error) {
	// Sorting makes the ordering deterministic across requests, which keyset
	// pagination requires: the aggregator's own order is not guaranteed stable
	// between calls (backend fan-out completes concurrently). A STABLE sort keeps
	// equal-keyed items in their incoming relative order, which costs nothing and
	// makes the duplicate case reproducible within a snapshot.
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b T) int {
		return strings.Compare(keyOf(a), keyOf(b))
	})

	start := 0
	carried := 0
	cursorKey := ""
	if cursor != "" {
		lastKey, delivered, err := decodeModernCursor(kind, cursor)
		if err != nil {
			return nil, "", err
		}
		cursorKey = lastKey
		// Binary search for the first key >= lastKey rather than scanning. The
		// scan was O(n) per page, so a full walk was O(n^2); this makes it
		// O(log n). A cursor whose key is no longer present lands at the next key
		// above it rather than failing, which is the graceful degradation the
		// "stable cursors" guidance is about -- BinarySearchFunc's insertion point
		// gives that for free.
		start, _ = slices.BinarySearchFunc(sorted, lastKey, func(item T, target string) int {
			return strings.Compare(keyOf(item), target)
		})
		// Then past the copies of lastKey already delivered, bounded by the run
		// itself so a stale count cannot overshoot into the next key.
		for range delivered {
			if start >= len(sorted) || keyOf(sorted[start]) != lastKey {
				break
			}
			start++
			carried++
		}
	}

	end := min(start+modernPageSize, len(sorted))
	page := sorted[start:end]

	// A next cursor is emitted only when items actually remain. Emitting one on
	// the final page would make a conforming client issue a guaranteed-empty extra
	// round trip -- and emitting an empty string rather than omitting the field
	// would make it loop on page one (see the file header).
	if end == len(sorted) || len(page) == 0 {
		return page, "", nil
	}

	// Count the items sharing the last delivered key in this page, plus anything
	// already counted for that same key on earlier pages, so a run spanning
	// several pages accumulates instead of resetting.
	lastKey := keyOf(page[len(page)-1])
	delivered := 0
	for i := len(page) - 1; i >= 0 && keyOf(page[i]) == lastKey; i-- {
		delivered++
	}
	if cursorKey == lastKey {
		delivered += carried
	}

	next, err := encodeModernCursor(kind, lastKey, delivered)
	if err != nil {
		return nil, "", err
	}
	return page, next, nil
}

// modernRequestCursor extracts the optional cursor from a list request's params.
//
// An absent or explicitly null cursor reads as "first page" -- the field is
// optional, so neither is an error. A cursor that is PRESENT but not a string
// (e.g. `{"cursor": 123}`) is rejected with errInvalidModernCursor rather than
// silently read as "first page": the draft's Error Handling section requires
// invalid cursors to yield -32602, and schema.ts lists "Pagination: Invalid or
// expired cursor values" under the invalid-params class.
//
// Discarding that decode error was a real defect, not a style point: "a
// malformed params object was already rejected upstream" is false for this shape,
// because `{"cursor": 42}` is well-formed JSON and nothing upstream inspects the
// cursor's TYPE. The practical cost is a client with a serialization bug
// re-reading page one forever, each iteration paying a full unmetered backend
// fan-out.
//
// Decoding into json.RawMessage rather than straight into a string is deliberate,
// and it removes a whole hazard class rather than just relocating it. With a
// `Cursor string` field, encoding/json can populate the field AND return an error
// on the same call -- verified: `{"cursor":42,"cursor":"valid"}` yields
// ("valid", type error) -- so a discarded error there threw away a value already
// decoded, and an honoured one would reject a cursor it had successfully read.
// RawMessage captures bytes without type-checking, so there is no partial-decode
// state: duplicate keys resolve last-wins (Go's behaviour everywhere), and the
// single explicit string check below is the only place a type verdict is reached.
//
// Note the asymmetry with an empty-STRING cursor, which is deliberate and not the
// same case: `{"cursor": ""}` is "first page", matching go-sdk's server
// (`params.cursorPtr() == nil || *params.cursorPtr() == ""` selects the full
// sequence). vMCP never mints an empty cursor, so an empty string can only mean
// "no cursor supplied".
func modernRequestCursor(params json.RawMessage) (string, error) {
	if len(params) == 0 {
		return "", nil
	}
	var raw struct {
		Cursor json.RawMessage `json:"cursor"`
	}
	if err := json.Unmarshal(params, &raw); err != nil || len(raw.Cursor) == 0 {
		// A params envelope that does not decode at all, or carries no cursor
		// field, is "first page"; a broken envelope is rejected upstream by the
		// parser.
		return "", nil
	}
	if string(raw.Cursor) == "null" {
		return "", nil
	}
	var cursor string
	if err := json.Unmarshal(raw.Cursor, &cursor); err != nil {
		return "", errInvalidModernCursor
	}
	return cursor, nil
}

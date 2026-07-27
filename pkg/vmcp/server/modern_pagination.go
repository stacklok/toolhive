// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Client-facing list pagination for the Modern (2026-07-28) dispatcher.
//
// WHY THIS IS NOT A PORT OF THE LEGACY BEHAVIOR. vMCP already follows
// pagination cursors in two places, but both are UPSTREAM -- vMCP acting as a
// client, walking a backend's pages to assemble the aggregated view
// (pkg/vmcp/client, pkg/vmcp/session/internal/backend, via
// pkg/vmcp/internal/pagination.ListAll; #5851). Nothing there is reusable here,
// which is a different direction entirely: this is vMCP acting as a SERVER,
// splitting its own already-aggregated list into pages for a downstream client.
// On the Legacy path that split is done for vMCP by the SDK's session-scoped
// feature store; dispatchModern bypasses the SDK, so it must do it itself.
//
// THE STATELESS CONSTRAINT, AND WHY THE SPEC PERMITS THIS. Modern has no
// sessions, so a cursor may not denote server-held iteration state -- there is
// nowhere to keep it, and any two requests may land on different replicas. The
// spec's pagination rules make a self-describing cursor the natural fit:
// nextCursor is "an opaque string token representing a position in the result
// set", clients "MUST treat cursors as opaque tokens" and must not parse,
// modify, or persist them across sessions, and servers "SHOULD provide stable
// cursors and handle invalid cursors gracefully". Opaque-to-the-client is
// precisely what lets the server encode position INTO the token instead of
// remembering it.
//
// So this is keyset pagination: the cursor carries the unique key of the last
// item already delivered, and the next page is the items sorted strictly above
// it. That satisfies "stable" in the sense that matters -- inserting or removing
// an item elsewhere in the set never causes a cursor to skip or repeat a
// different item, which an offset-based cursor would. It is also the scheme
// go-sdk's own server uses (paginateList/encodeCursor, server.go:2055-2120 in
// go-sdk@v1.7.0-pre.3), so Modern and Legacy pages behave alike.
//
// AGGREGATION ACROSS BACKENDS is handled by construction rather than by encoding
// per-backend positions. core.List* returns the complete, admission-filtered,
// conflict-resolved set in one call, so by the time pagination runs there is a
// single flat list whose keys the conflict resolver has already made unique
// across backends. The cursor therefore encodes a position in the AGGREGATED
// ordering; it never needs to name a backend, and adding or removing a backend
// cannot invalidate it. The cost is that each page re-runs the aggregation --
// the same per-request fan-out dispatchModernDiscover already documents, not a
// new one.

// modernPageSize is the maximum number of items in one Modern list page.
//
// It matches go-sdk's DefaultPageSize (server.go:36-37, value 1000), which is
// what the Legacy/SDK path uses for this same split: vMCP never calls
// mcpcompat's WithPageSize, so the SDK default is in force there. Keeping the
// two equal means a client sees the same page boundaries whichever revision it
// negotiates. mcpcompat does not re-export the constant, hence the local copy.
const modernPageSize = 1000

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

// errInvalidModernCursor marks a cursor that is malformed, or valid but minted
// for a different list verb. Callers map it to -32602, per the spec's
// "handle invalid cursors gracefully" and matching go-sdk, which returns
// ErrInvalidParams for an undecodable cursor (server.go:2091-2094).
var errInvalidModernCursor = errors.New("invalid pagination cursor")

// modernCursor is the decoded cursor payload. It is deliberately minimal: the
// position is fully described by the last key delivered, so no timestamp,
// offset, or result-set fingerprint is needed -- and none may be added that
// would require server-side state to interpret.
//
// The JSON field names are single letters only to keep the encoded token short;
// nothing outside this file may depend on them, since the token is opaque to
// clients by spec.
type modernCursor struct {
	Kind    string `json:"k"`
	LastKey string `json:"l"`
}

// encodeModernCursor builds the opaque token handed to the client as
// nextCursor. base64url keeps it safe in any JSON string and signals opacity;
// it is emphatically NOT encryption, and nothing secret may be put in it.
func encodeModernCursor(kind, lastKey string) (string, error) {
	payload, err := json.Marshal(modernCursor{Kind: kind, LastKey: lastKey})
	if err != nil {
		return "", fmt.Errorf("encoding pagination cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// decodeModernCursor recovers the last delivered key from a client-supplied
// cursor, rejecting anything that is not a well-formed cursor for wantKind.
//
// Every failure collapses to errInvalidModernCursor rather than surfacing the
// decode detail: the cursor is client-controlled input, and echoing back why it
// failed to parse tells a caller about the internal encoding it is required to
// treat as opaque.
func decodeModernCursor(wantKind, cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", errInvalidModernCursor
	}
	var decoded modernCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", errInvalidModernCursor
	}
	// A syntactically valid decode is not enough: the kind must match the verb
	// being served, and an empty LastKey would make the "strictly above" scan
	// below return the whole list, silently turning a page request into a
	// full-list request.
	if decoded.Kind != wantKind || decoded.LastKey == "" {
		return "", errInvalidModernCursor
	}
	return decoded.LastKey, nil
}

// paginateModern returns the page of items following cursor, plus the cursor for
// the page after it (empty when the returned page is the last one).
//
// keyOf must yield a key that is unique across the whole set -- Tool.Name,
// Resource.URI, ResourceTemplate.URITemplate, and Prompt.Name all are, since the
// aggregator's conflict resolver has already de-duplicated them across backends.
// Uniqueness is what makes "strictly above the last key" advance by exactly one
// page with no gap and no overlap; a duplicated key would silently drop every
// item sharing it.
//
// items is never mutated: it is cloned before sorting, because it comes straight
// from core.List* and the caller's slice must not be reordered underneath it.
func paginateModern[T any](items []T, keyOf func(T) string, kind, cursor string) ([]T, string, error) {
	// Sorting is what makes the ordering deterministic across requests, which
	// keyset pagination requires: the aggregator's own order is not guaranteed
	// stable between calls (backend fan-out completes concurrently), and an
	// unstable order would let a cursor skip or repeat items.
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b T) int {
		return cmpString(keyOf(a), keyOf(b))
	})

	start := 0
	if cursor != "" {
		lastKey, err := decodeModernCursor(kind, cursor)
		if err != nil {
			return nil, "", err
		}
		// Strictly above: the item named by the cursor was already delivered.
		// A cursor whose key is no longer present (its item was removed between
		// pages) still lands correctly here -- the scan resumes at the next key
		// above it rather than failing, which is the graceful degradation the
		// spec's "stable cursors" guidance is about.
		start = len(sorted)
		for i, item := range sorted {
			if keyOf(item) > lastKey {
				start = i
				break
			}
		}
	}

	end := min(start+modernPageSize, len(sorted))
	page := sorted[start:end]

	// A next cursor is emitted only when items actually remain. Emitting one on
	// the final page would make a conforming client issue a guaranteed-empty
	// extra round trip.
	if end == len(sorted) || len(page) == 0 {
		return page, "", nil
	}
	next, err := encodeModernCursor(kind, keyOf(page[len(page)-1]))
	if err != nil {
		return nil, "", err
	}
	return page, next, nil
}

// modernRequestCursor extracts the optional cursor from a list request's params.
// An absent, null, or non-string cursor reads as "first page": the field is
// optional, and a malformed params object has already been rejected upstream.
func modernRequestCursor(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var raw struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return ""
	}
	return raw.Cursor
}

// cmpString orders two keys. Spelled out rather than using strings.Compare so
// the comparison here provably matches the `>` used in paginateModern's scan --
// the two must agree, or a page boundary could skip an item.
func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

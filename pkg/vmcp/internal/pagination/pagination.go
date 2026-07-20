// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pagination provides a helper for following MCP list-pagination
// cursors when querying backends.
//
// MCP list operations (tools/list, resources/list, prompts/list) are
// paginated: a result may carry a NextCursor pointing at the next page, and a
// client that issues a single request without following the cursor silently
// drops every item beyond the first page. Backends served by the
// toolhive-core mcpcompat server paginate at DefaultPageSize=1000, so any
// backend advertising more than 1000 tools/resources/prompts would lose the
// tail without cursor following.
//
// This package lives under pkg/vmcp/internal so both pkg/vmcp/client and
// pkg/vmcp/session can depend on it without introducing an import cycle
// (pkg/vmcp/client already imports pkg/vmcp/session).
package pagination

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// maxIterations caps the number of pages ListAll will fetch. It is a generous
// upper bound (well above the ~O(items/pageSize) pages any legitimate backend
// needs) that exists solely to prevent an infinite loop if a backend keeps
// returning a non-empty cursor forever.
const maxIterations = 10000

// ListAll repeatedly invokes fetch, following the MCP pagination cursor, until
// the backend returns an empty next cursor. It accumulates and returns every
// item across all pages.
//
// fetch is called with the cursor for the page to retrieve (empty on the first
// call) and must return that page's items, the cursor for the next page (empty
// when no more pages remain), and any error. A fetch error is returned to the
// caller immediately.
//
// ListAll guards against a misbehaving backend in two ways: it returns an error
// if the same non-empty cursor is seen twice (a cycle), and it caps the total
// number of pages at maxIterations.
func ListAll[T any](
	ctx context.Context,
	fetch func(ctx context.Context, cursor mcp.Cursor) (items []T, next mcp.Cursor, err error),
) ([]T, error) {
	var all []T
	var cursor mcp.Cursor
	seen := make(map[mcp.Cursor]struct{})

	for i := 0; i < maxIterations; i++ {
		items, next, err := fetch(ctx, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)

		// An empty next cursor signals the last page.
		if next == "" {
			return all, nil
		}

		// A repeated cursor means the backend is not advancing; abort rather
		// than loop forever.
		if _, ok := seen[next]; ok {
			return nil, fmt.Errorf("pagination cursor %q repeated; backend is not advancing pages", next)
		}
		seen[next] = struct{}{}
		cursor = next
	}

	return nil, fmt.Errorf("pagination exceeded %d pages; aborting to avoid an unbounded loop", maxIterations)
}
